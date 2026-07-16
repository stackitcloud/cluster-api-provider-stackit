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
var stackitmachinetemplatelog = logf.Log.WithName("stackitmachinetemplate-resource")

// SetupStackitMachineTemplateWebhookWithManager registers the webhook for StackitMachineTemplate in the manager.
func SetupStackitMachineTemplateWebhookWithManager(mgr ctrl.Manager) error {
	return ctrl.NewWebhookManagedBy(mgr, &infrastructurev1alpha1.StackitMachineTemplate{}).
		WithValidator(&StackitMachineTemplateCustomValidator{}).
		WithDefaulter(&StackitMachineTemplateCustomDefaulter{}).
		Complete()
}

// +kubebuilder:webhook:path=/mutate-infrastructure-cluster-x-k8s-io-v1alpha1-stackitmachinetemplate,mutating=true,failurePolicy=fail,sideEffects=None,groups=infrastructure.cluster.x-k8s.io,resources=stackitmachinetemplates,verbs=create;update,versions=v1alpha1,name=mstackitmachinetemplate-v1alpha1.kb.io,admissionReviewVersions=v1

// StackitMachineTemplateCustomDefaulter struct is responsible for setting default values on the custom resource of the
// Kind StackitMachineTemplate when those are created or updated.
//
// NOTE: The +kubebuilder:object:generate=false marker prevents controller-gen from generating DeepCopy methods,
// as it is used only for temporary operations and does not need to be deeply copied.
type StackitMachineTemplateCustomDefaulter struct{}

// Default implements webhook.CustomDefaulter so a webhook will be registered for the Kind StackitMachineTemplate.
func (d *StackitMachineTemplateCustomDefaulter) Default(_ context.Context, obj *infrastructurev1alpha1.StackitMachineTemplate) error {
	stackitmachinetemplatelog.Info("Defaulting for StackitMachineTemplate", "name", obj.GetName())

	defaultMachineSpec(&obj.Spec.Template.Spec)
	return nil
}

// +kubebuilder:webhook:path=/validate-infrastructure-cluster-x-k8s-io-v1alpha1-stackitmachinetemplate,mutating=false,failurePolicy=fail,sideEffects=None,groups=infrastructure.cluster.x-k8s.io,resources=stackitmachinetemplates,verbs=create;update,versions=v1alpha1,name=vstackitmachinetemplate-v1alpha1.kb.io,admissionReviewVersions=v1

// StackitMachineTemplateCustomValidator struct is responsible for validating the StackitMachineTemplate resource
// when it is created, updated, or deleted.
//
// NOTE: The +kubebuilder:object:generate=false marker prevents controller-gen from generating DeepCopy methods,
// as this struct is used only for temporary operations and does not need to be deeply copied.
type StackitMachineTemplateCustomValidator struct{}

// ValidateCreate implements webhook.CustomValidator so a webhook will be registered for the type StackitMachineTemplate.
func (v *StackitMachineTemplateCustomValidator) ValidateCreate(_ context.Context, obj *infrastructurev1alpha1.StackitMachineTemplate) (admission.Warnings, error) {
	stackitmachinetemplatelog.Info("Validation for StackitMachineTemplate upon creation", "name", obj.GetName())

	allErrs := validateTemplateObjectMeta(obj.Spec.Template.ObjectMeta, fieldPathSpec().Child("template", "metadata"))
	allErrs = append(allErrs, validateStackitMachineSpec(obj.Spec.Template.Spec, fieldPathSpec().Child("template", "spec"), false)...)

	return nil, invalidFor("StackitMachineTemplate", obj.Name, allErrs)
}

// ValidateUpdate implements webhook.CustomValidator so a webhook will be registered for the type StackitMachineTemplate.
func (v *StackitMachineTemplateCustomValidator) ValidateUpdate(_ context.Context, oldObj, newObj *infrastructurev1alpha1.StackitMachineTemplate) (admission.Warnings, error) {
	stackitmachinetemplatelog.Info("Validation for StackitMachineTemplate upon update", "name", newObj.GetName())

	allErrs := validateTemplateObjectMeta(newObj.Spec.Template.ObjectMeta, fieldPathSpec().Child("template", "metadata"))
	allErrs = append(allErrs, validateStackitMachineSpec(newObj.Spec.Template.Spec, fieldPathSpec().Child("template", "spec"), false)...)
	oldSpec := oldObj.Spec.Template.Spec.DeepCopy()
	newSpec := newObj.Spec.Template.Spec.DeepCopy()
	defaultMachineSpec(oldSpec)
	defaultMachineSpec(newSpec)
	if !equality.Semantic.DeepEqual(oldSpec, newSpec) {
		allErrs = append(allErrs, field.Invalid(fieldPathSpec().Child("template", "spec"), newObj.Spec.Template.Spec, immutableTemplateSpecMessage))
	}

	return nil, invalidFor("StackitMachineTemplate", newObj.Name, allErrs)
}

// ValidateDelete implements webhook.CustomValidator so a webhook will be registered for the type StackitMachineTemplate.
func (v *StackitMachineTemplateCustomValidator) ValidateDelete(_ context.Context, obj *infrastructurev1alpha1.StackitMachineTemplate) (admission.Warnings, error) {
	stackitmachinetemplatelog.Info("Validation for StackitMachineTemplate upon deletion", "name", obj.GetName())

	return nil, nil
}
