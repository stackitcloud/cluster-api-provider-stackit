/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package loadbalancer

import (
	"context"
	"fmt"
	"strings"

	lb "github.com/stackitcloud/stackit-sdk-go/services/loadbalancer/v2api"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/stackitcloud/cluster-api-provider-stackit/cloud/services/shared"
)

func (s *Service) Reconcile(ctx context.Context) error {
	log := logf.FromContext(ctx)
	spec := s.scope.LoadBalancerSpec()
	if err := spec.validate(); err != nil {
		return err
	}

	loadBalancer, err := s.client.ensureLoadBalancer(ctx, spec)
	if err != nil {
		return err
	}
	if spec.SetID != nil {
		spec.SetID(loadBalancer.ID)
	}
	if spec.SetAddress != nil {
		spec.SetAddress(loadBalancer.IP, spec.Port)
	}
	if loadBalancer.IP == "" {
		return nil
	}

	// An empty set never overwrites a populated pool: cluster deletion filters
	// out every machine while the API server is still needed for drain.
	if len(spec.Targets) > 0 {
		if err := s.client.setTargets(ctx, loadBalancer.ID, spec.Port, spec.Targets); err != nil {
			return fmt.Errorf("%w: %w", errTargetPoolUpdate, err)
		}
		log.V(1).Info("Reconciled API server load balancer target pool", "targets", len(spec.Targets))
	}
	log.V(1).Info("Reconciled load balancer resources", "loadBalancerID", loadBalancer.ID)
	return nil
}

func (s *Service) Delete(ctx context.Context) error {
	log := logf.FromContext(ctx)
	spec := s.scope.LoadBalancerSpec()
	if spec.ID == "" && len(spec.Tags) == 0 {
		return fmt.Errorf("%w: load balancer ID or tags are required", shared.ErrInvalidInput)
	}

	deletedIDs, err := shared.DeleteByIDOrTags(ctx, spec.ID, spec.Tags, s.client.deleteByID, s.client.listIDsByTags)
	for _, deletedID := range deletedIDs {
		log.V(1).Info("Deleted load balancer", "loadBalancerID", deletedID)
	}
	if err != nil {
		return err
	}
	if spec.SetID != nil {
		spec.SetID("")
	}
	return nil
}

// get retrieves a STACKIT load balancer by ID.
func (c *client) get(ctx context.Context, id string) (*LoadBalancer, error) {
	if id == "" {
		return nil, fmt.Errorf("%w: load balancer ID is empty", shared.ErrInvalidInput)
	}
	loadBalancer, err := c.scope.LoadBalancerClient().DefaultAPI.GetLoadBalancer(ctx, c.scope.ProjectID(), c.scope.Region(), id).Execute()
	if err != nil {
		return nil, shared.ClassifySDKError("get load balancer", err)
	}
	return loadBalancerFromSDK(loadBalancer), nil
}

// findByTags finds a single STACKIT load balancer matching the given tag selector.
func (c *client) findByTags(ctx context.Context, tags map[string]string) (*LoadBalancer, error) {
	return shared.FindByTags(ctx, tags, c.listByTags, "load balancer", "load balancers")
}

// listByTags lists STACKIT load balancers matching the given tag selector.
func (c *client) listByTags(ctx context.Context, tags map[string]string) ([]*LoadBalancer, error) {
	if len(tags) == 0 {
		return nil, fmt.Errorf("%w: empty tag selector", shared.ErrInvalidInput)
	}
	resp, err := c.scope.LoadBalancerClient().DefaultAPI.ListLoadBalancers(ctx, c.scope.ProjectID(), c.scope.Region()).Execute()
	if err != nil {
		err := shared.ClassifySDKError("list load balancers", err)
		if isLoadBalancerServiceNotEnabled(err) {
			return nil, nil
		}
		return nil, err
	}
	matched := []*LoadBalancer{}
	labels := Labels(tags)
	for _, candidate := range resp.GetLoadBalancers() {
		if shared.MapContains(candidate.GetLabels(), labels) {
			candidate := candidate
			matched = append(matched, loadBalancerFromSDK(&candidate))
		}
	}
	return matched, nil
}

