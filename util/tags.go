/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

// Package util holds small, reusable helpers shared by the controllers.
package util

// Label / tag keys applied to provider-managed cloud resources. They mirror
// the canonical CAPI labels so operators can identify resources by cluster
// and machine.
const (
	LabelClusterName      = "cluster.x-k8s.io/cluster-name"
	LabelClusterNamespace = "cluster.x-k8s.io/cluster-namespace"
	LabelMachineName      = "cluster.x-k8s.io/machine-name"
	LabelMachineUID       = "cluster.x-k8s.io/machine-uid"
	LabelManagedBy        = "cluster.x-k8s.io/managed-by"
	LabelProviderManaged  = "cluster-api-provider-stackit/managed"
	LabelResourceRole     = "cluster-api-provider-stackit/resource-role"
	LabelE2E              = "cluster-api-provider-stackit/e2e"
	LabelTestID           = "cluster-api-provider-stackit/test-id"

	// ManagedByValue identifies resources managed by this provider.
	ManagedByValue       = "cluster-api-provider-stackit"
	ProviderManagedValue = "true"
	ResourceRoleBastion  = "bastion"
	ResourceRoleNodeSSH  = "node-ssh"
	E2EValue             = "true"
)

// ClusterTags returns the canonical tags applied to cluster-wide cloud
// resources (e.g. the API server load balancer). additionalLabels is merged in
// last so the provider's own tags cannot be overwritten.
func ClusterTags(clusterName, clusterNamespace string, additionalLabels map[string]string) map[string]string {
	out := map[string]string{
		LabelClusterName:      clusterName,
		LabelClusterNamespace: clusterNamespace,
		LabelManagedBy:        ManagedByValue,
		LabelProviderManaged:  ProviderManagedValue,
	}
	for k, v := range additionalLabels {
		if _, isReserved := out[k]; isReserved {
			continue
		}
		out[k] = v
	}
	return out
}

// MachineTags returns the canonical tags applied to per-machine cloud
// resources (e.g. VMs). additionalLabels is merged in last so the provider's
// own tags cannot be overwritten.
func MachineTags(
	clusterName, clusterNamespace, machineName, machineUID string,
	additionalLabels map[string]string,
) map[string]string {
	out := map[string]string{
		LabelClusterName:      clusterName,
		LabelClusterNamespace: clusterNamespace,
		LabelMachineName:      machineName,
		LabelMachineUID:       machineUID,
		LabelManagedBy:        ManagedByValue,
		LabelProviderManaged:  ProviderManagedValue,
	}
	for k, v := range additionalLabels {
		if _, isReserved := out[k]; isReserved {
			continue
		}
		out[k] = v
	}
	return out
}
