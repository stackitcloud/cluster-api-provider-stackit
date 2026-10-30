/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package cloud

import (
	"cmp"
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"slices"
	"strings"

	"github.com/stackitcloud/cluster-api-provider-stackit/cloud/services/securitygroup"
	"github.com/stackitcloud/cluster-api-provider-stackit/cloud/services/shared"
	"github.com/stackitcloud/stackit-sdk-go/core/config"
	iaas "github.com/stackitcloud/stackit-sdk-go/services/iaas/v2api"
	lb "github.com/stackitcloud/stackit-sdk-go/services/loadbalancer/v2api"
)

const (
	stackitNoAuthEnv        = "STACKIT_NO_AUTH"
	stackitTokenEndpointEnv = "STACKIT_TOKEN_BASEURL"
	stackitIAASEndpointEnv  = "STACKIT_IAAS_ENDPOINT"
	stackitLBEndpointEnv    = "STACKIT_LOADBALANCER_ENDPOINT"

	apiserverTargetPoolName = "apiserver"
	apiserverListenerName   = "apiserver"
	bootstrapTargetName     = "capi-bootstrap-placeholder"
	bootstrapTargetIP       = "10.0.0.1"
)

// SDKClient is the STACKIT SDK-backed implementation of Client.
type SDKClient struct {
	projectID string
	region    string

	iaasClient *iaas.APIClient
	lbClient   *lb.APIClient
}

// NewClient returns the real STACKIT cloud client.
//
// The credential secret format mirrors machine-controller-manager-provider-stackit:
// "project-id" plus "serviceaccount.json". Authentication uses the STACKIT
// service-account key flow.
func NewClient(_ context.Context, creds Credentials) (Client, error) {
	if creds.ProjectID == "" {
		return nil, fmt.Errorf("%w: project ID is empty", ErrInvalidInput)
	}
	if creds.Region == "" {
		return nil, fmt.Errorf("%w: region is empty", ErrInvalidInput)
	}
	if len(creds.ServiceAccountJSON) == 0 && os.Getenv(stackitNoAuthEnv) != "true" {
		return nil, fmt.Errorf("%w: service account JSON is empty", ErrInvalidInput)
	}

	iaasClient, err := iaas.NewAPIClient(stackitOptions(creds, os.Getenv(stackitIAASEndpointEnv))...)
	if err != nil {
		return nil, fmt.Errorf("%w: create IAAS SDK client: %v", ErrInvalidInput, err)
	}
	lbClient, err := lb.NewAPIClient(stackitOptions(creds, os.Getenv(stackitLBEndpointEnv))...)
	if err != nil {
		return nil, fmt.Errorf("%w: create load balancer SDK client: %v", ErrInvalidInput, err)
	}

	return &SDKClient{
		projectID:  creds.ProjectID,
		region:     creds.Region,
		iaasClient: iaasClient,
		lbClient:   lbClient,
	}, nil
}

func (c *SDKClient) ProjectID() string {
	return c.projectID
}

func (c *SDKClient) Region() string {
	return c.region
}

func (c *SDKClient) IaaSClient() *iaas.APIClient {
	return c.iaasClient
}

func stackitOptions(creds Credentials, endpoint string) []config.ConfigurationOption {
	opts := []config.ConfigurationOption{
		config.WithUserAgent("cluster-api-provider-stackit"),
	}
	if os.Getenv(stackitNoAuthEnv) == "true" {
		opts = append(opts, config.WithoutAuthentication())
	} else {
		opts = append(opts, config.WithServiceAccountKey(string(creds.ServiceAccountJSON)))
	}
	if endpoint != "" {
		opts = append(opts, config.WithEndpoint(endpoint))
	}
	if tokenEndpoint := os.Getenv(stackitTokenEndpointEnv); tokenEndpoint != "" {
		opts = append(opts, config.WithTokenEndpoint(tokenEndpoint))
	}
	return opts
}

func (c *SDKClient) GetServer(ctx context.Context, id string) (*Server, error) {
	server, err := c.iaasClient.DefaultAPI.GetServer(ctx, c.projectID, c.region, id).Execute()
	if err != nil {
		return nil, shared.ClassifySDKError("get server", err)
	}
	return c.serverFromSDK(ctx, server), nil
}