// deleteByID deletes a load balancer by ID, returning true if the load balancer existed and was deleted,
// or false if it was already absent.
func (c *client) deleteByID(ctx context.Context, id string) (bool, error) {
	if id == "" {
		return false, fmt.Errorf("%w: load balancer ID is empty", shared.ErrInvalidInput)
	}
	if _, err := c.scope.LoadBalancerClient().DefaultAPI.DeleteLoadBalancer(ctx, c.scope.ProjectID(), c.scope.Region(), id).Execute(); err != nil {
		err := shared.ClassifySDKError("delete load balancer", err)
		if shared.IsNotFound(err) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

func (c *client) ensureLoadBalancer(ctx context.Context, spec LoadBalancerSpec) (*LoadBalancer, error) {
	loadBalancer, err := shared.ResolveExisting(ctx, spec.ID, spec.Tags, c.get, c.findByTags, isIgnorableLookupError)
	if err != nil {
		return nil, err
	}
	if loadBalancer != nil {
		return loadBalancer, nil
	}

	payload := spec.toCreatePayload(c.scope.Region())
	lbResp, err := c.scope.LoadBalancerClient().DefaultAPI.CreateLoadBalancer(ctx, c.scope.ProjectID(), c.scope.Region()).
		CreateLoadBalancerPayload(payload).
		Execute()
	if err != nil {
		return nil, shared.ClassifySDKError("create load balancer", err)
	}
	out := loadBalancerFromSDK(lbResp)
	logf.FromContext(ctx).V(1).Info("Created load balancer", "name", spec.Name, "loadBalancerID", out.ID)
	return out, nil
}

func (c *client) listIDsByTags(ctx context.Context, tags map[string]string) ([]string, error) {
	loadBalancers, err := c.listByTags(ctx, tags)
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(loadBalancers))
	for _, loadBalancer := range loadBalancers {
		if loadBalancer.ID != "" {
			ids = append(ids, loadBalancer.ID)
		}
	}
	return ids, nil
}

func (s LoadBalancerSpec) validate() error {
	if s.Name == "" {
		return fmt.Errorf("%w: load balancer name is required", shared.ErrInvalidInput)
	}
	if s.NetworkID == "" {
		return fmt.Errorf("%w: load balancer network ID is required", shared.ErrInvalidInput)
	}
	if s.Port <= 0 {
		return fmt.Errorf("%w: load balancer port must be positive", shared.ErrInvalidInput)
	}
	if len(s.Tags) == 0 {
		return fmt.Errorf("%w: load balancer tags are required", shared.ErrInvalidInput)
	}
	return nil
}

func (s LoadBalancerSpec) toCreatePayload(region string) lb.CreateLoadBalancerPayload {
	payload := lb.NewCreateLoadBalancerPayload()
	payload.SetName(s.Name)
	payload.SetRegion(region)
	if len(s.Tags) > 0 {
		payload.SetLabels(Labels(s.Tags))
	}
	options := lb.NewLoadBalancerOptions()
	options.SetEphemeralAddress(true)
	payload.SetOptions(*options)

	network := lb.NewNetwork()
	network.SetNetworkId(s.NetworkID)
	network.SetRole("ROLE_LISTENERS_AND_TARGETS")
	payload.SetNetworks([]lb.Network{*network})

	targetPool := lb.NewTargetPool()
	targetPool.SetName(apiserverTargetPoolName)
	targetPool.SetTargetPort(s.Port)
	seed := s.SeedTargets
	if len(seed) == 0 {
		seed = s.Targets
	}
	targets := make([]lb.Target, 0, max(1, len(seed)))
	if len(seed) == 0 {
		bootstrap := BootstrapTarget("")
		target := lb.NewTarget()
		target.SetDisplayName(bootstrap.Name)
		target.SetIp(bootstrap.IP)
		targets = append(targets, *target)
	} else {
		for _, targetInput := range seed {
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
	listener.SetPort(s.Port)
	listener.SetProtocol("PROTOCOL_TCP")
	listener.SetTargetPool(apiserverTargetPoolName)
	payload.SetListeners([]lb.Listener{*listener})

	return *payload
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

func isIgnorableLookupError(err error) bool {
	return shared.IsNotFound(err) || isLoadBalancerServiceNotEnabled(err)
}

func isLoadBalancerServiceNotEnabled(err error) bool {
	return shared.IsUnauthorized(err) && strings.Contains(err.Error(), "Service not enabled")
}
