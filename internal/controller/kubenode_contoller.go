package controller

import (
	"context"
	v1 "github.com/slyngdk/node-drain/api/v1"
	"go.uber.org/zap"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

const (
	nodeDrainFinalizer = "nodedrain.k8s.slyng.dk/node"
)

type KubeNodeReconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Recorder record.EventRecorder
}

// +kubebuilder:rbac:groups="",resources=nodes,verbs=get;list;watch;update;patch

func (r *KubeNodeReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	l := zap.S().Named("kubenode")

	l.Info("kube node reconcile", "request", req)

	node := &corev1.Node{}
	if err := r.Get(ctx, req.NamespacedName, node); err != nil {
		err = client.IgnoreNotFound(err)
		if err != nil {
			l.Error(err, "unable to fetch Node")
		}
		// we'll ignore not-found errors, since they can't be fixed by an immediate
		// requeue (we'll need to wait for a new notification), and we can get them
		// on deleted requests.
		return ctrl.Result{}, err
	}

	nodeCRD := &v1.Node{}
	if err := r.Get(ctx, req.NamespacedName, nodeCRD); err != nil {
		if apierrors.IsNotFound(err) {
			// Create node as it is missing

			state := v1.NodeStateActive
			if node.Spec.Unschedulable {
				state = v1.NodeStateCordoned
			}

			nodeCRD = &v1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name:       node.Name,
					Finalizers: nil,
				},
				Spec: v1.NodeSpec{
					State: state,
				},
			}

			err = controllerutil.SetOwnerReference(node, nodeCRD, r.Scheme)
			if err != nil {
				return ctrl.Result{}, err
			}

			controllerutil.AddFinalizer(nodeCRD, nodeDrainFinalizer)

			err := r.Create(ctx, nodeCRD)
			if err != nil {
				return ctrl.Result{}, err
			}
			return ctrl.Result{}, nil
		}
		err = client.IgnoreNotFound(err)
		if err != nil {
			l.Error(err, "unable to fetch Node")
		}
		// we'll ignore not-found errors, since they can't be fixed by an immediate
		// requeue (we'll need to wait for a new notification), and we can get them
		// on deleted requests.
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
}

func (r *KubeNodeReconciler) SetupWithManager(mgr ctrl.Manager) error {
	c := ctrl.NewControllerManagedBy(mgr).
		For(&corev1.Node{})
	c.Named("KubeNode")
	return c.
		Complete(r)
}
