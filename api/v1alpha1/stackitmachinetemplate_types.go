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

// StackitMachineTemplateSpec defines the desired state of StackitMachineTemplate.
type StackitMachineTemplateSpec struct {
	// template wraps the StackitMachine spec used to create new machines.
	// +required
	Template StackitMachineTemplateResource `json:"template"`
}

// StackitMachineTemplateResource holds the spec for a StackitMachine created
// from a template.
type StackitMachineTemplateResource struct {
	// metadata is the standard object's metadata.
	// More info: https://git.k8s.io/community/contributors/devel/sig-architecture/api-conventions.md#metadata
	// +optional
	ObjectMeta clusterv1.ObjectMeta `json:"metadata,omitempty,omitzero"`

	// spec is the StackitMachineSpec that will be used to create the
	// StackitMachine.
	// +required
	Spec StackitMachineSpec `json:"spec"`
}

// +kubebuilder:object:root=true
// +kubebuilder:resource:path=stackitmachinetemplates,shortName=stimt,scope=Namespaced,categories=cluster-api
// +kubebuilder:printcolumn:name="Machine Type",type=string,JSONPath=".spec.template.spec.machineType"
// +kubebuilder:printcolumn:name="Image ID",type=string,JSONPath=".spec.template.spec.imageID"
// +kubebuilder:printcolumn:name="Disk GiB",type=integer,JSONPath=".spec.template.spec.rootVolume.sizeGiB"
// +kubebuilder:printcolumn:name="Zone",type=string,JSONPath=".spec.template.spec.availabilityZone"
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=".metadata.creationTimestamp"
// +kubebuilder:printcolumn:name="Network ID",type=string,JSONPath=".spec.template.spec.network.id",priority=1
// +kubebuilder:storageversion

// StackitMachineTemplate is the Schema for the stackitmachinetemplates API.
type StackitMachineTemplate struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is a standard object metadata
	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// spec defines the desired state of StackitMachineTemplate
	// +required
	Spec StackitMachineTemplateSpec `json:"spec"`
}

// +kubebuilder:object:root=true

// StackitMachineTemplateList contains a list of StackitMachineTemplate.
type StackitMachineTemplateList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []StackitMachineTemplate `json:"items"`
}

func init() {
	SchemeBuilder.Register(&StackitMachineTemplate{}, &StackitMachineTemplateList{})
}
