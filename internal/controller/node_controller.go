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
	"context"
	"fmt"
	"time"

	"github.com/slyngdk/node-drain/internal/config"
	"github.com/slyngdk/node-drain/internal/utils"
	"k8s.io/client-go/rest"

	drainv1 "github.com/slyngdk/node-drain/api/v1"
	"go.uber.org/zap"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

const (
	nodeDrainFinalizer = "nodedrain.k8s.slyng.dk/node"
)

// NodeReconciler reconciles a Node object
type nodeReconciler struct {
	client.Client
	Scheme           *runtime.Scheme
	l                *zap.Logger
	managerNamespace string
	rebootManager    *utils.RebootManager
}

func NewNodeReconciler(client client.Client, schema *runtime.Scheme, restConfig *rest.Config, managerNamespace string) (*nodeReconciler, error) {
	l, err := config.GetNamedLogger("node")
	if err != nil {
		return nil, err
	}

	rebootManager, err := utils.NewRebootManager(l, client, restConfig, managerNamespace)
	if err != nil {
		return nil, fmt.Errorf("failed to create reboot manager: %w", err)
	}

	return &nodeReconciler{
		Client:           client,
		Scheme:           schema,
		l:                l,
		managerNamespace: managerNamespace,
		rebootManager:    rebootManager,
	}, nil
}

// +kubebuilder:rbac:groups="",resources=nodes,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups="",resources=nodes/status,verbs=get
// +kubebuilder:rbac:groups=drain.k8s.slyng.dk,resources=nodes,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=drain.k8s.slyng.dk,resources=nodes/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=drain.k8s.slyng.dk,resources=nodes/finalizers,verbs=update
// +kubebuilder:rbac:groups="",namespace=system,resources=pods,verbs=list;watch;create;get;delete;deletecollection

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
		err = client.IgnoreNotFound(err)
		if err != nil {
			l.With(zap.Error(err)).Error("unable to fetch kube node")
		}
		// we'll ignore not-found errors, since they can't be fixed by an immediate
		// requeue (we'll need to wait for a new notification), and we can get them
		// on deleted requests.
		return ctrl.Result{}, err
	}

	node := &drainv1.Node{}
	if err := r.Get(ctx, req.NamespacedName, node); err != nil {
		if apierrors.IsNotFound(err) {
			// Create node as it is missing

			state := drainv1.NodeStateActive
			if kubeNode.Spec.Unschedulable {
				state = drainv1.NodeStateCordoned
			}

			node = &drainv1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name:       kubeNode.Name,
					Finalizers: nil,
				},
				Spec: drainv1.NodeSpec{
					State: state,
				},
			}

			err = controllerutil.SetOwnerReference(kubeNode, node, r.Scheme)
			if err != nil {
				return ctrl.Result{}, err
			}

			controllerutil.AddFinalizer(node, nodeDrainFinalizer)

			err = r.Create(ctx, node)
			if err != nil {
				return ctrl.Result{}, err
			}
			return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
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

	if !kubeNode.Spec.Unschedulable && node.Status.Drained {
		l.Debug("node is not unschedulable, but is still drained, updating drain status.")
		if result, err := r.unsetDrained(ctx, node); err != nil {
			return result, err
		}
	}

	if node.Spec.State != node.Status.CurrentState {
		l.With(zap.String("state", node.Spec.State.String()), zap.String("currentState", node.Status.CurrentState.String())).Info("node state have changed state, since last reconcile.")
		switch node.Spec.State {
		case drainv1.NodeStateActive:
			if node.Status.Drained {
				if result, err := r.unsetDrained(ctx, node); err != nil {
					return result, err
				}
			}
			if kubeNode.Spec.Unschedulable {
				patch := client.MergeFrom(kubeNode.DeepCopy())
				kubeNode.Spec.Unschedulable = false
				if err := r.Patch(ctx, kubeNode, patch); err != nil {
					return ctrl.Result{}, err
				}
				return ctrl.Result{}, nil
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
		case drainv1.NodeStateDrained:
			if !kubeNode.Spec.Unschedulable {
				patch := client.MergeFrom(kubeNode.DeepCopy())
				kubeNode.Spec.Unschedulable = true
				if err := r.Patch(ctx, kubeNode, patch); err != nil {
					return ctrl.Result{}, err
				}
				return ctrl.Result{}, nil
			}
		}
		if result, err := r.setCurrentState(ctx, node); err != nil {
			return result, err
		}
	}

	// Check reboot
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
			return ctrl.Result{}, fmt.Errorf("failed to check if reboot is required: %w", err)
		}
		// TODO change to use conditions
		patch := client.MergeFrom(node.DeepCopy())
		node.Status.RebootRequired = utils.PtrTo(required)
		node.Status.RebootRequiredLastChecked = &metav1.Time{Time: time.Now()}
		if err = r.Status().Patch(ctx, node, patch); err != nil {
			return ctrl.Result{}, fmt.Errorf("failed to update node reboot required last checked: %w", err)
		}
	}

	return ctrl.Result{}, nil
}

func (r *nodeReconciler) unsetDrained(ctx context.Context, node *drainv1.Node) (ctrl.Result, error) {
	patch := client.MergeFrom(node.DeepCopy())
	node.Status.Drained = false
	if err := r.Status().Patch(ctx, node, patch); err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to update node: %w", err)
	}
	return ctrl.Result{}, nil
}

func (r *nodeReconciler) setCurrentState(ctx context.Context, node *drainv1.Node) (ctrl.Result, error) {
	patch := client.MergeFrom(node.DeepCopy())
	node.Status.CurrentState = node.Spec.State
	if err := r.Status().Patch(ctx, node, patch); err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to update node with current state: %w", err)
	}
	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *nodeReconciler) SetupWithManager(mgr ctrl.Manager) error {
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
