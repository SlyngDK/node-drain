/*
Copyright 2024.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controller

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"time"

	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"k8s.io/client-go/kubernetes"
	"k8s.io/kubectl/pkg/drain"

	"github.com/slyngdk/node-drain/internal/config"
	"github.com/slyngdk/node-drain/internal/utils"
	"k8s.io/client-go/rest"

	drainv1 "github.com/slyngdk/node-drain/api/v1"
	"go.uber.org/zap"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

const (
	nodeDrainFinalizer = utils.LabelPrefix + "/node"

	currentStateField = "status.currentState"
)

// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch
// +kubebuilder:rbac:groups="",resources=nodes,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups="",resources=nodes/status,verbs=get
// +kubebuilder:rbac:groups=drain.k8s.slyng.dk,resources=nodes,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=drain.k8s.slyng.dk,resources=nodes/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=drain.k8s.slyng.dk,resources=nodes/finalizers,verbs=update
// +kubebuilder:rbac:groups="",namespace=system,resources=pods,verbs=list;watch;create;get;delete;deletecollection
// +kubebuilder:rbac:groups="",namespace=system,resources=pods/exec,verbs=create
// +kubebuilder:rbac:groups="",resources=pods,verbs=list;delete;get
// +kubebuilder:rbac:groups="",resources=pods/eviction,verbs=create
// +kubebuilder:rbac:groups=apps,resources=daemonsets,verbs=get;

// NodeReconciler reconciles a Node object
type nodeReconciler struct {
	client.Client
	Scheme           *runtime.Scheme
	restConfig       *rest.Config
	recorder         record.EventRecorder
	l                *zap.Logger
	managerNamespace string
	nodeName         string
	rebootManager    *utils.RebootManager
	drainManager     *utils.DrainManager
}

func NewNodeReconciler(ctx context.Context, client client.Client, schema *runtime.Scheme, restConfig *rest.Config, recorder record.EventRecorder, managerNamespace string, nameNode string) (*nodeReconciler, error) {
	l, err := config.GetNamedLogger("node")
	if err != nil {
		return nil, err
	}

	rebootManager, err := utils.NewRebootManager(l, client, restConfig, recorder, managerNamespace)
	if err != nil {
		return nil, fmt.Errorf("failed to create reboot manager: %w", err)
	}

	drainManager, err := utils.NewDrainManager(ctx, client, restConfig, recorder)
	if err != nil {
		return nil, fmt.Errorf("failed to create drain manager: %w", err)
	}

	return &nodeReconciler{
		Client:           client,
		Scheme:           schema,
		restConfig:       restConfig,
		recorder:         recorder,
		l:                l,
		managerNamespace: managerNamespace,
		nodeName:         nameNode,
		rebootManager:    rebootManager,
		drainManager:     drainManager,
	}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *nodeReconciler) SetupWithManager(mgr ctrl.Manager) error {
	if err := mgr.GetFieldIndexer().IndexField(context.Background(), &drainv1.Node{}, currentStateField, func(rawObj client.Object) []string {
		node := rawObj.(*drainv1.Node)
		if node.Status.CurrentState == "" {
			return nil
		}
		return []string{node.Status.CurrentState.String()}
	}); err != nil {
		return err
	}

	return ctrl.NewControllerManagedBy(mgr).
		For(&drainv1.Node{}).
		Watches(
			&corev1.Node{},
			handler.EnqueueRequestsFromMapFunc(func(ctx context.Context, object client.Object) []reconcile.Request {
				node := object.(*corev1.Node)
				return []reconcile.Request{
					{
						NamespacedName: types.NamespacedName{
							Name: node.Name,
						},
					},
				}
			}),
			builder.WithPredicates(predicate.ResourceVersionChangedPredicate{}),
		).
		Complete(r)
}

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.19.0/pkg/reconcile
// nolint:gocyclo
func (r *nodeReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	l := r.l.With(zap.String("node.name", req.Name))
	l.Debug("node reconcile")

	kubeNode := &corev1.Node{}
	if err := r.Get(ctx, req.NamespacedName, kubeNode); err != nil {
		l.With(zap.Error(err)).Error("unable to fetch kube node")
		return ctrl.Result{}, err
	}

	node := &drainv1.Node{}
	if err := r.Get(ctx, req.NamespacedName, node); err != nil {
		if apierrors.IsNotFound(err) {
			// Create node as it is missing
			return r.createNewNode(ctx, kubeNode)
		}
		l.With(zap.Error(err)).Error("unable to fetch node")
		// we'll ignore not-found errors, since they can't be fixed by an immediate
		// requeue (we'll need to wait for a new notification), and we can get them
		// on deleted requests.
		return ctrl.Result{}, err
	}

	if !node.DeletionTimestamp.IsZero() {
		// The object is being deleted
		if controllerutil.ContainsFinalizer(node, nodeDrainFinalizer) {
			// our finalizer is present, so lets handle any external dependency
			// TODO Handle if node is drained, k8s node removed, etc ...

			// remove our finalizer from the list and update it.
			controllerutil.RemoveFinalizer(node, nodeDrainFinalizer)
			if err := r.Update(ctx, node); err != nil {
				return ctrl.Result{}, err
			}
		}

		// Stop reconciliation as the item is being deleted
		return ctrl.Result{}, nil
	}

	if ok, result, err := r.undrain(ctx, l, node, kubeNode); !ok {
		return result, err
	}

	if !kubeNode.Spec.Unschedulable && node.Status.Drained {
		l.Debug("node is not unschedulable, but is still drained, updating drain status.")
		if err := r.setDrained(ctx, node, false); err != nil {
			return ctrl.Result{}, err
		}
	}

	// Check reboot
	if err := r.checkRebootRequired(ctx, node, l); err != nil {
		return ctrl.Result{}, err
	}

	switch node.Spec.State {
	case drainv1.NodeStateActive:
		managerOfUnschedulable := r.getManagerOfUnschedulable(kubeNode)
		if managerOfUnschedulable != "" && managerOfUnschedulable != utils.GetFieldOwner(r.managerNamespace) {
			l.Warn("node is current cordon by another manager", zap.String("manager", managerOfUnschedulable))
			r.recorder.Eventf(node, corev1.EventTypeWarning, "NodeNotSchedulable", "Node is current cordon by another manager: %s", managerOfUnschedulable)
			return ctrl.Result{}, nil
		}
		if err := r.setUnschedulable(ctx, kubeNode, false); err != nil {
			return ctrl.Result{}, err
		}
		if err := r.setCurrentState(ctx, l, node, drainv1.NodeCurrentStateOk); err != nil {
			return ctrl.Result{}, err
		}
		_ = r.rebootManager.CleanupNode(ctx, node.Name)
	case drainv1.NodeStateCordoned:
		if err := r.setUnschedulable(ctx, kubeNode, true); err != nil {
			return ctrl.Result{}, err
		}
		if err := r.setCurrentState(ctx, l, node, drainv1.NodeCurrentStateCordoned); err != nil {
			return ctrl.Result{}, err
		}
	case drainv1.NodeStateDrained, drainv1.NodeStateRebootIfRequired:
		if !node.Status.CurrentState.WorkState() && node.Status.CurrentState != drainv1.NodeCurrentStateQueued {
			if err := r.setCurrentState(ctx, l, node, drainv1.NodeCurrentStateQueued); err != nil {
				return ctrl.Result{}, err
			}
			return ctrl.Result{RequeueAfter: 1 * time.Second}, nil
		}
	}

	if node.Status.CurrentState.WorkState() {
		if node.Spec.State == drainv1.NodeStateRebootIfRequired &&
			(node.Status.CurrentState == drainv1.NodeCurrentStateNext ||
				node.Status.CurrentState == drainv1.NodeCurrentStateDrained) {
			var err error
			required := false
			if node.Status.RebootRequiredLastChecked == nil ||
				node.Status.RebootRequiredLastChecked.Time.IsZero() ||
				node.Status.RebootRequiredLastChecked.Time.Before(time.Now().Add(-(2 * time.Minute))) {

				required, err = r.isRebootRequired(ctx, node, l)
				if err != nil {
					return ctrl.Result{}, err
				}
			} else if node.Status.RebootRequired != nil {
				required = *node.Status.RebootRequired
			}

			if !required {
				l.Debug("Reboot is not required, setting node active")
				err = r.setState(ctx, l, node, drainv1.NodeStateActive)
				if err != nil {
					return ctrl.Result{}, err
				}
				return ctrl.Result{}, nil
			}
		}
		if node.Spec.State.Drain() && !node.Status.Drained {
			result, err := r.drain(ctx, l, node, kubeNode)
			if err != nil {
				return ctrl.Result{}, err
			}
			if result != nil {
				return *result, nil
			}
		}

		if node.Spec.State == drainv1.NodeStateRebootIfRequired {
			if node.Status.CurrentState == drainv1.NodeCurrentStateDrained && node.Status.Drained {
				l.Debug("Saving bootID before rebooting node")
				err := r.setBootID(ctx, node, kubeNode)
				if err != nil {
					return ctrl.Result{}, err
				}

				l.Info("Rebooting node")
				err = r.rebootManager.RebootNode(ctx, node)
				if err != nil {
					return ctrl.Result{}, err
				}

				err = r.setCurrentState(ctx, l, node, drainv1.NodeCurrentStateRebooting)
				if err != nil {
					return ctrl.Result{}, err
				}
				return ctrl.Result{RequeueAfter: time.Minute}, nil
			}
			if node.Status.CurrentState == drainv1.NodeCurrentStateRebooting {
				rebooted, err := r.rebootManager.IsNodeRebooted(ctx, kubeNode, node.Status.BootID)
				if err != nil {
					return ctrl.Result{}, err
				}

				if rebooted {
					l.Info("node is rebooted")

					patch := client.MergeFrom(node.DeepCopy())
					node.Status.BootID = ""
					node.Status.RebootRequired = utils.PtrTo(false)
					node.Status.RebootRequiredLastChecked = &metav1.Time{Time: time.Now()}
					if err = r.Status().Patch(ctx, node, patch); err != nil {
						return ctrl.Result{}, fmt.Errorf("failed to update node reboot required last checked: %w", err)
					}

					err = r.setState(ctx, l, node, drainv1.NodeStateActive)
					if err != nil {
						return ctrl.Result{}, err
					}
					r.recorder.Eventf(node, corev1.EventTypeNormal, "Reboot", "Node Rebooted")
					return ctrl.Result{RequeueAfter: time.Second}, nil
				} else {
					l.Info("Node not ready yet after reboot")
					return ctrl.Result{RequeueAfter: time.Minute}, nil
				}
			}
		}
	} else if node.Status.CurrentState == drainv1.NodeCurrentStateQueued {
		l.Debug("Checking if queued node is next")
		next, err := r.isNextNode(ctx, l, node)
		if err != nil {
			return ctrl.Result{}, err
		}
		if next {
			return ctrl.Result{RequeueAfter: 1 * time.Second}, nil
		} else {
			return ctrl.Result{RequeueAfter: 1 * time.Minute}, nil
		}
	}

	return ctrl.Result{}, nil
}

func (r *nodeReconciler) createNewNode(ctx context.Context, kubeNode *corev1.Node) (ctrl.Result, error) {
	state := drainv1.NodeStateActive
	if kubeNode.Spec.Unschedulable {
		state = drainv1.NodeStateCordoned
	}

	node := &drainv1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name:       kubeNode.Name,
			Finalizers: nil,
		},
		Spec: drainv1.NodeSpec{
			State: state,
		},
	}

	if err := controllerutil.SetOwnerReference(kubeNode, node, r.Scheme); err != nil {
		return ctrl.Result{}, err
	}

	controllerutil.AddFinalizer(node, nodeDrainFinalizer)

	if err := r.Create(ctx, node); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
}

func (r *nodeReconciler) setDrained(ctx context.Context, node *drainv1.Node, drained bool) error {
	patch := client.MergeFrom(node.DeepCopy())
	node.Status.Drained = drained
	if err := r.Status().Patch(ctx, node, patch); err != nil {
		return fmt.Errorf("failed to update drained status on node: %w", err)
	}
	return nil
}

func (r *nodeReconciler) setCurrentState(ctx context.Context, l *zap.Logger, node *drainv1.Node, s drainv1.NodeCurrentState) error {
	if node.Status.CurrentState == s {
		return nil
	}

	l.Info("setting current state on node", zap.String("currentState", s.String()))
	patch := client.MergeFrom(node.DeepCopy())
	node.Status.CurrentState = s
	if err := r.Status().Patch(ctx, node, patch); err != nil {
		return fmt.Errorf("failed to update current state on node: %w", err)
	}
	return nil
}

func (r *nodeReconciler) setState(ctx context.Context, l *zap.Logger, node *drainv1.Node, s drainv1.NodeState) error {
	if node.Spec.State == s {
		return nil
	}

	l.Info("setting state on node", zap.String("state", s.String()))
	patch := client.MergeFrom(node.DeepCopy())
	node.Spec.State = s
	if err := r.Patch(ctx, node, patch); err != nil {
		return fmt.Errorf("failed to update state on node: %w", err)
	}
	return nil
}

func (r *nodeReconciler) setUnschedulable(ctx context.Context, kubeNode *corev1.Node, unschedulable bool) error {
	if kubeNode.Spec.Unschedulable != unschedulable {
		patch := client.MergeFrom(kubeNode.DeepCopy())
		kubeNode.Spec.Unschedulable = unschedulable
		if err := r.Patch(ctx, kubeNode, patch); err != nil {
			return fmt.Errorf("failed to update unschedulable status on node: %w", err)
		}
	}
	return nil
}

func (r *nodeReconciler) setBootID(ctx context.Context, node *drainv1.Node, kubeNode *corev1.Node) error {
	if kubeNode.Status.NodeInfo.BootID != node.Status.BootID {
		patch := client.MergeFrom(node.DeepCopy())
		node.Status.BootID = kubeNode.Status.NodeInfo.BootID
		if err := r.Status().Patch(ctx, node, patch); err != nil {
			return fmt.Errorf("failed to update bootID on node: %w", err)
		}
	}
	return nil
}

func (r *nodeReconciler) checkRebootRequired(ctx context.Context, node *drainv1.Node, l *zap.Logger) error {
	// Only check if reboot is required, when not doing work
	if node.Status.CurrentState.WorkState() {
		return nil
	}
	rebootCheckInterval := config.GetKoanf().Duration("reboot.checkInterval")
	if rebootCheckInterval < 5*time.Minute {
		rebootCheckInterval = 24 * time.Hour
	}
	if node.Status.RebootRequiredLastChecked == nil ||
		node.Status.RebootRequiredLastChecked.Time.IsZero() ||
		node.Status.RebootRequiredLastChecked.Time.Before(time.Now().Add(-rebootCheckInterval)) {
		_, err := r.isRebootRequired(ctx, node, l)
		if err != nil {
			return err
		}
	}
	return nil
}

func (r *nodeReconciler) isRebootRequired(ctx context.Context, node *drainv1.Node, l *zap.Logger) (bool, error) {
	l.Debug("Checking if reboot is required")
	required, err := r.rebootManager.IsRebootRequired(ctx, node.Name)
	if err != nil {
		return false, fmt.Errorf("failed to check if reboot is required: %w", err)
	}
	// TODO change to use conditions
	patch := client.MergeFrom(node.DeepCopy())
	node.Status.RebootRequired = utils.PtrTo(required)
	node.Status.RebootRequiredLastChecked = &metav1.Time{Time: time.Now()}
	if err = r.Status().Patch(ctx, node, patch); err != nil {
		return false, fmt.Errorf("failed to update node reboot required last checked: %w", err)
	}
	if required {
		r.recorder.Eventf(node, corev1.EventTypeNormal, "RebootRequired", "Reboot is required")
	}
	return required, nil
}

func (r *nodeReconciler) drain(ctx context.Context, l *zap.Logger, node *drainv1.Node, kubeNode *corev1.Node) (*ctrl.Result, error) {
	if !node.Spec.State.Drain() || node.Status.Drained {
		return nil, nil
	}

	if node.Status.CurrentState == drainv1.NodeCurrentStateNext {

		// Check if cluster is healthy before starting drain
		healthy, err := r.drainManager.IsHealthy(ctx, node)
		if err != nil {
			return nil, fmt.Errorf("failed to check if cluster is healthy: %w", err)
		}
		if !healthy {
			return nil, fmt.Errorf("cluster is not healthy")
		}

		// Check if drain of node is ok
		drainOk, err := r.drainManager.IsDrainOk(ctx, node)
		if err != nil {
			return nil, fmt.Errorf("failed to check if node(%s) is ok to drain: %w", node.Name, err)
		}
		if !drainOk {
			return nil, fmt.Errorf("node(%s) is not ok to drain", node.Name)
		}

		err = r.setCurrentState(ctx, l, node, drainv1.NodeCurrentStateDraining)
		if err != nil {
			return nil, err
		}
	}

	if !kubeNode.Spec.Unschedulable {
		l.Info("Disable scheduling on node")
		if err := r.setUnschedulable(ctx, kubeNode, true); err != nil {
			return nil, err
		}
	}

	if r.nodeName == node.Name {
		l.Info("Running on the node which is about to be drained")
		err := r.rescheduleController(ctx)
		if err != nil {
			return nil, fmt.Errorf("failed to reschedule controller: %w", err)
		}
		return &ctrl.Result{RequeueAfter: 5 * time.Minute}, nil
	}

	// Run Plugin PreDrain
	err := r.drainManager.RunPreDrain(ctx, node)
	if err != nil {
		return nil, fmt.Errorf("failed to run plugin PreDrain for node(%s): %w", node.Name, err)
	}

	// TODO Ensure node is drained

	clientSet, err := kubernetes.NewForConfig(r.restConfig)
	if err != nil {
		return nil, err
	}

	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)

	drainHelper := &drain.Helper{
		Ctx:                 ctx,
		Client:              clientSet,
		GracePeriodSeconds:  -1, // Wait for pod's terminationGracePeriodSeconds
		IgnoreAllDaemonSets: true,
		Timeout:             60 * time.Second,
		DeleteEmptyDirData:  true,
		Out:                 stdout,
		ErrOut:              stderr,
	}

	l.Info("draining node")

	err = r.rebootManager.CleanupNode(ctx, node.Name)
	if err != nil {
		return nil, fmt.Errorf("failed to cleanup node(%s) for reboot manager pods: %w", node.Name, err)
	}

	err = drain.RunNodeDrain(drainHelper, node.Name)
	if err != nil {
		l.Error("failed to drain node",
			zap.String("stdout", stdout.String()),
			zap.String("stderr", stderr.String()))
		return nil, errors.Join(fmt.Errorf("failed to drain node: %s", node.Name), err)
	}

	l.Info("drained node",
		zap.String("stdout", stdout.String()),
		zap.String("stderr", stderr.String()))

	patch := client.MergeFrom(node.DeepCopy())
	node.Status.Drained = true
	node.Status.CurrentState = drainv1.NodeCurrentStateDrained
	if err := r.Status().Patch(ctx, node, patch); err != nil {
		return nil, fmt.Errorf("failed to update drained status on node: %w", err)
	}

	r.recorder.Eventf(node, corev1.EventTypeNormal, "Drained", "Node drained successfully")
	return nil, nil
}

func (r *nodeReconciler) undrain(ctx context.Context, l *zap.Logger, node *drainv1.Node, kubeNode *corev1.Node) (bool, ctrl.Result, error) {
	if node.Status.CurrentState == drainv1.NodeCurrentStateUndraining {
		if kubeNode.Spec.Unschedulable {
			healthy, err := r.drainManager.IsClusterNodesHealthy(ctx)
			if err != nil {
				l.Error("error checking if cluster is healthy", zap.Error(err))
				return false, ctrl.Result{RequeueAfter: 30 * time.Second}, nil
			}
			if !healthy {
				l.Debug("cluster nodes is not healthy, waiting for nodes to be healthy before enabling scheduling")
				return false, ctrl.Result{RequeueAfter: 30 * time.Second}, nil
			}

			if err = r.setUnschedulable(ctx, kubeNode, false); err != nil {
				return false, ctrl.Result{RequeueAfter: 30 * time.Second}, nil
			}
		}

		if err := r.drainManager.RunPostDrain(ctx, node); err != nil {
			l.Warn("failed to run PostDrain for node", zap.Error(err))
			return false, ctrl.Result{RequeueAfter: 30 * time.Second}, nil
		}

		if node.Status.Drained {
			if err := r.setDrained(ctx, node, false); err != nil {
				l.Warn("failed to set Drained=false for node", zap.Error(err))
				return false, ctrl.Result{RequeueAfter: 30 * time.Second}, nil
			}
		}

		if err := r.setCurrentState(ctx, l, node, drainv1.NodeCurrentStateOk); err != nil {
			l.Debug("failed to set current state on node", zap.Error(err))
			return false, ctrl.Result{}, err
		}

		r.recorder.Eventf(node, corev1.EventTypeNormal, "Undrain", "Undrained successfully")
		return true, ctrl.Result{}, nil
	}

	undrain := false
	if node.Status.CurrentState == drainv1.NodeCurrentStateDrained ||
		node.Status.CurrentState == drainv1.NodeCurrentStateDraining ||
		node.Status.CurrentState == drainv1.NodeCurrentStateRebooting {
		// Check need for undrain
		switch node.Spec.State {
		case drainv1.NodeStateActive:
			undrain = true
		case drainv1.NodeStateDrained, drainv1.NodeStateRebootIfRequired, drainv1.NodeStateCordoned:
			break
		default:
			return false, ctrl.Result{}, fmt.Errorf("unhandled state for undrain: %s", node.Spec.State)
		}
	}

	if undrain {
		err := r.setCurrentState(ctx, l, node, drainv1.NodeCurrentStateUndraining)
		if err != nil {
			return false, ctrl.Result{}, err
		}
		return false, ctrl.Result{RequeueAfter: 1}, nil
	}
	return true, ctrl.Result{}, nil
}

func (r *nodeReconciler) rescheduleController(ctx context.Context) error {
	clientset, err := kubernetes.NewForConfig(r.restConfig)
	if err != nil {
		return fmt.Errorf("failed to create clientset: %w", err)
	}
	podName, err := os.Hostname()
	if err != nil {
		return fmt.Errorf("failed to get hostname/podName: %w", err)
	}

	deletePolicy := metav1.DeletePropagationForeground
	return clientset.CoreV1().Pods(r.managerNamespace).Delete(ctx, podName, metav1.DeleteOptions{
		PropagationPolicy: &deletePolicy,
	})
}

func (r *nodeReconciler) isNextNode(ctx context.Context, l *zap.Logger, node *drainv1.Node) (bool, error) {
	nodeList := &drainv1.NodeList{}
	err := r.List(ctx, nodeList)
	if err != nil {
		return false, err
	}

	for _, n := range nodeList.Items {
		if n.Status.CurrentState.WorkState() {
			l.Debug("There is already a node doing work")
			return false, nil
		}
	}

	if node.Status.CurrentState == drainv1.NodeCurrentStateQueued {
		err = r.setCurrentState(ctx, l, node, drainv1.NodeCurrentStateNext)
		if err != nil {
			return false, err
		}
		return true, nil
	}

	return false, nil
}

func (r *nodeReconciler) getManagerOfUnschedulable(kubeNode *corev1.Node) string {
	for _, field := range kubeNode.ManagedFields {
		if field.Subresource != "" {
			continue
		}
		if field.FieldsV1 != nil {
			var f interface{}
			err := json.Unmarshal(field.FieldsV1.Raw, &f)
			if err != nil {
				continue
			}

			if m, ok := f.(map[string]interface{}); ok {
				if v, ok := m["f:spec"]; ok {
					if s, ok := v.(map[string]interface{}); ok {
						if _, ok := s["f:unschedulable"]; ok {
							return field.Manager
						}
					}
				}
			}
		}
	}
	return ""
}
