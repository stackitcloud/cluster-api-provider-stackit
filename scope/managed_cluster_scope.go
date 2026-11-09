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

	skev2 "github.com/stackitcloud/stackit-sdk-go/services/ske/v2api"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	kerrors "k8s.io/apimachinery/pkg/util/errors"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	"sigs.k8s.io/cluster-api/util/patch"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	infrav1 "github.com/stackitcloud/cluster-api-provider-stackit/api/v1alpha1"
	skeservice "github.com/stackitcloud/cluster-api-provider-stackit/cloud/services/ske"
	"github.com/stackitcloud/cluster-api-provider-stackit/util"
)

// ManagedNodePoolEntry pairs a discovered StackitManagedNodePool with its
// resolved per-pool pause state (true when either the owning MachinePool or the
// StackitManagedNodePool itself carries the CAPI paused annotation).
type ManagedNodePoolEntry struct {
	NodePool *infrav1.StackitManagedNodePool
	Paused   bool
}

// ManagedClusterScope holds the per-reconcile state for a StackitManagedCluster
// and its associated child StackitManagedNodePools.
type ManagedClusterScope struct {
	Client                client.Client
	Cluster               *clusterv1.Cluster
	StackitManagedCluster *infrav1.StackitManagedCluster

	skeClient            skev2.DefaultAPI
	patchHelper          *patch.Helper
	nodePools            []ManagedNodePoolEntry
	nodePoolByName       map[string]*infrav1.StackitManagedNodePool
	nodePoolPatchHelpers map[string]*patch.Helper
}

// NewManagedClusterScope constructs a ManagedClusterScope and snapshots the
// original StackitManagedCluster so the patchHelper can compute a minimal patch.
func NewManagedClusterScope(
	k8sClient client.Client,
	cluster *clusterv1.Cluster,
	stackitManagedCluster *infrav1.StackitManagedCluster,
) (*ManagedClusterScope, error) {
	patchHelper, err := patch.NewHelper(stackitManagedCluster, k8sClient)
	if err != nil {
		return nil, err
	}
	return &ManagedClusterScope{
		Client:                k8sClient,
		Cluster:               cluster,
		StackitManagedCluster: stackitManagedCluster,
		patchHelper:           patchHelper,
	}, nil
}

// SetManagedNodePools registers the associated StackitManagedNodePools for this
// reconcile pass and snapshots a patch.Helper for each child so projected
// status changes can be persisted on scope close.
func (s *ManagedClusterScope) SetManagedNodePools(entries []ManagedNodePoolEntry) error {
	s.nodePools = entries
	s.nodePoolByName = make(map[string]*infrav1.StackitManagedNodePool, len(entries))
	s.nodePoolPatchHelpers = make(map[string]*patch.Helper, len(entries))
	for _, entry := range entries {
		np := entry.NodePool
		helper, err := patch.NewHelper(np, s.Client)
		if err != nil {
			return err
		}
		s.nodePoolByName[np.Name] = np
		s.nodePoolPatchHelpers[np.Name] = helper
	}
	return nil
}

// PatchObject writes back any metadata/spec/status changes to the
// StackitManagedCluster and any projected status changes to associated
// StackitManagedNodePools, refreshing patchHelper baselines after each patch.
func (s *ManagedClusterScope) PatchObject(ctx context.Context) error {
	var errs []error

	if err := s.patchHelper.Patch(ctx, s.StackitManagedCluster, patch.WithOwnedConditions{Conditions: []string{
		infrav1.ManagedClusterReadyCondition,
		clusterv1.PausedCondition,
	}}); err != nil {
		errs = append(errs, err)
	} else if patchHelper, err := patch.NewHelper(s.StackitManagedCluster, s.Client); err != nil {
		errs = append(errs, err)
	} else {
		s.patchHelper = patchHelper
	}

	for _, entry := range s.nodePools {
		np := entry.NodePool
		helper := s.nodePoolPatchHelpers[np.Name]
		if helper == nil {
			continue
		}
		if err := helper.Patch(ctx, np, patch.WithOwnedConditions{Conditions: []string{
			infrav1.ManagedNodePoolReadyCondition,
		}}); err != nil {
			if apierrors.IsNotFound(err) && !np.DeletionTimestamp.IsZero() {
				continue
			}
			errs = append(errs, err)
			continue
		}
		if nextHelper, err := patch.NewHelper(np, s.Client); err == nil {
			s.nodePoolPatchHelpers[np.Name] = nextHelper
		}
	}

	return kerrors.NewAggregate(errs)
}

