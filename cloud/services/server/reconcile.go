/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package server

import (
	"context"
	"encoding/base64"
	"fmt"

	iaas "github.com/stackitcloud/stackit-sdk-go/services/iaas/v2api"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/stackitcloud/cluster-api-provider-stackit/cloud/services/shared"
)

func (s *Service) Reconcile(ctx context.Context) error {
	log := logf.FromContext(ctx)
	spec := s.scope.ServerSpec()
	if err := spec.validate(); err != nil {
		return err
	}

	srv, created, err := s.ensureServer(ctx, spec)
	if err != nil {
		return err
	}
	if spec.SetServer != nil {
		spec.SetServer(srv, created)
	}
	log.V(1).Info("Reconciled server resources", "serverID", srv.ID, "created", created)
	return nil
}

func (s *Service) Delete(ctx context.Context) error {
	log := logf.FromContext(ctx)
	spec := s.scope.ServerSpec()
	if spec.ID == "" && len(spec.Tags) == 0 {
		return fmt.Errorf("%w: server ID or tags are required", shared.ErrInvalidInput)
	}

	deletedID, err := s.deleteBySpec(ctx, spec)
	if err != nil {
		return err
	}
	if deletedID != "" {
		log.V(1).Info("Deleted server", "serverID", deletedID)
	}
	if spec.ClearServer != nil {
		spec.ClearServer()
	}
	return nil
}

// Get retrieves a STACKIT server by ID.
func (c *Client) Get(ctx context.Context, id string) (*Server, error) {
	if id == "" {
		return nil, fmt.Errorf("%w: server ID is empty", shared.ErrInvalidInput)
	}
	srv, err := c.IaaSClient().DefaultAPI.GetServer(ctx, c.ProjectID(), c.Region(), id).Execute()
	if err != nil {
		return nil, shared.ClassifySDKError("get server", err)
	}
	return c.serverFromSDK(ctx, srv), nil
}

// FindByTags finds a single STACKIT server matching the given tag selector.
func (c *Client) FindByTags(ctx context.Context, tags map[string]string) (*Server, error) {
	matched, err := c.ListByTags(ctx, tags)
	if err != nil {
		return nil, err
	}
	return shared.FindSingle(matched, "server", "servers")
}

// ListByTags lists STACKIT servers matching the given tag selector.
func (c *Client) ListByTags(ctx context.Context, tags map[string]string) ([]*Server, error) {
	items, err := c.listSDKServersByTags(ctx, tags, true)
	if err != nil {
		return nil, err
	}
	matched := make([]*Server, 0, len(items))
	for _, srv := range items {
		matched = append(matched, c.serverFromSDK(ctx, &srv))
	}
	return matched, nil
}

// ListIDsByTags lists the IDs of STACKIT servers matching the given tag selector.
func (c *Client) ListIDsByTags(ctx context.Context, tags map[string]string) ([]string, error) {
	items, err := c.listSDKServersByTags(ctx, tags, false)
	if err != nil {
		return nil, err
	}
	var ids []string
	for _, srv := range items {
		if id := srv.GetId(); id != "" {
			ids = append(ids, id)
		}
	}
	return ids, nil
}

func (c *Client) listSDKServersByTags(ctx context.Context, tags map[string]string, details bool) ([]iaas.Server, error) {
	if len(tags) == 0 {
		return nil, fmt.Errorf("%w: empty tag selector", shared.ErrInvalidInput)
	}
	req := c.IaaSClient().DefaultAPI.ListServers(ctx, c.ProjectID(), c.Region()).
		LabelSelector(shared.LabelSelector(tags))
	if details {
		req = req.Details(true)
	}
	resp, err := req.Execute()
	if err != nil {
		return nil, shared.ClassifySDKError("list servers", err)
	}
	var matched []iaas.Server
	for _, srv := range resp.GetItems() {
		if shared.LabelsContainTags(srv.GetLabels(), tags) {
			matched = append(matched, srv)
		}
	}
	return matched, nil
}

// DeleteByID deletes a server by ID, ignoring NotFound errors.
func (c *Client) DeleteByID(ctx context.Context, id string) error {
	if id == "" {
		return fmt.Errorf("%w: server ID is empty", shared.ErrInvalidInput)
	}
	if err := c.IaaSClient().DefaultAPI.DeleteServer(ctx, c.ProjectID(), c.Region(), id).Execute(); err != nil {
		err := shared.ClassifySDKError("delete server", err)
		if !shared.IsNotFound(err) {
			return err
		}
	}
	return nil
}

