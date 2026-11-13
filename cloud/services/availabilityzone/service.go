/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package availabilityzone

import "github.com/stackitcloud/cluster-api-provider-stackit/cloud/services/shared"

// AvailabilityZoneSpec describes the callback used to record discovered availability zones.
type AvailabilityZoneSpec struct {
	SetZones func(zones []string)
}

// Scope is an interface that provides scope data needed for availability zone reconciliation.
type Scope interface {
	shared.IaaSClientScope
	AvailabilityZoneSpec() AvailabilityZoneSpec
}

// client provides direct availability zone operations against the STACKIT IaaS API.
type client struct {
	scope shared.IaaSClientScope
}

// newClient returns a client for the given scope.
func newClient(scope shared.IaaSClientScope) *client {
	return &client{scope: scope}
}

// Service implements availability zones reconciler.
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
