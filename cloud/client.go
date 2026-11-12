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

	iaas "github.com/stackitcloud/stackit-sdk-go/services/iaas/v2api"
	lb "github.com/stackitcloud/stackit-sdk-go/services/loadbalancer/v2api"
)

// Reconciler is a generic interface used by components offering a type of service.
type Reconciler interface {
	Reconcile(ctx context.Context) error
	Delete(ctx context.Context) error
}

// Client is the abstraction over STACKIT cloud APIs used by the controllers.
//
// Implementations must:
//   - return ErrNotFound when a resource does not exist
//   - return ErrUnauthorized for permanent auth failures
//   - return ErrInvalidInput for permanent input validation errors
//   - return ErrConflict for retryable conflict errors
//   - return ErrTransient for retryable transient errors
type Client interface {
	ProjectID() string

	Region() string

	IaaSClient() *iaas.APIClient

	LoadBalancerClient() *lb.APIClient

	GetNetwork(ctx context.Context, id string) (*Network, error)

	ListAvailabilityZones(ctx context.Context) ([]string, error)
}

// Factory builds a Client from credentials (raw bytes from the configured
// Secret) plus the project ID and region from the StackitCluster spec.
//
// Controllers receive a Factory rather than a Client so that the real
// implementation can be swapped for the fake in tests.
type Factory func(ctx context.Context, creds Credentials) (Client, error)

// Credentials carries the materials needed to construct a STACKIT API client.
// For MVP we only need the service-account JSON and project ID.
type Credentials struct {
	ProjectID          string
	Region             string
	ServiceAccountJSON []byte
}
