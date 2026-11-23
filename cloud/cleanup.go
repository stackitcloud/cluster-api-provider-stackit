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
	"errors"
	"fmt"
	"time"

	ctrl "sigs.k8s.io/controller-runtime"

	"github.com/stackitcloud/cluster-api-provider-stackit/cloud/services/loadbalancer"
	"github.com/stackitcloud/cluster-api-provider-stackit/cloud/services/network"
	"github.com/stackitcloud/cluster-api-provider-stackit/cloud/services/publicip"
	"github.com/stackitcloud/cluster-api-provider-stackit/cloud/services/securitygroup"
	"github.com/stackitcloud/cluster-api-provider-stackit/cloud/services/server"
	"github.com/stackitcloud/cluster-api-provider-stackit/cloud/services/shared"
)

type cleanupScope struct {
	Client
	shared.NoOpReconcileScope
	tags map[string]string
}

// CleanupByTags deletes provider-managed cloud resources matching tags without
// relying on Kubernetes objects. Load balancers are deleted before servers so
// server deletion is not blocked by target attachments, servers are deleted
// before security groups so attached servers do not block security group
// deletion if server detachment fails or lags, and networks are deleted last.
func CleanupByTags(ctx context.Context, client Client, tags map[string]string) error {
	if len(tags) == 0 {
		return fmt.Errorf("%w: empty cleanup tag selector", shared.ErrInvalidInput)
	}

	scope := &cleanupScope{
		Client: client,
		tags:   tags,
	}
	var errs []error
	for _, reconciler := range []Reconciler{
		publicip.New(scope),
		loadbalancer.New(scope),
		server.New(scope),
		securitygroup.New(scope),
		network.New(scope),
	} {
		if _, err := reconciler.Delete(ctx); err != nil {
			errs = append(errs, err)
		}
	}

	if len(errs) > 0 {
		return fmt.Errorf("%w: %w", shared.ErrTransient, errors.Join(errs...))
	}
	return nil
}

func (s *cleanupScope) PublicIPSpec() publicip.PublicIPSpec {
	return publicip.PublicIPSpec{
		Tags:           s.tags,
		ServerSelector: s.tags,
	}
}

func (s *cleanupScope) SetPublicIP(_ *publicip.PublicIP) {}

func (s *cleanupScope) APIServerLoadBalancerEnabled() bool {
	return true
}

func (s *cleanupScope) ControlPlaneEndpointHost() string {
	return ""
}

func (s *cleanupScope) UseExternalControlPlaneEndpoint() {}

func (s *cleanupScope) APIServerEndpointHost() string {
	return ""
}

func (s *cleanupScope) ResolveLoadBalancerSpec(_ context.Context) (loadbalancer.LoadBalancerSpec, error) {
	return s.LoadBalancerSpec(), nil
}

func (s *cleanupScope) LoadBalancerSpec() loadbalancer.LoadBalancerSpec {
	return loadbalancer.LoadBalancerSpec{
		Tags: s.tags,
	}
}

func (s *cleanupScope) SetLoadBalancer(_ *loadbalancer.LoadBalancer) {}

func (s *cleanupScope) TargetPoolFailureResult(err error, _ time.Duration) (ctrl.Result, error) {
	return ctrl.Result{}, err
}

func (s *cleanupScope) ServerSpec() server.ServerSpec {
	return server.ServerSpec{
		Tags: s.tags,
	}
}

func (s *cleanupScope) SetServer(_ *server.Server, _ bool) {}

func (s *cleanupScope) SecurityGroupSpecs() []securitygroup.SecurityGroupSpec {
	return []securitygroup.SecurityGroupSpec{
		{
			Tags:           s.tags,
			ServerSelector: s.tags,
		},
	}
}

func (s *cleanupScope) SetSecurityGroup(_ string, _ *securitygroup.SecurityGroup) {}

func (s *cleanupScope) NetworkSpec() network.NetworkSpec {
	return network.NetworkSpec{
		Tags:    s.tags,
		Managed: true,
	}
}

func (s *cleanupScope) SetNetwork(_ *network.Network) {}
