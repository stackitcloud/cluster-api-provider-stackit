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

// StackitClusterTemplateSpec defines the desired state of StackitClusterTemplate.
type StackitClusterTemplateSpec struct {
	// template wraps the StackitCluster spec used to create new clusters.
	// +required
	Template StackitClusterTemplateResource `json:"template"`
}

// StackitClusterTemplateResource holds the spec for a StackitCluster created
// from a template.
type StackitClusterTemplateResource struct {
	// metadata is the standard object's metadata.
	// More info: https://git.k8s.io/community/contributors/devel/sig-architecture/api-conventions.md#metadata
	// +optional
	ObjectMeta clusterv1.ObjectMeta `json:"metadata,omitempty,omitzero"`

	// spec is the StackitClusterSpec that will be used to create the
	// StackitCluster.
	// +required
	Spec StackitClusterSpec `json:"spec"`
}

// +kubebuilder:object:root=true
// +kubebuilder:resource:path=stackitclustertemplates,shortName=stict,scope=Namespaced,categories=cluster-api
// +kubebuilder:printcolumn:name="Region",type=string,JSONPath=".spec.template.spec.region"
// +kubebuilder:printcolumn:name="LB Enabled",type=boolean,JSONPath=".spec.template.spec.apiServerLoadBalancer.enabled"
// +kubebuilder:printcolumn:name="Bastion Enabled",type=boolean,JSONPath=".spec.template.spec.bastion.enabled"
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=".metadata.creationTimestamp"
// +kubebuilder:printcolumn:name="Network ID",type=string,JSONPath=".spec.template.spec.network.id",priority=1
// +kubebuilder:printcolumn:name="Project ID",type=string,JSONPath=".spec.template.spec.projectID",priority=1
// +kubebuilder:storageversion

// StackitClusterTemplate is the Schema for the stackitclustertemplates API.
type StackitClusterTemplate struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is a standard object metadata
	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// spec defines the desired state of StackitClusterTemplate
	// +required
	Spec StackitClusterTemplateSpec `json:"spec"`
}

// +kubebuilder:object:root=true

// StackitClusterTemplateList contains a list of StackitClusterTemplate.
type StackitClusterTemplateList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []StackitClusterTemplate `json:"items"`
}

func init() {
	SchemeBuilder.Register(&StackitClusterTemplate{}, &StackitClusterTemplateList{})
}
