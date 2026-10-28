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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// StackitManagedClusterSpec defines the desired state of StackitManagedCluster.
type StackitManagedClusterSpec struct {
}

// StackitManagedClusterStatus defines the observed state of StackitManagedCluster.
type StackitManagedClusterStatus struct {
	// ready indicates that the infrastructure required for the cluster is ready.
	// +optional
	Ready bool `json:"ready,omitempty"`

	// initialization provides Cluster API v1beta2 contract state.
	// +optional
	Initialization StackitClusterInitializationStatus `json:"initialization,omitempty"`

	// conditions represent the current state of the StackitManagedCluster resource.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:resource:path=stackitmanagedclusters,shortName=stmc,scope=Namespaced,categories=cluster-api
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Ready",type=boolean,JSONPath=".status.ready"
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=".metadata.creationTimestamp"
// +kubebuilder:storageversion

// StackitManagedCluster is the Schema for the stackitmanagedclusters API.
type StackitManagedCluster struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is a standard object metadata
	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// spec defines the desired state of StackitManagedCluster
	// +required
	Spec StackitManagedClusterSpec `json:"spec"`

	// status defines the observed state of StackitManagedCluster
	// +optional
	Status StackitManagedClusterStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

// StackitManagedClusterList contains a list of StackitManagedCluster.
type StackitManagedClusterList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []StackitManagedCluster `json:"items"`
}

func init() {
	SchemeBuilder.Register(&StackitManagedCluster{}, &StackitManagedClusterList{})
}
