/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package loadbalancer

import (
	"errors"

	"github.com/stackitcloud/cluster-api-provider-stackit/cloud/services/shared"
)

const (
	// DefaultAPIServerPort is the default TCP port for the Kubernetes API server.
	DefaultAPIServerPort int32 = 6443

	// DefaultBootstrapTargetIP is the fallback IPv4 address used for the
	// bootstrap placeholder target when no network prefix can be derived.
	DefaultBootstrapTargetIP = "10.0.0.1"

	apiserverTargetPoolName = "apiserver"
	apiserverListenerName   = "apiserver"
	bootstrapTargetName     = "capi-bootstrap-placeholder"
	listenerProtocolTCP     = "PROTOCOL_TCP"
)

// errTargetPoolUpdate indicates that the load balancer was created/resolved
// successfully, but updating its target pool failed.
var errTargetPoolUpdate = errors.New("load balancer target pool update failed")

// IsTargetPoolUpdateError reports whether err wraps errTargetPoolUpdate.
func IsTargetPoolUpdateError(err error) bool {
	return errors.Is(err, errTargetPoolUpdate)
}

// LoadBalancer describes an API-server load balancer.
type LoadBalancer struct {
	ID   string
	Name string
	IP   string
	Port int32
}

// Target describes a VM target in the API-server load balancer target pool.
type Target struct {
	Name string
	IP   string
}

// LoadBalancerSpec describes the desired state of a STACKIT API server load balancer.
type LoadBalancerSpec struct {
	ID          string
	Name        string
	NetworkID   string
	Port        int32
	Tags        map[string]string
	Targets     []Target
	SeedTargets []Target
	SetID       func(id string)
	SetAddress  func(ip string, port int32)
}

// Scope is an interface that provides scope data needed for load balancer reconciliation.
type Scope interface {
	shared.LoadBalancerClientScope
	LoadBalancerSpec() LoadBalancerSpec
}

// Service implements load balancer reconciler.
type Service struct {
	scope Scope
}

// New returns Service from given scope.
func New(scope Scope) *Service {
	return &Service{
		scope: scope,
	}
}
