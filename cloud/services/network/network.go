/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package network

import (
	"context"

	infrav1 "github.com/stackitcloud/cluster-api-provider-stackit/api/v1alpha1"
	"github.com/stackitcloud/cluster-api-provider-stackit/cloud"
	"github.com/stackitcloud/cluster-api-provider-stackit/util"
)

const (
	// DefaultNetworkPrefix defines the default CIDR allocated for a managed cluster network.
	DefaultNetworkPrefix = "10.0.0.0/24"
)

func NetworkTags(stackitCluster *infrav1.StackitCluster) map[string]string {
	return util.ClusterTags(stackitCluster.Name, stackitCluster.Namespace, stackitCluster.Spec.AdditionalLabels)
}

func Input(stackitCluster *infrav1.StackitCluster) cloud.CreateNetworkInput {
	return cloud.CreateNetworkInput{
		Name:       stackitCluster.Name + "-network",
		IPv4Prefix: DefaultNetworkPrefix,
		Tags:       NetworkTags(stackitCluster),
	}
}

// ResolveManagedNetworkID finds the managed network ID to delete during cluster teardown.
// It returns empty string if the cluster is in BYO mode or if no managed network is found.
// Neither spec nor status is a trustworthy record of what exists in the cloud, so if status
// was lost, it falls back to looking up the network by cluster tags.
func ResolveManagedNetworkID(
	ctx context.Context,
	cloudClient cloud.Client,
	stackitCluster *infrav1.StackitCluster,
) (string, error) {
	// If spec.network.id is set, the network is BYO and must NEVER be deleted.
	if stackitCluster.Spec.Network.ID != "" {
		return "", nil
	}

	if stackitCluster.Status.Network.ID != "" {
		return stackitCluster.Status.Network.ID, nil
	}

	network, err := cloudClient.FindNetworkByTags(ctx, NetworkTags(stackitCluster))
	if err != nil {
		if cloud.IsNotFound(err) {
			return "", nil
		}
		return "", err
	}
	return network.ID, nil
}
