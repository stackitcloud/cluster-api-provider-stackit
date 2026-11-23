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
	"fmt"
	"net/netip"
	"slices"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	"sigs.k8s.io/cluster-api/util/collections"
	"sigs.k8s.io/cluster-api/util/patch"
	"sigs.k8s.io/cluster-api/util/record"

	infrav1 "github.com/stackitcloud/cluster-api-provider-stackit/api/v1alpha1"
	"github.com/stackitcloud/cluster-api-provider-stackit/cloud"
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

const (
	bastionSSHPort         = 22
	cloudInitRefKindSecret = "Secret"
)

// ClusterScope holds the per-reconcile state for a StackitCluster.
type ClusterScope struct {
	Cluster        *clusterv1.Cluster
	StackitCluster *infrav1.StackitCluster
	client         client.Client
	cloudClient    cloud.Client

	network *network.Network

	loadBalancerTargets     []loadbalancer.Target
	loadBalancerSeedTargets []loadbalancer.Target

	bastionCloudInit     []byte
	bastionCloudInitHash string

	notReady    bool
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
		client:         k8sClient,
		patchHelper:    patchHelper,
	}, nil
}

// PatchObject writes back any spec/status changes to the StackitCluster.
func (s *ClusterScope) PatchObject(ctx context.Context) error {
	return s.patchHelper.Patch(ctx, s.StackitCluster, patch.WithOwnedConditions{Conditions: []string{
		infrav1.ClusterReadyCondition,
		infrav1.ClusterNetworkReadyCondition,
		infrav1.ClusterLoadBalancerReadyCondition,
		infrav1.ClusterBastionReadyCondition,
		infrav1.ClusterCredentialsReadyCondition,
		clusterv1.PausedCondition,
	}})
}

func (s *ClusterScope) Name() string {
	return s.StackitCluster.Name
}

func (s *ClusterScope) FindCondition(conditionType string) *metav1.Condition {
	return meta.FindStatusCondition(s.StackitCluster.Status.Conditions, conditionType)
}

func (s *ClusterScope) Event(reason, message string) {
	record.Event(s.StackitCluster, reason, message)
}

func (s *ClusterScope) Eventf(reason, messageFmt string, args ...any) {
	record.Eventf(s.StackitCluster, reason, messageFmt, args...)
}

func (s *ClusterScope) Warn(reason, message string) {
	record.Warn(s.StackitCluster, reason, message)
}

func (s *ClusterScope) Warnf(reason, messageFmt string, args ...any) {
	record.Warnf(s.StackitCluster, reason, messageFmt, args...)
}

func (s *ClusterScope) SetConditions(status metav1.ConditionStatus, reason, message string, conditionTypes ...string) {
	setConditions(&s.StackitCluster.Status.Conditions, s.StackitCluster.Generation, status, reason, message, conditionTypes...)
}

func (s *ClusterScope) SetPausedCondition(paused bool, message string) {
	setPausedCondition(&s.StackitCluster.Status.Conditions, s.StackitCluster.Generation, paused, message)
}

func setConditions(
	conditions *[]metav1.Condition,
	generation int64,
	status metav1.ConditionStatus,
	reason, message string,
	conditionTypes ...string,
) {
	for _, conditionType := range conditionTypes {
		meta.SetStatusCondition(conditions, metav1.Condition{
			Type:               conditionType,
			Status:             status,
			Reason:             reason,
			Message:            message,
			ObservedGeneration: generation,
		})
	}
}

func setPausedCondition(conditions *[]metav1.Condition, generation int64, paused bool, message string) {
	if paused {
		setConditions(conditions, generation, metav1.ConditionTrue, clusterv1.PausedReason, message, clusterv1.PausedCondition)
		return
	}
	setConditions(conditions, generation, metav1.ConditionFalse, clusterv1.NotPausedReason, "", clusterv1.PausedCondition)
}

func (s *ClusterScope) SetReady() {
	s.StackitCluster.Status.Ready = true
	s.StackitCluster.Status.Initialization.Provisioned = true
	s.SetConditions(metav1.ConditionTrue, "Available", "", infrav1.ClusterReadyCondition)
}

func (s *ClusterScope) SetNotReady(reason, message string, conditionTypes ...string) {
	s.StackitCluster.Status.Ready = false
	s.notReady = true
	s.SetConditions(metav1.ConditionFalse, reason, message, conditionTypes...)
}

