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

// Client provides the target STACKIT project, region, and SDK API clients.
type Client interface {
	ProjectID() string

	Region() string

	IaaSClient() *iaas.APIClient

	LoadBalancerClient() *lb.APIClient
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
