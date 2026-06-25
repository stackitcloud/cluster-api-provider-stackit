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
	"context"

	ctrl "sigs.k8s.io/controller-runtime"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	infrastructurev1alpha1 "github.com/stackitcloud/cluster-api-provider-stackit/api/v1alpha1"
)

// nolint:unused
// log is for logging in this package.
var stackitclusterlog = logf.Log.WithName("stackitcluster-resource")

// SetupStackitClusterWebhookWithManager registers the webhook for StackitCluster in the manager.
func SetupStackitClusterWebhookWithManager(mgr ctrl.Manager) error {
	return ctrl.NewWebhookManagedBy(mgr, &infrastructurev1alpha1.StackitCluster{}).
		WithValidator(&StackitClusterCustomValidator{}).
		WithDefaulter(&StackitClusterCustomDefaulter{}).
		Complete()
}

// +kubebuilder:webhook:path=/mutate-infrastructure-cluster-x-k8s-io-v1alpha1-stackitcluster,mutating=true,failurePolicy=fail,sideEffects=None,groups=infrastructure.cluster.x-k8s.io,resources=stackitclusters,verbs=create;update,versions=v1alpha1,name=mstackitcluster-v1alpha1.kb.io,admissionReviewVersions=v1

// StackitClusterCustomDefaulter struct is responsible for setting default values on the custom resource of the
// Kind StackitCluster when those are created or updated.
//
// NOTE: The +kubebuilder:object:generate=false marker prevents controller-gen from generating DeepCopy methods,
// as it is used only for temporary operations and does not need to be deeply copied.
type StackitClusterCustomDefaulter struct{}

// Default implements webhook.CustomDefaulter so a webhook will be registered for the Kind StackitCluster.
func (d *StackitClusterCustomDefaulter) Default(_ context.Context, obj *infrastructurev1alpha1.StackitCluster) error {
	stackitclusterlog.Info("Defaulting for StackitCluster", "name", obj.GetName())

	defaultClusterSpec(&obj.Spec)
	return nil
}

// +kubebuilder:webhook:path=/validate-infrastructure-cluster-x-k8s-io-v1alpha1-stackitcluster,mutating=false,failurePolicy=fail,sideEffects=None,groups=infrastructure.cluster.x-k8s.io,resources=stackitclusters,verbs=create;update,versions=v1alpha1,name=vstackitcluster-v1alpha1.kb.io,admissionReviewVersions=v1

// StackitClusterCustomValidator struct is responsible for validating the StackitCluster resource
// when it is created, updated, or deleted.
//
// NOTE: The +kubebuilder:object:generate=false marker prevents controller-gen from generating DeepCopy methods,
// as this struct is used only for temporary operations and does not need to be deeply copied.
type StackitClusterCustomValidator struct{}

// ValidateCreate implements webhook.CustomValidator so a webhook will be registered for the type StackitCluster.
func (v *StackitClusterCustomValidator) ValidateCreate(_ context.Context, obj *infrastructurev1alpha1.StackitCluster) (admission.Warnings, error) {
	stackitclusterlog.Info("Validation for StackitCluster upon creation", "name", obj.GetName())

	return nil, invalidFor("StackitCluster", obj.Name, validateStackitClusterSpec(obj.Spec, fieldPathSpec()))
}

// ValidateUpdate implements webhook.CustomValidator so a webhook will be registered for the type StackitCluster.
func (v *StackitClusterCustomValidator) ValidateUpdate(_ context.Context, oldObj, newObj *infrastructurev1alpha1.StackitCluster) (admission.Warnings, error) {
	stackitclusterlog.Info("Validation for StackitCluster upon update", "name", newObj.GetName())

	allErrs := validateStackitClusterSpec(newObj.Spec, fieldPathSpec())
	allErrs = append(allErrs, validateStackitClusterSpecUpdate(oldObj, newObj)...)

	return nil, invalidFor("StackitCluster", newObj.Name, allErrs)
}

// ValidateDelete implements webhook.CustomValidator so a webhook will be registered for the type StackitCluster.
func (v *StackitClusterCustomValidator) ValidateDelete(_ context.Context, obj *infrastructurev1alpha1.StackitCluster) (admission.Warnings, error) {
	stackitclusterlog.Info("Validation for StackitCluster upon deletion", "name", obj.GetName())

	return nil, nil
}
