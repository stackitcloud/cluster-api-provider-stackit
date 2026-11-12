/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package publicip

import "github.com/stackitcloud/cluster-api-provider-stackit/cloud/services/shared"

// PublicIP describes a STACKIT public IP resource.
type PublicIP struct {
	ID                 string
	IP                 string
	NetworkInterfaceID string
}

// PublicIPSpec describes the desired state of a STACKIT public IP.
type PublicIPSpec struct {
	ID             string
	Tags           map[string]string
	ServerID       string
	ServerSelector map[string]string
	SetPublicIP    func(id, ip string)
}

// ClientScope is an interface that provides the IaaS client and target scope for public IP operations.
type ClientScope = shared.IaaSClientScope

// Scope is an interface that provides scope data needed for public IP reconciliation.
type Scope interface {
	ClientScope
	PublicIPSpec() PublicIPSpec
}

// Client provides direct public IP operations against the STACKIT IaaS API.
type Client struct {
	shared.IaaSScope
}

// NewClient returns a Client for the given scope.
func NewClient(scope ClientScope) *Client {
	return &Client{IaaSScope: shared.NewIaaSScope(scope)}
}

// Service implements public IPs reconciler.
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
