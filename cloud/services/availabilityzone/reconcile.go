/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package availabilityzone

import (
	"context"
	"fmt"

	logf "sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/stackitcloud/cluster-api-provider-stackit/cloud/services/shared"
)

func (s *Service) Reconcile(ctx context.Context) error {
	log := logf.FromContext(ctx)
	zones, err := s.List(ctx)
	if err != nil {
		return err
	}
	spec := s.scope.AvailabilityZoneSpec()
	if spec.SetZones != nil {
		spec.SetZones(zones)
	}
	log.V(1).Info("Reconciled availability zones", "count", len(zones))
	return nil
}

func (s *Service) Delete(_ context.Context) error {
	return nil
}

// List retrieves the availability zones for the configured region.
func (c *Client) List(ctx context.Context) ([]string, error) {
	if c.region == "" {
		return nil, fmt.Errorf("%w: region is empty", shared.ErrInvalidInput)
	}
	resp, err := c.iaasClient.DefaultAPI.ListAvailabilityZones(ctx, c.region).Execute()
	if err != nil {
		return nil, shared.ClassifySDKError("list availability zones", err)
	}
	if resp == nil || resp.Items == nil {
		return nil, nil
	}
	return resp.GetItems(), nil
}
