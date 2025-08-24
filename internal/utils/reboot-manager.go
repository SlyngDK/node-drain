package utils

import (
	"bytes"
	"context"
	"fmt"
	"time"

	"github.com/pkg/errors"
	"github.com/slyngdk/node-drain/internal/config"
	mod "github.com/slyngdk/node-drain/internal/modules"
	"go.uber.org/zap"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/selection"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type RebootManager struct {
	l         *zap.Logger
	namespace string
	client    client.Client
	clientSet *kubernetes.Clientset
	config    *rest.Config
}

func NewRebootManager(l *zap.Logger, client client.Client, restConfig *rest.Config, namespace string) (*RebootManager, error) {
	clientSet, err := kubernetes.NewForConfig(restConfig)
	if err != nil {
		return nil, err
	}
	return &RebootManager{
		l:         l,
		namespace: namespace,
		client:    client,
		clientSet: clientSet,
		config:    restConfig,
	}, nil
}

func (r *RebootManager) IsRebootRequired(ctx context.Context, nodeName string) (bool, error) {
	if mod.GetNode(ctx, r.clientSet, nodeName) == nil {
		return false, fmt.Errorf("node don't exists in cluster: %s", nodeName)
	}

	pod, err := r.clientSet.CoreV1().Pods(r.namespace).Create(ctx, r.rebootRequiredPod(nodeName), metav1.CreateOptions{})
	if err != nil {
		return false, errors.Wrap(err, "failed to create reboot-required pod")
	}
	defer func() {
		err := r.clientSet.CoreV1().Pods(r.namespace).Delete(ctx, pod.Name, metav1.DeleteOptions{})
		if err != nil {
			r.l.Error("failed to delete pod", zap.String("node.name", nodeName), zap.Error(err))
		}
	}()

	err = wait.PollUntilContextTimeout(ctx, time.Second, 30*time.Second, false, mod.IsPodRunning(r.clientSet, pod.GetName(), pod.GetNamespace()))
	if err != nil {
		return false, errors.Wrap(err, "pod did not complete while waiting")
	}

	pod, err = r.clientSet.CoreV1().Pods(pod.GetNamespace()).Get(ctx, pod.GetName(), metav1.GetOptions{})
	if err != nil {
		return false, errors.Wrap(err, "failed to get reboot-required pod")
	}

	rebootRequired := false

	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	command := "test -f /host/var/run/reboot-required"
	err = mod.ExecCmd(ctx, r.clientSet, r.config, types.NamespacedName{
		Namespace: pod.Namespace,
		Name:      pod.Name,
	}, command, nil, stdout, stderr)
	r.l.Debug("reboot-required pod output", zap.String("node.name", nodeName), zap.String("stdout", stdout.String()), zap.String("stderr", stderr.String()), zap.Error(err))
	if err == nil {
		rebootRequired = true
	}

	return rebootRequired, nil
}

func (r *RebootManager) rebootRequiredPod(nodeName string) *corev1.Pod {
	userId := int64(1000)
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: "reboot-required-",
			Namespace:    r.namespace,
			Labels:       map[string]string{LabelComponent: "reboot-required"},
		},
		Spec: corev1.PodSpec{
			Volumes: []corev1.Volume{{
				Name: "host-var-run",
				VolumeSource: corev1.VolumeSource{
					HostPath: &corev1.HostPathVolumeSource{
						Path: "/var/run/",
						Type: PtrTo(corev1.HostPathDirectory),
					},
				},
			}},
			Containers: []corev1.Container{{
				Name:    "shell",
				Image:   "alpine",
				Command: []string{"sleep", "300"},
				VolumeMounts: []corev1.VolumeMount{{
					Name:      "host-var-run",
					ReadOnly:  true,
					MountPath: "/host/var/run",
				}},
			}},
			RestartPolicy:                 "Never",
			TerminationGracePeriodSeconds: PtrTo(int64(1)),
			NodeName:                      nodeName,
			SecurityContext: &corev1.PodSecurityContext{
				RunAsUser:    &userId,
				RunAsGroup:   &userId,
				RunAsNonRoot: PtrTo(true),
			},
			Tolerations: []corev1.Toleration{{
				Key:      "node-role.kubernetes.io/control-plane",
				Operator: corev1.TolerationOpExists,
				Effect:   corev1.TaintEffectNoSchedule,
			}, {
				Key:      "node.kubernetes.io/unschedulable",
				Operator: corev1.TolerationOpExists,
				Effect:   corev1.TaintEffectNoSchedule,
			}},
		},
	}
}

func (r *RebootManager) RebootNode(ctx context.Context, nodeName string) error {
	node := mod.GetNode(ctx, r.clientSet, nodeName)
	if node == nil {
		return fmt.Errorf("node don't exists in cluster: %s", nodeName)
	}

	if !node.Spec.Unschedulable {
		return fmt.Errorf("node needs to cordoned before reboot: %s", nodeName)
	}

	if config.GetConfig().ContainerNode {
		r.l.Info("Skipping reboot, because running on containers", zap.String("node.name", nodeName))
		return nil
	}

	_, err := r.clientSet.CoreV1().Pods(r.namespace).Create(ctx, r.rebootNodePod(nodeName), metav1.CreateOptions{})
	if err != nil {
		return errors.Wrap(err, "failed to create reboot pod")
	}

	return nil
}

