/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package loadbalancer

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	lb "github.com/stackitcloud/stackit-sdk-go/services/loadbalancer/v2api"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	infrav1 "github.com/stackitcloud/cluster-api-provider-stackit/api/v1alpha1"
	"github.com/stackitcloud/cluster-api-provider-stackit/cloud/services/shared"
)

func (s *Service) Reconcile(ctx context.Context) (ctrl.Result, error) {
	log := logf.FromContext(ctx)
	if !s.scope.APIServerLoadBalancerEnabled() {
		if s.scope.ControlPlaneEndpointHost() != "" {
			s.scope.UseExternalControlPlaneEndpoint()
			s.scope.SetConditions(
				metav1.ConditionTrue,
				"Skipped",
				"external endpoint provided",
				infrav1.ClusterLoadBalancerReadyCondition,
			)
			return ctrl.Result{}, nil
		}
		log.Error(errors.New("controlPlaneEndpoint host is empty"), "API server load balancer is disabled and controlPlaneEndpoint is empty", "cluster", s.scope.Name())
		s.scope.Warn("EndpointMissing", "apiServerLoadBalancer.enabled is false and controlPlaneEndpoint is empty")
		s.scope.SetNotReady(
			"EndpointMissing",
			"apiServerLoadBalancer.enabled is false and controlPlaneEndpoint is empty",
			infrav1.ClusterLoadBalancerReadyCondition,
			infrav1.ClusterReadyCondition,
		)
		return ctrl.Result{}, nil
	}

	spec, err := s.scope.ResolveLoadBalancerSpec(ctx)
	if err != nil {
		return ctrl.Result{}, err
	}
	if err := spec.validate(); err != nil {
		return s.handleCreateError(ctx, err)
	}

	previousEndpointHost := s.scope.APIServerEndpointHost()
	cond := s.scope.FindCondition(infrav1.ClusterLoadBalancerReadyCondition)
	wasLBReady := cond != nil && cond.Status == metav1.ConditionTrue

	loadBalancer, created, err := s.ensureLoadBalancer(ctx, spec)
	if err != nil {
		return s.handleCreateError(ctx, err)
	}
	loadBalancer.Port = spec.Port
	s.scope.SetLoadBalancer(loadBalancer)
	if created {
		s.scope.Eventf(
			"LoadBalancerCreated",
			"Created API server load balancer %s", loadBalancer.ID,
		)
	}
	if loadBalancer.IP == "" {
		s.scope.SetNotReady(
			"Provisioning",
			"waiting for API server load balancer IP address",
			infrav1.ClusterLoadBalancerReadyCondition,
			infrav1.ClusterReadyCondition,
		)
		return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
	}

	// An empty set never overwrites a populated pool: cluster deletion filters
	// out every machine while the API server is still needed for drain.
	if len(spec.Targets) > 0 {
		if err := s.setTargets(ctx, loadBalancer.ID, spec.Port, spec.Targets); err != nil {
			targetErr := fmt.Errorf("%w: %w", errTargetPoolUpdate, err)
			if !shared.IsRetryable(targetErr) {
				s.scope.Warnf(
					"LoadBalancerTargetError",
					"Cannot update API server load balancer target pool: %v", targetErr,
				)
			}
			// Scoped to the load balancer condition: failing the cluster would
			// stop the machine reconciler from replacing the broken machine.
			s.scope.SetConditions(
				metav1.ConditionFalse,
				"LoadBalancerTargetError",
				targetErr.Error(),
				infrav1.ClusterLoadBalancerReadyCondition,
			)
			// Requeue even on a permanent error: this return skips SetReady, so
			// no machine exists yet whose events could retry it.
			return s.scope.TargetPoolFailureResult(targetErr, shared.RetryableErrorRequeueAfter)
		}
		log.V(1).Info("Reconciled API server load balancer target pool", "targets", len(spec.Targets))
	}

	isLBReady := wasLBReady && previousEndpointHost == loadBalancer.IP
	if !isLBReady {
		s.scope.SetConditions(
			metav1.ConditionTrue,
			"Available",
			"",
			infrav1.ClusterLoadBalancerReadyCondition,
		)
		s.scope.Eventf(
			"LoadBalancerReady",
			"API server load balancer is ready at %s", loadBalancer.IP,
		)
	}
	log.V(1).Info("Reconciled load balancer resources", "loadBalancerID", loadBalancer.ID, "created", created)
	return ctrl.Result{}, nil
}

func (s *Service) handleCreateError(ctx context.Context, err error) (ctrl.Result, error) {
	log := logf.FromContext(ctx)
	log.Error(err, "Failed to create API server load balancer",
		"cluster", s.scope.Name(),
		"region", s.scope.Region(),
	)
	s.scope.Warnf(
		"FailedCreateLoadBalancer",
		"Failed to create API server load balancer: %v", err,
	)
	return s.scope.CloudFailureResult(
		"LoadBalancerError",
		err,
		shared.RetryableErrorRequeueAfter,
		false,
		infrav1.ClusterLoadBalancerReadyCondition,
		infrav1.ClusterReadyCondition,
	)
}

