/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package scope

import (
	"context"
	"fmt"
	"time"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	"sigs.k8s.io/cluster-api/util/patch"
	"sigs.k8s.io/cluster-api/util/record"

	infrav1 "github.com/stackitcloud/cluster-api-provider-stackit/api/v1alpha1"
	"github.com/stackitcloud/cluster-api-provider-stackit/cloud"
	"github.com/stackitcloud/cluster-api-provider-stackit/cloud/services/securitygroup"
	"github.com/stackitcloud/cluster-api-provider-stackit/cloud/services/server"
	"github.com/stackitcloud/cluster-api-provider-stackit/util"
	iaas "github.com/stackitcloud/stackit-sdk-go/services/iaas/v2api"
)

// MachineScope holds the per-reconcile state for a StackitMachine.
type MachineScope struct {
	Machine        *clusterv1.Machine
	StackitCluster *infrav1.StackitCluster
	StackitMachine *infrav1.StackitMachine
	cluster        *clusterv1.Cluster
	cloudClient    cloud.Client

	bootstrapData []byte
	notReady      bool

	patchHelper *patch.Helper
}

// NewMachineScope constructs a MachineScope and snapshots the original
// StackitMachine for patching.
func NewMachineScope(
	k8sClient client.Client,
	cluster *clusterv1.Cluster,
	machine *clusterv1.Machine,
	stackitCluster *infrav1.StackitCluster,
	stackitMachine *infrav1.StackitMachine,
) (*MachineScope, error) {
	patchHelper, err := patch.NewHelper(stackitMachine, k8sClient)
	if err != nil {
		return nil, err
	}
	return &MachineScope{
		Machine:        machine,
		StackitCluster: stackitCluster,
		StackitMachine: stackitMachine,
		cluster:        cluster,
		patchHelper:    patchHelper,
	}, nil
}

// PatchObject writes back any spec/status changes to the StackitMachine.
func (s *MachineScope) PatchObject(ctx context.Context) error {
	return s.patchHelper.Patch(ctx, s.StackitMachine, patch.WithOwnedConditions{Conditions: []string{
		infrav1.MachineReadyCondition,
		infrav1.MachineBootstrapReadyCondition,
		infrav1.MachineCredentialsReadyCondition,
		infrav1.MachineInstanceReadyCondition,
		clusterv1.PausedCondition,
	}})
}

func (s *MachineScope) Name() string {
	return s.StackitMachine.Name
}

func (s *MachineScope) FindCondition(conditionType string) *metav1.Condition {
	return meta.FindStatusCondition(s.StackitMachine.Status.Conditions, conditionType)
}

func (s *MachineScope) Event(reason, message string) {
	record.Event(s.StackitMachine, reason, message)
}

func (s *MachineScope) Eventf(reason, messageFmt string, args ...any) {
	record.Eventf(s.StackitMachine, reason, messageFmt, args...)
}

func (s *MachineScope) Warn(reason, message string) {
	record.Warn(s.StackitMachine, reason, message)
}

func (s *MachineScope) Warnf(reason, messageFmt string, args ...any) {
	record.Warnf(s.StackitMachine, reason, messageFmt, args...)
}

func (s *MachineScope) SetConditions(status metav1.ConditionStatus, reason, message string, conditionTypes ...string) {
	setConditions(&s.StackitMachine.Status.Conditions, s.StackitMachine.Generation, status, reason, message, conditionTypes...)
}

func (s *MachineScope) SetPausedCondition(paused bool, message string) {
	setPausedCondition(&s.StackitMachine.Status.Conditions, s.StackitMachine.Generation, paused, message)
}

func (s *MachineScope) SetReady() {
	s.StackitMachine.Status.Ready = true
	s.SetConditions(metav1.ConditionTrue, "Available", "", infrav1.MachineInstanceReadyCondition, infrav1.MachineReadyCondition)
}

func (s *MachineScope) SetNotReady(reason, message string, conditionTypes ...string) {
	s.StackitMachine.Status.Ready = false
	s.notReady = true
	s.SetConditions(metav1.ConditionFalse, reason, message, conditionTypes...)
}

func (s *MachineScope) NotReady() bool {
	return s.notReady
}

// CredentialFailureResult records the failure and decides how to retry.
func (s *MachineScope) CredentialFailureResult(
	err error,
	requeueAfter time.Duration,
	conditionTypes ...string,
) (ctrl.Result, error) {
	s.SetNotReady("CredentialsInvalid", err.Error(), conditionTypes...)
	return credentialFailureResult(err, requeueAfter)
}

func (s *MachineScope) CloudFailureResult(
	reason string,
	err error,
	requeueAfter time.Duration,
	returnError bool,
	conditionTypes ...string,
) (ctrl.Result, error) {
	s.SetNotReady(reason, err.Error(), conditionTypes...)
	return cloudFailureResult(err, requeueAfter, returnError)
}

func (s *MachineScope) tags() map[string]string {
	return util.MachineTags(
		s.cluster.Name,
		s.cluster.Namespace,
		s.Machine.Name,
		string(s.Machine.UID),
		s.StackitMachine.Spec.AdditionalLabels,
	)
}

