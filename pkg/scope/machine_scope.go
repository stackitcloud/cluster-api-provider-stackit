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

	"sigs.k8s.io/controller-runtime/pkg/client"

	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	"sigs.k8s.io/cluster-api/util/patch"

	infrav1 "github.com/stackitcloud/cluster-api-provider-stackit/api/v1alpha1"
)

// MachineScope holds the per-reconcile state for a StackitMachine.
type MachineScope struct {
	Client         client.Client
	Cluster        *clusterv1.Cluster
	Machine        *clusterv1.Machine
	StackitCluster *infrav1.StackitCluster
	StackitMachine *infrav1.StackitMachine

	patchHelper *patch.Helper
}

// NewMachineScope constructs a MachineScope and snapshots the original
// StackitMachine for patching.
func NewMachineScope(
	k8sClient client.Client,
	cluster *clusterv1.Cluster,
	machine *clusterv1.Machine,
	stackitCluster *infrav1.StackitCluster,
	stackitMachine *infrav1.StackitMachine,
) (*MachineScope, error) {
	ph, err := patch.NewHelper(stackitMachine, k8sClient)
	if err != nil {
		return nil, err
	}
	return &MachineScope{
		Client:         k8sClient,
		Cluster:        cluster,
		Machine:        machine,
		StackitCluster: stackitCluster,
		StackitMachine: stackitMachine,
		patchHelper:    ph,
	}, nil
}

// PatchObject writes back any spec/status changes to the StackitMachine.
func (s *MachineScope) PatchObject(ctx context.Context) error {
	return s.patchHelper.Patch(ctx, s.StackitMachine, patch.WithOwnedConditions{Conditions: []string{
		infrav1.MachineReadyCondition,
		infrav1.MachineBootstrapReadyCondition,
		infrav1.MachineCredentialsReadyCondition,
		infrav1.MachineInstanceReadyCondition,
		clusterv1.PausedCondition,
	}})
}
