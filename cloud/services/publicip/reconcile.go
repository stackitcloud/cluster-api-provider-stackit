/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package publicip

import (
	"context"
	"fmt"

	iaas "github.com/stackitcloud/stackit-sdk-go/services/iaas/v2api"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/stackitcloud/cluster-api-provider-stackit/cloud/services/server"
	"github.com/stackitcloud/cluster-api-provider-stackit/cloud/services/shared"
)

func (s *Service) Reconcile(ctx context.Context) error {
	log := logf.FromContext(ctx)
	spec := s.scope.PublicIPSpec()
	if err := spec.validate(); err != nil {
		return err
	}

	publicIP, err := s.ensurePublicIP(ctx, spec)
	if err != nil {
		return err
	}

	if spec.ServerID != "" && publicIP.NetworkInterfaceID == "" {
		if err := s.AttachToServer(ctx, spec.ServerID, publicIP.ID); err != nil {
			return err
		}
		publicIP, err = s.Get(ctx, publicIP.ID)
		if err != nil {
			return err
		}
	}

	if spec.SetPublicIP != nil {
		spec.SetPublicIP(publicIP.ID, publicIP.IP)
	}
	log.V(1).Info("Reconciled public IP resources", "publicIPID", publicIP.ID)
	return nil
}

func (s *Service) Delete(ctx context.Context) error {
	log := logf.FromContext(ctx)
	spec := s.scope.PublicIPSpec()
	if spec.ID == "" && len(spec.Tags) == 0 {
		return fmt.Errorf("%w: public IP ID or tags are required", shared.ErrInvalidInput)
	}

	publicIP, err := s.resolvePublicIP(ctx, spec)
	if err != nil {
		return err
	}
	if publicIP == nil {
		if spec.SetPublicIP != nil {
			spec.SetPublicIP("", "")
		}
		return nil
	}

	serverID, err := s.resolveServerID(ctx, spec)
	if err != nil {
		return err
	}
	if serverID != "" {
		if err := s.DetachFromServer(ctx, serverID, publicIP.ID); err != nil {
			return err
		}
	}

	if err := s.DeleteByID(ctx, publicIP.ID); err != nil {
		return err
	}
	log.V(1).Info("Deleted public IP", "publicIPID", publicIP.ID)

	if spec.SetPublicIP != nil {
		spec.SetPublicIP("", "")
	}
	return nil
}

// Get retrieves a STACKIT public IP by ID.
func (c *Client) Get(ctx context.Context, id string) (*PublicIP, error) {
	if id == "" {
		return nil, fmt.Errorf("%w: public IP ID is empty", shared.ErrInvalidInput)
	}
	publicIP, err := c.iaasClient.DefaultAPI.GetPublicIP(ctx, c.projectID, c.region, id).Execute()
	if err != nil {
		return nil, shared.ClassifySDKError("get public IP", err)
	}
	return publicIPFromSDK(publicIP), nil
}

// FindByTags finds a single STACKIT public IP matching the given tag selector.
func (c *Client) FindByTags(ctx context.Context, tags map[string]string) (*PublicIP, error) {
	matched, err := c.ListByTags(ctx, tags)
	if err != nil {
		return nil, err
	}
	if len(matched) == 0 {
		return nil, fmt.Errorf("%w: no public IP matching tags", shared.ErrNotFound)
	}
	if len(matched) > 1 {
		return nil, fmt.Errorf("%w: multiple public IPs matching tags", shared.ErrConflict)
	}
	return matched[0], nil
}

// ListByTags lists STACKIT public IPs matching the given tag selector.
func (c *Client) ListByTags(ctx context.Context, tags map[string]string) ([]*PublicIP, error) {
	if len(tags) == 0 {
		return nil, fmt.Errorf("%w: empty tag selector", shared.ErrInvalidInput)
	}
	resp, err := c.iaasClient.DefaultAPI.ListPublicIPs(ctx, c.projectID, c.region).
		LabelSelector(shared.LabelSelector(tags)).
		Execute()
	if err != nil {
		return nil, shared.ClassifySDKError("list public IPs", err)
	}
	matched := []*PublicIP{}
	for _, publicIP := range resp.GetItems() {
		if shared.LabelsContainTags(publicIP.GetLabels(), tags) {
			publicIP := publicIP
			matched = append(matched, publicIPFromSDK(&publicIP))
		}
	}
	return matched, nil
}

