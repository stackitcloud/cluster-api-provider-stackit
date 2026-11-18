/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package network

import "github.com/stackitcloud/cluster-api-provider-stackit/cloud/services/shared"

// DefaultNetworkPrefix is the default IPv4 CIDR block used when creating a managed network.
const DefaultNetworkPrefix = "10.0.0.0/24"

// Network describes a STACKIT virtual network.
type Network struct {
	ID           string
	Name         string
	IPv4Prefixes []string
}

// NetworkSpec describes the desired state of a STACKIT network.
type NetworkSpec struct {
	ID         string
	Name       string
	IPv4Prefix string
	Tags       map[string]string
	Managed    bool
	SetNetwork func(network *Network, created bool)
}

// Scope is an interface that provides scope data needed for network reconciliation.
type Scope interface {
	shared.IaaSClientScope
	NetworkSpec() NetworkSpec
}

// Service implements network reconciler.
type Service struct {
	scope Scope
}

// New returns Service from given scope.
func New(scope Scope) *Service {
	return &Service{
		scope: scope,
	}
}