func (c *SDKClient) FindServerByTags(ctx context.Context, tags map[string]string) (*Server, error) {
	servers, err := c.ListServersByTags(ctx, tags)
	if err != nil {
		return nil, err
	}
	if len(servers) == 0 {
		return nil, fmt.Errorf("%w: no server matching tags", ErrNotFound)
	}
	if len(servers) > 1 {
		return nil, fmt.Errorf("%w: multiple servers matching tags", ErrConflict)
	}
	return servers[0], nil
}

func (c *SDKClient) ListServersByTags(ctx context.Context, tags map[string]string) ([]*Server, error) {
	if len(tags) == 0 {
		return nil, fmt.Errorf("%w: empty tag selector", ErrInvalidInput)
	}
	resp, err := c.iaasClient.DefaultAPI.ListServers(ctx, c.projectID, c.region).
		LabelSelector(shared.LabelSelector(tags)).
		Details(true).
		Execute()
	if err != nil {
		return nil, shared.ClassifySDKError("list servers", err)
	}
	matched := []*Server{}
	for _, server := range resp.GetItems() {
		if shared.LabelsContainTags(server.GetLabels(), tags) {
			server := server
			matched = append(matched, c.serverFromSDK(ctx, &server))
		}
	}
	return matched, nil
}

func (c *SDKClient) CreateServer(ctx context.Context, input CreateServerInput) (*Server, error) {
	if existing, err := c.FindServerByTags(ctx, input.Tags); err == nil {
		return existing, nil
	} else if !IsNotFound(err) {
		return nil, err
	}

	payload := &iaas.CreateServerPayload{}
	payload.SetName(input.Name)
	payload.SetMachineType(input.MachineType)
	useBootVolume := input.RootVolume.SizeGiB > 0 || input.RootVolume.PerformanceClass != ""
	if input.ImageID != "" && !useBootVolume {
		payload.SetImageId(input.ImageID)
	}
	if len(input.Tags) > 0 {
		payload.SetLabels(shared.TagsToSDKLabels(input.Tags))
	}
	networking := iaas.NewCreateServerNetworking()
	if input.NetworkID != "" {
		networking.SetNetworkId(input.NetworkID)
	}
	payload.SetNetworking(iaas.CreateServerNetworkingAsCreateServerPayloadAllOfNetworking(networking))
	if len(input.SecurityGroups) > 0 {
		payload.SetSecurityGroups(input.SecurityGroups)
	}
	if len(input.UserData) > 0 {
		payload.SetUserData(base64.StdEncoding.EncodeToString(input.UserData))
		payload.SetConfigDrive(true)
	}
	if useBootVolume {
		bootVolume := iaas.NewBootVolume()
		if input.ImageID != "" {
			bootVolume.SetSource(*iaas.NewBootVolumeSource(input.ImageID, "image"))
		}
		if input.RootVolume.SizeGiB > 0 {
			bootVolume.SetSize(int64(input.RootVolume.SizeGiB))
		}
		if input.RootVolume.PerformanceClass != "" {
			bootVolume.SetPerformanceClass(input.RootVolume.PerformanceClass)
		}
		bootVolume.SetDeleteOnTermination(input.RootVolume.DeleteOnTermination)
		payload.SetBootVolume(*bootVolume)
	}
	if input.SSHKeyName != "" {
		payload.SetKeypairName(input.SSHKeyName)
	}
	if input.AvailabilityZone != "" {
		payload.SetAvailabilityZone(input.AvailabilityZone)
	}

	server, err := c.iaasClient.DefaultAPI.CreateServer(ctx, c.projectID, c.region).
		CreateServerPayload(*payload).
		Execute()
	if err != nil {
		return nil, shared.ClassifySDKError("create server", err)
	}
	return c.serverFromSDK(ctx, server), nil
}

func (c *SDKClient) ListAvailabilityZones(ctx context.Context) ([]string, error) {
	resp, err := c.iaasClient.DefaultAPI.ListAvailabilityZones(ctx, c.region).Execute()
	if err != nil {
		return nil, shared.ClassifySDKError("list availability zones", err)
	}
	if resp == nil || resp.Items == nil {
		return nil, nil
	}
	return resp.GetItems(), nil
}

func (c *SDKClient) DeleteServer(ctx context.Context, id string) error {
	if err := c.iaasClient.DefaultAPI.DeleteServer(ctx, c.projectID, c.region, id).Execute(); err != nil {
		return shared.ClassifySDKError("delete server", err)
	}
	return nil
}

