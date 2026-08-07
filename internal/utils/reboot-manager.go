package utils

import (
	"bytes"
	"context"
	"fmt"
	"time"

	drainv1 "github.com/slyngdk/node-drain/api/v1"
	"github.com/slyngdk/node-drain/internal/config"
	"go.uber.org/zap"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/selection"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type RebootManager struct {
	l         *zap.Logger
	namespace string
	client    client.Client
	clientSet *kubernetes.Clientset
	config    *rest.Config
	recorder  record.EventRecorder
}

func NewRebootManager(l *zap.Logger, client client.Client, restConfig *rest.Config, recorder record.EventRecorder, namespace string) (*RebootManager, error) {
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
		recorder:  recorder,
	}, nil
}

func (r *RebootManager) IsRebootRequired(ctx context.Context, nodeName string) (bool, error) {
	kubeNode := GetNode(ctx, r.clientSet, nodeName)
	if kubeNode == nil {
		return false, fmt.Errorf("node don't exists in cluster: %s", nodeName)
	}

	pod, err := r.clientSet.CoreV1().Pods(r.namespace).Create(ctx, r.rebootRequiredPod(kubeNode), metav1.CreateOptions{})
	if err != nil {
		return false, fmt.Errorf("failed to create reboot-required pod: %w", err)
	}
	defer func() {
		err := r.clientSet.CoreV1().Pods(r.namespace).Delete(ctx, pod.Name, metav1.DeleteOptions{})
		if err != nil {
			r.l.Error("failed to delete pod", zap.String(config.LogNodeName, nodeName), zap.Error(err))
		}
	}()

	err = wait.PollUntilContextTimeout(ctx, time.Second, 30*time.Second, false, IsPodRunning(r.clientSet, pod.GetName(), pod.GetNamespace()))
	if err != nil {
		return false, fmt.Errorf("pod did not complete while waiting: %w", err)
	}

	pod, err = r.clientSet.CoreV1().Pods(pod.GetNamespace()).Get(ctx, pod.GetName(), metav1.GetOptions{})
	if err != nil {
		return false, fmt.Errorf("failed to get reboot-required pod: %w", err)
	}

	rebootRequired := false

	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	command := "test -f /host/var/run/reboot-required"
	err = ExecCmd(ctx, r.clientSet, r.config, types.NamespacedName{
		Namespace: pod.Namespace,
		Name:      pod.Name,
	}, command, nil, stdout, stderr)
	r.l.Debug("reboot-required pod output", zap.String(config.LogNodeName, nodeName), zap.String("stdout", stdout.String()), zap.String("stderr", stderr.String()), zap.Error(err))
	if err == nil {
		rebootRequired = true
	}

	return rebootRequired, nil
}

func (r *RebootManager) rebootRequiredPod(kubeNode *corev1.Node) *corev1.Pod {
	userId := int64(1000)
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName:    "reboot-required-",
			Namespace:       r.namespace,
			Labels:          map[string]string{LabelComponent: "reboot-required"},
			OwnerReferences: nodeOwnerReference(kubeNode),
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
				Name:      "shell",
				Image:     "alpine",
				Command:   []string{"sleep", "300"},
				Resources: utilityPodResources(),
				VolumeMounts: []corev1.VolumeMount{{
					Name:      "host-var-run",
					ReadOnly:  true,
					MountPath: "/host/var/run",
				}},
			}},
			RestartPolicy:                 "Never",
			TerminationGracePeriodSeconds: PtrTo(int64(1)),
			Affinity:                      nodeNameAffinity(kubeNode.Name),
			PriorityClassName:             utilityPodPriorityClassName(),
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

func (r *RebootManager) RebootNode(ctx context.Context, node *drainv1.Node) error {
	kubeNode := GetNode(ctx, r.clientSet, node.Name)
	if kubeNode == nil {
		return fmt.Errorf("node don't exists in cluster: %s", node.Name)
	}

	if !kubeNode.Spec.Unschedulable {
		return fmt.Errorf("node needs to be cordoned before reboot: %s", node.Name)
	}

	if config.GetConfig().ContainerNode {
		r.recorder.Eventf(node, corev1.EventTypeNormal, "Reboot", "Skipping reboot, because running on containers")
		r.l.Info("Skipping reboot, because running on containers", zap.String(config.LogNodeName, node.Name))
		return nil
	}

	r.recorder.Eventf(node, corev1.EventTypeNormal, "Reboot", "Rebooting node")
	_, err := r.clientSet.CoreV1().Pods(r.namespace).Create(ctx, r.rebootNodePod(kubeNode), metav1.CreateOptions{})
	if err != nil {
		r.recorder.Eventf(node, corev1.EventTypeWarning, "Reboot", "Failed to create reboot pod: %v", err)
		return fmt.Errorf("failed to create reboot pod: %w", err)
	}

	return nil
}

