/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package shared

// Server describes a STACKIT compute instance in provider-neutral terms.
type Server struct {
	ID               string
	Name             string
	State            string
	PowerStatus      string
	ProviderID       string
	AvailabilityZone string
	Addresses        []Address
}

// Address is an IP or DNS endpoint of a Server.
type Address struct {
	Type    string
	Address string
}
