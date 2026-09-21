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

// ClusterFinalizer allows StackitClusterReconciler to clean up resources before
// the StackitCluster is finally removed from the API server.
const ClusterFinalizer = "stackitcluster.infrastructure.cluster.x-k8s.io"

// StackitClusterSpec defines the desired state of StackitCluster.
// +kubebuilder:validation:XValidation:rule="has(self.credentialsSecretRef.name) && size(self.credentialsSecretRef.name) > 0",message="credentialsSecretRef.name is required"
// +kubebuilder:validation:XValidation:rule="has(self.apiServerLoadBalancer) && has(self.apiServerLoadBalancer.enabled) && self.apiServerLoadBalancer.enabled ? true : has(self.controlPlaneEndpoint) && has(self.controlPlaneEndpoint.host) && size(self.controlPlaneEndpoint.host) > 0",message="controlPlaneEndpoint.host is required when apiServerLoadBalancer.enabled is false"
// +kubebuilder:validation:XValidation:rule="!has(self.bastion) || !has(self.bastion.enabled) || !self.bastion.enabled || (has(self.bastion.imageID) && size(self.bastion.imageID) > 0 && has(self.bastion.machineType) && size(self.bastion.machineType) > 0 && has(self.bastion.sshKeyName) && size(self.bastion.sshKeyName) > 0 && has(self.bastion.allowedCIDRs) && size(self.bastion.allowedCIDRs) > 0)",message="bastion.imageID, bastion.machineType, bastion.sshKeyName, and bastion.allowedCIDRs are required when bastion.enabled is true"
type StackitClusterSpec struct {
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

	// credentialsSecretRef references the Secret containing STACKIT credentials.
	// +required
	CredentialsSecretRef corev1.SecretReference `json:"credentialsSecretRef"`

	// network references the existing STACKIT network the cluster will use.
	// For MVP the network must already exist.
	// +required
	Network StackitClusterNetworkSpec `json:"network"`

	// apiServerLoadBalancer configures the load balancer in front of the
	// Kubernetes API server.
	// +optional
	APIServerLoadBalancer StackitAPIServerLoadBalancerSpec `json:"apiServerLoadBalancer,omitempty"`

	// controlPlaneEndpoint allows the user to provide a pre-existing endpoint
	// when apiServerLoadBalancer.enabled is false.
	// +optional
	ControlPlaneEndpoint clusterv1.APIEndpoint `json:"controlPlaneEndpoint,omitempty,omitzero"`

	// bastion configures an optional provider-managed SSH bastion host.
	// +optional
	Bastion StackitBastionSpec `json:"bastion,omitempty"`

	// additionalLabels is merged into the labels applied to cluster-wide
	// STACKIT resources such as the API server load balancer.
	// +optional
	// +kubebuilder:validation:MaxProperties=64
	AdditionalLabels map[string]string `json:"additionalLabels,omitempty"`
}

// StackitClusterNetworkSpec references an existing STACKIT network.
type StackitClusterNetworkSpec struct {
	// id is the STACKIT network ID.
	// +required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:Pattern=`^[0-9a-fA-F-]{36}$`
	ID string `json:"id"`
}

// StackitAPIServerLoadBalancerSpec configures an optional load balancer for
// the Kubernetes API server.
type StackitAPIServerLoadBalancerSpec struct {
	// enabled toggles creation of an API server load balancer.
	// +optional
	Enabled bool `json:"enabled,omitempty"`
}

// StackitBastionSpec configures an optional provider-managed SSH bastion host.
type StackitBastionSpec struct {
	// enabled toggles creation of a provider-managed SSH bastion host.
	// +optional
	Enabled bool `json:"enabled,omitempty"`

	// imageID is the ID of the STACKIT image to boot the bastion from.
	// Required when enabled is true.
	// +optional
	// +kubebuilder:validation:Pattern=`^[0-9a-fA-F-]{36}$`
	ImageID string `json:"imageID,omitempty"`

	// machineType selects the STACKIT machine flavor for the bastion VM.
	// Required when enabled is true.
	// +optional
	// +kubebuilder:validation:Pattern=`^[a-z0-9][a-z0-9.-]*[a-z0-9]$`
	MachineType string `json:"machineType,omitempty"`

	// sshKeyName is the name of an existing SSH key in the STACKIT project.
	// Required when enabled is true.
	// +optional
	// +kubebuilder:validation:MaxLength=127
	// +kubebuilder:validation:Pattern=`^[A-Za-z0-9_.@-]+$`
	SSHKeyName string `json:"sshKeyName,omitempty"`

	// allowedCIDRs are CIDR ranges allowed to connect to TCP/22 on the bastion.
	// Required when enabled is true.
	// +optional
	// +kubebuilder:validation:items:Format=cidr
	AllowedCIDRs []string `json:"allowedCIDRs,omitempty"`

	// rootVolume configures the bastion root disk.
	// +optional
	RootVolume StackitRootVolumeSpec `json:"rootVolume,omitempty"`

	// cloudInitRef references cloud-init user data passed to the bastion VM.
	// It can point to a ConfigMap for non-sensitive configuration or a Secret
	// when the cloud-init document contains sensitive values.
	// +optional
	CloudInitRef *StackitBastionCloudInitRef `json:"cloudInitRef,omitempty"`
}

