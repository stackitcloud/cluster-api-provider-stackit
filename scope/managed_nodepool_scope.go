/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package scope

import (
	"context"

	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	"sigs.k8s.io/cluster-api/util/patch"
	"sigs.k8s.io/controller-runtime/pkg/client"

	infrav1 "github.com/stackitcloud/cluster-api-provider-stackit/api/v1alpha1"
)

// ManagedNodePoolScope holds the Kubernetes-only per-reconcile state for a
// StackitManagedNodePool.
//
// StackitManagedNodePoolReconciler owns metadata finalizers and the Paused
// condition only; cloud-derived status (Ready, Initialization.Provisioned, and
// the Ready condition) is projected by StackitManagedClusterReconciler.
type ManagedNodePoolScope struct {
	Client                 client.Client
	Cluster                *clusterv1.Cluster
	MachinePool            *clusterv1.MachinePool
	StackitManagedCluster  *infrav1.StackitManagedCluster
	StackitManagedNodePool *infrav1.StackitManagedNodePool

	patchHelper *patch.Helper
}

// NewManagedNodePoolScope constructs a ManagedNodePoolScope and snapshots the
// original StackitManagedNodePool for minimal patching.
func NewManagedNodePoolScope(
	k8sClient client.Client,
	cluster *clusterv1.Cluster,
	machinePool *clusterv1.MachinePool,
	managedCluster *infrav1.StackitManagedCluster,
	nodePool *infrav1.StackitManagedNodePool,
) (*ManagedNodePoolScope, error) {
	patchHelper, err := patch.NewHelper(nodePool, k8sClient)
	if err != nil {
		return nil, err
	}
	return &ManagedNodePoolScope{
		Client:                 k8sClient,
		Cluster:                cluster,
		MachinePool:            machinePool,
		StackitManagedCluster:  managedCluster,
		StackitManagedNodePool: nodePool,
		patchHelper:            patchHelper,
	}, nil
}

// PatchObject writes back metadata/finalizer and PausedCondition changes to the
// StackitManagedNodePool and refreshes the patchHelper baseline.
func (s *ManagedNodePoolScope) PatchObject(ctx context.Context) error {
	if err := s.patchHelper.Patch(ctx, s.StackitManagedNodePool, patch.WithOwnedConditions{Conditions: []string{
		clusterv1.PausedCondition,
	}}); err != nil {
		return err
	}
	patchHelper, err := patch.NewHelper(s.StackitManagedNodePool, s.Client)
	if err != nil {
		return err
	}
	s.patchHelper = patchHelper
	return nil
}
