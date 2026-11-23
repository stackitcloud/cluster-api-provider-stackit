/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package cloud

import (
	"fmt"
	"os"

	"github.com/stackitcloud/cluster-api-provider-stackit/cloud/services/shared"
	"github.com/stackitcloud/stackit-sdk-go/core/config"
	iaas "github.com/stackitcloud/stackit-sdk-go/services/iaas/v2api"
	lb "github.com/stackitcloud/stackit-sdk-go/services/loadbalancer/v2api"
)

const (
	stackitNoAuthEnv        = "STACKIT_NO_AUTH"
	stackitTokenEndpointEnv = "STACKIT_TOKEN_BASEURL"
	stackitIAASEndpointEnv  = "STACKIT_IAAS_ENDPOINT"
	stackitLBEndpointEnv    = "STACKIT_LOADBALANCER_ENDPOINT"
)

// SDKClient is the STACKIT SDK-backed implementation of Client.
type SDKClient struct {
	projectID string
	region    string

	iaasClient *iaas.APIClient
	lbClient   *lb.APIClient
}

// NewClient returns the real STACKIT cloud client.
//
// The credential secret format mirrors machine-controller-manager-provider-stackit:
// "project-id" plus "serviceaccount.json". Authentication uses the STACKIT
// service-account key flow.
func NewClient(creds Credentials) (Client, error) {
	if creds.ProjectID == "" {
		return nil, fmt.Errorf("%w: project ID is empty", shared.ErrInvalidInput)
	}
	if creds.Region == "" {
		return nil, fmt.Errorf("%w: region is empty", shared.ErrInvalidInput)
	}
	if len(creds.ServiceAccountJSON) == 0 && os.Getenv(stackitNoAuthEnv) != "true" {
		return nil, fmt.Errorf("%w: service account JSON is empty", shared.ErrInvalidInput)
	}

	iaasClient, err := iaas.NewAPIClient(stackitOptions(creds, os.Getenv(stackitIAASEndpointEnv))...)
	if err != nil {
		return nil, fmt.Errorf("%w: create IAAS SDK client: %v", shared.ErrInvalidInput, err)
	}
	lbClient, err := lb.NewAPIClient(stackitOptions(creds, os.Getenv(stackitLBEndpointEnv))...)
	if err != nil {
		return nil, fmt.Errorf("%w: create load balancer SDK client: %v", shared.ErrInvalidInput, err)
	}

	return &SDKClient{
		projectID:  creds.ProjectID,
		region:     creds.Region,
		iaasClient: iaasClient,
		lbClient:   lbClient,
	}, nil
}

func (c *SDKClient) ProjectID() string {
	return c.projectID
}

func (c *SDKClient) Region() string {
	return c.region
}

func (c *SDKClient) IaaSClient() *iaas.APIClient {
	return c.iaasClient
}

func (c *SDKClient) LoadBalancerClient() *lb.APIClient {
	return c.lbClient
}

func stackitOptions(creds Credentials, endpoint string) []config.ConfigurationOption {
	opts := []config.ConfigurationOption{
		config.WithUserAgent("cluster-api-provider-stackit"),
	}
	if os.Getenv(stackitNoAuthEnv) == "true" {
		opts = append(opts, config.WithoutAuthentication())
	} else {
		opts = append(opts, config.WithServiceAccountKey(string(creds.ServiceAccountJSON)))
	}
	if endpoint != "" {
		opts = append(opts, config.WithEndpoint(endpoint))
	}
	if tokenEndpoint := os.Getenv(stackitTokenEndpointEnv); tokenEndpoint != "" {
		opts = append(opts, config.WithTokenEndpoint(tokenEndpoint))
	}
	return opts
}
