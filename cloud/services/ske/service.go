/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package ske

import (
	skev2 "github.com/stackitcloud/stackit-sdk-go/services/ske/v2api"
)

// ManagedNodePoolState represents the Kubernetes-side state of an associated
// StackitManagedNodePool passed into the SKE service.
type ManagedNodePoolState struct {
	ResourceName string
	Desired      ManagedNodePoolDesiredState
	Finalized    bool
	Deleting     bool
	Paused       bool
}

// Scope defines the cluster scope data and status callbacks required by the
// SKE service.
type Scope interface {
	ProjectID() string
	Region() string
	ClusterName() string
	KubernetesVersion() string
	NetworkID() string
	Labels() map[string]string
	OwnershipLabels() map[string]string
	SKEClient() skev2.DefaultAPI
	ManagedNodePools() []ManagedNodePoolState
	SetReady()
	SetNotReady(reason, message string)
	SetNodePoolReady(resourceName string)
	SetNodePoolNotReady(resourceName, reason, message string)
}

// Service reconciles SKE clusters and embedded node pools for a
// StackitManagedCluster.
type Service struct {
	scope     Scope
	skeClient skev2.DefaultAPI
}

// New returns a new SKE Service from the given scope.
func New(scope Scope) *Service {
	return &Service{
		scope:     scope,
		skeClient: scope.SKEClient(),
	}
}
