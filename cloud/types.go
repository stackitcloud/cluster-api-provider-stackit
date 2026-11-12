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

import (
	"github.com/stackitcloud/cluster-api-provider-stackit/cloud/services/loadbalancer"
	"github.com/stackitcloud/cluster-api-provider-stackit/cloud/services/network"
	"github.com/stackitcloud/cluster-api-provider-stackit/cloud/services/publicip"
	"github.com/stackitcloud/cluster-api-provider-stackit/cloud/services/securitygroup"
	"github.com/stackitcloud/cluster-api-provider-stackit/cloud/services/server"
)

// Server describes a STACKIT compute instance in provider-neutral terms.
type Server = server.Server

// Address is an IP or DNS endpoint of a Server.
type Address = server.Address

// Network is an existing STACKIT virtual network referenced by the provider.
type Network = network.Network

// LoadBalancer describes an API-server load balancer.
type LoadBalancer = loadbalancer.LoadBalancer

// PublicIP describes a STACKIT public IP resource.
type PublicIP = publicip.PublicIP

// SecurityGroup describes a STACKIT security group.
type SecurityGroup = securitygroup.SecurityGroup
