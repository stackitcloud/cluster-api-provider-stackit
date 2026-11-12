/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package availabilityzone

import iaas "github.com/stackitcloud/stackit-sdk-go/services/iaas/v2api"

// AvailabilityZoneSpec describes the callback used to record discovered availability zones.
type AvailabilityZoneSpec struct {
	SetZones func(zones []string)
}

// clientScope is an interface that provides the IaaS client and target region for availability zone operations.
type clientScope interface {
	Region() string
	IaaSClient() *iaas.APIClient
}

// Scope is an interface that provides scope data needed for availability zone reconciliation.
type Scope interface {
	clientScope
	AvailabilityZoneSpec() AvailabilityZoneSpec
}

// client provides direct availability zone operations against the STACKIT IaaS API.
type client struct {
	iaasClient *iaas.APIClient
	region     string
}

// newClient returns a client for the given scope.
func newClient(scope clientScope) *client {
	return &client{
		iaasClient: scope.IaaSClient(),
		region:     scope.Region(),
	}
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
