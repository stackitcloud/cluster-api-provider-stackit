/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package securitygroup

import iaas "github.com/stackitcloud/stackit-sdk-go/services/iaas/v2api"

// Direction specifies the traffic direction of a security group rule.
type Direction string

const (
	DirectionIngress Direction = "ingress"
	DirectionEgress  Direction = "egress"
)

// Protocol specifies the network protocol of a security group rule.
type Protocol string

const (
	ProtocolTCP  Protocol = "tcp"
	ProtocolUDP  Protocol = "udp"
	ProtocolICMP Protocol = "icmp"
)

// SecurityGroup describes a STACKIT security group.
type SecurityGroup struct {
	ID   string
	Name string
}

// Rule describes a desired STACKIT security group rule.
type Rule struct {
	Direction               Direction
	Protocol                Protocol
	PortMin                 int64
	PortMax                 int64
	IPRange                 string
	RemoteSecurityGroupID   string
	RemoteSecurityGroupTags map[string]string
}

// SecurityGroupSpec describes the desired state of a STACKIT security group and its rules.
type SecurityGroupSpec struct {
	ID             string
	Name           string
	Description    string
	Tags           map[string]string
	Rules          []Rule
	ServerIDs      []string
	ServerSelector map[string]string
	SetID          func(string)
}

// ClientScope is an interface that provides the IaaS client and target scope for security group operations.
type ClientScope interface {
	ProjectID() string
	Region() string
	IaaSClient() *iaas.APIClient
}

// Scope is an interface that provides scope data needed for security group reconciliation.
type Scope interface {
	ClientScope
	SecurityGroupSpecs() []SecurityGroupSpec
}

// Client provides direct security group operations against the STACKIT IaaS API.
type Client struct {
	iaasClient *iaas.APIClient
	projectID  string
	region     string
}

// NewClient returns a Client for the given scope.
func NewClient(scope ClientScope) *Client {
	return &Client{
		iaasClient: scope.IaaSClient(),
		projectID:  scope.ProjectID(),
		region:     scope.Region(),
	}
}

// Service implements security groups reconciler.
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
