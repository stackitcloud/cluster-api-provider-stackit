/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package cloud

import (
	"context"
	"encoding/base64"
	"fmt"
	"os"

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

func (c *SDKClient) LoadBalancerClient() *lb.APIClient {
	return c.lbClient
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
