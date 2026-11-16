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

	logf "sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/stackitcloud/cluster-api-provider-stackit/cloud/services/shared"
)

func (s *Service) Reconcile(ctx context.Context) error {
	log := logf.FromContext(ctx)
	spec := s.scope.NetworkSpec()
	if err := spec.validate(); err != nil {
		return err
	}

	net, err := s.get(ctx, spec.ID)
	if err != nil {
		return err
	}
	if spec.SetNetwork != nil {
		spec.SetNetwork(net)
	}
	log.V(1).Info("Reconciled network resources", "networkID", net.ID)
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
	ipv4 := net.GetIpv4()
	return &Network{
		ID:           net.GetId(),
		Name:         net.GetName(),
		IPv4Prefixes: ipv4.GetPrefixes(),
	}, nil
}

func (s NetworkSpec) validate() error {
	if s.ID == "" {
		return fmt.Errorf("%w: network ID is required", shared.ErrInvalidInput)
	}
	return nil
}
