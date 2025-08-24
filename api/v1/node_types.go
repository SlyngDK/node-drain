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

package v1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// EDIT THIS FILE!  THIS IS SCAFFOLDING FOR YOU TO OWN!
// NOTE: json tags are required.  Any new fields you add must have json tags for the fields to be serialized.

type NodeState string

func (c NodeState) String() string {
	return string(c)
}

const (
	NodeStateActive           NodeState = "Active"
	NodeStateCordoned         NodeState = "Cordoned"
	NodeStateDrained          NodeState = "Drained"
	NodeStateRebootIfRequired NodeState = "RebootIfRequired"
	// TODO Upgrade
)

func (c NodeState) Drain() bool {
	switch c {
	case NodeStateDrained, NodeStateRebootIfRequired:
		return true
	default:
		return false
	}
}

// NodeSpec defines the desired state of Node
type NodeSpec struct {
	// INSERT ADDITIONAL SPEC FIELDS - desired state of cluster
	// Important: Run "make" to regenerate code after modifying this file

	// +kubebuilder:validation:Required
	// +kubebuilder:default=Active
	// +kubebuilder:validation:Enum=Active;Cordoned;Drained;RebootIfRequired
	State NodeState `json:"state,omitempty"`
}
type NodeCurrentState string

func (c NodeCurrentState) String() string {
	return string(c)
}
func (c NodeCurrentState) WorkState() bool {
	switch c {
	case NodeCurrentStateOk, NodeCurrentStateCordoned, NodeCurrentStateQueued:
		return false
	default:
		return true
	}
}

const (
	NodeCurrentStateOk         NodeCurrentState = "OK"
	NodeCurrentStateCordoned   NodeCurrentState = "Cordoned"
	NodeCurrentStateQueued     NodeCurrentState = "Queued"
	NodeCurrentStateNext       NodeCurrentState = "Next"
	NodeCurrentStateDraining   NodeCurrentState = "Draining"
	NodeCurrentStateDrained    NodeCurrentState = "Drained"
	NodeCurrentStateUndraining NodeCurrentState = "Undraining"
	NodeCurrentStateRebooting  NodeCurrentState = "Rebooting"
)

var (
	nodeCurrentStates = [...]NodeCurrentState{
		NodeCurrentStateOk,
		NodeCurrentStateCordoned,
		NodeCurrentStateQueued,
		NodeCurrentStateNext,
		NodeCurrentStateDraining,
		NodeCurrentStateDrained,
		NodeCurrentStateUndraining,
		NodeCurrentStateRebooting,
	}
)

func GetNodeCurrentStates() []NodeCurrentState {
	return nodeCurrentStates[:]
}

// NodeStatus defines the observed state of Node
type NodeStatus struct {
	// INSERT ADDITIONAL STATUS FIELD - define observed state of cluster
	// Important: Run "make" to regenerate code after modifying this file

	Conditions []Condition `json:"conditions,omitempty" patchStrategy:"merge" patchMergeKey:"type" protobuf:"bytes,1,rep,name=conditions"`

	// +optional
	// +kubebuilder:validation:Type=string
	// +kubebuilder:validation:Format=date-time
	RebootRequiredLastChecked *metav1.Time `json:"rebootRequiredLastChecked,omitempty"`
	// +optional
	RebootRequired *bool `json:"rebootRequired"`

	// +kubebuilder:validation:Required
	// +kubebuilder:default=false
	Drained bool `json:"drained"`

	// +kubebuilder:validation:Required
	// +kubebuilder:default=OK
	// +kubebuilder:validation:Enum=OK;Cordoned;Queued;Next;Draining;Drained;Undraining;Rebooting
	CurrentState NodeCurrentState `json:"currentState,omitempty"`

	BootID string `json:"bootID,omitempty"`
}

type Condition struct {
	metav1.Condition `json:",inline"`

	// lastCheckTime is the last time the condition has been checked.
	// +optional
	// +kubebuilder:validation:Type=string
	// +kubebuilder:validation:Format=date-time
	LastCheckTime *metav1.Time `json:"lastCheckTime" protobuf:"bytes,4,opt,name=lastCheckTime"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster,shortName=nd
// +kubebuilder:printcolumn:name="Requested State",type="string",JSONPath=".spec.state"
// +kubebuilder:printcolumn:name="CurrentState",type="string",JSONPath=".status.currentState"
// +kubebuilder:printcolumn:name="Drained",type="boolean",JSONPath=".status.drained"
// +kubebuilder:printcolumn:name="Reboot Required",type="boolean",JSONPath=".status.rebootRequired"

// Node is the Schema for the nodes API
type Node struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   NodeSpec   `json:"spec,omitempty"`
	Status NodeStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// NodeList contains a list of Node
type NodeList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Node `json:"items"`
}

func init() {
	SchemeBuilder.Register(&Node{}, &NodeList{})
}
