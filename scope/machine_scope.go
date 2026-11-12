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

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	"sigs.k8s.io/cluster-api/util/patch"

	infrav1 "github.com/stackitcloud/cluster-api-provider-stackit/api/v1alpha1"
	"github.com/stackitcloud/cluster-api-provider-stackit/cloud"
	"github.com/stackitcloud/cluster-api-provider-stackit/cloud/services/securitygroup"
	"github.com/stackitcloud/cluster-api-provider-stackit/cloud/services/server"
	"github.com/stackitcloud/cluster-api-provider-stackit/util"
	iaas "github.com/stackitcloud/stackit-sdk-go/services/iaas/v2api"
)

// MachineScope holds the per-reconcile state for a StackitMachine.
type MachineScope struct {
	Client         client.Client
	Cluster        *clusterv1.Cluster
	Machine        *clusterv1.Machine
	StackitCluster *infrav1.StackitCluster
	StackitMachine *infrav1.StackitMachine
	CloudClient    cloud.Client

	bootstrapData       []byte
	instancePowerStatus string
	instanceCreated     bool

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
		Client:         k8sClient,
		Cluster:        cluster,
		Machine:        machine,
		StackitCluster: stackitCluster,
		StackitMachine: stackitMachine,
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

func (s *MachineScope) SetConditions(status metav1.ConditionStatus, reason, message string, conditionTypes ...string) {
	util.SetConditions(&s.StackitMachine.Status.Conditions, s.StackitMachine.Generation, status, reason, message, conditionTypes...)
}

func (s *MachineScope) SetReady() {
	s.StackitMachine.Status.Ready = true
	s.SetConditions(metav1.ConditionTrue, "Available", "", infrav1.MachineInstanceReadyCondition, infrav1.MachineReadyCondition)
}

func (s *MachineScope) SetNotReady(reason, message string, conditionTypes ...string) {
	s.StackitMachine.Status.Ready = false
	s.SetConditions(metav1.ConditionFalse, reason, message, conditionTypes...)
}

func (s *MachineScope) Tags() map[string]string {
	return util.MachineTags(
		s.Cluster.Name,
		s.Cluster.Namespace,
		s.Machine.Name,
		string(s.Machine.UID),
		s.StackitMachine.Spec.AdditionalLabels,
	)
}

func (s *MachineScope) SetInstance(srv *cloud.Server) string {
	providerID := cloud.NewProviderID(s.StackitCluster.Spec.ProjectID, s.StackitCluster.Spec.Region, srv.ID)
	s.StackitMachine.Status.InstanceID = srv.ID
	s.StackitMachine.Status.InstanceState = srv.State
	s.StackitMachine.Status.AvailabilityZone = srv.AvailabilityZone
	s.StackitMachine.Spec.ProviderID = &providerID
	s.StackitMachine.Status.ProviderID = providerID
	s.StackitMachine.Status.Initialization.Provisioned = true
	return providerID
}

func (s *MachineScope) ClearInstance() {
	s.StackitMachine.Status.InstanceID = ""
	s.StackitMachine.Status.InstanceState = ""
	s.StackitMachine.Status.AvailabilityZone = ""
}

// AvailabilityZone returns the resolved availability zone for the machine.
// Following Cluster API conventions:
//  1. If Machine.Spec.FailureDomain is set (e.g. by KCP spreading control plane nodes or MachineDeployment),
//     it is used.
//  2. Otherwise, if StackitMachine.Spec.AvailabilityZone is set, it is used.
//  3. Otherwise, if neither is set, it defaults to the first available failure domain
//     from StackitCluster.Status.FailureDomains (sorted alphabetically). This prevents the STACKIT IaaS
//     API from defaulting to the expensive metro zone (eu01-m).
func (s *MachineScope) AvailabilityZone() string {
	if s.Machine != nil && s.Machine.Spec.FailureDomain != "" {
		return s.Machine.Spec.FailureDomain
	}

	if s.StackitMachine != nil && s.StackitMachine.Spec.AvailabilityZone != "" {
		return s.StackitMachine.Spec.AvailabilityZone
	}

	return DefaultAvailabilityZone(s.StackitCluster)
}

func (s *MachineScope) SetCloudClient(cloudClient cloud.Client) {
	s.CloudClient = cloudClient
}

func (s *MachineScope) ProjectID() string {
	if s.StackitCluster.Spec.ProjectID != "" {
		return s.StackitCluster.Spec.ProjectID
	}
	return s.CloudClient.ProjectID()
}

func (s *MachineScope) Region() string {
	if s.StackitCluster.Spec.Region != "" {
		return s.StackitCluster.Spec.Region
	}
	return s.CloudClient.Region()
}

func (s *MachineScope) IaaSClient() *iaas.APIClient {
	return s.CloudClient.IaaSClient()
}

func (s *MachineScope) SetBootstrapData(data []byte) {
	s.bootstrapData = data
}

func (s *MachineScope) InstancePowerStatus() string {
	return s.instancePowerStatus
}

func (s *MachineScope) InstanceCreated() bool {
	return s.instanceCreated
}

func (s *MachineScope) ServerSpec() server.ServerSpec {
	return server.ServerSpec{
		ID:               s.StackitMachine.Status.InstanceID,
		Name:             s.StackitMachine.Name,
		ImageID:          s.StackitMachine.Spec.ImageID,
		MachineType:      s.StackitMachine.Spec.MachineType,
		AvailabilityZone: s.AvailabilityZone(),
		SSHKeyName:       s.StackitMachine.Spec.SSHKeyName,
		NetworkID:        s.StackitMachine.Spec.Network.ID,
		SecurityGroups:   s.StackitMachine.Spec.SecurityGroups,
		UserData:         s.bootstrapData,
		Tags:             s.Tags(),
		RootVolume:       rootVolumeFromSpec(s.StackitMachine.Spec.RootVolume),
		DisallowRecreate: s.StackitMachine.Status.Initialization.Provisioned,
		SetServer: func(srv *server.Server, created bool) {
			s.instanceCreated = created
			s.instancePowerStatus = srv.PowerStatus
			s.StackitMachine.Status.Addresses = machineAddressesFromServer(srv.Addresses)
			s.SetInstance(srv)
		},
		ClearServer: s.ClearInstance,
	}
}

func (s *MachineScope) SecurityGroupSpecs() []securitygroup.SecurityGroupSpec {
	return []securitygroup.SecurityGroupSpec{
		nodeSSHSecurityGroupSpec(
			s.StackitCluster,
			s.StackitCluster.Status.Bastion.SecurityGroupID,
			s.StackitMachine.Status.InstanceID,
		),
	}
}

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