func (r *RebootManager) rebootNodePod(nodeName string) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: "reboot-",
			Namespace:    r.namespace,
			Labels:       map[string]string{LabelComponent: "reboot"},
			Annotations:  map[string]string{"container.apparmor.security.beta.kubernetes.io/shell": "unconfined"},
		},
		Spec: corev1.PodSpec{
			Tolerations: []corev1.Toleration{{
				Key:      "node-role.kubernetes.io/control-plane",
				Operator: corev1.TolerationOpExists,
				Effect:   corev1.TaintEffectNoSchedule,
			}, {
				Key:      "node.kubernetes.io/unschedulable",
				Operator: corev1.TolerationOpExists,
				Effect:   corev1.TaintEffectNoSchedule,
			}},
			HostPID:                       true, // Facilitate entering the host mount namespace via init
			TerminationGracePeriodSeconds: PtrTo(int64(1)),
			NodeName:                      nodeName,
			Containers: []corev1.Container{{
				Name:    "shell",
				Image:   "alpine",
				Command: []string{"kill", "-39", "1"}, // kill -SIGRTMIN+5 1 - telling systemd to reboot
				SecurityContext: &corev1.SecurityContext{
					Capabilities: &corev1.Capabilities{
						// Drop: []corev1.Capability{"*"},
						Add: []corev1.Capability{"CAP_KILL"},
					},
					AllowPrivilegeEscalation: PtrTo(false),
					Privileged:               PtrTo(false),
					ReadOnlyRootFilesystem:   PtrTo(true),
				},
			}},
			RestartPolicy: "Never",
		},
	}
}

func (r *RebootManager) IsNodeRebooted(ctx context.Context, kubeNode *corev1.Node, oldBootId string) (bool, error) {
	if config.GetConfig().ContainerNode {
		r.l.Info("Node was not rebooted, because running on containers", zap.String("node.name", kubeNode.Name))
		pod := r.rebootRequiredPod(kubeNode.Name)
		pod.ObjectMeta.GenerateName = "reboot-required-remove-"
		pod.Spec.Containers[0].Command = []string{"rm", "-f", "/host/var/run/reboot-required"}
		pod.Spec.Containers[0].VolumeMounts[0].ReadOnly = false
		pod.Spec.SecurityContext.RunAsUser = PtrTo(int64(0))
		pod.Spec.SecurityContext.RunAsNonRoot = PtrTo(false)

		pod, err := r.clientSet.CoreV1().Pods(r.namespace).Create(ctx, pod, metav1.CreateOptions{})
		if err != nil {
			return false, errors.Wrap(err, "failed to create reboot-required-remove pod")
		}
		defer func() {
			err := r.clientSet.CoreV1().Pods(r.namespace).Delete(ctx, pod.Name, metav1.DeleteOptions{})
			if err != nil {
				r.l.Error("failed to delete pod", zap.String("node.name", kubeNode.Name), zap.Error(err))
			}
		}()

		err = wait.PollUntilContextTimeout(ctx, time.Second, 30*time.Second, false, mod.IsPodCompleted(r.clientSet, pod.GetName(), pod.GetNamespace()))
		if err != nil {
			return false, errors.Wrap(err, "pod did not complete while waiting")
		}

		return true, nil
	}

	if kubeNode.Status.NodeInfo.BootID != oldBootId {
		nodeReady := false
		for _, condition := range kubeNode.Status.Conditions {
			if condition.Type == corev1.NodeReady {
				if condition.Status == corev1.ConditionTrue {
					nodeReady = true
				}
			}
		}
		return nodeReady, nil
	}

	return false, nil
}

func (r *RebootManager) Cleanup(ctx context.Context) error {
	return r.CleanupNode(ctx, "")
}

func (r *RebootManager) CleanupNode(ctx context.Context, nodeName string) error {
	var labelSelector labels.Selector = labels.ValidatedSetSelector{}

	requirement, err := labels.NewRequirement(LabelComponent, selection.In, []string{"reboot-required", "reboot"})
	if err != nil {
		return err
	}
	labelSelector = labelSelector.Add(*requirement)
	options := metav1.ListOptions{
		LabelSelector: labelSelector.String(),
	}

	if nodeName != "" {
		options.FieldSelector = fmt.Sprintf("spec.nodeName=%s", nodeName)
	}

	err = r.clientSet.CoreV1().Pods(r.namespace).DeleteCollection(ctx, metav1.DeleteOptions{}, options)
	if err != nil {
		return errors.Wrap(err, "failed to delete pods")
	}
	return nil
}
