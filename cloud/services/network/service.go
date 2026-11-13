/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package network

import "github.com/stackitcloud/cluster-api-provider-stackit/cloud/services/shared"

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

// Scope is an interface that provides scope data needed for network reconciliation.
type Scope interface {
	shared.IaaSClientScope
	NetworkSpec() NetworkSpec
}

// client provides direct network operations against the STACKIT IaaS API.
type client struct {
	scope shared.IaaSClientScope
}

// newClient returns a client for the given scope.
func newClient(scope shared.IaaSClientScope) *client {
	return &client{scope: scope}
}

// Service implements network reconciler.
type Service struct {
	scope  Scope
	client *client
}

// New returns Service from given scope.
func New(scope Scope) *Service {
	return &Service{
		scope:  scope,
		client: newClient(scope),
	}
}
