/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package cloud

import "context"

// Client is the abstraction over STACKIT cloud APIs used by the controllers.
//
// Implementations must:
//   - return ErrNotFound when a resource does not exist
//   - return ErrUnauthorized for permanent auth failures
//   - return ErrInvalidInput for permanent input validation errors
//   - return ErrConflict for retryable conflict errors
//   - return ErrTransient for retryable transient errors
//   - be idempotent (CreateServer/EnsureAPIServerLoadBalancer must not produce
//     duplicates when a resource with matching tags already exists)
type Client interface {
	GetServer(ctx context.Context, id string) (*Server, error)

	FindServerByTags(ctx context.Context, tags map[string]string) (*Server, error)

	ListServersByTags(ctx context.Context, tags map[string]string) ([]*Server, error)

	CreateServer(ctx context.Context, input CreateServerInput) (*Server, error)

	DeleteServer(ctx context.Context, id string) error

	GetNetwork(ctx context.Context, id string) (*Network, error)

	EnsureBastion(ctx context.Context, input BastionInput) (*Bastion, error)

	DeleteBastion(ctx context.Context, input BastionInput, status Bastion) error

	EnsureNodeSSHAccess(ctx context.Context, input NodeSSHAccessInput) (*SecurityGroup, error)

	DeleteNodeSSHAccess(ctx context.Context, tags map[string]string) error

	ListPublicIPsByTags(ctx context.Context, tags map[string]string) ([]*PublicIP, error)

	ListSecurityGroupsByTags(ctx context.Context, tags map[string]string) ([]*SecurityGroup, error)

	EnsureAPIServerLoadBalancer(ctx context.Context, input LoadBalancerInput) (*LoadBalancer, error)

	ListAPIServerLoadBalancersByTags(ctx context.Context, tags map[string]string) ([]*LoadBalancer, error)

	EnsureAPIServerLoadBalancerTarget(ctx context.Context, input LoadBalancerTargetInput) error

	DeleteAPIServerLoadBalancerTarget(ctx context.Context, input LoadBalancerTargetInput) error

	DeleteAPIServerLoadBalancer(ctx context.Context, id string) error
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