func (r *RebootManager) rebootNodePod(kubeNode *corev1.Node) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName:    "reboot-",
			Namespace:       r.namespace,
			Labels:          map[string]string{LabelComponent: "reboot"},
			OwnerReferences: nodeOwnerReference(kubeNode),
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
			HostPID:           true, // Facilitate entering the host mount namespace via init
			Affinity:          nodeNameAffinity(kubeNode.Name),
			PriorityClassName: utilityPodPriorityClassName(),
			Containers: []corev1.Container{{
				Name:      "shell",
				Image:     "alpine",
				Command:   []string{"kill", "-39", "1"}, // kill -SIGRTMIN+5 1 - telling systemd to reboot
				Resources: utilityPodResources(),
				SecurityContext: &corev1.SecurityContext{
					Capabilities: &corev1.Capabilities{
						// Drop: []corev1.Capability{"*"},
						Add: []corev1.Capability{"CAP_KILL"},
					},
					AllowPrivilegeEscalation: PtrTo(false),
					Privileged:               PtrTo(false),
					ReadOnlyRootFilesystem:   PtrTo(true),
					AppArmorProfile: &corev1.AppArmorProfile{
						Type: corev1.AppArmorProfileTypeUnconfined,
					},
				},
			}},
			RestartPolicy: "Never",
		},
	}
}

// nodeOwnerReference marks the utility pod as controlled by its target node,
// like kubelet mirror pods. This lets the drain helper evict a leftover pod
// (it refuses pods that declare no controller) and garbage-collects pods for
// deleted nodes.
func nodeOwnerReference(kubeNode *corev1.Node) []metav1.OwnerReference {
	return []metav1.OwnerReference{{
		APIVersion: "v1",
		Kind:       "Node",
		Name:       kubeNode.Name,
		UID:        kubeNode.UID,
		Controller: PtrTo(true),
	}}
}

// nodeNameAffinity pins the pod to a node through the scheduler instead of
// spec.nodeName. Bypassing the scheduler skips preemption, so on a node at
// max-pods capacity the kubelet rejects the pod outright (OutOfpods) even
// when it has priority to preempt.
func nodeNameAffinity(nodeName string) *corev1.Affinity {
	return &corev1.Affinity{
		NodeAffinity: &corev1.NodeAffinity{
			RequiredDuringSchedulingIgnoredDuringExecution: &corev1.NodeSelector{
				NodeSelectorTerms: []corev1.NodeSelectorTerm{{
					MatchFields: []corev1.NodeSelectorRequirement{{
						Key:      "metadata.name",
						Operator: corev1.NodeSelectorOpIn,
						Values:   []string{nodeName},
					}},
				}},
			},
		},
	}
}

func utilityPodPriorityClassName() string {
	if c := config.GetConfig(); c != nil {
		return c.Reboot.PodPriorityClassName
	}
	return ""
}

func utilityPodResources() corev1.ResourceRequirements {
	return corev1.ResourceRequirements{
		Requests: corev1.ResourceList{
			corev1.ResourceCPU:    resource.MustParse("10m"),
			corev1.ResourceMemory: resource.MustParse("16Mi"),
		},
		Limits: corev1.ResourceList{
			corev1.ResourceCPU:    resource.MustParse("100m"),
			corev1.ResourceMemory: resource.MustParse("32Mi"),
		},
	}
}

func (r *RebootManager) IsNodeRebooted(ctx context.Context, kubeNode *corev1.Node, oldBootId string) (bool, error) {
	l := r.l.With(zap.String(config.LogNodeName, kubeNode.Name))
	if config.GetConfig().ContainerNode {
		l.Info("Node was not rebooted, because running on containers")
		pod := r.rebootRequiredPod(kubeNode)
		pod.GenerateName = "reboot-required-remove-"
		pod.Spec.Containers[0].Command = []string{"rm", "-f", "/host/var/run/reboot-required"}
		pod.Spec.Containers[0].VolumeMounts[0].ReadOnly = false
		pod.Spec.SecurityContext.RunAsUser = PtrTo(int64(0))
		pod.Spec.SecurityContext.RunAsNonRoot = PtrTo(false)

		pod, err := r.clientSet.CoreV1().Pods(r.namespace).Create(ctx, pod, metav1.CreateOptions{})
		if err != nil {
			return false, fmt.Errorf("failed to create reboot-required-remove pod: %w", err)
		}
		defer func() {
			err := r.clientSet.CoreV1().Pods(r.namespace).Delete(ctx, pod.Name, metav1.DeleteOptions{})
			if err != nil {
				l.Error("failed to delete pod", zap.Error(err))
			}
		}()

		err = wait.PollUntilContextTimeout(ctx, time.Second, 30*time.Second, false, IsPodCompleted(r.clientSet, pod.GetName(), pod.GetNamespace()))
		if err != nil {
			return false, fmt.Errorf("pod did not complete while waiting: %w", err)
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
		if nodeReady {
			l.Debug("Cleanup reboot container")
			if err := r.CleanupNode(ctx, kubeNode.Name); err != nil {
				return false, fmt.Errorf("failed to cleanup node after reboot: %w", err)
			}
		}
		return nodeReady, nil
	}

	return false, nil
}

func (r *RebootManager) CleanupNode(ctx context.Context, nodeName string) error {
	if nodeName == "" {
		return fmt.Errorf("node name is required")
	}
	var labelSelector labels.Selector = labels.ValidatedSetSelector{}

	requirement, err := labels.NewRequirement(LabelComponent, selection.In, []string{"reboot-required", "reboot"})
	if err != nil {
		return err
	}
	labelSelector = labelSelector.Add(*requirement)
	options := metav1.ListOptions{
		LabelSelector: labelSelector.String(),
		FieldSelector: fmt.Sprintf("spec.nodeName=%s", nodeName),
	}

	err = r.clientSet.CoreV1().Pods(r.namespace).DeleteCollection(ctx, metav1.DeleteOptions{}, options)
	if err != nil {
		return fmt.Errorf("failed to delete pods: %w", err)
	}
	return nil
}
