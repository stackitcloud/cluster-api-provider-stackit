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
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
)

// MachineFinalizer allows StackitMachineReconciler to clean up the underlying
// VM before the StackitMachine is finally removed from the API server.
const MachineFinalizer = "stackitmachine.infrastructure.cluster.x-k8s.io"

// StackitMachineSpec defines the desired state of StackitMachine.
//
// NOTE: There is intentionally no userData field on StackitMachineSpec.
// Bootstrap data is provided by CABPK/KubeadmConfig via
// Machine.spec.bootstrap.dataSecretName, read by the reconciler and passed to
// the cloud client during VM creation.
type StackitMachineSpec struct {
	// providerID is the unique identifier the cloud-provider will use to
	// reconcile this machine with the corresponding Node. It is set by the
	// controller once the VM has been created.
	// +optional
	ProviderID *string `json:"providerID,omitempty"`

	// imageID is the ID of the STACKIT image to boot from.
	// +required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:Pattern=`^[0-9a-fA-F-]{36}$`
	ImageID string `json:"imageID"`

	// machineType selects the STACKIT machine flavor (e.g. c2i.2).
	// +required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:Pattern=`^[a-z0-9][a-z0-9.-]*[a-z0-9]$`
	MachineType string `json:"machineType"`

	// availabilityZone selects a STACKIT availability zone (e.g. eu01-1).
	// +optional
	// +kubebuilder:validation:Pattern=`^[a-z]{2}[0-9]{2}-[a-z0-9]+$`
	AvailabilityZone string `json:"availabilityZone,omitempty"`

	// sshKeyName is the name of an existing SSH key in the STACKIT project.
	// +optional
	// +kubebuilder:validation:MaxLength=127
	// +kubebuilder:validation:Pattern=`^[A-Za-z0-9_.@-]+$`
	SSHKeyName string `json:"sshKeyName,omitempty"`

	// rootVolume configures the root disk for the VM.
	// +optional
	RootVolume StackitRootVolumeSpec `json:"rootVolume,omitempty"`

	// network references the STACKIT network the VM should be attached to.
	// +required
	Network StackitMachineNetworkSpec `json:"network"`

	// securityGroups is a list of STACKIT security group IDs applied to the VM.
	// +optional
	// +kubebuilder:validation:items:Pattern=`^[0-9a-fA-F-]{36}$`
	SecurityGroups []string `json:"securityGroups,omitempty"`

	// additionalLabels is merged into the labels applied to STACKIT resources.
	// +optional
	// +kubebuilder:validation:MaxProperties=64
	AdditionalLabels map[string]string `json:"additionalLabels,omitempty"`
}

// StackitRootVolumeSpec configures the boot/root disk of a VM.
type StackitRootVolumeSpec struct {
	// sizeGiB is the size of the root volume in GiB.
	// +optional
	// +kubebuilder:validation:Minimum=0
	SizeGiB int `json:"sizeGiB,omitempty"`

	// performanceClass selects a STACKIT storage performance class.
	// +optional
	// +kubebuilder:validation:MinLength=1
	PerformanceClass string `json:"performanceClass,omitempty"`

	// deleteOnTermination causes the root volume to be deleted when the VM is
	// deleted.
	// +optional
	DeleteOnTermination *bool `json:"deleteOnTermination,omitempty"`
}

// StackitMachineNetworkSpec references an existing STACKIT network.
type StackitMachineNetworkSpec struct {
	// id is the STACKIT network ID.
	// +required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:Pattern=`^[0-9a-fA-F-]{36}$`
	ID string `json:"id"`
}

// StackitMachineStatus defines the observed state of StackitMachine.
type StackitMachineStatus struct {
	// ready indicates the underlying VM is provisioned and known to the
	// provider.
	// +optional
	Ready bool `json:"ready,omitempty"`

	// initialization provides Cluster API v1beta2 contract state.
	// +optional
	Initialization StackitMachineInitializationStatus `json:"initialization,omitempty"`

	// providerID is the providerID written back to the owning Machine.
	// +optional
	ProviderID string `json:"providerID,omitempty"`

	// instanceID is the STACKIT server ID assigned to this machine.
	// +optional
	InstanceID string `json:"instanceID,omitempty"`

	// instanceState is the latest known state of the STACKIT server.
	// +optional
	InstanceState string `json:"instanceState,omitempty"`

	// availabilityZone is the STACKIT availability zone where the VM was placed.
	// +optional
	AvailabilityZone string `json:"availabilityZone,omitempty"`

	// addresses contains the IP addresses associated with the VM.
	// +optional
	Addresses []clusterv1.MachineAddress `json:"addresses,omitempty"`

	// conditions represent the current state of the StackitMachine resource.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// StackitMachineInitializationStatus holds Cluster API initialization state.
type StackitMachineInitializationStatus struct {
	// provisioned is true when the VM is provisioned.
	// +optional
	Provisioned bool `json:"provisioned,omitempty"`
}

// Condition types maintained by the StackitMachineReconciler.
const (
	MachineReadyCondition            = "Ready"
	MachineBootstrapReadyCondition   = "BootstrapReady"
	MachineCredentialsReadyCondition = "CredentialsReady"
	MachineInstanceReadyCondition    = "InstanceReady"
)

// +kubebuilder:object:root=true
// +kubebuilder:resource:path=stackitmachines,shortName=stim,scope=Namespaced,categories=cluster-api
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Cluster",type=string,JSONPath=".metadata.labels['cluster\\.x-k8s\\.io/cluster-name']",description="Cluster to which this resource belongs"
// +kubebuilder:printcolumn:name="Ready",type=boolean,JSONPath=".status.ready"
// +kubebuilder:printcolumn:name="State",type=string,JSONPath=".status.instanceState",description="STACKIT server state"
// +kubebuilder:printcolumn:name="Machine Type",type=string,JSONPath=".spec.machineType"
// +kubebuilder:printcolumn:name="IP",type=string,JSONPath=".status.addresses[?(@.type=='InternalIP')].address"
// +kubebuilder:printcolumn:name="Zone",type=string,JSONPath=".status.availabilityZone"
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=".metadata.creationTimestamp"
// +kubebuilder:storageversion

// StackitMachine is the Schema for the stackitmachines API.
type StackitMachine struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is a standard object metadata
	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// spec defines the desired state of StackitMachine
	// +required
	Spec StackitMachineSpec `json:"spec"`

	// status defines the observed state of StackitMachine
	// +optional
	Status StackitMachineStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

// StackitMachineList contains a list of StackitMachine.
type StackitMachineList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []StackitMachine `json:"items"`
}

func init() {
	SchemeBuilder.Register(&StackitMachine{}, &StackitMachineList{})
}
