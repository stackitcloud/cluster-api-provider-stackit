/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package bastion

import (
	"context"
	"crypto/sha256"
	"fmt"

	"github.com/stackitcloud/cluster-api-provider-stackit/cloud/services/publicip"
	"github.com/stackitcloud/cluster-api-provider-stackit/cloud/services/securitygroup"
	"github.com/stackitcloud/cluster-api-provider-stackit/cloud/services/server"
)

// DisabledReason is read back to decide whether the disabled cleanup already
// ran, so it must not drift.
const DisabledReason = "Skipped"

// Scope is an interface that provides scope data needed for bastion reconciliation.
type Scope interface {
	server.Scope
	securitygroup.Scope
	publicip.Scope
	BastionEnabled() bool
	ResolveBastionCloudInit(ctx context.Context) ([]byte, error)
	SetBastionCloudInit(cloudInit []byte, cloudInitHash string)
	BastionServerID() string
	BastionCloudInitHash() string
	BastionPublicIP() string
	HasBastionStatus() bool
}

// Service implements the bastion reconciler.
type Service struct {
	scope Scope
}

// New returns Service from given scope.
func New(scope Scope) *Service {
	return &Service{
		scope: scope,
	}
}

// CloudInitHash returns the SHA-256 hex digest of cloudInit, or an empty string
// when cloudInit is empty.
func CloudInitHash(cloudInit []byte) string {
	if len(cloudInit) == 0 {
		return ""
	}
	return fmt.Sprintf("%x", sha256.Sum256(cloudInit))
}
