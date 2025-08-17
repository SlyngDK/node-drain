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
	"errors"
	"fmt"
	"os"
	"time"

	"k8s.io/apimachinery/pkg/types"
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
	nodeDrainFinalizer = "nodedrain.k8s.slyng.dk/node"

	currentStateField = "status.currentState"
)

// +kubebuilder:rbac:groups="",resources=nodes,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups="",resources=nodes/status,verbs=get
// +kubebuilder:rbac:groups=drain.k8s.slyng.dk,resources=nodes,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=drain.k8s.slyng.dk,resources=nodes/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=drain.k8s.slyng.dk,resources=nodes/finalizers,verbs=update
// +kubebuilder:rbac:groups="",namespace=system,resources=pods,verbs=list;watch;create;get;delete;deletecollection
// +kubebuilder:rbac:groups="",resources=pods,verbs=list;delete;get
// +kubebuilder:rbac:groups="",resources=pods/eviction,verbs=create
// +kubebuilder:rbac:groups="apps",resources=daemonsets,verbs=get;

// NodeReconciler reconciles a Node object
type nodeReconciler struct {
	client.Client
	Scheme           *runtime.Scheme
	restConfig       *rest.Config
	l                *zap.Logger
	managerNamespace string
	nodeName         string
	rebootManager    *utils.RebootManager
}

