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

	drainv1 "github.com/slyngdk/node-drain/api/v1"
	"go.uber.org/zap"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

// NodeReconciler reconciles a Node object
type nodeReconciler struct {
	client.Client
	Scheme *runtime.Scheme
	l      *zap.SugaredLogger
}

func NewNodeReconciler(client client.Client, schema *runtime.Scheme, managerNamespace string) (*nodeReconciler, error) {
	l := zap.S().Named("node")

	return &nodeReconciler{
		Client: client,
		Scheme: schema,
		l:      l,
	}, nil
}

// +kubebuilder:rbac:groups=drain.k8s.slyng.dk,resources=nodes,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=drain.k8s.slyng.dk,resources=nodes/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=drain.k8s.slyng.dk,resources=nodes/finalizers,verbs=update

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
	r.l.Info("node reconcile", "request", req)

	node := &drainv1.Node{}
	if err := r.Get(ctx, req.NamespacedName, node); err != nil {
		err = client.IgnoreNotFound(err)
		if err != nil {
			r.l.Error(err, "unable to fetch Node")
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

	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *nodeReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&drainv1.Node{}).
		Complete(r)
}
