/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package server

import (
	"errors"

	"github.com/stackitcloud/cluster-api-provider-stackit/cloud/services/shared"
)

// ErrServerGone indicates that an already-provisioned server is missing in
// STACKIT and must not be recreated with stale bootstrap data.
var ErrServerGone = errors.New("server gone")

// IsServerGone reports whether err wraps ErrServerGone.
func IsServerGone(err error) bool {
	return errors.Is(err, ErrServerGone)
}

// Server describes a STACKIT compute instance in provider-neutral terms.
type Server struct {
	ID               string
	Name             string
	State            string
	PowerStatus      string
	AvailabilityZone string
	Addresses        []Address
}

// Address is an IP or DNS endpoint of a Server.
type Address struct {
	Type    string
	Address string
}

// RootVolume describes the root disk of a VM.
type RootVolume struct {
	SizeGiB             int
	PerformanceClass    string
	DeleteOnTermination bool
}

// ServerSpec describes the desired state of a STACKIT server.
type ServerSpec struct {
	ID               string
	Name             string
	ImageID          string
	MachineType      string
	AvailabilityZone string
	SSHKeyName       string
	NetworkID        string
	SecurityGroups   []string
	UserData         []byte
	Tags             map[string]string
	RootVolume       RootVolume
	DisallowRecreate bool
	SetServer        func(server *Server, created bool)
	ClearServer      func()
}

// ClientScope is an interface that provides the IaaS client and target scope for server operations.
type ClientScope = shared.IaaSClientScope

// Scope is an interface that provides scope data needed for server reconciliation.
type Scope interface {
	ClientScope
	ServerSpec() ServerSpec
}

// Client provides direct server operations against the STACKIT IaaS API.
type Client struct {
	shared.IaaSScope
}

// NewClient returns a Client for the given scope.
func NewClient(scope ClientScope) *Client {
	return &Client{IaaSScope: shared.NewIaaSScope(scope)}
}

// Service implements server reconciler.
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
