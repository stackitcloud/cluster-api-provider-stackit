/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package helpers

import (
	"context"
	"fmt"

	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"

	infrav1 "github.com/stackitcloud/cluster-api-provider-stackit/api/v1alpha1"
	"github.com/stackitcloud/cluster-api-provider-stackit/pkg/cloud"
	"github.com/stackitcloud/cluster-api-provider-stackit/pkg/util"
)

const defaultAPIServerPort int32 = 6443

func APIServerLoadBalancerTags(stackitCluster *infrav1.StackitCluster) map[string]string {
	return util.ClusterTags(stackitCluster.Name, stackitCluster.Namespace, stackitCluster.Spec.AdditionalLabels)
}

func APIServerLoadBalancerInput(
	stackitCluster *infrav1.StackitCluster,
	targets []cloud.LoadBalancerTargetInput,
) cloud.LoadBalancerInput {
	return cloud.LoadBalancerInput{
		Name:      stackitCluster.Name + "-apiserver",
		ProjectID: stackitCluster.Spec.ProjectID,
		Region:    stackitCluster.Spec.Region,
		NetworkID: stackitCluster.Spec.Network.ID,
		Port:      defaultAPIServerPort,
		Tags:      APIServerLoadBalancerTags(stackitCluster),
		Targets:   targets,
	}
}

func BootstrapAPIServerLoadBalancerTarget(ip string) cloud.LoadBalancerTargetInput {
	return cloud.LoadBalancerTargetInput{
		Name: "capi-bootstrap-placeholder",
		IP:   ip,
		Port: defaultAPIServerPort,
	}
}

func APIServerLoadBalancerTargetForMachine(
	machineName string,
	addresses []cloud.Address,
) (cloud.LoadBalancerTargetInput, error) {
	ip := FirstInternalIP(addresses)
	if ip == "" {
		return cloud.LoadBalancerTargetInput{}, fmt.Errorf("%w: server has no internal IP address", cloud.ErrTransient)
	}

	return cloud.LoadBalancerTargetInput{
		Name: machineName,
		IP:   ip,
		Port: defaultAPIServerPort,
	}, nil
}

func ResolveAPIServerLoadBalancerID(
	ctx context.Context,
	cloudClient cloud.Client,
	stackitCluster *infrav1.StackitCluster,
) (string, error) {
	if stackitCluster.Status.APIServerLoadBalancerID != "" {
		return stackitCluster.Status.APIServerLoadBalancerID, nil
	}

	loadBalancers, err := cloudClient.ListAPIServerLoadBalancersByTags(ctx, APIServerLoadBalancerTags(stackitCluster))
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

func EnsureAPIServerLoadBalancerForMachine(
	ctx context.Context,
	cloudClient cloud.Client,
	stackitCluster *infrav1.StackitCluster,
	machineName string,
	addresses []cloud.Address,
) (string, error) {
	loadBalancerID, err := ResolveAPIServerLoadBalancerID(ctx, cloudClient, stackitCluster)
	if err != nil {
		return "", err
	}
	if loadBalancerID != "" {
		return loadBalancerID, nil
	}

	target, err := APIServerLoadBalancerTargetForMachine(machineName, addresses)
	if err != nil {
		return "", err
	}

	loadBalancer, err := cloudClient.EnsureAPIServerLoadBalancer(
		ctx,
		APIServerLoadBalancerInput(stackitCluster, []cloud.LoadBalancerTargetInput{target}),
	)
	if err != nil {
		return "", err
	}
	if loadBalancer == nil || loadBalancer.ID == "" {
		return "", fmt.Errorf("%w: API server load balancer ID is empty", cloud.ErrTransient)
	}
	return loadBalancer.ID, nil
}

func FirstInternalIP(addresses []cloud.Address) string {
	for _, address := range addresses {
		if address.Type == string(clusterv1.MachineInternalIP) && address.Address != "" {
			return address.Address
		}
	}
	return ""
}
