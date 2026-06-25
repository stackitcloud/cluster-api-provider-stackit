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
	"fmt"
	"net/netip"
	"reflect"
	"regexp"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/apimachinery/pkg/util/validation/field"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"

	infrav1 "github.com/stackitcloud/cluster-api-provider-stackit/api/v1alpha1"
	"github.com/stackitcloud/cluster-api-provider-stackit/cloud"
)

const (
	immutableTemplateSpecMessage = "field is immutable; create a new template resource instead"
)

var (
	uuidPattern             = regexp.MustCompile(`^[0-9a-fA-F-]{36}$`)
	regionPattern           = regexp.MustCompile(`^[a-z]{2}[0-9]{2}$`)
	machineTypePattern      = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]*[a-z0-9]$`)
	availabilityZonePattern = regexp.MustCompile(`^[a-z]{2}[0-9]{2}-[a-z0-9]+$`)
	sshKeyNamePattern       = regexp.MustCompile(`^[A-Za-z0-9_.@-]+$`)
)

func invalidFor(kind string, name string, allErrs field.ErrorList) error {
	if len(allErrs) == 0 {
		return nil
	}
	return apierrors.NewInvalid(infrav1.GroupVersion.WithKind(kind).GroupKind(), name, allErrs)
}

func fieldPathSpec() *field.Path {
	return field.NewPath("spec")
}

func defaultClusterSpec(spec *infrav1.StackitClusterSpec) {
	if spec.Bastion.Enabled || rootVolumeConfigured(spec.Bastion.RootVolume) {
		defaultRootVolume(&spec.Bastion.RootVolume)
	}
}

func defaultMachineSpec(spec *infrav1.StackitMachineSpec) {
	if rootVolumeConfigured(spec.RootVolume) {
		defaultRootVolume(&spec.RootVolume)
	}
}

func defaultRootVolume(rootVolume *infrav1.StackitRootVolumeSpec) {
	if rootVolume.DeleteOnTermination == nil {
		deleteOnTermination := true
		rootVolume.DeleteOnTermination = &deleteOnTermination
	}
}

func rootVolumeConfigured(rootVolume infrav1.StackitRootVolumeSpec) bool {
	return rootVolume.SizeGiB > 0 || rootVolume.PerformanceClass != "" || rootVolume.DeleteOnTermination != nil
}

func validateStackitClusterSpec(spec infrav1.StackitClusterSpec, fldPath *field.Path) field.ErrorList {
	var allErrs field.ErrorList

	allErrs = append(allErrs, validateRequiredPattern(spec.ProjectID, uuidPattern, fldPath.Child("projectID"), "must be a STACKIT project UUID")...)
	allErrs = append(allErrs, validateRequiredPattern(spec.Region, regionPattern, fldPath.Child("region"), "must use STACKIT region format, for example eu01")...)
	allErrs = append(allErrs, validateSecretReference(spec.CredentialsSecretRef, fldPath.Child("credentialsSecretRef"))...)
	allErrs = append(allErrs, validateRequiredPattern(spec.Network.ID, uuidPattern, fldPath.Child("network", "id"), "must be a STACKIT network UUID")...)

	if !spec.APIServerLoadBalancer.Enabled && spec.ControlPlaneEndpoint.Host == "" {
		allErrs = append(allErrs, field.Required(fldPath.Child("controlPlaneEndpoint", "host"), "required when apiServerLoadBalancer.enabled is false"))
	}

	allErrs = append(allErrs, validateBastionSpec(spec.Bastion, fldPath.Child("bastion"))...)
	allErrs = append(allErrs, validateAdditionalLabels(spec.AdditionalLabels, fldPath.Child("additionalLabels"))...)

	return allErrs
}

func validateStackitClusterSpecUpdate(oldCluster, newCluster *infrav1.StackitCluster) field.ErrorList {
	var allErrs field.ErrorList
	specPath := field.NewPath("spec")

	if oldCluster.Spec.ProjectID != newCluster.Spec.ProjectID {
		allErrs = append(allErrs, field.Forbidden(specPath.Child("projectID"), "field is immutable"))
	}
	if oldCluster.Spec.Region != newCluster.Spec.Region {
		allErrs = append(allErrs, field.Forbidden(specPath.Child("region"), "field is immutable"))
	}
	if oldCluster.Spec.Network.ID != newCluster.Spec.Network.ID {
		allErrs = append(allErrs, field.Forbidden(specPath.Child("network", "id"), "field is immutable"))
	}
	if !reflect.DeepEqual(oldCluster.Spec.CredentialsSecretRef, newCluster.Spec.CredentialsSecretRef) {
		allErrs = append(allErrs, field.Forbidden(specPath.Child("credentialsSecretRef"), "field is immutable"))
	}
	if oldCluster.Status.APIServerLoadBalancerID != "" &&
		oldCluster.Spec.APIServerLoadBalancer.Enabled != newCluster.Spec.APIServerLoadBalancer.Enabled {
		allErrs = append(allErrs, field.Forbidden(specPath.Child("apiServerLoadBalancer", "enabled"), "field is immutable after the API server load balancer has been provisioned"))
	}

	if oldCluster.Status.Bastion.ServerID != "" &&
		oldCluster.Spec.Bastion.Enabled &&
		newCluster.Spec.Bastion.Enabled {
		oldBastion := oldCluster.Spec.Bastion.DeepCopy()
		newBastion := newCluster.Spec.Bastion.DeepCopy()
		oldBastion.CloudInitRef = nil
		newBastion.CloudInitRef = nil
		if !equality.Semantic.DeepEqual(oldBastion, newBastion) {
			allErrs = append(allErrs, field.Forbidden(specPath.Child("bastion"), "bastion fields other than cloudInitRef are immutable after the bastion server has been provisioned"))
		}
	}

	return allErrs
}

func validateBastionSpec(spec infrav1.StackitBastionSpec, fldPath *field.Path) field.ErrorList {
	var allErrs field.ErrorList

	if spec.Enabled {
		allErrs = append(allErrs, validateRequiredPattern(spec.ImageID, uuidPattern, fldPath.Child("imageID"), "must be a STACKIT image UUID")...)
		allErrs = append(allErrs, validateRequiredPattern(spec.MachineType, machineTypePattern, fldPath.Child("machineType"), "must be a valid STACKIT machine type")...)
		allErrs = append(allErrs, validateRequiredPattern(spec.SSHKeyName, sshKeyNamePattern, fldPath.Child("sshKeyName"), "must be a valid STACKIT SSH key name")...)
		if len(spec.AllowedCIDRs) == 0 {
			allErrs = append(allErrs, field.Required(fldPath.Child("allowedCIDRs"), "required when bastion.enabled is true"))
		}
	}

	for i, cidr := range spec.AllowedCIDRs {
		if _, err := netip.ParsePrefix(cidr); err != nil {
			allErrs = append(allErrs, field.Invalid(fldPath.Child("allowedCIDRs").Index(i), cidr, "must be a valid CIDR"))
		}
	}

	if spec.ImageID != "" && !uuidPattern.MatchString(spec.ImageID) {
		allErrs = append(allErrs, field.Invalid(fldPath.Child("imageID"), spec.ImageID, "must be a STACKIT image UUID"))
	}
	if spec.MachineType != "" && !machineTypePattern.MatchString(spec.MachineType) {
		allErrs = append(allErrs, field.Invalid(fldPath.Child("machineType"), spec.MachineType, "must be a valid STACKIT machine type"))
	}
	if spec.SSHKeyName != "" && !sshKeyNamePattern.MatchString(spec.SSHKeyName) {
		allErrs = append(allErrs, field.Invalid(fldPath.Child("sshKeyName"), spec.SSHKeyName, "must be a valid STACKIT SSH key name"))
	}

	allErrs = append(allErrs, validateRootVolume(spec.RootVolume, fldPath.Child("rootVolume"))...)
	if spec.CloudInitRef != nil {
		allErrs = append(allErrs, validateBastionCloudInitRef(*spec.CloudInitRef, fldPath.Child("cloudInitRef"))...)
	}

	return allErrs
}

func validateBastionCloudInitRef(ref infrav1.StackitBastionCloudInitRef, fldPath *field.Path) field.ErrorList {
	var allErrs field.ErrorList
	if ref.Kind != "ConfigMap" && ref.Kind != "Secret" {
		allErrs = append(allErrs, field.NotSupported(fldPath.Child("kind"), ref.Kind, []string{"ConfigMap", "Secret"}))
	}
	if ref.Name == "" {
		allErrs = append(allErrs, field.Required(fldPath.Child("name"), "required when cloudInitRef is set"))
	} else {
		allErrs = append(allErrs, validateDNS1123Subdomain(ref.Name, fldPath.Child("name"))...)
	}
	if ref.Key == "" {
		allErrs = append(allErrs, field.Required(fldPath.Child("key"), "required when cloudInitRef is set"))
	}
	return allErrs
}

func validateStackitMachineSpec(spec infrav1.StackitMachineSpec, fldPath *field.Path, allowProviderID bool) field.ErrorList {
	var allErrs field.ErrorList

	if spec.ProviderID != nil {
		if !allowProviderID {
			allErrs = append(allErrs, field.Forbidden(fldPath.Child("providerID"), "providerID is generated per machine and must not be set in a template"))
		} else if _, _, _, err := cloud.ParseProviderID(*spec.ProviderID); err != nil {
			allErrs = append(allErrs, field.Invalid(fldPath.Child("providerID"), *spec.ProviderID, "must use STACKIT providerID format stackit://<server-id>"))
		}
	}

	allErrs = append(allErrs, validateRequiredPattern(spec.ImageID, uuidPattern, fldPath.Child("imageID"), "must be a STACKIT image UUID")...)
	allErrs = append(allErrs, validateRequiredPattern(spec.MachineType, machineTypePattern, fldPath.Child("machineType"), "must be a valid STACKIT machine type")...)
	if spec.AvailabilityZone != "" && !availabilityZonePattern.MatchString(spec.AvailabilityZone) {
		allErrs = append(allErrs, field.Invalid(fldPath.Child("availabilityZone"), spec.AvailabilityZone, "must use STACKIT availability zone format, for example eu01-1"))
	}
	if spec.SSHKeyName != "" && !sshKeyNamePattern.MatchString(spec.SSHKeyName) {
		allErrs = append(allErrs, field.Invalid(fldPath.Child("sshKeyName"), spec.SSHKeyName, "must be a valid STACKIT SSH key name"))
	}
	allErrs = append(allErrs, validateRequiredPattern(spec.Network.ID, uuidPattern, fldPath.Child("network", "id"), "must be a STACKIT network UUID")...)
	allErrs = append(allErrs, validateRootVolume(spec.RootVolume, fldPath.Child("rootVolume"))...)
	allErrs = append(allErrs, validateSecurityGroups(spec.SecurityGroups, fldPath.Child("securityGroups"))...)
	allErrs = append(allErrs, validateAdditionalLabels(spec.AdditionalLabels, fldPath.Child("additionalLabels"))...)

	return allErrs
}

func validateStackitMachineSpecUpdate(oldMachine, newMachine *infrav1.StackitMachine) field.ErrorList {
	var allErrs field.ErrorList
	specPath := field.NewPath("spec")

	oldSpec := oldMachine.Spec.DeepCopy()
	newSpec := newMachine.Spec.DeepCopy()
	oldProviderID := oldSpec.ProviderID
	newProviderID := newSpec.ProviderID
	oldSpec.ProviderID = nil
	newSpec.ProviderID = nil
	defaultMachineSpec(oldSpec)
	defaultMachineSpec(newSpec)

	oldAdditionalLabels := oldSpec.AdditionalLabels
	newAdditionalLabels := newSpec.AdditionalLabels
	oldSpec.AdditionalLabels = nil
	newSpec.AdditionalLabels = nil

	if !equality.Semantic.DeepEqual(oldSpec, newSpec) {
		allErrs = append(allErrs, field.Forbidden(specPath, "VM creation fields are immutable; create a replacement Machine instead"))
	}

	if oldProviderID != nil && newProviderID == nil {
		allErrs = append(allErrs, field.Forbidden(specPath.Child("providerID"), "providerID is controller-owned and cannot be removed"))
	}
	if oldProviderID != nil && newProviderID != nil && *oldProviderID != *newProviderID {
		allErrs = append(allErrs, field.Forbidden(specPath.Child("providerID"), "providerID is controller-owned and cannot be changed"))
	}

	if oldMachine.Status.InstanceID != "" && !equality.Semantic.DeepEqual(oldAdditionalLabels, newAdditionalLabels) {
		allErrs = append(allErrs, field.Forbidden(specPath.Child("additionalLabels"), "field is immutable after the server has been provisioned"))
	}

	return allErrs
}

func validateRootVolume(rootVolume infrav1.StackitRootVolumeSpec, fldPath *field.Path) field.ErrorList {
	var allErrs field.ErrorList
	if rootVolume.SizeGiB < 0 {
		allErrs = append(allErrs, field.Invalid(fldPath.Child("sizeGiB"), rootVolume.SizeGiB, "must be greater than or equal to 0"))
	}
	if rootVolume.DeleteOnTermination != nil && rootVolume.SizeGiB == 0 && rootVolume.PerformanceClass == "" {
		allErrs = append(allErrs, field.Invalid(fldPath.Child("deleteOnTermination"), *rootVolume.DeleteOnTermination, "requires rootVolume.sizeGiB or rootVolume.performanceClass"))
	}
	return allErrs
}

func validateSecurityGroups(securityGroups []string, fldPath *field.Path) field.ErrorList {
	var allErrs field.ErrorList
	seen := map[string]struct{}{}
	for i, securityGroup := range securityGroups {
		if !uuidPattern.MatchString(securityGroup) {
			allErrs = append(allErrs, field.Invalid(fldPath.Index(i), securityGroup, "must be a STACKIT security group UUID"))
			continue
		}
		if _, ok := seen[securityGroup]; ok {
			allErrs = append(allErrs, field.Duplicate(fldPath.Index(i), securityGroup))
			continue
		}
		seen[securityGroup] = struct{}{}
	}
	return allErrs
}

func validateAdditionalLabels(labels map[string]string, fldPath *field.Path) field.ErrorList {
	var allErrs field.ErrorList
	for key, value := range labels {
		if errs := validation.IsQualifiedName(key); len(errs) > 0 {
			allErrs = append(allErrs, field.Invalid(fldPath.Key(key), key, fmt.Sprintf("invalid label key: %s", joinValidationErrors(errs))))
		}
		if errs := validation.IsValidLabelValue(value); len(errs) > 0 {
			allErrs = append(allErrs, field.Invalid(fldPath.Key(key), value, fmt.Sprintf("invalid label value: %s", joinValidationErrors(errs))))
		}
	}
	return allErrs
}

func validateTemplateObjectMeta(metadata clusterv1.ObjectMeta, fldPath *field.Path) field.ErrorList {
	return metadata.Validate(fldPath)
}

func validateSecretReference(ref corev1.SecretReference, fldPath *field.Path) field.ErrorList {
	var allErrs field.ErrorList
	if ref.Name == "" {
		allErrs = append(allErrs, field.Required(fldPath.Child("name"), "required"))
	} else {
		allErrs = append(allErrs, validateDNS1123Subdomain(ref.Name, fldPath.Child("name"))...)
	}
	if ref.Namespace != "" {
		allErrs = append(allErrs, validateDNS1123Subdomain(ref.Namespace, fldPath.Child("namespace"))...)
	}
	return allErrs
}

func validateRequiredPattern(value string, pattern *regexp.Regexp, fldPath *field.Path, detail string) field.ErrorList {
	if value == "" {
		return field.ErrorList{field.Required(fldPath, "required")}
	}
	if !pattern.MatchString(value) {
		return field.ErrorList{field.Invalid(fldPath, value, detail)}
	}
	return nil
}

func validateDNS1123Subdomain(value string, fldPath *field.Path) field.ErrorList {
	if errs := validation.IsDNS1123Subdomain(value); len(errs) > 0 {
		return field.ErrorList{field.Invalid(fldPath, value, joinValidationErrors(errs))}
	}
	return nil
}

func joinValidationErrors(errs []string) string {
	return strings.Join(errs, "; ")
}