func (c *SDKClient) GetNetwork(ctx context.Context, id string) (*Network, error) {
	network, err := c.iaasClient.DefaultAPI.GetNetwork(ctx, c.projectID, c.region, id).Execute()
	if err != nil {
		return nil, shared.ClassifySDKError("get network", err)
	}
	ipv4 := network.GetIpv4()
	return &Network{
		ID:           network.GetId(),
		Name:         network.GetName(),
		IPv4Prefixes: ipv4.GetPrefixes(),
	}, nil
}

func (c *SDKClient) EnsureBastion(ctx context.Context, input BastionInput) (*Bastion, error) {
	if input.Name == "" || input.NetworkID == "" || input.ImageID == "" ||
		input.MachineType == "" || input.SSHKeyName == "" {
		return nil, fmt.Errorf(
			"%w: bastion name, network ID, image ID, machine type, and SSH key name are required",
			ErrInvalidInput,
		)
	}
	if len(input.AllowedCIDRs) == 0 {
		return nil, fmt.Errorf("%w: bastion allowed CIDRs are required", ErrInvalidInput)
	}
	if len(input.Tags) == 0 {
		return nil, fmt.Errorf("%w: bastion tags are required", ErrInvalidInput)
	}

	sgClient := securitygroup.NewClient(c)
	securityGroupID := input.SecurityGroupID
	if securityGroupID == "" {
		securityGroup, err := sgClient.FindByTags(ctx, input.Tags)
		if err != nil {
			return nil, err
		}
		securityGroupID = securityGroup.ID
	}

	server, err := c.CreateServer(ctx, CreateServerInput{
		Name:             input.Name,
		ProjectID:        input.ProjectID,
		Region:           input.Region,
		ImageID:          input.ImageID,
		MachineType:      input.MachineType,
		AvailabilityZone: input.AvailabilityZone,
		SSHKeyName:       input.SSHKeyName,
		NetworkID:        input.NetworkID,
		SecurityGroups:   []string{securityGroupID},
		UserData:         input.CloudInit,
		Tags:             input.Tags,
		RootVolume:       input.RootVolume,
	})
	if err != nil {
		return nil, err
	}
	// The group is already in the CreateServer payload, but CreateServer
	// short-circuits on an existing server found by tags — this is then the only
	// path that re-attaches a group that was detached out of band (for example
	// by an interrupted DeleteBastion). Kept deliberately, and idempotent: the
	// duplicate/no-port-yet responses are tolerated so they can no longer abort
	// the reconcile before the public IP below is assigned.
	if err := sgClient.AttachToServer(ctx, server.ID, securityGroupID); err != nil {
		return nil, err
	}

	return &Bastion{
		ServerID:          server.ID,
		ServerState:       server.State,
		ServerPowerStatus: server.PowerStatus,
		SecurityGroupID:   securityGroupID,
	}, nil
}

func (c *SDKClient) DeleteBastion(ctx context.Context, input BastionInput, status Bastion) error {
	serverID := status.ServerID
	if serverID == "" {
		server, err := c.FindServerByTags(ctx, input.Tags)
		if err == nil {
			serverID = server.ID
		} else if !IsNotFound(err) {
			return err
		}
	}
	if serverID == "" {
		return nil
	}

	sgClient := securitygroup.NewClient(c)
	securityGroupID := status.SecurityGroupID
	if securityGroupID == "" {
		securityGroupID = input.SecurityGroupID
	}
	if securityGroupID == "" && len(input.Tags) > 0 {
		securityGroup, err := sgClient.FindByTags(ctx, input.Tags)
		if err == nil {
			securityGroupID = securityGroup.ID
		} else if !IsNotFound(err) {
			return err
		}
	}

	if securityGroupID != "" {
		if err := sgClient.DetachFromServer(ctx, serverID, securityGroupID); err != nil {
			return err
		}
	}
	if err := c.DeleteServer(ctx, serverID); err != nil && !IsNotFound(err) {
		return err
	}
	return nil
}

