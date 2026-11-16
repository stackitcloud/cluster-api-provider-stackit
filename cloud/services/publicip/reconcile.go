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
		if err := s.attachToServer(ctx, spec.ServerID, publicIP.ID); err != nil {
			return err
		}
		publicIP, err = s.get(ctx, publicIP.ID)
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

	serverIDs, err := s.resolveServerIDs(ctx, spec)
	if err != nil {
		return err
	}
	deletedIDs, err := shared.DeleteByIDOrTags(ctx, spec.ID, spec.Tags, func(ctx context.Context, id string) (bool, error) {
		return s.detachAndDeleteByID(ctx, id, serverIDs)
	}, s.listIDsByTags)
	for _, deletedID := range deletedIDs {
		log.V(1).Info("Deleted public IP", "publicIPID", deletedID)
	}
	if err != nil {
		return err
	}

	if spec.SetPublicIP != nil {
		spec.SetPublicIP("", "")
	}
	return nil
}

// get retrieves a STACKIT public IP by ID.
func (s *Service) get(ctx context.Context, id string) (*PublicIP, error) {
	if id == "" {
		return nil, fmt.Errorf("%w: public IP ID is empty", shared.ErrInvalidInput)
	}
	publicIP, err := s.scope.IaaSClient().DefaultAPI.GetPublicIP(ctx, s.scope.ProjectID(), s.scope.Region(), id).Execute()
	if err != nil {
		return nil, shared.ClassifySDKError("get public IP", err)
	}
	return publicIPFromSDK(publicIP), nil
}

// findByTags finds a single STACKIT public IP matching the given tag selector.
func (s *Service) findByTags(ctx context.Context, tags map[string]string) (*PublicIP, error) {
	return shared.FindByTags(ctx, tags, s.listByTags, "public IP", "public IPs")
}

// listByTags lists STACKIT public IPs matching the given tag selector.
func (s *Service) listByTags(ctx context.Context, tags map[string]string) ([]*PublicIP, error) {
	if len(tags) == 0 {
		return nil, fmt.Errorf("%w: empty tag selector", shared.ErrInvalidInput)
	}
	resp, err := s.scope.IaaSClient().DefaultAPI.ListPublicIPs(ctx, s.scope.ProjectID(), s.scope.Region()).
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

// attachToServer attaches a public IP to a server idempotently, tolerating Conflict errors.
func (s *Service) attachToServer(ctx context.Context, serverID, publicIPID string) error {
	if serverID == "" || publicIPID == "" {
		return fmt.Errorf("%w: server ID and public IP ID are required", shared.ErrInvalidInput)
	}
	if err := s.scope.IaaSClient().DefaultAPI.
		AddPublicIpToServer(ctx, s.scope.ProjectID(), s.scope.Region(), serverID, publicIPID).
		Execute(); err != nil {
		err := shared.ClassifySDKError("add public IP to server", err)
		if !shared.IsConflict(err) {
			return err
		}
		logf.FromContext(ctx).V(1).Info("Public IP already attached to server", "publicIPID", publicIPID, "serverID", serverID, "err", err)
		return nil
	}
	logf.FromContext(ctx).V(1).Info("Attached public IP to server", "publicIPID", publicIPID, "serverID", serverID)
	return nil
}

// detachFromServer detaches a public IP from a server, ignoring NotFound and Conflict errors.
func (s *Service) detachFromServer(ctx context.Context, serverID, publicIPID string) error {
	if serverID == "" || publicIPID == "" {
		return fmt.Errorf("%w: server ID and public IP ID are required", shared.ErrInvalidInput)
	}
	if err := s.scope.IaaSClient().DefaultAPI.
		RemovePublicIpFromServer(ctx, s.scope.ProjectID(), s.scope.Region(), serverID, publicIPID).
		Execute(); err != nil {
		err := shared.ClassifySDKError("remove public IP from server", err)
		if !shared.IsNotFound(err) && !shared.IsConflict(err) {
			return err
		}
		logf.FromContext(ctx).V(1).Info("Public IP already detached from server", "publicIPID", publicIPID, "serverID", serverID, "err", err)
		return nil
	}
	logf.FromContext(ctx).V(1).Info("Detached public IP from server", "publicIPID", publicIPID, "serverID", serverID)
	return nil
}

// deleteByID deletes a public IP by ID, returning true if the public IP existed and was deleted,
// or false if it was already absent.
func (s *Service) deleteByID(ctx context.Context, id string) (bool, error) {
	if id == "" {
		return false, fmt.Errorf("%w: public IP ID is empty", shared.ErrInvalidInput)
	}
	if err := s.scope.IaaSClient().DefaultAPI.DeletePublicIP(ctx, s.scope.ProjectID(), s.scope.Region(), id).Execute(); err != nil {
		err := shared.ClassifySDKError("delete public IP", err)
		if shared.IsNotFound(err) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

func (s *Service) detachAndDeleteByID(ctx context.Context, id string, serverIDs []string) (bool, error) {
	for _, serverID := range serverIDs {
		if err := s.detachFromServer(ctx, serverID, id); err != nil {
			return false, err
		}
	}
	return s.deleteByID(ctx, id)
}

func (s *Service) listIDsByTags(ctx context.Context, tags map[string]string) ([]string, error) {
	return shared.ListIDsByTags(ctx, tags, s.listByTags, func(publicIP *PublicIP) string {
		return publicIP.ID
	})
}

func (s *Service) ensurePublicIP(ctx context.Context, spec PublicIPSpec) (*PublicIP, error) {
	publicIP, err := shared.ResolveExisting(ctx, spec.ID, spec.Tags, s.get, s.findByTags, shared.IsNotFound)
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
	created, err := s.scope.IaaSClient().DefaultAPI.
		CreatePublicIP(ctx, s.scope.ProjectID(), s.scope.Region()).
		CreatePublicIPPayload(*payload).
		Execute()
	if err != nil {
		return nil, shared.ClassifySDKError("create public IP", err)
	}
	out := publicIPFromSDK(created)
	logf.FromContext(ctx).V(1).Info("Created public IP", "publicIPID", out.ID)
	return out, nil
}

func (s *Service) resolveServerIDs(ctx context.Context, spec PublicIPSpec) ([]string, error) {
	var ids []string
	if spec.ServerID != "" {
		ids = append(ids, spec.ServerID)
	}
	if len(spec.ServerSelector) == 0 {
		return ids, nil
	}
	selectedIDs, err := shared.ListServerIDsByTags(ctx, s.scope, spec.ServerSelector)
	if err != nil {
		return nil, err
	}
	for _, id := range selectedIDs {
		if id != spec.ServerID {
			ids = append(ids, id)
		}
	}
	return ids, nil
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
