/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

// Package cloud defines the abstraction layer between the controllers and the
// STACKIT SDK. Controllers must not reach into the SDK directly; all calls go
// through the Client interface in this package.
package cloud

// Server describes a STACKIT compute instance in provider-neutral terms.
type Server struct {
	ID         string
	Name       string
	State      string
	ProviderID string
	Addresses  []Address
}

// Address is an IP or DNS endpoint of a Server.
type Address struct {
	Type    string
	Address string
}

// Network is an existing STACKIT virtual network referenced by the provider.
type Network struct {
	ID           string
	Name         string
	IPv4Prefixes []string
}

// LoadBalancer describes an API-server load balancer.
type LoadBalancer struct {
	ID      string
	Name    string
	IP      string
	DNSName string
	Port    int32
}

// PublicIP describes a STACKIT public IP resource.
type PublicIP struct {
	ID                 string
	IP                 string
	NetworkInterfaceID string
}

// SecurityGroup describes a STACKIT security group.
type SecurityGroup struct {
	ID   string
	Name string
}

// Bastion describes the provider-managed SSH bastion resources.
type Bastion struct {
	ServerID        string
	ServerState     string
	PublicIPID      string
	PublicIP        string
	SecurityGroupID string
}

// CreateServerInput holds all parameters required to create a new VM.
type CreateServerInput struct {
	Name             string
	ProjectID        string
	Region           string
	ImageID          string
	MachineType      string
	AvailabilityZone string
	SSHKeyName       string
	NetworkID        string
	SecurityGroups   []string
	UserData         []byte
	Tags             map[string]string
	RootVolume       RootVolumeInput
}

// BastionInput holds all parameters required to ensure a bastion host.
type BastionInput struct {
	Name         string
	ProjectID    string
	Region       string
	NetworkID    string
	ImageID      string
	MachineType  string
	SSHKeyName   string
	AllowedCIDRs []string
	Tags         map[string]string
	RootVolume   RootVolumeInput
	CloudInit    []byte
}

// NodeSSHAccessInput holds parameters required to allow SSH from the bastion
// security group to cluster nodes.
type NodeSSHAccessInput struct {
	Name                   string
	ServerID               string
	BastionSecurityGroupID string
	Tags                   map[string]string
}

// RootVolumeInput describes the root disk of a VM.
type RootVolumeInput struct {
	SizeGiB             int
	PerformanceClass    string
	DeleteOnTermination bool
}

// LoadBalancerInput holds all parameters required to ensure an API-server LB.
type LoadBalancerInput struct {
	Name      string
	ProjectID string
	Region    string
	NetworkID string
	Tags      map[string]string
	Port      int32
	Targets   []LoadBalancerTargetInput
}

// LoadBalancerTargetInput describes a VM target in the API-server load
// balancer target pool.
type LoadBalancerTargetInput struct {
	LoadBalancerID string
	Name           string
	IP             string
	Port           int32
}