func (c *SDKClient) EnsureAPIServerLoadBalancer(ctx context.Context, input LoadBalancerInput) (*LoadBalancer, error) {
	if existing, err := c.findLoadBalancerByTags(ctx, input.Tags); err == nil {
		return existing, nil
	} else if !IsNotFound(err) && !isLoadBalancerServiceNotEnabled(err) {
		return nil, err
	}
	payload := lb.NewCreateLoadBalancerPayload()
	payload.SetName(input.Name)
	payload.SetRegion(input.Region)
	if len(input.Tags) > 0 {
		payload.SetLabels(shared.LoadBalancerLabels(input.Tags))
	}
	options := lb.NewLoadBalancerOptions()
	options.SetEphemeralAddress(true)
	payload.SetOptions(*options)

	network := lb.NewNetwork()
	network.SetNetworkId(input.NetworkID)
	network.SetRole("ROLE_LISTENERS_AND_TARGETS")
	payload.SetNetworks([]lb.Network{*network})

	targetPool := lb.NewTargetPool()
	targetPool.SetName(apiserverTargetPoolName)
	targetPool.SetTargetPort(input.Port)
	targets := make([]lb.Target, 0, len(input.Targets))
	if len(input.Targets) == 0 {
		target := lb.NewTarget()
		target.SetDisplayName(bootstrapTargetName)
		target.SetIp(bootstrapTargetIP)
		targets = append(targets, *target)
	} else {
		for _, targetInput := range input.Targets {
			target := lb.NewTarget()
			target.SetDisplayName(targetInput.Name)
			target.SetIp(targetInput.IP)
			targets = append(targets, *target)
		}
	}
	targetPool.SetTargets(targets)
	payload.SetTargetPools([]lb.TargetPool{*targetPool})

	listener := lb.NewListener()
	listener.SetDisplayName(apiserverListenerName)
	listener.SetPort(input.Port)
	listener.SetProtocol("PROTOCOL_TCP")
	listener.SetTargetPool(apiserverTargetPoolName)
	payload.SetListeners([]lb.Listener{*listener})

	lbResp, err := c.lbClient.DefaultAPI.CreateLoadBalancer(ctx, c.projectID, c.region).
		CreateLoadBalancerPayload(*payload).
		Execute()
	if err != nil {
		return nil, shared.ClassifySDKError("create load balancer", err)
	}
	return loadBalancerFromSDK(lbResp), nil
}

func (c *SDKClient) DeleteAPIServerLoadBalancer(ctx context.Context, id string) error {
	if _, err := c.lbClient.DefaultAPI.DeleteLoadBalancer(ctx, c.projectID, c.region, id).Execute(); err != nil {
		return shared.ClassifySDKError("delete load balancer", err)
	}
	return nil
}

func (c *SDKClient) ListAPIServerLoadBalancersByTags(
	ctx context.Context,
	tags map[string]string,
) ([]*LoadBalancer, error) {
	if len(tags) == 0 {
		return nil, fmt.Errorf("%w: empty tag selector", ErrInvalidInput)
	}
	resp, err := c.lbClient.DefaultAPI.ListLoadBalancers(ctx, c.projectID, c.region).Execute()
	if err != nil {
		err := shared.ClassifySDKError("list load balancers", err)
		if isLoadBalancerServiceNotEnabled(err) {
			return nil, nil
		}
		return nil, err
	}
	matched := []*LoadBalancer{}
	labels := shared.LoadBalancerLabels(tags)
	for _, candidate := range resp.GetLoadBalancers() {
		if shared.MapContains(candidate.GetLabels(), labels) {
			candidate := candidate
			matched = append(matched, loadBalancerFromSDK(&candidate))
		}
	}
	return matched, nil
}

func (c *SDKClient) SetAPIServerLoadBalancerTargets(
	ctx context.Context,
	loadBalancerID string,
	port int32,
	targets []LoadBalancerTargetInput,
) error {
	if loadBalancerID == "" || port <= 0 {
		return fmt.Errorf("%w: load balancer ID and target port are required", ErrInvalidInput)
	}
	// STACKIT NLB target pools must contain at least one target.
	if len(targets) == 0 {
		return fmt.Errorf("%w: at least one target is required", ErrInvalidInput)
	}
	desired := make([]lb.Target, 0, len(targets))
	for _, targetInput := range targets {
		if targetInput.Name == "" || targetInput.IP == "" {
			return fmt.Errorf("%w: target name and target IP are required", ErrInvalidInput)
		}
		target := lb.NewTarget()
		target.SetDisplayName(targetInput.Name)
		target.SetIp(targetInput.IP)
		desired = append(desired, *target)
	}

	loadBalancer, err := c.lbClient.DefaultAPI.GetLoadBalancer(ctx, c.projectID, c.region, loadBalancerID).Execute()
	if err != nil {
		return shared.ClassifySDKError("get load balancer", err)
	}
	targetPool := apiServerTargetPool(loadBalancer)
	if targetPool == nil {
		return fmt.Errorf(
			"%w: load balancer %q has no %q target pool",
			ErrNotFound,
			loadBalancerID,
			apiserverTargetPoolName,
		)
	}

	// Every cluster reconcile ends up here; skip the update call when the pool
	// already matches.
	if targetPool.GetTargetPort() == port && sameTargets(targetPool.GetTargets(), desired) {
		return nil
	}
	return c.updateAPIServerTargetPool(ctx, loadBalancerID, targetPool, desired, port)
}

