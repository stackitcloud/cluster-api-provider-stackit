/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package network

import iaas "github.com/stackitcloud/stackit-sdk-go/services/iaas/v2api"

// Network describes an existing STACKIT virtual network referenced by the provider.
type Network struct {
	ID           string
	Name         string
	IPv4Prefixes []string
}

// NetworkSpec describes the desired STACKIT network to resolve.
type NetworkSpec struct {
	ID         string
	SetNetwork func(network *Network)
}

// ClientScope is an interface that provides the IaaS client and target scope for network operations.
type ClientScope interface {
	ProjectID() string
	Region() string
	IaaSClient() *iaas.APIClient
}

// Scope is an interface that provides scope data needed for network reconciliation.
type Scope interface {
	ClientScope
	NetworkSpec() NetworkSpec
}

// Client provides direct network operations against the STACKIT IaaS API.
type Client struct {
	iaasClient *iaas.APIClient
	projectID  string
	region     string
}

// NewClient returns a Client for the given scope.
func NewClient(scope ClientScope) *Client {
	return &Client{
		iaasClient: scope.IaaSClient(),
		projectID:  scope.ProjectID(),
		region:     scope.Region(),
	}
}

// Service implements network reconciler.
type Service struct {
	scope Scope
	*Client
}

// New returns Service from given scope.
func New(scope Scope) *Service {
	return &Service{
		scope:  scope,
		Client: NewClient(scope),
	}
}