func (s *ClusterScope) NotReady() bool {
	return s.notReady
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

func (s *ClusterScope) BastionEnabled() bool {
	return s.StackitCluster.Spec.Bastion.Enabled
}

func (s *ClusterScope) ResolveBastionCloudInit(ctx context.Context) ([]byte, error) {
	ref := s.StackitCluster.Spec.Bastion.CloudInitRef
	if ref == nil {
		return nil, nil
	}
	key := types.NamespacedName{Namespace: s.StackitCluster.Namespace, Name: ref.Name}
	switch ref.Kind {
	case "ConfigMap":
		configMap := &corev1.ConfigMap{}
		if err := s.client.Get(ctx, key, configMap); err != nil {
			if apierrors.IsNotFound(err) {
				return nil, fmt.Errorf("bastion.cloudInitRef ConfigMap %s not found", key)
			}
			return nil, err
		}
		value, ok := configMap.Data[ref.Key]
		if !ok {
			return nil, fmt.Errorf("bastion.cloudInitRef key %q not found in ConfigMap %s", ref.Key, key)
		}
		return []byte(value), nil
	case cloudInitRefKindSecret:
		secret := &corev1.Secret{}
		if err := s.client.Get(ctx, key, secret); err != nil {
			if apierrors.IsNotFound(err) {
				return nil, fmt.Errorf("bastion.cloudInitRef Secret %s not found", key)
			}
			return nil, err
		}
		value, ok := secret.Data[ref.Key]
		if !ok {
			return nil, fmt.Errorf("bastion.cloudInitRef key %q not found in Secret %s", ref.Key, key)
		}
		return append([]byte(nil), value...), nil
	default:
		return nil, fmt.Errorf("bastion.cloudInitRef.kind must be ConfigMap or Secret")
	}
}

func (s *ClusterScope) SetBastionCloudInit(cloudInit []byte, cloudInitHash string) {
	s.bastionCloudInit = cloudInit
	s.bastionCloudInitHash = cloudInitHash
}

func (s *ClusterScope) BastionServerID() string {
	return s.StackitCluster.Status.Bastion.ServerID
}

func (s *ClusterScope) BastionCloudInitHash() string {
	return s.StackitCluster.Status.Bastion.CloudInitHash
}

func (s *ClusterScope) BastionPublicIP() string {
	return s.StackitCluster.Status.Bastion.PublicIP
}

func (s *ClusterScope) SetServer(srv *server.Server, created bool) {
	if srv == nil {
		s.StackitCluster.Status.Bastion.ServerID = ""
		s.StackitCluster.Status.Bastion.CloudInitHash = ""
		return
	}
	s.StackitCluster.Status.Bastion.ServerID = srv.ID
	if created {
		s.StackitCluster.Status.Bastion.CloudInitHash = s.bastionCloudInitHash
	}
}

func (s *ClusterScope) ServerGoneResult(err error) (ctrl.Result, error) {
	return ctrl.Result{}, err
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
		NetworkID:        s.NetworkID(),
		UserData:         s.bastionCloudInit,
		Tags:             s.bastionTags(),
		RootVolume:       rootVolumeFromSpec(s.StackitCluster.Spec.Bastion.RootVolume),
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

func (s *ClusterScope) SetSecurityGroup(specName string, sg *securitygroup.SecurityGroup) {
	if specName != s.StackitCluster.Name+"-bastion-ssh" {
		return
	}
	if sg == nil {
		s.StackitCluster.Status.Bastion.SecurityGroupID = ""
		return
	}
	s.StackitCluster.Status.Bastion.SecurityGroupID = sg.ID
}

func (s *ClusterScope) HasBastionStatus() bool {
	status := s.StackitCluster.Status.Bastion
	return status.ServerID != "" || status.PublicIPID != "" || status.PublicIP != "" || status.SecurityGroupID != "" || status.CloudInitHash != ""
}

func (s *ClusterScope) SetFailureDomains(failureDomains []clusterv1.FailureDomain) {
	s.StackitCluster.Status.FailureDomains = failureDomains
}

func (s *ClusterScope) SetZones(zones []string) {
	s.SetFailureDomains(failureDomainsFromZones(s.Region(), zones))
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

func (s *ClusterScope) APIServerLoadBalancerEnabled() bool {
	return s.StackitCluster.Spec.APIServerLoadBalancer.Enabled
}

func (s *ClusterScope) ControlPlaneEndpointHost() string {
	return s.StackitCluster.Spec.ControlPlaneEndpoint.Host
}

func (s *ClusterScope) UseExternalControlPlaneEndpoint() {
	s.StackitCluster.Status.APIServerEndpoint = s.StackitCluster.Spec.ControlPlaneEndpoint
}

func (s *ClusterScope) APIServerEndpointHost() string {
	return s.StackitCluster.Status.APIServerEndpoint.Host
}

func (s *ClusterScope) SetLoadBalancer(apiServerLB *loadbalancer.LoadBalancer) {
	if apiServerLB == nil {
		s.StackitCluster.Status.APIServerLoadBalancerID = ""
		s.StackitCluster.Status.APIServerEndpoint = clusterv1.APIEndpoint{}
		return
	}
	s.StackitCluster.Status.APIServerLoadBalancerID = apiServerLB.ID
	if apiServerLB.IP == "" {
		s.StackitCluster.Status.APIServerEndpoint = clusterv1.APIEndpoint{}
		return
	}
	s.setAPIServerEndpoint(clusterv1.APIEndpoint{
		Host: apiServerLB.IP,
		Port: apiServerLB.Port,
	})
}

func (s *ClusterScope) TargetPoolFailureResult(_ error, requeueAfter time.Duration) (ctrl.Result, error) {
	return ctrl.Result{RequeueAfter: requeueAfter}, nil
}

func (s *ClusterScope) SetAPIServerLoadBalancerTargets(targets, seedTargets []loadbalancer.Target) {
	s.loadBalancerTargets = targets
	s.loadBalancerSeedTargets = seedTargets
}

func (s *ClusterScope) ResolveLoadBalancerSpec(ctx context.Context) (loadbalancer.LoadBalancerSpec, error) {
	if s.client != nil && s.Cluster != nil {
		// STACKIT takes an explicit target list rather than label-based
		// membership, so exactly one reconciler owns the pool. Machines being
		// deleted drop out here, before the node is drained.
		machines, err := collections.GetFilteredMachinesForCluster(
			ctx,
			s.client,
			s.Cluster,
			collections.ControlPlaneMachines(s.Cluster.Name),
			collections.ActiveMachines,
		)
		if err != nil {
			s.SetConditions(
				metav1.ConditionFalse,
				"MachineListError",
				err.Error(),
				infrav1.ClusterLoadBalancerReadyCondition,
			)
			return loadbalancer.LoadBalancerSpec{}, fmt.Errorf("list control plane machines: %w", err)
		}
		targets := loadbalancer.APIServerTargets(machines.UnsortedList())

		// Seeding with the real targets lets a load balancer deleted out of band
		// come back with the control plane behind it.
		seed := targets
		if len(seed) == 0 {
			seed = []loadbalancer.Target{loadbalancer.BootstrapTarget(s.BootstrapTargetIP())}
		}
		s.SetAPIServerLoadBalancerTargets(targets, seed)
	}
	return s.LoadBalancerSpec(), nil
}

func (s *ClusterScope) LoadBalancerSpec() loadbalancer.LoadBalancerSpec {
	return loadbalancer.LoadBalancerSpec{
		ID:          s.StackitCluster.Status.APIServerLoadBalancerID,
		Name:        s.StackitCluster.Name + "-apiserver",
		NetworkID:   s.NetworkID(),
		Port:        loadbalancer.DefaultAPIServerPort,
		Tags:        s.apiServerLoadBalancerTags(),
		Targets:     s.loadBalancerTargets,
		SeedTargets: s.loadBalancerSeedTargets,
	}
}

func (s *ClusterScope) SetNetwork(net *network.Network) {
	if net == nil {
		s.network = nil
		s.StackitCluster.Status.Network = infrav1.StackitNetworkStatus{}
		return
	}
	s.network = net
	s.StackitCluster.Status.Network = infrav1.StackitNetworkStatus{
		ID:      net.ID,
		Managed: s.StackitCluster.Spec.Network.ID == "",
	}
}

func (s *ClusterScope) NetworkSpec() network.NetworkSpec {
	if s.StackitCluster.Spec.Network.ID != "" {
		return network.NetworkSpec{
			ID: s.StackitCluster.Spec.Network.ID,
		}
	}
	return network.NetworkSpec{
		ID:         s.StackitCluster.Status.Network.ID,
		Name:       s.StackitCluster.Name + "-network",
		IPv4Prefix: network.DefaultNetworkPrefix,
		Tags:       util.ClusterTags(s.StackitCluster.Name, s.StackitCluster.Namespace, s.StackitCluster.Spec.AdditionalLabels),
		Managed:    true,
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

func (s *ClusterScope) SetPublicIP(pip *publicip.PublicIP) {
	if pip == nil {
		s.StackitCluster.Status.Bastion.PublicIPID = ""
		s.StackitCluster.Status.Bastion.PublicIP = ""
		return
	}
	s.StackitCluster.Status.Bastion.PublicIPID = pip.ID
	s.StackitCluster.Status.Bastion.PublicIP = pip.IP
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

// NetworkID returns the cluster network ID (from spec if BYO, or status if managed).
func (s *ClusterScope) NetworkID() string {
	if s.StackitCluster == nil {
		return ""
	}
	if s.StackitCluster.Spec.Network.ID != "" {
		return s.StackitCluster.Spec.Network.ID
	}
	return s.StackitCluster.Status.Network.ID
}
