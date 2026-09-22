/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package loadbalancer

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"slices"
	"strings"

	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"

	infrav1 "github.com/stackitcloud/cluster-api-provider-stackit/api/v1alpha1"
	"github.com/stackitcloud/cluster-api-provider-stackit/cloud"
	"github.com/stackitcloud/cluster-api-provider-stackit/util"
)

const (
	defaultAPIServerPort int32 = 6443

	// maxTargetNameLength is the STACKIT limit on a target display name.
	maxTargetNameLength = 63

	targetNameDigestLength = 7
)

func APIServerTags(stackitCluster *infrav1.StackitCluster) map[string]string {
	return util.ClusterTags(stackitCluster.Name, stackitCluster.Namespace, stackitCluster.Spec.AdditionalLabels)
}

func APIServerInput(
	stackitCluster *infrav1.StackitCluster,
	targets []cloud.LoadBalancerTargetInput,
) cloud.LoadBalancerInput {
	return cloud.LoadBalancerInput{
		Name:      stackitCluster.Name + "-apiserver",
		ProjectID: stackitCluster.Spec.ProjectID,
		Region:    stackitCluster.Spec.Region,
		NetworkID: stackitCluster.Spec.Network.ID,
		Port:      defaultAPIServerPort,
		Tags:      APIServerTags(stackitCluster),
		Targets:   targets,
	}
}

// BootstrapTarget seeds a new load balancer; STACKIT rejects an empty pool.
func BootstrapTarget(ip string) cloud.LoadBalancerTargetInput {
	return cloud.LoadBalancerTargetInput{
		Name: "capi-bootstrap-placeholder",
		IP:   ip,
	}
}

// APIServerTargets builds the desired API server target pool, sorted by machine
// name so the pool can be compared without spurious updates. The result is empty
// while no control plane machine has an internal IP yet.
func APIServerTargets(machines []*clusterv1.Machine) []cloud.LoadBalancerTargetInput {
	sorted := make([]*clusterv1.Machine, 0, len(machines))
	for _, machine := range machines {
		if machine != nil {
			sorted = append(sorted, machine)
		}
	}
	slices.SortFunc(sorted, func(a, b *clusterv1.Machine) int {
		return strings.Compare(a.Name, b.Name)
	})

	targets := make([]cloud.LoadBalancerTargetInput, 0, len(sorted))
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
		targets = append(targets, cloud.LoadBalancerTargetInput{Name: targetName(machine.Name), IP: ip})
	}
	return targets
}

// targetName turns a machine name into a valid STACKIT target display name:
// letters, digits and inner hyphens, at most 63 characters. A qualifying name is
// returned unchanged; any other carries a digest so that two machines cannot
// collapse onto one target.
func targetName(machineName string) string {
	var sanitized strings.Builder
	previousHyphen := false
	for _, r := range machineName {
		switch {
		case (r >= '0' && r <= '9') || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z'):
			sanitized.WriteRune(r)
			previousHyphen = false
		case !previousHyphen:
			sanitized.WriteByte('-')
			previousHyphen = true
		}
	}
	name := strings.Trim(sanitized.String(), "-")
	if name == machineName && len(name) <= maxTargetNameLength {
		return name
	}

	sum := sha256.Sum256([]byte(machineName))
	digest := hex.EncodeToString(sum[:])[:targetNameDigestLength]
	if name == "" {
		return digest
	}
	suffix := "-" + digest
	if len(name) > maxTargetNameLength-len(suffix) {
		name = strings.TrimRight(name[:maxTargetNameLength-len(suffix)], "-")
	}
	return name + suffix
}

func ResolveID(
	ctx context.Context,
	cloudClient cloud.Client,
	stackitCluster *infrav1.StackitCluster,
) (string, error) {
	if stackitCluster.Status.APIServerLoadBalancerID != "" {
		return stackitCluster.Status.APIServerLoadBalancerID, nil
	}

	loadBalancers, err := cloudClient.ListAPIServerLoadBalancersByTags(ctx, APIServerTags(stackitCluster))
	if err != nil {
		return "", err
	}
	if len(loadBalancers) == 0 {
		return "", nil
	}
	if len(loadBalancers) > 1 {
		return "", fmt.Errorf("multiple API server load balancers match cluster tags: %w", cloud.ErrConflict)
	}
	return loadBalancers[0].ID, nil
}

func firstInternalIP(addresses []clusterv1.MachineAddress) string {
	for _, address := range addresses {
		if address.Type == clusterv1.MachineInternalIP && address.Address != "" {
			return address.Address
		}
	}
	return ""
}
