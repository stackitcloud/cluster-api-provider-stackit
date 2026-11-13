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
	"errors"
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

	srv, created, err := s.client.ensureServer(ctx, spec)
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

	deletedIDs, err := s.client.deleteBySpec(ctx, spec)
	for _, deletedID := range deletedIDs {
		log.V(1).Info("Deleted server", "serverID", deletedID)
	}
	if err != nil {
		return err
	}
	if spec.ClearServer != nil {
		spec.ClearServer()
	}
	return nil
}

// get retrieves a STACKIT server by ID.
func (c *client) get(ctx context.Context, id string) (*Server, error) {
	if id == "" {
		return nil, fmt.Errorf("%w: server ID is empty", shared.ErrInvalidInput)
	}
	srv, err := c.scope.IaaSClient().DefaultAPI.GetServer(ctx, c.scope.ProjectID(), c.scope.Region(), id).Execute()
	if err != nil {
		return nil, shared.ClassifySDKError("get server", err)
	}
	return c.serverFromSDK(ctx, srv), nil
}

// findByTags finds a single STACKIT server matching the given tag selector.
func (c *client) findByTags(ctx context.Context, tags map[string]string) (*Server, error) {
	srv, err := shared.FindByTags(ctx, tags, c.listByTags, "server", "servers")
	if err != nil {
		return nil, err
	}
	return c.serverFromSDK(ctx, srv), nil
}

// listByTags lists STACKIT servers matching the given tag selector.
func (c *client) listByTags(ctx context.Context, tags map[string]string) ([]*iaas.Server, error) {
	if len(tags) == 0 {
		return nil, fmt.Errorf("%w: empty tag selector", shared.ErrInvalidInput)
	}
	resp, err := c.scope.IaaSClient().DefaultAPI.ListServers(ctx, c.scope.ProjectID(), c.scope.Region()).
		LabelSelector(shared.LabelSelector(tags)).
		Details(true).
		Execute()
	if err != nil {
		return nil, shared.ClassifySDKError("list servers", err)
	}
	matched := []*iaas.Server{}
	for _, srv := range resp.GetItems() {
		if shared.LabelsContainTags(srv.GetLabels(), tags) {
			srv := srv
			matched = append(matched, &srv)
		}
	}
	return matched, nil
}

// deleteByID deletes a server by ID, ignoring NotFound errors.
func (c *client) deleteByID(ctx context.Context, id string) error {
	if id == "" {
		return fmt.Errorf("%w: server ID is empty", shared.ErrInvalidInput)
	}
	if err := c.scope.IaaSClient().DefaultAPI.DeleteServer(ctx, c.scope.ProjectID(), c.scope.Region(), id).Execute(); err != nil {
		err := shared.ClassifySDKError("delete server", err)
		if !shared.IsNotFound(err) {
			return err
		}
	}
	return nil
}

// create creates a new STACKIT server from the given spec.
func (c *client) create(ctx context.Context, spec ServerSpec) (*Server, error) {
	payload := spec.toCreatePayload()
	created, err := c.scope.IaaSClient().DefaultAPI.CreateServer(ctx, c.scope.ProjectID(), c.scope.Region()).
		CreateServerPayload(payload).
		Execute()
	if err != nil {
		return nil, shared.ClassifySDKError("create server", err)
	}
	out := c.serverFromSDK(ctx, created)
	logf.FromContext(ctx).V(1).Info("Created server", "name", spec.Name, "serverID", out.ID)
	return out, nil
}

func (c *client) ensureServer(ctx context.Context, spec ServerSpec) (*Server, bool, error) {
	if spec.ID != "" {
		srv, err := c.get(ctx, spec.ID)
		if err == nil {
			return srv, false, nil
		}
		if !shared.IsNotFound(err) {
			return nil, false, err
		}
	}
	if srv, err := c.findByTags(ctx, spec.Tags); err == nil {
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
			errServerGone, spec.ID,
		)
	}

	out, err := c.create(ctx, spec)
	if err != nil {
		return nil, false, err
	}
	return out, true, nil
}

func (c *client) deleteBySpec(ctx context.Context, spec ServerSpec) ([]string, error) {
	if spec.ID != "" {
		err := c.scope.IaaSClient().DefaultAPI.DeleteServer(ctx, c.scope.ProjectID(), c.scope.Region(), spec.ID).Execute()
		if err == nil {
			return []string{spec.ID}, nil
		}
		err = shared.ClassifySDKError("delete server", err)
		if !shared.IsNotFound(err) {
			return nil, err
		}
	}
	if len(spec.Tags) == 0 {
		return nil, nil
	}
	ids, err := shared.ListServerIDsByTags(ctx, c.scope, spec.Tags)
	if err != nil {
		return nil, err
	}
	var (
		deletedIDs []string
		errs       []error
	)
	for _, id := range ids {
		if err := c.deleteByID(ctx, id); err != nil {
			errs = append(errs, err)
			continue
		}
		deletedIDs = append(deletedIDs, id)
	}
	return deletedIDs, errors.Join(errs...)
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

func (c *client) serverFromSDK(ctx context.Context, srv *iaas.Server) *Server {
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
	nics, err := c.scope.IaaSClient().DefaultAPI.ListServerNICs(ctx, c.scope.ProjectID(), c.scope.Region(), out.ID).Execute()
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