func (c *Client) ensureServer(ctx context.Context, spec ServerSpec) (*Server, bool, error) {
	if spec.ID != "" {
		srv, err := c.Get(ctx, spec.ID)
		if err == nil {
			return srv, false, nil
		}
		if !shared.IsNotFound(err) {
			return nil, false, err
		}
	}
	if srv, err := c.FindByTags(ctx, spec.Tags); err == nil {
		return srv, false, nil
	} else if !shared.IsNotFound(err) {
		return nil, false, err
	}

	// Recreating an already-provisioned server would replay bootstrap data
	// pinned to the previous identity, so surface the loss and let Cluster API
	// replace the Machine.
	if spec.DisallowRecreate {
		return nil, false, fmt.Errorf(
			"%w: server %s; the Machine must be replaced",
			ErrServerGone, spec.ID,
		)
	}

	payload := spec.toCreatePayload()
	created, err := c.IaaSClient().DefaultAPI.CreateServer(ctx, c.ProjectID(), c.Region()).
		CreateServerPayload(payload).
		Execute()
	if err != nil {
		return nil, false, shared.ClassifySDKError("create server", err)
	}
	out := c.serverFromSDK(ctx, created)
	logf.FromContext(ctx).V(1).Info("Created server", "name", spec.Name, "serverID", out.ID)
	return out, true, nil
}

func (c *Client) deleteBySpec(ctx context.Context, spec ServerSpec) (string, error) {
	if spec.ID != "" {
		err := c.IaaSClient().DefaultAPI.DeleteServer(ctx, c.ProjectID(), c.Region(), spec.ID).Execute()
		if err == nil {
			return spec.ID, nil
		}
		err = shared.ClassifySDKError("delete server", err)
		if !shared.IsNotFound(err) {
			return "", err
		}
	}
	if len(spec.Tags) == 0 {
		return "", nil
	}
	srv, err := c.FindByTags(ctx, spec.Tags)
	if err != nil {
		if shared.IsNotFound(err) {
			return "", nil
		}
		return "", err
	}
	if err := c.DeleteByID(ctx, srv.ID); err != nil {
		return "", err
	}
	return srv.ID, nil
}

func (s ServerSpec) validate() error {
	if s.Name == "" {
		return fmt.Errorf("%w: server name is required", shared.ErrInvalidInput)
	}
	if s.ImageID == "" {
		return fmt.Errorf("%w: server image ID is required", shared.ErrInvalidInput)
	}
	if s.MachineType == "" {
		return fmt.Errorf("%w: server machine type is required", shared.ErrInvalidInput)
	}
	if s.NetworkID == "" {
		return fmt.Errorf("%w: server network ID is required", shared.ErrInvalidInput)
	}
	if len(s.Tags) == 0 {
		return fmt.Errorf("%w: server tags are required", shared.ErrInvalidInput)
	}
	return nil
}

func (s ServerSpec) toCreatePayload() iaas.CreateServerPayload {
	payload := &iaas.CreateServerPayload{}
	payload.SetName(s.Name)
	payload.SetMachineType(s.MachineType)
	useBootVolume := s.RootVolume.SizeGiB > 0 || s.RootVolume.PerformanceClass != ""
	if s.ImageID != "" && !useBootVolume {
		payload.SetImageId(s.ImageID)
	}
	if len(s.Tags) > 0 {
		payload.SetLabels(shared.TagsToSDKLabels(s.Tags))
	}
	networking := iaas.NewCreateServerNetworking()
	if s.NetworkID != "" {
		networking.SetNetworkId(s.NetworkID)
	}
	payload.SetNetworking(iaas.CreateServerNetworkingAsCreateServerPayloadAllOfNetworking(networking))
	if len(s.SecurityGroups) > 0 {
		payload.SetSecurityGroups(s.SecurityGroups)
	}
	if len(s.UserData) > 0 {
		payload.SetUserData(base64.StdEncoding.EncodeToString(s.UserData))
		payload.SetConfigDrive(true)
	}
	if useBootVolume {
		bootVolume := iaas.NewBootVolume()
		if s.ImageID != "" {
			bootVolume.SetSource(*iaas.NewBootVolumeSource(s.ImageID, "image"))
		}
		if s.RootVolume.SizeGiB > 0 {
			bootVolume.SetSize(int64(s.RootVolume.SizeGiB))
		}
		if s.RootVolume.PerformanceClass != "" {
			bootVolume.SetPerformanceClass(s.RootVolume.PerformanceClass)
		}
		bootVolume.SetDeleteOnTermination(s.RootVolume.DeleteOnTermination)
		payload.SetBootVolume(*bootVolume)
	}
	if s.SSHKeyName != "" {
		payload.SetKeypairName(s.SSHKeyName)
	}
	if s.AvailabilityZone != "" {
		payload.SetAvailabilityZone(s.AvailabilityZone)
	}
	return *payload
}

func (c *Client) serverFromSDK(ctx context.Context, srv *iaas.Server) *Server {
	if srv == nil {
		return nil
	}
	out := &Server{
		ID:               srv.GetId(),
		Name:             srv.GetName(),
		State:            srv.GetStatus(),
		PowerStatus:      srv.GetPowerStatus(),
		AvailabilityZone: srv.GetAvailabilityZone(),
	}
	nics, err := c.IaaSClient().DefaultAPI.ListServerNICs(ctx, c.ProjectID(), c.Region(), out.ID).Execute()
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
