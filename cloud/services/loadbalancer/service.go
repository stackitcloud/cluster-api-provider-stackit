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

	lb "github.com/stackitcloud/stackit-sdk-go/services/loadbalancer/v2api"
)

const (
	// DefaultAPIServerPort is the default TCP port for the Kubernetes API server.
	DefaultAPIServerPort int32 = 6443

	defaultBootstrapTargetIP = "10.0.0.1"
	apiserverTargetPoolName  = "apiserver"
	apiserverListenerName    = "apiserver"
	bootstrapTargetName      = "capi-bootstrap-placeholder"
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

// ClientScope is an interface that provides the LoadBalancer client and target scope for load balancer operations.
type ClientScope interface {
	ProjectID() string
	Region() string
	LoadBalancerClient() *lb.APIClient
}

// Scope is an interface that provides scope data needed for load balancer reconciliation.
type Scope interface {
	ClientScope
	LoadBalancerSpec() LoadBalancerSpec
}

// Client provides direct load balancer operations against the STACKIT Load Balancer API.
type Client struct {
	lbClient  *lb.APIClient
	projectID string
	region    string
}

// NewClient returns a Client for the given scope.
func NewClient(scope ClientScope) *Client {
	return &Client{
		lbClient:  scope.LoadBalancerClient(),
		projectID: scope.ProjectID(),
		region:    scope.Region(),
	}
}

// Service implements load balancer reconciler.
type Service struct {
	scope  Scope
	client *Client
}

// New returns Service from given scope.
func New(scope Scope) *Service {
	return &Service{
		scope:  scope,
		client: NewClient(scope),
	}
}