func NewNodeReconciler(client client.Client, schema *runtime.Scheme, restConfig *rest.Config, managerNamespace string, nameNode string) (*nodeReconciler, error) {
	l, err := config.GetNamedLogger("node")
	if err != nil {
		return nil, err
	}

	rebootManager, err := utils.NewRebootManager(l, client, restConfig, managerNamespace)
	if err != nil {
		return nil, fmt.Errorf("failed to create reboot manager: %w", err)
	}

	drainManager, err := utils.NewDrainManager()
	if err != nil {
		return nil, fmt.Errorf("failed to create drain manager: %w", err)
	}

	if err = drainManager.LoadPluginsFromDir(); err != nil {
		return nil, fmt.Errorf("failed to load plugins: %w", err)
	}
	if err = drainManager.InitPlugins(context.TODO()); err != nil {
		return nil, fmt.Errorf("failed to initialize plugins: %w", err)
	}

	return &nodeReconciler{
		Client:           client,
		Scheme:           schema,
		restConfig:       restConfig,
		l:                l,
		managerNamespace: managerNamespace,
		nodeName:         nameNode,
		rebootManager:    rebootManager,
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
// TODO(user): Modify the Reconcile function to compare the state specified by
// the Node object against the actual cluster state, and then
// perform operations to make the cluster state reflect the state specified by
// the user.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.19.0/pkg/reconcile
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
		if err != nil {
			l.With(zap.Error(err)).Error("unable to fetch node")
		}
		// we'll ignore not-found errors, since they can't be fixed by an immediate
		// requeue (we'll need to wait for a new notification), and we can get them
		// on deleted requests.
		return ctrl.Result{}, err
	}

	if !node.DeletionTimestamp.IsZero() {
		// The object is being deleted
		if controllerutil.ContainsFinalizer(node, nodeDrainFinalizer) {
			// our finalizer is present, so lets handle any external dependency
			// TODO Handle if node is drained, etc ...

			// remove our finalizer from the list and update it.
			controllerutil.RemoveFinalizer(node, nodeDrainFinalizer)
			if err := r.Update(ctx, node); err != nil {
				return ctrl.Result{}, err
			}
		}

		// Stop reconciliation as the item is being deleted
		return ctrl.Result{}, nil
	}

	if (!kubeNode.Spec.Unschedulable || node.Spec.State == drainv1.NodeStateActive) && node.Status.Drained {
		l.Debug("node is not unschedulable, but is still drained, updating drain status.")
		if err := r.setDrained(ctx, node, false); err != nil {
			return ctrl.Result{}, err
		}
	}

	switch node.Spec.State {
	case drainv1.NodeStateActive:
		if kubeNode.Spec.Unschedulable {
			patch := client.MergeFrom(kubeNode.DeepCopy())
			kubeNode.Spec.Unschedulable = false
			if err := r.Patch(ctx, kubeNode, patch); err != nil {
				return ctrl.Result{}, err
			}
			return ctrl.Result{}, nil
		}
		if err := r.setCurrentState(ctx, l, node, drainv1.NodeCurrentStateOk); err != nil {
			return ctrl.Result{}, err
		}
	case drainv1.NodeStateCordoned:
		if !kubeNode.Spec.Unschedulable {
			patch := client.MergeFrom(kubeNode.DeepCopy())
			kubeNode.Spec.Unschedulable = true
			if err := r.Patch(ctx, kubeNode, patch); err != nil {
				return ctrl.Result{}, err
			}
			return ctrl.Result{}, nil
		}
		if err := r.setCurrentState(ctx, l, node, drainv1.NodeCurrentStateCordoned); err != nil {
			return ctrl.Result{}, err
		}
	case drainv1.NodeStateDrained:
		if !node.Status.CurrentState.WorkState() && node.Status.CurrentState != drainv1.NodeCurrentStateQueued {
			if err := r.setCurrentState(ctx, l, node, drainv1.NodeCurrentStateQueued); err != nil {
				return ctrl.Result{}, err
			}
			return ctrl.Result{RequeueAfter: 1 * time.Second}, nil
		}
	}

	// Check reboot
	if err := r.checkRebootRequired(ctx, node, l); err != nil {
		return ctrl.Result{}, err
	}

	if node.Status.CurrentState.WorkState() {
		if node.Spec.State == drainv1.NodeStateDrained && !node.Status.Drained {
			result, err := r.drain(ctx, l, node, kubeNode)
			if err != nil {
				return ctrl.Result{}, err
			}
			if result != nil {
				return *result, nil
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
	l.Info("setting current state on node", zap.String("currentState", s.String()))
	patch := client.MergeFrom(node.DeepCopy())
	node.Status.CurrentState = s
	if err := r.Status().Patch(ctx, node, patch); err != nil {
		return fmt.Errorf("failed to update current state on node: %w", err)
	}
	return nil
}

func (r *nodeReconciler) checkRebootRequired(ctx context.Context, node *drainv1.Node, l *zap.Logger) error {
	rebootCheckInterval := config.GetKoanf().Duration("reboot.checkInterval")
	if rebootCheckInterval < 5*time.Minute {
		rebootCheckInterval = 24 * time.Hour
	}
	if node.Status.RebootRequiredLastChecked == nil ||
		node.Status.RebootRequiredLastChecked.Time.IsZero() ||
		node.Status.RebootRequiredLastChecked.Time.Before(time.Now().Add(-rebootCheckInterval)) {
		l.Debug("Checking if reboot is required")
		required, err := r.rebootManager.IsRebootRequired(ctx, node.Name)
		if err != nil {
			return fmt.Errorf("failed to check if reboot is required: %w", err)
		}
		// TODO change to use conditions
		patch := client.MergeFrom(node.DeepCopy())
		node.Status.RebootRequired = utils.PtrTo(required)
		node.Status.RebootRequiredLastChecked = &metav1.Time{Time: time.Now()}
		if err = r.Status().Patch(ctx, node, patch); err != nil {
			return fmt.Errorf("failed to update node reboot required last checked: %w", err)
		}
	}
	return nil
}

func (r *nodeReconciler) drain(ctx context.Context, l *zap.Logger, node *drainv1.Node, kubeNode *corev1.Node) (*ctrl.Result, error) {
	if node.Spec.State != drainv1.NodeStateDrained || node.Status.Drained {
		return nil, nil
	}

	if node.Status.CurrentState == drainv1.NodeCurrentStateNext {
		err := r.setCurrentState(ctx, l, node, drainv1.NodeCurrentStateDraining)
		if err != nil {
			return nil, err
		}
	}

	if !kubeNode.Spec.Unschedulable {
		l.Info("Disable scheduling on node")
		patch := client.MergeFrom(kubeNode.DeepCopy())
		kubeNode.Spec.Unschedulable = true
		if err := r.Patch(ctx, kubeNode, patch); err != nil {
			return nil, err
		}
	}

	if r.nodeName == node.Name {
		l.Info("Running on the node which is about to be drained")
		// TODO stop this controller after Cordon of the node
		err := r.rescheduleController(ctx)
		if err != nil {
			return nil, fmt.Errorf("failed to reschedule controller: %w", err)
		}
		return &ctrl.Result{RequeueAfter: 5 * time.Minute}, nil
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

	// if dryRun {
	//	drainHelper.DryRunStrategy = cmdutil.DryRunServer
	// }

	l.Info("draining node")

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

	return nil, nil
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
