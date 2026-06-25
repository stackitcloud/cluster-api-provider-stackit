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
var stackitmachinelog = logf.Log.WithName("stackitmachine-resource")

// SetupStackitMachineWebhookWithManager registers the webhook for StackitMachine in the manager.
func SetupStackitMachineWebhookWithManager(mgr ctrl.Manager) error {
	return ctrl.NewWebhookManagedBy(mgr, &infrastructurev1alpha1.StackitMachine{}).
		WithValidator(&StackitMachineCustomValidator{}).
		WithDefaulter(&StackitMachineCustomDefaulter{}).
		Complete()
}

// +kubebuilder:webhook:path=/mutate-infrastructure-cluster-x-k8s-io-v1alpha1-stackitmachine,mutating=true,failurePolicy=fail,sideEffects=None,groups=infrastructure.cluster.x-k8s.io,resources=stackitmachines,verbs=create;update,versions=v1alpha1,name=mstackitmachine-v1alpha1.kb.io,admissionReviewVersions=v1

// StackitMachineCustomDefaulter struct is responsible for setting default values on the custom resource of the
// Kind StackitMachine when those are created or updated.
//
// NOTE: The +kubebuilder:object:generate=false marker prevents controller-gen from generating DeepCopy methods,
// as it is used only for temporary operations and does not need to be deeply copied.
type StackitMachineCustomDefaulter struct{}

// Default implements webhook.CustomDefaulter so a webhook will be registered for the Kind StackitMachine.
func (d *StackitMachineCustomDefaulter) Default(_ context.Context, obj *infrastructurev1alpha1.StackitMachine) error {
	stackitmachinelog.Info("Defaulting for StackitMachine", "name", obj.GetName())

	defaultMachineSpec(&obj.Spec)
	return nil
}

// +kubebuilder:webhook:path=/validate-infrastructure-cluster-x-k8s-io-v1alpha1-stackitmachine,mutating=false,failurePolicy=fail,sideEffects=None,groups=infrastructure.cluster.x-k8s.io,resources=stackitmachines,verbs=create;update,versions=v1alpha1,name=vstackitmachine-v1alpha1.kb.io,admissionReviewVersions=v1

// StackitMachineCustomValidator struct is responsible for validating the StackitMachine resource
// when it is created, updated, or deleted.
//
// NOTE: The +kubebuilder:object:generate=false marker prevents controller-gen from generating DeepCopy methods,
// as this struct is used only for temporary operations and does not need to be deeply copied.
type StackitMachineCustomValidator struct{}

// ValidateCreate implements webhook.CustomValidator so a webhook will be registered for the type StackitMachine.
func (v *StackitMachineCustomValidator) ValidateCreate(_ context.Context, obj *infrastructurev1alpha1.StackitMachine) (admission.Warnings, error) {
	stackitmachinelog.Info("Validation for StackitMachine upon creation", "name", obj.GetName())

	return nil, invalidFor("StackitMachine", obj.Name, validateStackitMachineSpec(obj.Spec, fieldPathSpec(), true))
}

// ValidateUpdate implements webhook.CustomValidator so a webhook will be registered for the type StackitMachine.
func (v *StackitMachineCustomValidator) ValidateUpdate(_ context.Context, oldObj, newObj *infrastructurev1alpha1.StackitMachine) (admission.Warnings, error) {
	stackitmachinelog.Info("Validation for StackitMachine upon update", "name", newObj.GetName())

	allErrs := validateStackitMachineSpec(newObj.Spec, fieldPathSpec(), true)
	allErrs = append(allErrs, validateStackitMachineSpecUpdate(oldObj, newObj)...)

	return nil, invalidFor("StackitMachine", newObj.Name, allErrs)
}

// ValidateDelete implements webhook.CustomValidator so a webhook will be registered for the type StackitMachine.
func (v *StackitMachineCustomValidator) ValidateDelete(_ context.Context, obj *infrastructurev1alpha1.StackitMachine) (admission.Warnings, error) {
	stackitmachinelog.Info("Validation for StackitMachine upon deletion", "name", obj.GetName())

	return nil, nil
}
