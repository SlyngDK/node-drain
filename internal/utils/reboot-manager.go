package utils

import (
	"bytes"
	"context"
	"fmt"
	mod "github.com/slyngdk/node-drain/internal/modules"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"time"

	"k8s.io/apimachinery/pkg/selection"

	"github.com/pkg/errors"
	"go.uber.org/zap"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/util/wait"
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
	defer func(r *RebootManager, ctx context.Context) {
		err := r.cleanup(ctx)
		if err != nil {
			r.l.Error("failed to cleanup", zap.Error(err))
		}
	}(r, ctx)

	if mod.GetNode(ctx, r.clientSet, nodeName) == nil {
		return false, fmt.Errorf("node don't exists in cluster: %s", nodeName)
	}

	pod, err := r.clientSet.CoreV1().Pods(r.namespace).Create(ctx, r.rebootRequiredPod(nodeName), metav1.CreateOptions{})
	if err != nil {
		return false, errors.Wrap(err, "failed to create reboot-required pod")
	}

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
	if err == nil {
		rebootRequired = true
	}

	return rebootRequired, nil
}

func (r *RebootManager) rebootRequiredPod(nodeName string) *corev1.Pod {
	userId := int64(1000)
	t := true
	hostPathType := corev1.HostPathDirectory
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: "reboot-required-",
			Namespace:    r.namespace,
			Labels:       map[string]string{"kubenodedrainer.cego.dk/component": "reboot-required"},
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
			NodeName: nodeName,
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
			RestartPolicy: "Never",
			SecurityContext: &corev1.PodSecurityContext{
				RunAsUser:    &userId,
				RunAsGroup:   &userId,
				RunAsNonRoot: &t,
			},
			Volumes: []corev1.Volume{{
				Name: "host-var-run",
				VolumeSource: corev1.VolumeSource{
					HostPath: &corev1.HostPathVolumeSource{
						Path: "/var/run/",
						Type: &hostPathType,
					},
				},
			}},
		},
	}
}

func (r *RebootManager) RebootNode(ctx context.Context, nodeName string) error {
	defer func(r *RebootManager, ctx context.Context) {
		err := r.cleanup(ctx)
		if err != nil {
			r.l.Error("failed to cleanup", zap.Error(err))
		}
	}(r, ctx)

	node := mod.GetNode(ctx, r.clientSet, nodeName)
	if node == nil {
		return fmt.Errorf("node don't exists in cluster: %s", nodeName)
	}

	if !node.Spec.Unschedulable {
		return fmt.Errorf("node needs to cordoned before reboot: %s", nodeName)
	}

	bootIdOld := node.Status.NodeInfo.BootID

	_, err := r.clientSet.CoreV1().Pods(r.namespace).Create(ctx, r.rebootNodePod(nodeName), metav1.CreateOptions{})
	if err != nil {
		return errors.Wrap(err, "failed to create reboot pod")
	}

	err = wait.PollUntilContextTimeout(ctx, time.Second, 10*time.Minute, false, mod.IsNodeRebooted(r.l, r.clientSet, nodeName, bootIdOld))
	if err != nil {
		return errors.Wrap(err, "node reboot did not complete within 10 minutes")
	}

	return err
}

func (r *RebootManager) rebootNodePod(nodeName string) *corev1.Pod {
	t := true
	f := false
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: "reboot-",
			Namespace:    r.namespace,
			Labels:       map[string]string{"kubenodedrainer.cego.dk/component": "reboot"},
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
			HostPID:  true, // Facilitate entering the host mount namespace via init
			NodeName: nodeName,
			Containers: []corev1.Container{{
				Name:    "shell",
				Image:   "alpine",
				Command: []string{"kill", "-39", "1"}, //kill -SIGRTMIN+5 1 - telling systemd to reboot
				SecurityContext: &corev1.SecurityContext{
					Capabilities: &corev1.Capabilities{
						Drop: []corev1.Capability{"*"},
						Add:  []corev1.Capability{"CAP_KILL"},
					},
					AllowPrivilegeEscalation: &f,
					Privileged:               &f,
					ReadOnlyRootFilesystem:   &t,
				},
			}},
			RestartPolicy: "Never",
		},
	}
}

func (r *RebootManager) cleanup(ctx context.Context) error {
	var labelSelector labels.Selector = labels.ValidatedSetSelector{}
	requirement, err := labels.NewRequirement("kubenodedrainer.cego.dk/component", selection.In, []string{"reboot-required", "reboot"})
	if err != nil {
		return err
	}
	labelSelector = labelSelector.Add(*requirement)
	pods, err := r.clientSet.CoreV1().Pods(r.namespace).List(ctx, metav1.ListOptions{
		LabelSelector: labelSelector.String(),
	})
	if err != nil {
		return errors.Wrap(err, "failed to get reboot required pods running on node")
	}

	for _, pod := range pods.Items {
		err := r.clientSet.CoreV1().Pods(r.namespace).Delete(ctx, pod.GetName(), metav1.DeleteOptions{})
		if err != nil {
			return errors.Wrapf(err, "failed to delete existing pod: %s", pod.GetName())
		}
	}
	return nil
}