func (s *Service) Delete(ctx context.Context) (ctrl.Result, error) {
	log := logf.FromContext(ctx)
	spec := s.scope.LoadBalancerSpec()
	if spec.ID == "" && len(spec.Tags) == 0 {
		return ctrl.Result{}, fmt.Errorf("%w: load balancer ID or tags are required", shared.ErrInvalidInput)
	}

	deletedIDs, err := shared.DeleteByIDOrTags(ctx, spec.ID, spec.Tags, s.deleteByID, s.listIDsByTags)
	for _, deletedID := range deletedIDs {
		log.V(1).Info("Deleted load balancer", "loadBalancerID", deletedID)
	}
	if err != nil {
		log.Error(err, "Failed to delete API server load balancer", "cluster", s.scope.Name(), "loadBalancerID", spec.ID)
		s.scope.Warnf("FailedDeleteLoadBalancer",
			"Failed to delete API server load balancer %s: %v", spec.ID, err)
		return ctrl.Result{}, err
	}
	s.scope.SetLoadBalancer(nil)
	if spec.ID != "" {
		s.scope.Eventf(
			"LoadBalancerDeleted",
			"Deleted API server load balancer %s", spec.ID,
		)
	}
	return ctrl.Result{}, nil
}

// get retrieves a STACKIT load balancer by ID.
func (s *Service) get(ctx context.Context, id string) (*LoadBalancer, error) {
	if id == "" {
		return nil, fmt.Errorf("%w: load balancer ID is empty", shared.ErrInvalidInput)
	}
	loadBalancer, err := s.scope.LoadBalancerClient().DefaultAPI.GetLoadBalancer(ctx, s.scope.ProjectID(), s.scope.Region(), id).Execute()
	if err != nil {
		return nil, shared.ClassifySDKError("get load balancer", err)
	}
	return loadBalancerFromSDK(loadBalancer), nil
}

// findByTags finds a single STACKIT load balancer matching the given tag selector.
func (s *Service) findByTags(ctx context.Context, tags map[string]string) (*LoadBalancer, error) {
	return shared.FindByTags(ctx, tags, s.listByTags, "load balancer", "load balancers")
}

// listByTags lists STACKIT load balancers matching the given tag selector.
func (s *Service) listByTags(ctx context.Context, tags map[string]string) ([]*LoadBalancer, error) {
	return shared.ListByTags(Labels(tags), func(_ string) ([]lb.LoadBalancer, error) {
		resp, err := s.scope.LoadBalancerClient().DefaultAPI.ListLoadBalancers(ctx, s.scope.ProjectID(), s.scope.Region()).Execute()
		if err != nil {
			err := shared.ClassifySDKError("list load balancers", err)
			if isLoadBalancerServiceNotEnabled(err) {
				return nil, nil
			}
			return nil, err
		}
		return resp.GetLoadBalancers(), nil
	}, loadBalancerFromSDK)
}

// deleteByID deletes a load balancer by ID, returning true if the load balancer existed and was deleted,
// or false if it was already absent.
func (s *Service) deleteByID(ctx context.Context, id string) (bool, error) {
	if id == "" {
		return false, fmt.Errorf("%w: load balancer ID is empty", shared.ErrInvalidInput)
	}
	if _, err := s.scope.LoadBalancerClient().DefaultAPI.DeleteLoadBalancer(ctx, s.scope.ProjectID(), s.scope.Region(), id).Execute(); err != nil {
		err := shared.ClassifySDKError("delete load balancer", err)
		if shared.IsNotFound(err) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

func (s *Service) ensureLoadBalancer(ctx context.Context, spec LoadBalancerSpec) (*LoadBalancer, bool, error) {
	loadBalancer, err := shared.ResolveExisting(ctx, spec.ID, spec.Tags, s.get, s.findByTags, isIgnorableLookupError)
	if err != nil {
		return nil, false, err
	}
	if loadBalancer != nil {
		return loadBalancer, false, nil
	}

	payload := spec.toCreatePayload(s.scope.Region())
	lbResp, err := s.scope.LoadBalancerClient().DefaultAPI.CreateLoadBalancer(ctx, s.scope.ProjectID(), s.scope.Region()).
		CreateLoadBalancerPayload(payload).
		Execute()
	if err != nil {
		return nil, false, shared.ClassifySDKError("create load balancer", err)
	}
	out := loadBalancerFromSDK(lbResp)
	logf.FromContext(ctx).V(1).Info("Created load balancer", "name", spec.Name, "loadBalancerID", out.ID)
	return out, true, nil
}

func (s *Service) listIDsByTags(ctx context.Context, tags map[string]string) ([]string, error) {
	return shared.ListIDsByTags(ctx, tags, s.listByTags, func(loadBalancer *LoadBalancer) string {
		return loadBalancer.ID
	})
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
	listener.SetProtocol(listenerProtocolTCP)
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
		IP:   cmp.Or(in.GetExternalAddress(), in.GetPrivateAddress()),
	}
}

func isIgnorableLookupError(err error) bool {
	return shared.IsNotFound(err) || isLoadBalancerServiceNotEnabled(err)
}

func isLoadBalancerServiceNotEnabled(err error) bool {
	return shared.IsUnauthorized(err) && strings.Contains(err.Error(), "Service not enabled")
}
