/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package loadbalancer

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"slices"
	"strings"

	lb "github.com/stackitcloud/stackit-sdk-go/services/loadbalancer/v2api"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"

	"github.com/stackitcloud/cluster-api-provider-stackit/cloud/services/shared"
)

const (
	// maxTargetNameLength is the RFC 1123 label length, which STACKIT enforces on
	// a target display name.
	maxTargetNameLength = 63

	targetNameDigestLength = 7
)

// BootstrapTarget seeds a new load balancer; STACKIT rejects an empty pool.
func BootstrapTarget(ip string) Target {
	if ip == "" {
		ip = DefaultBootstrapTargetIP
	}
	return Target{
		Name: bootstrapTargetName,
		IP:   ip,
	}
}

// APIServerTargets builds the desired API server target pool, sorted by machine
// name so the pool can be compared without spurious updates. The result is empty
// while no control plane machine has an internal IP yet.
func APIServerTargets(machines []*clusterv1.Machine) []Target {
	sorted := make([]*clusterv1.Machine, 0, len(machines))
	for _, machine := range machines {
		if machine != nil {
			sorted = append(sorted, machine)
		}
	}
	slices.SortFunc(sorted, func(a, b *clusterv1.Machine) int {
		return strings.Compare(a.Name, b.Name)
	})

	targets := make([]Target, 0, len(sorted))
	// A target IP must be unique within the pool, and a replacement machine can
	// transiently report the IP of the one it replaces.
	seen := make(map[string]struct{}, len(sorted))
	for _, machine := range sorted {
		ip := firstInternalIP(machine.Status.Addresses)
		if ip == "" {
			continue
		}
		if _, duplicate := seen[ip]; duplicate {
			continue
		}
		seen[ip] = struct{}{}
		targets = append(targets, Target{Name: targetName(machine.Name), IP: ip})
	}
	return targets
}

// Labels converts tag keys containing "/" into "." to satisfy STACKIT Load Balancer label rules.
func Labels(tags map[string]string) map[string]string {
	out := make(map[string]string, len(tags))
	for k, v := range tags {
		out[strings.ReplaceAll(k, "/", ".")] = v
	}
	return out
}

// targetName maps a machine name, an RFC 1123 subdomain, onto a STACKIT target
// display name, an RFC 1123 label. A name that already is a label is returned
// unchanged; any other carries a digest so that two machines cannot collapse
// onto one target.
func targetName(machineName string) string {
	if len(machineName) <= maxTargetNameLength && !strings.Contains(machineName, ".") {
		return machineName
	}

	sum := sha256.Sum256([]byte(machineName))
	suffix := "-" + hex.EncodeToString(sum[:])[:targetNameDigestLength]
	name := strings.ReplaceAll(machineName, ".", "-")
	if len(name) > maxTargetNameLength-len(suffix) {
		name = strings.TrimRight(name[:maxTargetNameLength-len(suffix)], "-")
	}
	return name + suffix
}

func firstInternalIP(addresses []clusterv1.MachineAddress) string {
	for _, address := range addresses {
		if address.Type == clusterv1.MachineInternalIP && address.Address != "" {
			return address.Address
		}
	}
	return ""
}

// setTargets replaces the contents of the API-server target pool. STACKIT rejects an empty pool.
func (s *Service) setTargets(
	ctx context.Context,
	loadBalancerID string,
	port int32,
	targets []Target,
) error {
	if loadBalancerID == "" || port <= 0 {
		return fmt.Errorf("%w: load balancer ID and target port are required", shared.ErrInvalidInput)
	}
	// STACKIT NLB target pools must contain at least one target.
	if len(targets) == 0 {
		return fmt.Errorf("%w: at least one target is required", shared.ErrInvalidInput)
	}
	desired := make([]lb.Target, 0, len(targets))
	for _, targetInput := range targets {
		if targetInput.Name == "" || targetInput.IP == "" {
			return fmt.Errorf("%w: target name and target IP are required", shared.ErrInvalidInput)
		}
		target := lb.NewTarget()
		target.SetDisplayName(targetInput.Name)
		target.SetIp(targetInput.IP)
		desired = append(desired, *target)
	}

	loadBalancer, err := s.scope.LoadBalancerClient().DefaultAPI.GetLoadBalancer(ctx, s.scope.ProjectID(), s.scope.Region(), loadBalancerID).Execute()
	if err != nil {
		return shared.ClassifySDKError("get load balancer", err)
	}
	targetPool := apiServerTargetPool(loadBalancer)
	if targetPool == nil {
		return fmt.Errorf(
			"%w: load balancer %q has no %q target pool",
			shared.ErrNotFound,
			loadBalancerID,
			apiserverTargetPoolName,
		)
	}

	// Every cluster reconcile ends up here; skip the update call when the pool
	// already matches.
	if targetPool.GetTargetPort() == port && sameTargets(targetPool.GetTargets(), desired) {
		return nil
	}
	return s.updateAPIServerTargetPool(ctx, loadBalancerID, targetPool, desired, port)
}

func (s *Service) updateAPIServerTargetPool(
	ctx context.Context,
	loadBalancerID string,
	current *lb.TargetPool,
	targets []lb.Target,
	port int32,
) error {
	payload := lb.NewUpdateTargetPoolPayload()
	payload.SetName(apiserverTargetPoolName)
	payload.SetTargets(targets)
	if _, err := s.scope.LoadBalancerClient().DefaultAPI.
		UpdateTargetPool(ctx, s.scope.ProjectID(), s.scope.Region(), loadBalancerID, apiserverTargetPoolName).
		UpdateTargetPoolPayload(*payload).
		Execute(); err != nil {
		return shared.ClassifySDKError("update load balancer target pool", err)
	}
	return nil
}

func apiServerTargetPool(loadBalancer *lb.LoadBalancer) *lb.TargetPool {
	if loadBalancer == nil {
		return nil
	}
	for _, targetPool := range loadBalancer.GetTargetPools() {
		if targetPool.GetName() == apiserverTargetPoolName {
			return &targetPool
		}
	}
	return nil
}

func sameTargets(current, desired []lb.Target) bool {
	if len(current) != len(desired) {
		return false
	}
	return slices.Equal(sortedTargetKeys(current), sortedTargetKeys(desired))
}

type targetKey struct{ name, ip string }

func sortedTargetKeys(targets []lb.Target) []targetKey {
	keys := make([]targetKey, 0, len(targets))
	for _, target := range targets {
		keys = append(keys, targetKey{name: target.GetDisplayName(), ip: target.GetIp()})
	}
	slices.SortFunc(keys, func(a, b targetKey) int {
		return cmp.Or(strings.Compare(a.name, b.name), strings.Compare(a.ip, b.ip))
	})
	return keys
}
