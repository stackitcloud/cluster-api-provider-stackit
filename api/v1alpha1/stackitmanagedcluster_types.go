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
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
)

// StackitManagedClusterSpec defines the desired state of StackitManagedCluster.
// +kubebuilder:validation:XValidation:rule="has(self.credentialsSecretRef.name) && size(self.credentialsSecretRef.name) > 0",message="credentialsSecretRef.name is required"
type StackitManagedClusterSpec struct {
	// projectID is the STACKIT project that owns the cluster infrastructure.
	// +required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:Pattern=`^[0-9a-fA-F-]{36}$`
	ProjectID string `json:"projectID"`

	// region is the STACKIT region in which infrastructure is created.
	// +required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:Pattern=`^[a-z]{2}[0-9]{2}$`
	Region string `json:"region"`

	// clusterName is the SKE cluster name in STACKIT.
	// +required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=11
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([a-z0-9-]{0,9}[a-z0-9])?$`
	ClusterName string `json:"clusterName"`

	// credentialsSecretRef references the Secret containing STACKIT credentials.
	// +required
	CredentialsSecretRef corev1.SecretReference `json:"credentialsSecretRef"`

	// kubernetes configures the SKE cluster Kubernetes version.
	// +required
	Kubernetes StackitManagedClusterKubernetesSpec `json:"kubernetes"`

	// network configures the STACKIT network for the SKE cluster.
	// +optional
	Network *StackitManagedClusterNetworkSpec `json:"network,omitempty"`

	// controlPlaneEndpoint represents the endpoint used to communicate with the
	// control plane.
	// +optional
	ControlPlaneEndpoint clusterv1.APIEndpoint `json:"controlPlaneEndpoint,omitempty,omitzero"`

	// labels are key-value pairs applied to the SKE cluster object.
	// +optional
	Labels map[string]string `json:"labels,omitempty"`
}

// StackitManagedClusterKubernetesSpec configures the Kubernetes version for an
// SKE cluster.
type StackitManagedClusterKubernetesSpec struct {
	// version is the full Kubernetes version used for the cluster
	// (major.minor.patch).
	// +required
	// +kubebuilder:validation:Pattern=`^[0-9]+\.[0-9]+\.[0-9]+$`
	Version string `json:"version"`
}

// StackitManagedClusterNetworkSpec configures the STACKIT network for an SKE
// cluster.
type StackitManagedClusterNetworkSpec struct {
	// id is the ID of the STACKIT network into which the cluster will be
	// deployed.
	// +required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:Pattern=`^[0-9a-fA-F-]{36}$`
	ID string `json:"id"`
}

// StackitManagedClusterStatus defines the observed state of StackitManagedCluster.
type StackitManagedClusterStatus struct {
	// ready indicates that the infrastructure required for the cluster is ready.
	// +optional
	Ready bool `json:"ready,omitempty"`

	// initialization provides Cluster API v1beta2 contract state.
	// +optional
	Initialization StackitManagedClusterInitializationStatus `json:"initialization,omitempty,omitzero"`

	// conditions represent the current state of the StackitManagedCluster resource.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// StackitManagedClusterInitializationStatus provides observations of the
// StackitManagedCluster initialization process.
// +kubebuilder:validation:MinProperties=1
type StackitManagedClusterInitializationStatus struct {
	// provisioned is true when the infrastructure provider reports that the
	// cluster infrastructure is fully provisioned.
	// +optional
	Provisioned *bool `json:"provisioned,omitempty"`
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
