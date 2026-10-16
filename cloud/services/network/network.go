/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package network

import (
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