func (s *ManagedClusterScope) SetConditions(status metav1.ConditionStatus, reason, message string, conditionTypes ...string) {
	util.SetConditions(&s.StackitManagedCluster.Status.Conditions, s.StackitManagedCluster.Generation, status, reason, message, conditionTypes...)
}

// SetReady marks the StackitManagedCluster as ready and records that initial
// infrastructure provisioning has completed.
func (s *ManagedClusterScope) SetReady() {
	s.StackitManagedCluster.Status.Ready = true
	s.StackitManagedCluster.Status.Initialization.Provisioned = new(true)
	s.SetConditions(metav1.ConditionTrue, "Available", "", infrav1.ManagedClusterReadyCondition)
}

// SetNotReady marks the StackitManagedCluster as not ready without resetting
// Initialization.Provisioned if the cluster was previously provisioned.
func (s *ManagedClusterScope) SetNotReady(reason, message string) {
	s.StackitManagedCluster.Status.Ready = false
	s.SetConditions(metav1.ConditionFalse, reason, message, infrav1.ManagedClusterReadyCondition)
}

// ManagedNodePools returns the Kubernetes-side state of all associated
// StackitManagedNodePools for the SKE service.
func (s *ManagedClusterScope) ManagedNodePools() []skeservice.ManagedNodePoolState {
	if len(s.nodePools) == 0 {
		return nil
	}
	out := make([]skeservice.ManagedNodePoolState, len(s.nodePools))
	for i, entry := range s.nodePools {
		np := entry.NodePool
		out[i] = skeservice.ManagedNodePoolState{
			ResourceName: np.Name,
			Desired:      skeservice.NodePoolDesiredState(np),
			Finalized:    controllerutil.ContainsFinalizer(np, infrav1.ManagedNodePoolFinalizer),
			Deleting:     !np.DeletionTimestamp.IsZero(),
			Paused:       entry.Paused,
		}
	}
	return out
}

// SetNodePoolReady marks an associated StackitManagedNodePool as ready and
// records sticky Initialization.Provisioned=true.
func (s *ManagedClusterScope) SetNodePoolReady(resourceName string) {
	np, ok := s.nodePoolByName[resourceName]
	if !ok || np == nil {
		return
	}
	np.Status.Ready = true
	np.Status.Initialization.Provisioned = new(true)
	util.SetConditions(&np.Status.Conditions, np.Generation, metav1.ConditionTrue, "Available", "", infrav1.ManagedNodePoolReadyCondition)
}

// SetNodePoolNotReady marks an associated StackitManagedNodePool as not ready
// while preserving sticky Initialization.Provisioned if already true.
func (s *ManagedClusterScope) SetNodePoolNotReady(resourceName, reason, message string) {
	np, ok := s.nodePoolByName[resourceName]
	if !ok || np == nil {
		return
	}
	np.Status.Ready = false
	util.SetConditions(&np.Status.Conditions, np.Generation, metav1.ConditionFalse, reason, message, infrav1.ManagedNodePoolReadyCondition)
}

func (s *ManagedClusterScope) SetSKEClient(skeClient skev2.DefaultAPI) {
	s.skeClient = skeClient
}

func (s *ManagedClusterScope) SKEClient() skev2.DefaultAPI {
	return s.skeClient
}

func (s *ManagedClusterScope) ProjectID() string {
	return s.StackitManagedCluster.Spec.ProjectID
}

func (s *ManagedClusterScope) Region() string {
	return s.StackitManagedCluster.Spec.Region
}

func (s *ManagedClusterScope) ClusterName() string {
	return s.StackitManagedCluster.Spec.ClusterName
}

func (s *ManagedClusterScope) KubernetesVersion() string {
	return s.StackitManagedCluster.Spec.Kubernetes.Version
}

func (s *ManagedClusterScope) NetworkID() string {
	if s.StackitManagedCluster.Spec.Network == nil {
		return ""
	}
	return s.StackitManagedCluster.Spec.Network.ID
}

func (s *ManagedClusterScope) Labels() map[string]string {
	return util.ManagedClusterTags(
		s.StackitManagedCluster.Name,
		s.StackitManagedCluster.Namespace,
		string(s.StackitManagedCluster.UID),
		s.StackitManagedCluster.Spec.Labels,
	)
}

func (s *ManagedClusterScope) OwnershipLabels() map[string]string {
	return util.ManagedClusterTags(
		s.StackitManagedCluster.Name,
		s.StackitManagedCluster.Namespace,
		string(s.StackitManagedCluster.UID),
		nil,
	)
}
