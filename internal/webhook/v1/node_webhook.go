/*
Copyright 2025.

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

package v1

import (
	"context"
	"fmt"

	"github.com/slyngdk/node-drain/internal/config"

	"go.uber.org/zap"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	drainv1 "github.com/slyngdk/node-drain/api/v1"
)

// SetupNodeWebhookWithManager registers the webhook for Node in the manager.
func SetupNodeWebhookWithManager(mgr ctrl.Manager) error {
	l, err := config.GetNamedLogger("node-webhook")
	if err != nil {
		return fmt.Errorf("failed to get logger: %w", err)
	}

	return ctrl.NewWebhookManagedBy(mgr).For(&drainv1.Node{}).
		WithValidator(&NodeCustomValidator{
			l: l,
		}).
		WithDefaulter(&NodeCustomDefaulter{
			l:      l,
			client: mgr.GetClient(),
		}, admission.DefaulterRemoveUnknownOrOmitableFields).
		Complete()

}

// +kubebuilder:webhook:path=/mutate-drain-k8s-slyng-dk-v1-node,mutating=true,failurePolicy=fail,sideEffects=None,groups=drain.k8s.slyng.dk,resources=nodes,verbs=create;update,versions=v1,name=mnode-v1.kb.io,admissionReviewVersions=v1

// NodeCustomDefaulter struct is responsible for setting default values on the custom resource of the
// Kind Node when those are created or updated.
//
// NOTE: The +kubebuilder:object:generate=false marker prevents controller-gen from generating DeepCopy methods,
// as it is used only for temporary operations and does not need to be deeply copied.
type NodeCustomDefaulter struct {
	l      *zap.Logger
	client client.Client
}

var _ webhook.CustomDefaulter = &NodeCustomDefaulter{}

// Default implements webhook.CustomDefaulter so a webhook will be registered for the Kind Node.
func (d *NodeCustomDefaulter) Default(ctx context.Context, obj runtime.Object) error {
	newNode, ok := obj.(*drainv1.Node)
	if !ok {
		return fmt.Errorf("expected an Node object but got %T", obj)
	}
	l := d.l.With(zap.String("name", newNode.Name))
	l.Debug("Defaulting for Node")

	oldNode := &drainv1.Node{}
	if err := d.client.Get(ctx, types.NamespacedName{Name: newNode.Name}, oldNode); err != nil {
		l.With(zap.Error(err)).Debug("Error getting Node")
		if !errors.IsNotFound(err) {
			return fmt.Errorf("could not find node %q: %w", newNode.Name, err)
		}
	}

	// Set default values
	d.applyDefaults(newNode)
	return nil
}

func (d *NodeCustomDefaulter) applyDefaults(node *drainv1.Node) {
	if node.Spec.State == "" {
		node.Spec.State = drainv1.NodeStateActive
	}
}

// NOTE: The 'path' attribute must follow a specific pattern and should not be modified directly here.
// Modifying the path for an invalid path can cause API server errors; failing to locate the webhook.
// +kubebuilder:webhook:path=/validate-drain-k8s-slyng-dk-v1-node,mutating=false,failurePolicy=fail,sideEffects=None,groups=drain.k8s.slyng.dk,resources=nodes,verbs=create;update,versions=v1,name=vnode-v1.kb.io,admissionReviewVersions=v1

// NodeCustomValidator struct is responsible for validating the Node resource
// when it is created, updated, or deleted.
//
// NOTE: The +kubebuilder:object:generate=false marker prevents controller-gen from generating DeepCopy methods,
// as this struct is used only for temporary operations and does not need to be deeply copied.
type NodeCustomValidator struct {
	l *zap.Logger
}

var _ webhook.CustomValidator = &NodeCustomValidator{}

// ValidateCreate implements webhook.CustomValidator so a webhook will be registered for the type Node.
func (v *NodeCustomValidator) ValidateCreate(_ context.Context, obj runtime.Object) (admission.Warnings, error) {
	node, ok := obj.(*drainv1.Node)
	if !ok {
		return nil, fmt.Errorf("expected a Node object but got %T", obj)
	}
	l := v.l.With(zap.String("name", node.GetName()))
	l.Info("Validation for Node upon creation")

	// TODO(user): fill in your validation logic upon object creation.

	return nil, nil
}

// ValidateUpdate implements webhook.CustomValidator so a webhook will be registered for the type Node.
func (v *NodeCustomValidator) ValidateUpdate(_ context.Context, oldObj, newObj runtime.Object) (admission.Warnings, error) {
	node, ok := newObj.(*drainv1.Node)
	if !ok {
		return nil, fmt.Errorf("expected a Node object for the newObj but got %T", newObj)
	}
	l := v.l.With(zap.String("name", node.GetName()))
	l.Info("Validation for Node upon update")

	// TODO(user): fill in your validation logic upon object update.

	return nil, nil
}

// ValidateDelete implements webhook.CustomValidator so a webhook will be registered for the type Node.
func (v *NodeCustomValidator) ValidateDelete(ctx context.Context, obj runtime.Object) (admission.Warnings, error) {
	node, ok := obj.(*drainv1.Node)
	if !ok {
		return nil, fmt.Errorf("expected a Node object but got %T", obj)
	}
	l := v.l.With(zap.String("name", node.GetName()))
	l.Info("Validation for Node upon deletion")

	// TODO(user): fill in your validation logic upon object deletion.

	return nil, nil
}
