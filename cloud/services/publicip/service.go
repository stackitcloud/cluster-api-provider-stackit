/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package publicip

import (
	"context"

	iaas "github.com/stackitcloud/stackit-sdk-go/services/iaas/v2api"

	infrav1 "github.com/stackitcloud/cluster-api-provider-stackit/api/v1alpha1"
	"github.com/stackitcloud/cluster-api-provider-stackit/cloud/services/shared"
)

// PublicIP describes a STACKIT public IP resource.
type PublicIP struct {
	ID                 string
	IP                 string
	NetworkInterfaceID string
}

// Client defines the cloud API operations required by the public IP service.
type Client interface {
	FindServerByTags(ctx context.Context, tags map[string]string) (*shared.Server, error)
}

// Scope is an interface that provides cluster scope data needed for public IP reconciliation.
type Scope[C Client] interface {
	scopeData
	Cloud() C
}

type scopeData interface {
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
	scope      scopeData
	client     Client
	iaasClient *iaas.APIClient
}

// New returns Service from given scope.
func New[C Client](scope Scope[C]) *Service {
	return &Service{
		scope:      scope,
		client:     scope.Cloud(),
		iaasClient: scope.IaaSClient(),
	}
}
