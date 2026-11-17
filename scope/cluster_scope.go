/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

// Package scope bundles the resolved CAPI / Stackit objects a reconcile pass
// operates on, together with a patch helper, mirroring the convention used
// by upstream CAPI providers.
package scope

import (
	"context"
	"errors"
	"net/netip"
	"slices"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	"sigs.k8s.io/cluster-api/util/patch"

	infrav1 "github.com/stackitcloud/cluster-api-provider-stackit/api/v1alpha1"
	"github.com/stackitcloud/cluster-api-provider-stackit/cloud"
	"github.com/stackitcloud/cluster-api-provider-stackit/cloud/services/availabilityzone"
	"github.com/stackitcloud/cluster-api-provider-stackit/cloud/services/loadbalancer"
	"github.com/stackitcloud/cluster-api-provider-stackit/cloud/services/network"
	"github.com/stackitcloud/cluster-api-provider-stackit/cloud/services/publicip"
	"github.com/stackitcloud/cluster-api-provider-stackit/cloud/services/securitygroup"
	"github.com/stackitcloud/cluster-api-provider-stackit/cloud/services/server"
	"github.com/stackitcloud/cluster-api-provider-stackit/cloud/services/shared"
	"github.com/stackitcloud/cluster-api-provider-stackit/util"
	iaas "github.com/stackitcloud/stackit-sdk-go/services/iaas/v2api"
	lb "github.com/stackitcloud/stackit-sdk-go/services/loadbalancer/v2api"
)

const bastionSSHPort = 22

// ClusterScope holds the per-reconcile state for a StackitCluster.
type ClusterScope struct {
	Cluster        *clusterv1.Cluster
	StackitCluster *infrav1.StackitCluster
	cloudClient    cloud.Client

	network *network.Network

	loadBalancerTargets     []loadbalancer.Target
	loadBalancerSeedTargets []loadbalancer.Target

	bastionCloudInit         []byte
	bastionCloudInitHash     string
	bastionServerState       string
	bastionServerPowerStatus string
	bastionServerCreated     bool

	patchHelper *patch.Helper
}

// NewClusterScope constructs a ClusterScope and snapshots the original
// resource so the patchHelper can compute a minimal patch on close.
func NewClusterScope(
	k8sClient client.Client,
	cluster *clusterv1.Cluster,
	stackitCluster *infrav1.StackitCluster,
) (*ClusterScope, error) {
	patchHelper, err := patch.NewHelper(stackitCluster, k8sClient)
	if err != nil {
		return nil, err
	}
	return &ClusterScope{
		Cluster:        cluster,
		StackitCluster: stackitCluster,
		patchHelper:    patchHelper,
	}, nil
}

// PatchObject writes back any spec/status changes to the StackitCluster.
func (s *ClusterScope) PatchObject(ctx context.Context) error {
	return s.patchHelper.Patch(ctx, s.StackitCluster, patch.WithOwnedConditions{Conditions: []string{
		infrav1.ClusterReadyCondition,
		infrav1.ClusterNetworkReadyCondition,
		infrav1.ClusterLoadBalancerReadyCondition,
		infrav1.ClusterCredentialsReadyCondition,
		clusterv1.PausedCondition,
	}})
}

func (s *ClusterScope) SetConditions(status metav1.ConditionStatus, reason, message string, conditionTypes ...string) {
	util.SetConditions(&s.StackitCluster.Status.Conditions, s.StackitCluster.Generation, status, reason, message, conditionTypes...)
}

func (s *ClusterScope) SetReady() {
	s.StackitCluster.Status.Ready = true
	s.StackitCluster.Status.Initialization.Provisioned = true
	s.SetConditions(metav1.ConditionTrue, "Available", "", infrav1.ClusterReadyCondition)
}

func (s *ClusterScope) SetNotReady(reason, message string, conditionTypes ...string) {
	s.StackitCluster.Status.Ready = false
	s.SetConditions(metav1.ConditionFalse, reason, message, conditionTypes...)
}

// CredentialFailureResult records the failure and decides how to retry.
//
// Invalid credentials need an operator to fix the Secret, so they requeue on a
// slow timer rather than returning an error and spinning through the controller
// backoff. The reconcile reads the Secret again, which is what picks up the fix.
func (s *ClusterScope) CredentialFailureResult(
	err error,
	requeueAfter time.Duration,
	conditionTypes ...string,
) (ctrl.Result, error) {
	s.SetNotReady("CredentialsInvalid", err.Error(), conditionTypes...)
	return credentialFailureResult(err, requeueAfter)
}

func (s *ClusterScope) CloudFailureResult(
	reason string,
	err error,
	requeueAfter time.Duration,
	returnError bool,
	conditionTypes ...string,
) (ctrl.Result, error) {
	s.SetNotReady(reason, err.Error(), conditionTypes...)
	return cloudFailureResult(err, requeueAfter, returnError)
}