// AttachToServer attaches a public IP to a server idempotently, tolerating Conflict errors.
func (c *Client) AttachToServer(ctx context.Context, serverID, publicIPID string) error {
	if serverID == "" || publicIPID == "" {
		return fmt.Errorf("%w: server ID and public IP ID are required", shared.ErrInvalidInput)
	}
	if err := c.iaasClient.DefaultAPI.
		AddPublicIpToServer(ctx, c.projectID, c.region, serverID, publicIPID).
		Execute(); err != nil {
		err := shared.ClassifySDKError("add public IP to server", err)
		if !shared.IsConflict(err) {
			return err
		}
	}
	logf.FromContext(ctx).V(1).Info("Attached public IP to server", "publicIPID", publicIPID, "serverID", serverID)
	return nil
}

// DetachFromServer detaches a public IP from a server, ignoring NotFound and Conflict errors.
func (c *Client) DetachFromServer(ctx context.Context, serverID, publicIPID string) error {
	if serverID == "" || publicIPID == "" {
		return fmt.Errorf("%w: server ID and public IP ID are required", shared.ErrInvalidInput)
	}
	if err := c.iaasClient.DefaultAPI.
		RemovePublicIpFromServer(ctx, c.projectID, c.region, serverID, publicIPID).
		Execute(); err != nil {
		err := shared.ClassifySDKError("remove public IP from server", err)
		if !shared.IsNotFound(err) && !shared.IsConflict(err) {
			return err
		}
	}
	logf.FromContext(ctx).V(1).Info("Detached public IP from server", "publicIPID", publicIPID, "serverID", serverID)
	return nil
}

// DeleteByID deletes a public IP by ID, ignoring NotFound errors.
func (c *Client) DeleteByID(ctx context.Context, id string) error {
	if id == "" {
		return fmt.Errorf("%w: public IP ID is empty", shared.ErrInvalidInput)
	}
	if err := c.iaasClient.DefaultAPI.DeletePublicIP(ctx, c.projectID, c.region, id).Execute(); err != nil {
		err := shared.ClassifySDKError("delete public IP", err)
		if !shared.IsNotFound(err) {
			return err
		}
	}
	return nil
}

func (c *Client) ensurePublicIP(ctx context.Context, spec PublicIPSpec) (*PublicIP, error) {
	publicIP, err := c.resolvePublicIP(ctx, spec)
	if err != nil {
		return nil, err
	}
	if publicIP != nil {
		return publicIP, nil
	}

	payload := iaas.NewCreatePublicIPPayload()
	if len(spec.Tags) > 0 {
		payload.SetLabels(shared.TagsToSDKLabels(spec.Tags))
	}
	created, err := c.iaasClient.DefaultAPI.
		CreatePublicIP(ctx, c.projectID, c.region).
		CreatePublicIPPayload(*payload).
		Execute()
	if err != nil {
		return nil, shared.ClassifySDKError("create public IP", err)
	}
	out := publicIPFromSDK(created)
	logf.FromContext(ctx).V(1).Info("Created public IP", "publicIPID", out.ID)
	return out, nil
}

func (c *Client) resolvePublicIP(ctx context.Context, spec PublicIPSpec) (*PublicIP, error) {
	if spec.ID != "" {
		publicIP, err := c.Get(ctx, spec.ID)
		if err == nil {
			return publicIP, nil
		}
		if !shared.IsNotFound(err) {
			return nil, err
		}
	}
	if len(spec.Tags) == 0 {
		return nil, nil
	}
	publicIP, err := c.FindByTags(ctx, spec.Tags)
	if shared.IsNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return publicIP, nil
}

func (s *Service) resolveServerID(ctx context.Context, spec PublicIPSpec) (string, error) {
	if spec.ServerID != "" {
		return spec.ServerID, nil
	}
	if len(spec.ServerSelector) == 0 {
		return "", nil
	}
	srv, err := server.NewClient(s.scope).FindByTags(ctx, spec.ServerSelector)
	if err != nil {
		if shared.IsNotFound(err) {
			return "", nil
		}
		return "", err
	}
	return srv.ID, nil
}

func (s PublicIPSpec) validate() error {
	if len(s.Tags) == 0 {
		return fmt.Errorf("%w: public IP tags are required", shared.ErrInvalidInput)
	}
	return nil
}

func publicIPFromSDK(in *iaas.PublicIp) *PublicIP {
	if in == nil {
		return nil
	}
	return &PublicIP{
		ID:                 in.GetId(),
		IP:                 in.GetIp(),
		NetworkInterfaceID: in.GetNetworkInterface(),
	}
}
