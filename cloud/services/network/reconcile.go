/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package network

import (
	"context"
	"fmt"

	iaas "github.com/stackitcloud/stackit-sdk-go/services/iaas/v2api"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/stackitcloud/cluster-api-provider-stackit/cloud/services/shared"
)

func (s *Service) Reconcile(ctx context.Context) error {
	log := logf.FromContext(ctx)
	spec := s.scope.NetworkSpec()
	if err := spec.validate(); err != nil {
		return err
	}

	var (
		net     *Network
		created bool
		err     error
	)
	if spec.Managed {
		net, created, err = s.ensureNetwork(ctx, spec)
	} else {
		net, err = s.get(ctx, spec.ID)
	}
	if err != nil {
		return err
	}
	if spec.SetNetwork != nil {
		spec.SetNetwork(net, created)
	}
	log.V(1).Info("Reconciled network resources", "networkID", net.ID, "created", created)
	return nil
}

func (s *Service) Delete(ctx context.Context) error {
	log := logf.FromContext(ctx)
	spec := s.scope.NetworkSpec()
	if !spec.Managed {
		return nil
	}
	if spec.ID == "" && len(spec.Tags) == 0 {
		return fmt.Errorf("%w: network ID or tags are required", shared.ErrInvalidInput)
	}

	deletedIDs, err := shared.DeleteByIDOrTags(ctx, spec.ID, spec.Tags, s.deleteByID, s.listIDsByTags)
	for _, deletedID := range deletedIDs {
		log.V(1).Info("Deleted network", "networkID", deletedID)
	}
	if err != nil {
		return err
	}
	if spec.SetNetwork != nil {
		spec.SetNetwork(nil, false)
	}
	return nil
}

// get retrieves a STACKIT network by ID.
func (s *Service) get(ctx context.Context, id string) (*Network, error) {
	if id == "" {
		return nil, fmt.Errorf("%w: network ID is empty", shared.ErrInvalidInput)
	}
	net, err := s.scope.IaaSClient().DefaultAPI.GetNetwork(ctx, s.scope.ProjectID(), s.scope.Region(), id).Execute()
	if err != nil {
		return nil, shared.ClassifySDKError("get network", err)
	}
	return networkFromSDK(net), nil
}

// findByTags finds a single STACKIT network matching the given tag selector.
func (s *Service) findByTags(ctx context.Context, tags map[string]string) (*Network, error) {
	return shared.FindByTags(ctx, tags, s.listByTags, "network", "networks")
}

// listByTags lists STACKIT networks matching the given tag selector.
func (s *Service) listByTags(ctx context.Context, tags map[string]string) ([]*Network, error) {
	return shared.ListByTags(tags, func(selector string) ([]iaas.Network, error) {
		resp, err := s.scope.IaaSClient().DefaultAPI.ListNetworks(ctx, s.scope.ProjectID(), s.scope.Region()).
			LabelSelector(selector).
			Execute()
		if err != nil {
			return nil, shared.ClassifySDKError("list networks", err)
		}
		return resp.GetItems(), nil
	}, networkFromSDK)
}

func (s *Service) listIDsByTags(ctx context.Context, tags map[string]string) ([]string, error) {
	return shared.ListIDsByTags(ctx, tags, s.listByTags, func(net *Network) string {
		return net.ID
	})
}

// deleteByID deletes a network by ID, returning true if the network existed and was deleted,
// or false if it was already absent.
func (s *Service) deleteByID(ctx context.Context, id string) (bool, error) {
	if id == "" {
		return false, fmt.Errorf("%w: network ID is empty", shared.ErrInvalidInput)
	}
	if err := s.scope.IaaSClient().DefaultAPI.DeleteNetwork(ctx, s.scope.ProjectID(), s.scope.Region(), id).Execute(); err != nil {
		err := shared.ClassifySDKError("delete network", err)
		if shared.IsNotFound(err) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// create creates a new STACKIT network from the given spec.
func (s *Service) create(ctx context.Context, spec NetworkSpec) (*Network, error) {
	payload := spec.toCreatePayload()
	created, err := s.scope.IaaSClient().DefaultAPI.CreateNetwork(ctx, s.scope.ProjectID(), s.scope.Region()).
		CreateNetworkPayload(payload).
		Execute()
	if err != nil {
		return nil, shared.ClassifySDKError("create network", err)
	}
	out := networkFromSDK(created)
	logf.FromContext(ctx).V(1).Info("Created network", "name", spec.Name, "networkID", out.ID)
	return out, nil
}

func (s *Service) ensureNetwork(ctx context.Context, spec NetworkSpec) (*Network, bool, error) {
	net, err := shared.ResolveExisting(ctx, spec.ID, spec.Tags, s.get, s.findByTags, shared.IsNotFound)
	if err != nil {
		return nil, false, err
	}
	if net != nil {
		return net, false, nil
	}

	out, err := s.create(ctx, spec)
	if err != nil {
		return nil, false, err
	}
	return out, true, nil
}

func (s NetworkSpec) validate() error {
	if s.Managed {
		if s.Name == "" {
			return fmt.Errorf("%w: network name is required", shared.ErrInvalidInput)
		}
		if len(s.Tags) == 0 {
			return fmt.Errorf("%w: network tags are required", shared.ErrInvalidInput)
		}
		return nil
	}
	if s.ID == "" {
		return fmt.Errorf("%w: network ID is required", shared.ErrInvalidInput)
	}
	return nil
}

func (s NetworkSpec) toCreatePayload() iaas.CreateNetworkPayload {
	payload := iaas.NewCreateNetworkPayload(s.Name)
	if len(s.Tags) > 0 {
		payload.SetLabels(shared.TagsToSDKLabels(s.Tags))
	}
	if s.IPv4Prefix != "" {
		ipv4Payload := iaas.NewCreateNetworkIPv4WithPrefix(s.IPv4Prefix)
		payload.SetIpv4(iaas.CreateNetworkIPv4WithPrefixAsCreateNetworkIPv4(ipv4Payload))
	}
	return *payload
}

func networkFromSDK(net *iaas.Network) *Network {
	if net == nil {
		return nil
	}
	ipv4 := net.GetIpv4()
	return &Network{
		ID:           net.GetId(),
		Name:         net.GetName(),
		IPv4Prefixes: ipv4.GetPrefixes(),
	}
}
