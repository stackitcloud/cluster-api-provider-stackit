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

// Reconcile reconciles cluster public IP resources.
func (s *Service) Reconcile(ctx context.Context) error {
	log := logf.FromContext(ctx)
	if !s.scope.BastionSpec().Enabled {
		return s.Delete(ctx)
	}

	log.V(1).Info("Reconciling public IP resources")
	publicIP, err := s.createOrGetPublicIP(ctx)
	if err != nil {
		return err
	}

	serverID := s.scope.BastionStatus().ServerID
	if serverID != "" && publicIP.NetworkInterfaceID == "" {
		log.V(1).Info("Attaching public IP to server", "publicIPID", publicIP.ID, "serverID", serverID)
		if err := s.iaasClient.DefaultAPI.
			AddPublicIpToServer(ctx, s.scope.ProjectID(), s.scope.Region(), serverID, publicIP.ID).
			Execute(); err != nil {
			err := shared.ClassifySDKError("add public IP to server", err)
			if !shared.IsConflict(err) {
				log.Error(err, "Failed to attach public IP to server", "publicIPID", publicIP.ID, "serverID", serverID)
				return err
			}
		}
		publicIP, err = s.getPublicIP(ctx, publicIP.ID)
		if err != nil {
			log.Error(err, "Failed to get public IP after attachment", "publicIPID", publicIP.ID)
			return err
		}
	}

	s.scope.SetBastionPublicIP(publicIP.ID, publicIP.IP)
	return nil
}

// Delete deletes cluster public IP resources.
func (s *Service) Delete(ctx context.Context) error {
	log := logf.FromContext(ctx)
	log.V(1).Info("Deleting public IP resources")

	publicIP, err := s.findPublicIP(ctx)
	if err != nil {
		return err
	}
	if publicIP == nil {
		s.scope.SetBastionPublicIP("", "")
		return nil
	}

	serverID, err := s.resolveServerID(ctx)
	if err != nil {
		return err
	}
	if serverID != "" {
		log.V(1).Info("Detaching public IP from server", "publicIPID", publicIP.ID, "serverID", serverID)
		if err := DetachFromServer(ctx, s.iaasClient, s.scope.ProjectID(), s.scope.Region(), serverID, publicIP.ID); err != nil {
			log.Error(err, "Failed to detach public IP from server", "publicIPID", publicIP.ID, "serverID", serverID)
			return err
		}
	}

	log.V(1).Info("Deleting public IP", "publicIPID", publicIP.ID)
	if err := DeleteByID(ctx, s.iaasClient, s.scope.ProjectID(), s.scope.Region(), publicIP.ID); err != nil {
		log.Error(err, "Failed to delete public IP", "publicIPID", publicIP.ID)
		return err
	}

	s.scope.SetBastionPublicIP("", "")
	return nil
}

func (s *Service) createOrGetPublicIP(ctx context.Context) (*PublicIP, error) {
	log := logf.FromContext(ctx)

	publicIP, err := s.findPublicIP(ctx)
	if err != nil {
		return nil, err
	}
	if publicIP != nil {
		return publicIP, nil
	}

	log.V(1).Info("Creating public IP")
	payload := iaas.NewCreatePublicIPPayload()
	if tags := s.scope.BastionTags(); len(tags) > 0 {
		payload.SetLabels(shared.TagsToSDKLabels(tags))
	}
	created, err := s.iaasClient.DefaultAPI.
		CreatePublicIP(ctx, s.scope.ProjectID(), s.scope.Region()).
		CreatePublicIPPayload(*payload).
		Execute()
	if err != nil {
		err := shared.ClassifySDKError("create public IP", err)
		log.Error(err, "Failed to create public IP")
		return nil, err
	}
	return publicIPFromSDK(created), nil
}

func (s *Service) findPublicIP(ctx context.Context) (*PublicIP, error) {
	log := logf.FromContext(ctx)
	if id := s.scope.BastionStatus().PublicIPID; id != "" {
		publicIP, err := s.getPublicIP(ctx, id)
		if err == nil {
			return publicIP, nil
		}
		if !shared.IsNotFound(err) {
			log.Error(err, "Failed to get public IP", "publicIPID", id)
			return nil, err
		}
	}

	matched, err := ListByTags(ctx, s.iaasClient, s.scope.ProjectID(), s.scope.Region(), s.scope.BastionTags())
	if err != nil {
		log.Error(err, "Failed to list public IPs by tags")
		return nil, err
	}
	if len(matched) == 0 {
		return nil, nil
	}
	if len(matched) > 1 {
		return nil, fmt.Errorf("%w: multiple public IPs matching tags", shared.ErrConflict)
	}
	return matched[0], nil
}

func (s *Service) getPublicIP(ctx context.Context, id string) (*PublicIP, error) {
	if id == "" {
		return nil, fmt.Errorf("%w: public IP ID is empty", shared.ErrInvalidInput)
	}
	publicIP, err := s.iaasClient.DefaultAPI.GetPublicIP(ctx, s.scope.ProjectID(), s.scope.Region(), id).Execute()
	if err != nil {
		return nil, shared.ClassifySDKError("get public IP", err)
	}
	return publicIPFromSDK(publicIP), nil
}

func (s *Service) resolveServerID(ctx context.Context) (string, error) {
	if id := s.scope.BastionStatus().ServerID; id != "" {
		return id, nil
	}
	server, err := s.client.FindServerByTags(ctx, s.scope.BastionTags())
	if err != nil {
		if shared.IsNotFound(err) {
			return "", nil
		}
		return "", err
	}
	return server.ID, nil
}

// ListByTags lists STACKIT public IPs matching the given tag selector.
func ListByTags(
	ctx context.Context,
	iaasClient *iaas.APIClient,
	projectID, region string,
	tags map[string]string,
) ([]*PublicIP, error) {
	if len(tags) == 0 {
		return nil, fmt.Errorf("%w: empty tag selector", shared.ErrInvalidInput)
	}
	resp, err := iaasClient.DefaultAPI.ListPublicIPs(ctx, projectID, region).
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

// DetachFromServer detaches a public IP from a server, ignoring NotFound and Conflict errors.
func DetachFromServer(
	ctx context.Context,
	iaasClient *iaas.APIClient,
	projectID, region, serverID, publicIPID string,
) error {
	if serverID == "" || publicIPID == "" {
		return fmt.Errorf("%w: server ID and public IP ID are required", shared.ErrInvalidInput)
	}
	if err := iaasClient.DefaultAPI.
		RemovePublicIpFromServer(ctx, projectID, region, serverID, publicIPID).
		Execute(); err != nil {
		err := shared.ClassifySDKError("remove public IP from server", err)
		if !shared.IsNotFound(err) && !shared.IsConflict(err) {
			return err
		}
	}
	return nil
}

// DeleteByID deletes a public IP by ID, ignoring NotFound errors.
func DeleteByID(
	ctx context.Context,
	iaasClient *iaas.APIClient,
	projectID, region, id string,
) error {
	if id == "" {
		return fmt.Errorf("%w: public IP ID is empty", shared.ErrInvalidInput)
	}
	if err := iaasClient.DefaultAPI.DeletePublicIP(ctx, projectID, region, id).Execute(); err != nil {
		err := shared.ClassifySDKError("delete public IP", err)
		if !shared.IsNotFound(err) {
			return err
		}
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
