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

	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/util/validation/field"
	ctrl "sigs.k8s.io/controller-runtime"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	infrastructurev1alpha1 "github.com/stackitcloud/cluster-api-provider-stackit/api/v1alpha1"
)

// nolint:unused
// log is for logging in this package.
var stackitclustertemplatelog = logf.Log.WithName("stackitclustertemplate-resource")

// SetupStackitClusterTemplateWebhookWithManager registers the webhook for StackitClusterTemplate in the manager.
func SetupStackitClusterTemplateWebhookWithManager(mgr ctrl.Manager) error {
	return ctrl.NewWebhookManagedBy(mgr, &infrastructurev1alpha1.StackitClusterTemplate{}).
		WithValidator(&StackitClusterTemplateCustomValidator{}).
		WithDefaulter(&StackitClusterTemplateCustomDefaulter{}).
		Complete()
}

// +kubebuilder:webhook:path=/mutate-infrastructure-cluster-x-k8s-io-v1alpha1-stackitclustertemplate,mutating=true,failurePolicy=fail,sideEffects=None,groups=infrastructure.cluster.x-k8s.io,resources=stackitclustertemplates,verbs=create;update,versions=v1alpha1,name=mstackitclustertemplate-v1alpha1.kb.io,admissionReviewVersions=v1

// StackitClusterTemplateCustomDefaulter struct is responsible for setting default values on the custom resource of the
// Kind StackitClusterTemplate when those are created or updated.
//
// NOTE: The +kubebuilder:object:generate=false marker prevents controller-gen from generating DeepCopy methods,
// as it is used only for temporary operations and does not need to be deeply copied.
type StackitClusterTemplateCustomDefaulter struct{}

// Default implements webhook.CustomDefaulter so a webhook will be registered for the Kind StackitClusterTemplate.
func (d *StackitClusterTemplateCustomDefaulter) Default(_ context.Context, obj *infrastructurev1alpha1.StackitClusterTemplate) error {
	stackitclustertemplatelog.Info("Defaulting for StackitClusterTemplate", "name", obj.GetName())

	defaultClusterSpec(&obj.Spec.Template.Spec)
	return nil
}

// +kubebuilder:webhook:path=/validate-infrastructure-cluster-x-k8s-io-v1alpha1-stackitclustertemplate,mutating=false,failurePolicy=fail,sideEffects=None,groups=infrastructure.cluster.x-k8s.io,resources=stackitclustertemplates,verbs=create;update,versions=v1alpha1,name=vstackitclustertemplate-v1alpha1.kb.io,admissionReviewVersions=v1

// StackitClusterTemplateCustomValidator struct is responsible for validating the StackitClusterTemplate resource
// when it is created, updated, or deleted.
//
// NOTE: The +kubebuilder:object:generate=false marker prevents controller-gen from generating DeepCopy methods,
// as this struct is used only for temporary operations and does not need to be deeply copied.
type StackitClusterTemplateCustomValidator struct{}

// ValidateCreate implements webhook.CustomValidator so a webhook will be registered for the type StackitClusterTemplate.
func (v *StackitClusterTemplateCustomValidator) ValidateCreate(_ context.Context, obj *infrastructurev1alpha1.StackitClusterTemplate) (admission.Warnings, error) {
	stackitclustertemplatelog.Info("Validation for StackitClusterTemplate upon creation", "name", obj.GetName())

	allErrs := validateTemplateObjectMeta(obj.Spec.Template.ObjectMeta, fieldPathSpec().Child("template", "metadata"))
	allErrs = append(allErrs, validateStackitClusterSpec(obj.Spec.Template.Spec, fieldPathSpec().Child("template", "spec"))...)

	return nil, invalidFor("StackitClusterTemplate", obj.Name, allErrs)
}

// ValidateUpdate implements webhook.CustomValidator so a webhook will be registered for the type StackitClusterTemplate.
func (v *StackitClusterTemplateCustomValidator) ValidateUpdate(_ context.Context, oldObj, newObj *infrastructurev1alpha1.StackitClusterTemplate) (admission.Warnings, error) {
	stackitclustertemplatelog.Info("Validation for StackitClusterTemplate upon update", "name", newObj.GetName())

	allErrs := validateTemplateObjectMeta(newObj.Spec.Template.ObjectMeta, fieldPathSpec().Child("template", "metadata"))
	allErrs = append(allErrs, validateStackitClusterSpec(newObj.Spec.Template.Spec, fieldPathSpec().Child("template", "spec"))...)
	oldSpec := oldObj.Spec.Template.Spec.DeepCopy()
	newSpec := newObj.Spec.Template.Spec.DeepCopy()
	defaultClusterSpec(oldSpec)
	defaultClusterSpec(newSpec)
	if !equality.Semantic.DeepEqual(oldSpec, newSpec) {
		allErrs = append(allErrs, field.Invalid(fieldPathSpec().Child("template", "spec"), newObj.Spec.Template.Spec, immutableTemplateSpecMessage))
	}

	return nil, invalidFor("StackitClusterTemplate", newObj.Name, allErrs)
}

// ValidateDelete implements webhook.CustomValidator so a webhook will be registered for the type StackitClusterTemplate.
func (v *StackitClusterTemplateCustomValidator) ValidateDelete(_ context.Context, obj *infrastructurev1alpha1.StackitClusterTemplate) (admission.Warnings, error) {
	stackitclustertemplatelog.Info("Validation for StackitClusterTemplate upon deletion", "name", obj.GetName())

	return nil, nil
}
