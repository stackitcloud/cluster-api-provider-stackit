/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package availabilityzone

import "github.com/stackitcloud/cluster-api-provider-stackit/cloud/services/shared"

// Scope is an interface that provides scope data needed for availability zone reconciliation.
type Scope interface {
	shared.IaaSClientScope
	shared.ReconcileScope
	SetZones(zones []string)
}

// Service implements availability zones reconciler.
type Service struct {
	scope Scope
}

// New returns Service from given scope.
func New(scope Scope) *Service {
	return &Service{
		scope: scope,
	}
}