func credentialFailureResult(err error, requeueAfter time.Duration) (ctrl.Result, error) {
	if shared.IsUnauthorized(err) || shared.IsInvalidInput(err) || errors.Is(err, util.ErrCredentialsInvalid) {
		return ctrl.Result{RequeueAfter: requeueAfter}, nil
	}
	return ctrl.Result{}, err
}

func cloudFailureResult(err error, requeueAfter time.Duration, returnError bool) (ctrl.Result, error) {
	if shared.IsRetryable(err) {
		return ctrl.Result{RequeueAfter: requeueAfter}, nil
	}
	if returnError {
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
}

func (s *ClusterScope) setAPIServerEndpoint(endpoint clusterv1.APIEndpoint) {
	s.StackitCluster.Spec.ControlPlaneEndpoint = endpoint
	s.StackitCluster.Status.APIServerEndpoint = endpoint
}

func (s *ClusterScope) SetBastionCloudInit(cloudInit []byte, cloudInitHash string) {
	s.bastionCloudInit = cloudInit
	s.bastionCloudInitHash = cloudInitHash
}

func (s *ClusterScope) BastionServerState() (state, powerStatus string) {
	return s.bastionServerState, s.bastionServerPowerStatus
}

func (s *ClusterScope) BastionServerCreated() bool {
	return s.bastionServerCreated
}

func (s *ClusterScope) setBastionServer(srv *server.Server, created bool) {
	if srv == nil {
		s.bastionServerCreated = false
		s.bastionServerState = ""
		s.bastionServerPowerStatus = ""
		s.StackitCluster.Status.Bastion.ServerID = ""
		s.StackitCluster.Status.Bastion.CloudInitHash = ""
		return
	}
	s.bastionServerCreated = created
	s.bastionServerState = srv.State
	s.bastionServerPowerStatus = srv.PowerStatus
	s.StackitCluster.Status.Bastion.ServerID = srv.ID
	if created {
		s.StackitCluster.Status.Bastion.CloudInitHash = s.bastionCloudInitHash
	}
}

func (s *ClusterScope) ServerSpec() server.ServerSpec {
	return server.ServerSpec{
		ID:               s.StackitCluster.Status.Bastion.ServerID,
		Name:             s.StackitCluster.Name + "-bastion",
		ImageID:          s.StackitCluster.Spec.Bastion.ImageID,
		MachineType:      s.StackitCluster.Spec.Bastion.MachineType,
		AvailabilityZone: defaultAvailabilityZone(s.StackitCluster),
		SSHKeyName:       s.StackitCluster.Spec.Bastion.SSHKeyName,
		RequireSSHKey:    true,
		NetworkID:        s.StackitCluster.Spec.Network.ID,
		UserData:         s.bastionCloudInit,
		Tags:             s.bastionTags(),
		RootVolume:       rootVolumeFromSpec(s.StackitCluster.Spec.Bastion.RootVolume),
		SetServer:        s.setBastionServer,
	}
}

func rootVolumeFromSpec(in infrav1.StackitRootVolumeSpec) server.RootVolume {
	deleteOnTermination := true
	if in.DeleteOnTermination != nil {
		deleteOnTermination = *in.DeleteOnTermination
	}
	return server.RootVolume{
		SizeGiB:             in.SizeGiB,
		PerformanceClass:    in.PerformanceClass,
		DeleteOnTermination: deleteOnTermination,
	}
}

func (s *ClusterScope) setBastionSecurityGroupID(id string) {
	s.StackitCluster.Status.Bastion.SecurityGroupID = id
}

func (s *ClusterScope) HasBastionStatus() bool {
	status := s.StackitCluster.Status.Bastion
	return status.ServerID != "" || status.PublicIPID != "" || status.PublicIP != "" || status.SecurityGroupID != "" || status.CloudInitHash != ""
}

func (s *ClusterScope) SetFailureDomains(failureDomains []clusterv1.FailureDomain) {
	s.StackitCluster.Status.FailureDomains = failureDomains
}

// defaultAvailabilityZone returns the default single availability zone for a
// StackitCluster (the alphabetically first published failure domain, falling
// back to <region>-1). This prevents the STACKIT IaaS API from defaulting to
// the expensive metro zone (<region>-m).
func defaultAvailabilityZone(stackitCluster *infrav1.StackitCluster) string {
	if stackitCluster == nil {
		return ""
	}
	if len(stackitCluster.Status.FailureDomains) > 0 {
		zones := make([]string, 0, len(stackitCluster.Status.FailureDomains))
		for _, fd := range stackitCluster.Status.FailureDomains {
			zones = append(zones, fd.Name)
		}
		slices.Sort(zones)
		return zones[0]
	}
	if stackitCluster.Spec.Region != "" {
		return stackitCluster.Spec.Region + "-1"
	}
	return ""
}

func (s *ClusterScope) SetCloudClient(cloudClient cloud.Client) {
	s.cloudClient = cloudClient
}

func (s *ClusterScope) ProjectID() string {
	if s.StackitCluster.Spec.ProjectID != "" {
		return s.StackitCluster.Spec.ProjectID
	}
	return s.cloudClient.ProjectID()
}

func (s *ClusterScope) Region() string {
	if s.StackitCluster.Spec.Region != "" {
		return s.StackitCluster.Spec.Region
	}
	return s.cloudClient.Region()
}

func (s *ClusterScope) IaaSClient() *iaas.APIClient {
	return s.cloudClient.IaaSClient()
}

func (s *ClusterScope) LoadBalancerClient() *lb.APIClient {
	return s.cloudClient.LoadBalancerClient()
}

func (s *ClusterScope) apiServerLoadBalancerTags() map[string]string {
	return util.ClusterTags(s.StackitCluster.Name, s.StackitCluster.Namespace, s.StackitCluster.Spec.AdditionalLabels)
}

func (s *ClusterScope) setAPIServerLoadBalancerID(id string) {
	s.StackitCluster.Status.APIServerLoadBalancerID = id
}

func (s *ClusterScope) setAPIServerLoadBalancerAddress(ip string, port int32) {
	if ip == "" {
		s.StackitCluster.Status.APIServerEndpoint = clusterv1.APIEndpoint{}
		return
	}
	s.setAPIServerEndpoint(clusterv1.APIEndpoint{
		Host: ip,
		Port: port,
	})
}

func (s *ClusterScope) SetAPIServerLoadBalancerTargets(targets, seedTargets []loadbalancer.Target) {
	s.loadBalancerTargets = targets
	s.loadBalancerSeedTargets = seedTargets
}

func (s *ClusterScope) LoadBalancerSpec() loadbalancer.LoadBalancerSpec {
	return loadbalancer.LoadBalancerSpec{
		ID:          s.StackitCluster.Status.APIServerLoadBalancerID,
		Name:        s.StackitCluster.Name + "-apiserver",
		NetworkID:   s.StackitCluster.Spec.Network.ID,
		Port:        loadbalancer.DefaultAPIServerPort,
		Tags:        s.apiServerLoadBalancerTags(),
		Targets:     s.loadBalancerTargets,
		SeedTargets: s.loadBalancerSeedTargets,
		SetID:       s.setAPIServerLoadBalancerID,
		SetAddress:  s.setAPIServerLoadBalancerAddress,
	}
}

func (s *ClusterScope) AvailabilityZoneSpec() availabilityzone.AvailabilityZoneSpec {
	return availabilityzone.AvailabilityZoneSpec{
		SetZones: func(zones []string) {
			s.SetFailureDomains(failureDomainsFromZones(s.Region(), zones))
		},
	}
}

func (s *ClusterScope) NetworkSpec() network.NetworkSpec {
	return network.NetworkSpec{
		ID: s.StackitCluster.Spec.Network.ID,
		SetNetwork: func(net *network.Network) {
			s.network = net
		},
	}
}

func (s *ClusterScope) BootstrapTargetIP() string {
	if s.network == nil {
		return loadbalancer.DefaultBootstrapTargetIP
	}
	for _, prefixValue := range s.network.IPv4Prefixes {
		prefix, err := netip.ParsePrefix(prefixValue)
		if err != nil || !prefix.Addr().Is4() {
			continue
		}
		address := prefix.Masked().Addr()
		for range 10 {
			address = address.Next()
		}
		if prefix.Contains(address) {
			return address.String()
		}
	}
	return loadbalancer.DefaultBootstrapTargetIP
}

func (s *ClusterScope) PublicIPSpec() publicip.PublicIPSpec {
	return publicip.PublicIPSpec{
		ID:             s.StackitCluster.Status.Bastion.PublicIPID,
		Tags:           s.bastionTags(),
		ServerID:       s.StackitCluster.Status.Bastion.ServerID,
		ServerSelector: s.bastionTags(),
		SetPublicIP:    s.setBastionPublicIP,
	}
}

func (s *ClusterScope) bastionTags() map[string]string {
	return bastionTags(s.StackitCluster)
}

func DefaultFailureDomains(region string) []clusterv1.FailureDomain {
	controlPlane := true
	return []clusterv1.FailureDomain{
		{
			Name:         region + "-1",
			ControlPlane: &controlPlane,
			Attributes: map[string]string{
				"region": region,
			},
		},
		{
			Name:         region + "-2",
			ControlPlane: &controlPlane,
			Attributes: map[string]string{
				"region": region,
			},
		},
		{
			Name:         region + "-3",
			ControlPlane: &controlPlane,
			Attributes: map[string]string{
				"region": region,
			},
		},
	}
}

func failureDomainsFromZones(region string, zones []string) []clusterv1.FailureDomain {
	controlPlane := true
	failureDomains := make([]clusterv1.FailureDomain, 0, len(zones))
	for _, zone := range zones {
		// Filter out virtual/metro zones (such as eu01-m or any zone with suffix -m)
		if strings.HasSuffix(zone, "-m") {
			continue
		}
		failureDomains = append(failureDomains, clusterv1.FailureDomain{
			Name:         zone,
			ControlPlane: &controlPlane,
			Attributes: map[string]string{
				"region": region,
			},
		})
	}
	if len(failureDomains) == 0 {
		return DefaultFailureDomains(region)
	}
	slices.SortFunc(failureDomains, func(a, b clusterv1.FailureDomain) int {
		return strings.Compare(a.Name, b.Name)
	})
	return failureDomains
}

func bastionTags(stackitCluster *infrav1.StackitCluster) map[string]string {
	tags := util.ClusterTags(stackitCluster.Name, stackitCluster.Namespace, stackitCluster.Spec.AdditionalLabels)
	tags[util.LabelResourceRole] = util.ResourceRoleBastion
	return tags
}

func NodeSSHTags(stackitCluster *infrav1.StackitCluster) map[string]string {
	tags := util.ClusterTags(stackitCluster.Name, stackitCluster.Namespace, stackitCluster.Spec.AdditionalLabels)
	tags[util.LabelResourceRole] = util.ResourceRoleNodeSSH
	return tags
}

func (s *ClusterScope) setBastionPublicIP(id, ip string) {
	s.StackitCluster.Status.Bastion.PublicIPID = id
	s.StackitCluster.Status.Bastion.PublicIP = ip
}

func (s *ClusterScope) SecurityGroupSpecs() []securitygroup.SecurityGroupSpec {
	allowedCIDRs := s.StackitCluster.Spec.Bastion.AllowedCIDRs
	if len(allowedCIDRs) == 0 {
		allowedCIDRs = []string{""}
	}
	rules := make([]securitygroup.Rule, 0, len(allowedCIDRs)+1)
	for _, cidr := range allowedCIDRs {
		rules = append(rules, securitygroup.Rule{
			Direction: securitygroup.DirectionIngress,
			Protocol:  securitygroup.ProtocolTCP,
			PortMin:   bastionSSHPort,
			PortMax:   bastionSSHPort,
			IPRange:   cidr,
		})
	}
	rules = append(rules, securitygroup.Rule{
		Direction: securitygroup.DirectionEgress,
	})
	return []securitygroup.SecurityGroupSpec{
		{
			ID:             s.StackitCluster.Status.Bastion.SecurityGroupID,
			Name:           s.StackitCluster.Name + "-bastion-ssh",
			Description:    "Cluster API Provider STACKIT bastion SSH access",
			Tags:           s.bastionTags(),
			Rules:          rules,
			ServerIDs:      []string{s.StackitCluster.Status.Bastion.ServerID},
			ServerSelector: s.bastionTags(),
			SetID:          s.setBastionSecurityGroupID,
		},
		nodeSSHSecurityGroupSpec(s.StackitCluster, ""),
	}
}

func nodeSSHSecurityGroupSpec(stackitCluster *infrav1.StackitCluster, remoteSecurityGroupID string, serverIDs ...string) securitygroup.SecurityGroupSpec {
	ingressRule := securitygroup.Rule{
		Direction:             securitygroup.DirectionIngress,
		Protocol:              securitygroup.ProtocolTCP,
		PortMin:               bastionSSHPort,
		PortMax:               bastionSSHPort,
		RemoteSecurityGroupID: remoteSecurityGroupID,
	}
	if remoteSecurityGroupID == "" {
		ingressRule.RemoteSecurityGroupTags = bastionTags(stackitCluster)
	}
	return securitygroup.SecurityGroupSpec{
		Name:           stackitCluster.Name + "-node-ssh",
		Description:    "Cluster API Provider STACKIT node SSH access from bastion",
		Tags:           NodeSSHTags(stackitCluster),
		ServerIDs:      serverIDs,
		ServerSelector: util.ClusterTags(stackitCluster.Name, stackitCluster.Namespace, stackitCluster.Spec.AdditionalLabels),
		Rules: []securitygroup.Rule{
			ingressRule,
			{
				Direction: securitygroup.DirectionEgress,
			},
		},
	}
}
