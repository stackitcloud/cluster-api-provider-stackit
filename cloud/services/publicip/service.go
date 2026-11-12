/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package publicip

import (
	iaas "github.com/stackitcloud/stackit-sdk-go/services/iaas/v2api"

	infrav1 "github.com/stackitcloud/cluster-api-provider-stackit/api/v1alpha1"
)

// PublicIP describes a STACKIT public IP resource.
type PublicIP struct {
	ID                 string
	IP                 string
	NetworkInterfaceID string
}

// Scope is an interface that provides cluster scope data needed for public IP reconciliation.
type Scope interface {
	ProjectID() string
	Region() string
	IaaSClient() *iaas.APIClient
	BastionSpec() infrav1.StackitBastionSpec
	BastionStatus() infrav1.StackitBastionStatus
	BastionTags() map[string]string
	SetBastionPublicIP(id, ip string)
}

// Service implements public IPs reconciler.
type Service struct {
	scope      Scope
	iaasClient *iaas.APIClient
}

// New returns Service from given scope.
func New(scope Scope) *Service {
	return &Service{
		scope:      scope,
		iaasClient: scope.IaaSClient(),
	}
}
