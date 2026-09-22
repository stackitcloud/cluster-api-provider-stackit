/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package loadbalancer

import (
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"

	"github.com/stackitcloud/cluster-api-provider-stackit/cloud"
)

func machineWithAddresses(name string, addresses ...clusterv1.MachineAddress) *clusterv1.Machine {
	return &clusterv1.Machine{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Status:     clusterv1.MachineStatus{Addresses: addresses},
	}
}

func internalIP(ip string) clusterv1.MachineAddress {
	return clusterv1.MachineAddress{Type: clusterv1.MachineInternalIP, Address: ip}
}

func TestAPIServerTargets(t *testing.T) {
	tests := []struct {
		name     string
		machines []*clusterv1.Machine
		want     []cloud.LoadBalancerTargetInput
	}{
		{
			name: "sorts targets by machine name",
			machines: []*clusterv1.Machine{
				machineWithAddresses("cp-2", internalIP("10.0.0.12")),
				machineWithAddresses("cp-0", internalIP("10.0.0.10")),
				machineWithAddresses("cp-1", internalIP("10.0.0.11")),
			},
			want: []cloud.LoadBalancerTargetInput{
				{Name: "cp-0", IP: "10.0.0.10"},
				{Name: "cp-1", IP: "10.0.0.11"},
				{Name: "cp-2", IP: "10.0.0.12"},
			},
		},
		{
			name: "skips machines that have no internal IP yet",
			machines: []*clusterv1.Machine{
				machineWithAddresses("cp-0", internalIP("10.0.0.10")),
				machineWithAddresses("cp-1"),
				machineWithAddresses("cp-2", clusterv1.MachineAddress{
					Type:    clusterv1.MachineExternalIP,
					Address: "203.0.113.10",
				}),
			},
			want: []cloud.LoadBalancerTargetInput{{Name: "cp-0", IP: "10.0.0.10"}},
		},
		{
			name: "drops a duplicate IP address",
			machines: []*clusterv1.Machine{
				machineWithAddresses("cp-1", internalIP("10.0.0.10")),
				machineWithAddresses("cp-0", internalIP("10.0.0.10")),
			},
			want: []cloud.LoadBalancerTargetInput{{Name: "cp-0", IP: "10.0.0.10"}},
		},
		{
			name:     "falls back to the bootstrap placeholder without machines",
			machines: nil,
			want:     []cloud.LoadBalancerTargetInput{{Name: "capi-bootstrap-placeholder", IP: "10.0.0.10"}},
		},
		{
			name:     "tolerates nil entries",
			machines: []*clusterv1.Machine{nil, machineWithAddresses("cp-0", internalIP("10.0.0.11"))},
			want:     []cloud.LoadBalancerTargetInput{{Name: "cp-0", IP: "10.0.0.11"}},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := APIServerTargets(test.machines, "10.0.0.10")
			if len(got) != len(test.want) {
				t.Fatalf("APIServerTargets() = %#v, want %#v", got, test.want)
			}
			for i := range got {
				if got[i] != test.want[i] {
					t.Errorf("APIServerTargets()[%d] = %#v, want %#v", i, got[i], test.want[i])
				}
			}
		})
	}
}

func TestTargetName(t *testing.T) {
	longName := strings.Repeat("a", 70)

	tests := []struct {
		name        string
		machineName string
		want        string
	}{
		{
			name:        "leaves a valid name untouched",
			machineName: "cluster-control-plane-abcde",
			want:        "cluster-control-plane-abcde",
		},
		{
			name:        "replaces characters the load balancer API rejects",
			machineName: "foo.bar-control-plane-abcde",
			// SHA-256 of the machine name, first seven hex characters.
			want: "foo-bar-control-plane-abcde-" + digestPrefix("foo.bar-control-plane-abcde"),
		},
		{
			name:        "collapses a run of invalid characters and trims the edges",
			machineName: ".foo..bar.",
			want:        "foo-bar-" + digestPrefix(".foo..bar."),
		},
		{
			name:        "shortens an overlong name",
			machineName: longName,
			want:        strings.Repeat("a", 55) + "-" + digestPrefix(longName),
		},
		{
			name:        "falls back to the digest when nothing survives sanitizing",
			machineName: "...",
			want:        digestPrefix("..."),
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := targetName(test.machineName)
			if got != test.want {
				t.Fatalf("targetName(%q) = %q, want %q", test.machineName, got, test.want)
			}
			// One bad name makes the API reject the whole pool.
			if !validTargetName.MatchString(got) {
				t.Errorf("targetName(%q) = %q, which the load balancer API rejects", test.machineName, got)
			}
		})
	}
}

// validTargetName is the pattern STACKIT enforces on a target display name.
var validTargetName = regexp.MustCompile(`^[0-9a-zA-Z](?:(?:[0-9a-zA-Z]|-){0,61}[0-9a-zA-Z])?$`)

func TestTargetNameKeepsCollidingMachinesApart(t *testing.T) {
	// Both names sanitize to the same string; only the digest tells them apart.
	first := targetName("foo.bar-a")
	second := targetName("foo-bar-a")
	if first == second {
		t.Fatalf("targetName() returned %q for both machine names", first)
	}
}

// digestPrefix recomputes the expected suffix so that the expectations do not
// derive from the code under test.
func digestPrefix(machineName string) string {
	digest := sha256.Sum256([]byte(machineName))
	return hex.EncodeToString(digest[:])[:targetNameDigestLength]
}