func sameTargets(current, desired []lb.Target) bool {
	if len(current) != len(desired) {
		return false
	}
	return slices.Equal(sortedTargetKeys(current), sortedTargetKeys(desired))
}

type targetKey struct{ name, ip string }

func sortedTargetKeys(targets []lb.Target) []targetKey {
	keys := make([]targetKey, 0, len(targets))
	for _, target := range targets {
		keys = append(keys, targetKey{name: target.GetDisplayName(), ip: target.GetIp()})
	}
	slices.SortFunc(keys, func(a, b targetKey) int {
		return cmp.Or(strings.Compare(a.name, b.name), strings.Compare(a.ip, b.ip))
	})
	return keys
}

func (c *SDKClient) findLoadBalancerByTags(ctx context.Context, tags map[string]string) (*LoadBalancer, error) {
	matched, err := c.ListAPIServerLoadBalancersByTags(ctx, tags)
	if err != nil {
		return nil, err
	}
	if len(matched) == 0 {
		return nil, fmt.Errorf("%w: no load balancer matching tags", ErrNotFound)
	}
	if len(matched) > 1 {
		return nil, fmt.Errorf("%w: multiple load balancers matching tags", ErrConflict)
	}
	return matched[0], nil
}

func (c *SDKClient) updateAPIServerTargetPool(
	ctx context.Context,
	loadBalancerID string,
	current *lb.TargetPool,
	targets []lb.Target,
	port int32,
) error {
	payload := lb.NewUpdateTargetPoolPayload()
	payload.SetName(apiserverTargetPoolName)
	if port > 0 {
		payload.SetTargetPort(port)
	} else if current.GetTargetPort() > 0 {
		payload.SetTargetPort(current.GetTargetPort())
	}
	payload.SetTargets(targets)
	if _, err := c.lbClient.DefaultAPI.
		UpdateTargetPool(ctx, c.projectID, c.region, loadBalancerID, apiserverTargetPoolName).
		UpdateTargetPoolPayload(*payload).
		Execute(); err != nil {
		return shared.ClassifySDKError("update load balancer target pool", err)
	}
	return nil
}

func apiServerTargetPool(loadBalancer *lb.LoadBalancer) *lb.TargetPool {
	if loadBalancer == nil {
		return nil
	}
	for _, targetPool := range loadBalancer.GetTargetPools() {
		if targetPool.GetName() == apiserverTargetPoolName {
			return &targetPool
		}
	}
	return nil
}

func (c *SDKClient) serverFromSDK(ctx context.Context, server *iaas.Server) *Server {
	if server == nil {
		return nil
	}
	out := &Server{
		ID:               server.GetId(),
		Name:             server.GetName(),
		State:            server.GetStatus(),
		PowerStatus:      server.GetPowerStatus(),
		AvailabilityZone: server.GetAvailabilityZone(),
	}
	nics, err := c.iaasClient.DefaultAPI.ListServerNICs(ctx, c.projectID, c.region, out.ID).Execute()
	if err == nil {
		for _, nic := range nics.GetItems() {
			if ipv4 := nic.GetIpv4(); ipv4 != "" {
				out.Addresses = append(out.Addresses, Address{Type: "InternalIP", Address: ipv4})
			}
			if ipv6 := nic.GetIpv6(); ipv6 != "" {
				out.Addresses = append(out.Addresses, Address{Type: "InternalIP", Address: ipv6})
			}
		}
	}
	return out
}

func loadBalancerFromSDK(in *lb.LoadBalancer) *LoadBalancer {
	if in == nil {
		return nil
	}
	return &LoadBalancer{
		ID:   in.GetName(),
		Name: in.GetName(),
		IP:   shared.FirstNonEmpty(in.GetExternalAddress(), in.GetPrivateAddress()),
		Port: listenerPort(in.GetListeners()),
	}
}

func listenerPort(listeners []lb.Listener) int32 {
	for _, listener := range listeners {
		if port := listener.GetPort(); port > 0 {
			return port
		}
	}
	return 0
}

func isLoadBalancerServiceNotEnabled(err error) bool {
	return IsUnauthorized(err) && strings.Contains(err.Error(), "Service not enabled")
}