func (s *MachineScope) SetServer(srv *server.Server, _ bool) {
	if srv == nil {
		s.StackitMachine.Status.Addresses = nil
		s.StackitMachine.Status.InstanceID = ""
		s.StackitMachine.Status.InstanceState = ""
		s.StackitMachine.Status.AvailabilityZone = ""
		return
	}
	s.StackitMachine.Status.Addresses = machineAddressesFromServer(srv.Addresses)
	providerID := fmt.Sprintf("stackit://%s", srv.ID)
	s.StackitMachine.Status.InstanceID = srv.ID
	s.StackitMachine.Status.InstanceState = srv.State
	s.StackitMachine.Status.AvailabilityZone = srv.AvailabilityZone
	s.StackitMachine.Spec.ProviderID = &providerID
	s.StackitMachine.Status.ProviderID = providerID
	s.StackitMachine.Status.Initialization.Provisioned = true
}

func (s *MachineScope) ServerGoneResult(_ error) (ctrl.Result, error) {
	return ctrl.Result{}, nil
}

// availabilityZone returns the resolved availability zone for the machine.
// Following Cluster API conventions:
//  1. If Machine.Spec.FailureDomain is set (e.g. by KCP spreading control plane nodes or MachineDeployment),
//     it is used.
//  2. Otherwise, if StackitMachine.Spec.AvailabilityZone is set, it is used.
//  3. Otherwise, if neither is set, it defaults to the first available failure domain
//     from StackitCluster.Status.FailureDomains (sorted alphabetically). This prevents the STACKIT IaaS
//     API from defaulting to the expensive metro zone (eu01-m).
func (s *MachineScope) availabilityZone() string {
	if s.Machine != nil && s.Machine.Spec.FailureDomain != "" {
		return s.Machine.Spec.FailureDomain
	}

	if s.StackitMachine != nil && s.StackitMachine.Spec.AvailabilityZone != "" {
		return s.StackitMachine.Spec.AvailabilityZone
	}

	return defaultAvailabilityZone(s.StackitCluster)
}

func (s *MachineScope) SetCloudClient(cloudClient cloud.Client) {
	s.cloudClient = cloudClient
}

func (s *MachineScope) ProjectID() string {
	if s.StackitCluster.Spec.ProjectID != "" {
		return s.StackitCluster.Spec.ProjectID
	}
	return s.cloudClient.ProjectID()
}

func (s *MachineScope) Region() string {
	if s.StackitCluster.Spec.Region != "" {
		return s.StackitCluster.Spec.Region
	}
	return s.cloudClient.Region()
}

func (s *MachineScope) IaaSClient() *iaas.APIClient {
	return s.cloudClient.IaaSClient()
}

func (s *MachineScope) SetBootstrapData(data []byte) {
	s.bootstrapData = data
}

func (s *MachineScope) ServerSpec() server.ServerSpec {
	return server.ServerSpec{
		ID:               s.StackitMachine.Status.InstanceID,
		Name:             s.StackitMachine.Name,
		ImageID:          s.StackitMachine.Spec.ImageID,
		MachineType:      s.StackitMachine.Spec.MachineType,
		AvailabilityZone: s.availabilityZone(),
		SSHKeyName:       s.StackitMachine.Spec.SSHKeyName,
		NetworkID:        s.NetworkID(),
		SecurityGroups:   s.StackitMachine.Spec.SecurityGroups,
		UserData:         s.bootstrapData,
		Tags:             s.tags(),
		RootVolume:       rootVolumeFromSpec(s.StackitMachine.Spec.RootVolume),
		DisallowRecreate: s.StackitMachine.Status.Initialization.Provisioned,
	}
}

func (s *MachineScope) SecurityGroupSpecs() []securitygroup.SecurityGroupSpec {
	if !s.StackitCluster.Spec.Bastion.Enabled {
		return nil
	}
	return []securitygroup.SecurityGroupSpec{
		nodeSSHSecurityGroupSpec(
			s.StackitCluster,
			s.StackitCluster.Status.Bastion.SecurityGroupID,
			s.StackitMachine.Status.InstanceID,
		),
	}
}

func (s *MachineScope) SetSecurityGroup(_ string, _ *securitygroup.SecurityGroup) {}

func machineAddressesFromServer(in []server.Address) []clusterv1.MachineAddress {
	if len(in) == 0 {
		return nil
	}

	out := make([]clusterv1.MachineAddress, len(in))
	for i, address := range in {
		out[i] = clusterv1.MachineAddress{
			Type:    clusterv1.MachineAddressType(address.Type),
			Address: address.Address,
		}
	}
	return out
}

// NetworkID returns the network ID for this machine, resolving in order:
// 1. StackitMachine.Spec.Network.ID (machine-level override)
// 2. StackitCluster.Spec.Network.ID (BYO cluster network)
// 3. StackitCluster.Status.Network.ID (managed cluster network)
func (s *MachineScope) NetworkID() string {
	if s.StackitMachine != nil && s.StackitMachine.Spec.Network.ID != "" {
		return s.StackitMachine.Spec.Network.ID
	}
	if s.StackitCluster != nil {
		if s.StackitCluster.Spec.Network.ID != "" {
			return s.StackitCluster.Spec.Network.ID
		}
		return s.StackitCluster.Status.Network.ID
	}
	return ""
}
