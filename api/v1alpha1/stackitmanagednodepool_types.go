/*
Copyright 2026.

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

package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// StackitManagedNodePoolSpec defines the desired state of StackitManagedNodePool.
// +kubebuilder:validation:XValidation:rule="self.minimum <= self.maximum",message="minimum must be less than or equal to maximum"
type StackitManagedNodePoolSpec struct {
	// name is the name of the node pool in SKE.
	// +required
	// +kubebuilder:validation:MaxLength=15
	Name string `json:"name"`

	// availabilityZones is the list of availability zones for the node pool.
	// +required
	AvailabilityZones []string `json:"availabilityZones"`

	// machine configures the VM instance type and OS image for the node pool.
	// +required
	Machine StackitManagedNodePoolMachineSpec `json:"machine"`

	// minimum is the minimum number of nodes in the pool.
	// +required
	// +kubebuilder:validation:Maximum=1000
	Minimum int32 `json:"minimum"`

	// maximum is the maximum number of nodes in the pool.
	// +required
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=1000
	Maximum int32 `json:"maximum"`

	// volume configures the disk volume for each node in the pool.
	// +required
	Volume StackitManagedNodePoolVolumeSpec `json:"volume"`

	// allowSystemComponents allows system components to run on this node pool.
	// +optional
	AllowSystemComponents *bool `json:"allowSystemComponents,omitempty"`

	// labels is a map of labels attached to all worker nodes of this node pool.
	// +optional
	// +kubebuilder:validation:MaxProperties=80
	Labels map[string]string `json:"labels,omitempty"`

	// taints is a list of taints for all worker nodes of this node pool.
	// +optional
	Taints []StackitManagedNodePoolTaint `json:"taints,omitempty"`

	// maxSurge is the maximum number of additional VMs created during an update.
	// +optional
	MaxSurge *int32 `json:"maxSurge,omitempty"`

	// maxUnavailable is the maximum number of VMs that can be unavailable during
	// an update.
	// +optional
	MaxUnavailable *int32 `json:"maxUnavailable,omitempty"`

	// providerIDList are the identification IDs of machine instances provided by
	// the provider. This field must match the provider IDs as seen on the Node
	// objects corresponding to a MachinePool's machine instances.
	// +optional
	// +listType=atomic
	// +kubebuilder:validation:MaxItems=10000
	// +kubebuilder:validation:items:MinLength=1
	// +kubebuilder:validation:items:MaxLength=512
	ProviderIDList []string `json:"providerIDList,omitempty"`
}

// StackitManagedNodePoolMachineSpec configures the machine flavor and OS image
// for an SKE node pool.
type StackitManagedNodePoolMachineSpec struct {
	// type selects the STACKIT machine type for the node pool.
	// +required
	Type string `json:"type"`

	// image configures the OS image for the node pool.
	// +required
	Image StackitManagedNodePoolImageSpec `json:"image"`
}

// StackitManagedNodePoolImageSpec configures the OS image name and version for
// an SKE node pool.
type StackitManagedNodePoolImageSpec struct {
	// name is the OS image name.
	// +required
	Name string `json:"name"`

	// version is the OS image version.
	// +required
	Version string `json:"version"`
}

// StackitManagedNodePoolVolumeSpec configures the boot disk volume for an SKE
// node pool.
type StackitManagedNodePoolVolumeSpec struct {
	// size is the size of the volume in GiB.
	// +required
	// +kubebuilder:validation:Minimum=20
	// +kubebuilder:validation:Maximum=10240
	Size int32 `json:"size"`

	// type is the volume performance class.
	// +optional
	Type string `json:"type,omitempty"`
}

// StackitManagedNodePoolTaint defines a Kubernetes taint applied to nodes in
// an SKE node pool.
type StackitManagedNodePoolTaint struct {
	// effect is the taint effect to apply.
	// +required
	// +kubebuilder:validation:Enum=NoSchedule;PreferNoSchedule;NoExecute
	Effect corev1.TaintEffect `json:"effect"`

	// key is the taint key to be applied to a node.
	// +required
	Key string `json:"key"`

	// value is the taint value corresponding to the taint key.
	// +optional
	Value string `json:"value,omitempty"`
}

// StackitManagedNodePoolStatus defines the observed state of StackitManagedNodePool.
type StackitManagedNodePoolStatus struct {
	// ready indicates that the node pool infrastructure is ready.
	// +optional
	Ready bool `json:"ready,omitempty"`

	// initialization provides Cluster API v1beta2 contract state.
	// +optional
	Initialization StackitManagedNodePoolInitializationStatus `json:"initialization,omitempty,omitzero"`

	// replicas is the most recently observed number of replicas.
	// +optional
	Replicas int32 `json:"replicas"`

	// conditions represent the current state of the StackitManagedNodePool resource.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// StackitManagedNodePoolInitializationStatus holds Cluster API initialization
// state for a StackitManagedNodePool.
// +kubebuilder:validation:MinProperties=1
type StackitManagedNodePoolInitializationStatus struct {
	// provisioned is true when the node pool infrastructure is provisioned.
	// +optional
	Provisioned *bool `json:"provisioned,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:resource:path=stackitmanagednodepools,shortName=stmnp,scope=Namespaced,categories=cluster-api
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Ready",type=boolean,JSONPath=".status.ready"
// +kubebuilder:printcolumn:name="Replicas",type=integer,JSONPath=".status.replicas"
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=".metadata.creationTimestamp"
// +kubebuilder:storageversion

// StackitManagedNodePool is the Schema for the stackitmanagednodepools API.
type StackitManagedNodePool struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is a standard object metadata
	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// spec defines the desired state of StackitManagedNodePool
	// +required
	Spec StackitManagedNodePoolSpec `json:"spec"`

	// status defines the observed state of StackitManagedNodePool
	// +optional
	Status StackitManagedNodePoolStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

// StackitManagedNodePoolList contains a list of StackitManagedNodePool.
type StackitManagedNodePoolList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []StackitManagedNodePool `json:"items"`
}

func init() {
	SchemeBuilder.Register(&StackitManagedNodePool{}, &StackitManagedNodePoolList{})
}