// StackitBastionCloudInitRef references cloud-init user data for the bastion.
type StackitBastionCloudInitRef struct {
	// kind is the Kubernetes object kind containing the cloud-init document.
	// +required
	// +kubebuilder:validation:Enum=ConfigMap;Secret
	Kind string `json:"kind"`

	// name is the ConfigMap or Secret name in the StackitCluster namespace.
	// +required
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`

	// key is the data key containing the cloud-init document.
	// +required
	// +kubebuilder:validation:MinLength=1
	Key string `json:"key"`
}

// StackitClusterStatus defines the observed state of StackitCluster.
type StackitClusterStatus struct {
	// ready indicates that the infrastructure required for the cluster is ready.
	// +optional
	Ready bool `json:"ready,omitempty"`

	// initialization provides Cluster API v1beta2 contract state.
	// +optional
	Initialization StackitClusterInitializationStatus `json:"initialization,omitempty"`

	// apiServerEndpoint is the endpoint the Kubernetes API server is reachable at.
	// +optional
	APIServerEndpoint clusterv1.APIEndpoint `json:"apiServerEndpoint,omitempty,omitzero"`

	// apiServerLoadBalancerID stores the ID of a provider-managed API server
	// load balancer so it can be deleted on cluster teardown.
	// +optional
	APIServerLoadBalancerID string `json:"apiServerLoadBalancerID,omitempty"`

	// bastion stores observed provider-managed bastion resources.
	// +optional
	Bastion StackitBastionStatus `json:"bastion,omitempty"`

	// failureDomains is a list of availability zones available to the cluster.
	// +listType=map
	// +listMapKey=name
	// +optional
	FailureDomains []clusterv1.FailureDomain `json:"failureDomains,omitempty"`

	// conditions represent the current state of the StackitCluster resource.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// StackitClusterInitializationStatus holds Cluster API initialization state.
type StackitClusterInitializationStatus struct {
	// provisioned is true when the cluster infrastructure is provisioned.
	// +optional
	Provisioned bool `json:"provisioned,omitempty"`
}

// StackitBastionStatus holds provider-managed bastion resource IDs.
type StackitBastionStatus struct {
	// serverID is the provider-managed bastion server ID.
	// +optional
	ServerID string `json:"serverID,omitempty"`

	// publicIPID is the provider-managed public IP resource ID.
	// +optional
	PublicIPID string `json:"publicIPID,omitempty"`

	// publicIP is the routable IP address users connect to.
	// +optional
	PublicIP string `json:"publicIP,omitempty"`

	// securityGroupID is the provider-managed bastion security group ID.
	// +optional
	SecurityGroupID string `json:"securityGroupID,omitempty"`

	// cloudInitHash records the resolved cloudInitRef content used to create
	// the current provider-managed bastion server.
	// +optional
	CloudInitHash string `json:"cloudInitHash,omitempty"`
}

// Condition types maintained by the StackitClusterReconciler.
const (
	ClusterReadyCondition             = "Ready"
	ClusterNetworkReadyCondition      = "NetworkReady"
	ClusterLoadBalancerReadyCondition = "LoadBalancerReady"
	ClusterCredentialsReadyCondition  = "CredentialsReady"
	ClusterBastionReadyCondition      = "BastionReady"
)

// +kubebuilder:object:root=true
// +kubebuilder:resource:path=stackitclusters,shortName=stic,scope=Namespaced,categories=cluster-api
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Ready",type=boolean,JSONPath=".status.ready"
// +kubebuilder:printcolumn:name="Region",type=string,JSONPath=".spec.region"
// +kubebuilder:printcolumn:name="Endpoint",type=string,JSONPath=".status.apiServerEndpoint.host"
// +kubebuilder:printcolumn:name="Bastion IP",type=string,JSONPath=".status.bastion.publicIP"
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=".metadata.creationTimestamp"
// +kubebuilder:printcolumn:name="Network ID",type=string,JSONPath=".spec.network.id",priority=1
// +kubebuilder:printcolumn:name="Project ID",type=string,JSONPath=".spec.projectID",priority=1
// +kubebuilder:storageversion

// StackitCluster is the Schema for the stackitclusters API.
type StackitCluster struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is a standard object metadata
	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// spec defines the desired state of StackitCluster
	// +required
	Spec StackitClusterSpec `json:"spec"`

	// status defines the observed state of StackitCluster
	// +optional
	Status StackitClusterStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

// StackitClusterList contains a list of StackitCluster.
type StackitClusterList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []StackitCluster `json:"items"`
}

func init() {
	SchemeBuilder.Register(&StackitCluster{}, &StackitClusterList{})
}
