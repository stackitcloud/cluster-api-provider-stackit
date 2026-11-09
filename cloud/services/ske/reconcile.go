/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package ske

import (
	"cmp"
	"context"
	"fmt"
	"os"
	"slices"
	"time"

	skev2 "github.com/stackitcloud/stackit-sdk-go/services/ske/v2api"
	ctrl "sigs.k8s.io/controller-runtime"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	infrav1 "github.com/stackitcloud/cluster-api-provider-stackit/api/v1alpha1"
	"github.com/stackitcloud/cluster-api-provider-stackit/cloud"
)

const (
	requeueAfter = time.Minute

	// BootstrapNodePoolName is the reserved SKE node-pool name used for the
	// provider-managed bootstrap/system fallback pool.
	BootstrapNodePoolName      = infrav1.ManagedNodePoolBootstrapName
	defaultNodePoolMachineType = "c2i.2"
	defaultNodePoolImageName   = "flatcar"
	// defaultNodePoolImageVersion is the fallback Flatcar image version used for
	// the provider bootstrap node pool when STACKIT_SKE_NODEPOOL_IMAGE_VERSION is
	// not set.
	// TODO(ske): This fallback version is time-sensitive as SKE rotates supported
	// Flatcar images; replace with a durable configuration or provider-options
	// selection strategy.
	defaultNodePoolImageVersion    = "4593.2.5-containerd2.1.9"
	defaultNodePoolImageVersionEnv = "STACKIT_SKE_NODEPOOL_IMAGE_VERSION"
	defaultNodePoolMinimum         = int32(1)
	defaultNodePoolMaximum         = int32(1)
	defaultNodePoolVolumeSize      = int32(20)
	defaultNodePoolVolumeType      = "storage_premium_perf1"

	ReasonAvailable                    = "Available"
	ReasonCreating                     = "Creating"
	ReasonProvisioning                 = "Provisioning"
	ReasonReconciling                  = "Reconciling"
	ReasonDeleting                     = "Deleting"
	ReasonDeleted                      = "Deleted"
	ReasonUnhealthy                    = "Unhealthy"
	ReasonHibernated                   = "Hibernated"
	ReasonUnknownState                 = "UnknownState"
	ReasonDuplicateNodePoolName        = "DuplicateNodePoolName"
	ReasonInvalidNodePoolConfiguration = "InvalidNodePoolConfiguration"
	ReasonClusterNotManaged            = "ClusterNotManaged"
)

// Reconcile ensures the SKE cluster and its desired nodepools[] configuration
// exist, evaluates cluster and node-pool lifecycle state from a single
// GetCluster call, and projects child statuses back into the scope.
func (s *Service) Reconcile(ctx context.Context) (ctrl.Result, error) {
	log := logf.FromContext(ctx)
	if s.skeClient == nil {
		err := fmt.Errorf("%w: SKE client is not configured", cloud.ErrInvalidInput)
		s.scope.SetNotReady("SKEClientError", err.Error())
		s.projectAllActiveNodePoolsNotReady("SKEClientError", err.Error())
		return ctrl.Result{}, err
	}

	if invalidReason, invalidMessage := s.validateChildNodePools(); invalidReason != "" {
		s.scope.SetNotReady(invalidReason, invalidMessage)
		return ctrl.Result{}, nil
	}

	cluster, err := s.skeClient.GetCluster(ctx, s.scope.ProjectID(), s.scope.Region(), s.scope.ClusterName()).Execute()
	if err != nil {
		err = cloud.ClassifySDKError("get SKE cluster", err)
		if !cloud.IsNotFound(err) {
			log.Error(err, "Failed to get SKE cluster", "clusterName", s.scope.ClusterName())
			s.scope.SetNotReady("SKEAPIError", err.Error())
			s.projectAllActiveNodePoolsNotReady("SKEAPIError", err.Error())
			return ctrl.Result{}, err
		}

		return s.reconcileInitialCreate(ctx)
	}

	if cluster == nil {
		err := fmt.Errorf("%w: get SKE cluster returned nil cluster without error", cloud.ErrTransient)
		log.Error(err, "Unexpected nil SKE cluster response", "clusterName", s.scope.ClusterName())
		s.scope.SetNotReady("SKEAPIError", err.Error())
		s.projectAllActiveNodePoolsNotReady("SKEAPIError", err.Error())
		return ctrl.Result{}, err
	}

	if !s.hasOwnershipLabels(cluster) {
		err := fmt.Errorf(
			"%w: existing SKE cluster %q is not managed by this StackitManagedCluster",
			cloud.ErrConflict,
			s.scope.ClusterName(),
		)
		log.Error(err, "Refusing to adopt existing SKE cluster without matching ownership labels", "clusterName", s.scope.ClusterName())
		s.scope.SetNotReady(ReasonClusterNotManaged, err.Error())
		s.projectAllNodePoolsNotReady(ReasonClusterNotManaged, err.Error())
		return ctrl.Result{}, err
	}

	state, stateOK := clusterAggregatedState(cluster)
	observedByName := indexObservedNodePools(cluster.GetNodepools())
	desiredPools := s.computeDesiredNodePools(observedByName, state)
	needsUpdate := !NodePoolListMatchesObserved(desiredPools, cluster.GetNodepools())

	updateTriggered := false
	if needsUpdate && state == skev2.CLUSTERSTATUSSTATE_STATE_HEALTHY {
		log.Info("Updating SKE cluster node pools", "clusterName", s.scope.ClusterName(), "desiredPoolCount", len(desiredPools))
		payload := s.buildClusterPayload(desiredPools)
		if _, updateErr := s.skeClient.
			CreateOrUpdateCluster(ctx, s.scope.ProjectID(), s.scope.Region(), s.scope.ClusterName()).
			CreateOrUpdateClusterPayload(payload).
			Execute(); updateErr != nil {
			updateErr = cloud.ClassifySDKError("update SKE cluster", updateErr)
			if cloud.IsConflict(updateErr) {
				log.V(1).Info("SKE cluster update already in progress", "clusterName", s.scope.ClusterName())
				updateTriggered = true
			} else {
				log.Error(updateErr, "Failed to update SKE cluster", "clusterName", s.scope.ClusterName())
				reason := "UpdateFailed"
				if cloud.IsInvalidInput(updateErr) {
					reason = ReasonInvalidNodePoolConfiguration
				}
				s.scope.SetNotReady(reason, updateErr.Error())
				s.projectNodePoolStatuses(cluster, observedByName, state, stateOK, false, reason, updateErr.Error())
				return ctrl.Result{}, updateErr
			}
		} else {
			updateTriggered = true
		}
	}

	s.projectNodePoolStatuses(cluster, observedByName, state, stateOK, updateTriggered, "", "")

	if !stateOK {
		log.V(1).Info("SKE cluster status is not reported yet", "clusterName", s.scope.ClusterName())
		s.scope.SetNotReady(ReasonUnknownState, "SKE cluster status is not reported yet")
		return ctrl.Result{RequeueAfter: requeueAfter}, nil
	}

	if updateTriggered {
		s.scope.SetNotReady(ReasonReconciling, "SKE cluster reconciliation in progress")
		return ctrl.Result{RequeueAfter: requeueAfter}, nil
	}

	switch state {
	case skev2.CLUSTERSTATUSSTATE_STATE_HEALTHY:
		if needsUpdate {
			s.scope.SetNotReady(ReasonReconciling, "SKE cluster node pools reconciliation pending")
			return ctrl.Result{RequeueAfter: requeueAfter}, nil
		}
		s.scope.SetReady()
		return ctrl.Result{}, nil
	case skev2.CLUSTERSTATUSSTATE_STATE_CREATING:
		s.scope.SetNotReady(ReasonCreating, "SKE cluster creation in progress")
		return ctrl.Result{RequeueAfter: requeueAfter}, nil
	case skev2.CLUSTERSTATUSSTATE_STATE_RECONCILING,
		skev2.CLUSTERSTATUSSTATE_STATE_WAKINGUP,
		skev2.CLUSTERSTATUSSTATE_STATE_HIBERNATING:
		s.scope.SetNotReady(ReasonReconciling, fmt.Sprintf("SKE cluster is in state %s", state))
		return ctrl.Result{RequeueAfter: requeueAfter}, nil
	case skev2.CLUSTERSTATUSSTATE_STATE_HIBERNATED:
		s.scope.SetNotReady(ReasonHibernated, "SKE cluster is hibernated")
		return ctrl.Result{RequeueAfter: requeueAfter}, nil
	case skev2.CLUSTERSTATUSSTATE_STATE_UNHEALTHY:
		s.scope.SetNotReady(ReasonUnhealthy, clusterErrorMessage(cluster))
		return ctrl.Result{RequeueAfter: requeueAfter}, nil
	case skev2.CLUSTERSTATUSSTATE_STATE_DELETING:
		s.scope.SetNotReady(ReasonDeleting, "SKE cluster deletion in progress")
		return ctrl.Result{RequeueAfter: requeueAfter}, nil
	default:
		s.scope.SetNotReady(ReasonUnknownState, fmt.Sprintf("SKE cluster reported unexpected state %q", state))
		return ctrl.Result{RequeueAfter: requeueAfter}, nil
	}
}

func (s *Service) reconcileInitialCreate(ctx context.Context) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	// Any unpaused child pool already marked for deletion while the parent SKE
	// cluster is confirmed absent (404) is externally deleted.
	for _, pool := range s.scope.ManagedNodePools() {
		if pool.Deleting && !pool.Paused {
			s.scope.SetNodePoolNotReady(pool.ResourceName, ReasonDeleted, "external SKE cluster does not exist")
		}
	}

	initialPools := s.initialNodePools()
	log.Info("Creating SKE cluster", "clusterName", s.scope.ClusterName(), "region", s.scope.Region(), "nodePoolCount", len(initialPools))
	payload := s.buildClusterPayload(initialPools)
	if _, createErr := s.skeClient.
		CreateOrUpdateCluster(ctx, s.scope.ProjectID(), s.scope.Region(), s.scope.ClusterName()).
		CreateOrUpdateClusterPayload(payload).
		Execute(); createErr != nil {
		createErr = cloud.ClassifySDKError("create SKE cluster", createErr)
		if cloud.IsConflict(createErr) {
			log.V(1).Info("SKE cluster creation already in progress", "clusterName", s.scope.ClusterName())
			s.scope.SetNotReady(ReasonCreating, "SKE cluster creation in progress")
			s.projectAllActiveNodePoolsNotReady(ReasonProvisioning, "SKE cluster creation in progress")
			return ctrl.Result{RequeueAfter: requeueAfter}, nil
		}
		log.Error(createErr, "Failed to create SKE cluster", "clusterName", s.scope.ClusterName())
		reason := "CreateFailed"
		if cloud.IsInvalidInput(createErr) {
			reason = ReasonInvalidNodePoolConfiguration
		}
		s.scope.SetNotReady(reason, createErr.Error())
		s.projectAllActiveNodePoolsNotReady(reason, createErr.Error())
		return ctrl.Result{}, createErr
	}

	s.scope.SetNotReady(ReasonCreating, "SKE cluster creation in progress")
	s.projectAllActiveNodePoolsNotReady(ReasonProvisioning, "SKE cluster creation in progress")
	return ctrl.Result{RequeueAfter: requeueAfter}, nil
}

// Delete triggers SKE cluster deletion and requeues until GetCluster confirms
// that the cluster no longer exists (HTTP 404).
func (s *Service) Delete(ctx context.Context) (ctrl.Result, error) {
	log := logf.FromContext(ctx)
	if s.skeClient == nil {
		err := fmt.Errorf("%w: SKE client is not configured", cloud.ErrInvalidInput)
		s.scope.SetNotReady("SKEClientError", err.Error())
		return ctrl.Result{}, err
	}

	cluster, err := s.skeClient.GetCluster(ctx, s.scope.ProjectID(), s.scope.Region(), s.scope.ClusterName()).Execute()
	if err != nil {
		err = cloud.ClassifySDKError("get SKE cluster", err)
		if cloud.IsNotFound(err) {
			log.Info("Confirmed SKE cluster absence", "clusterName", s.scope.ClusterName())
			return ctrl.Result{}, nil
		}
		log.Error(err, "Failed to get SKE cluster during deletion", "clusterName", s.scope.ClusterName())
		s.scope.SetNotReady("DeleteFailed", err.Error())
		return ctrl.Result{}, err
	}

	if cluster == nil {
		err := fmt.Errorf("%w: get SKE cluster returned nil cluster without error", cloud.ErrTransient)
		log.Error(err, "Unexpected nil SKE cluster response during deletion", "clusterName", s.scope.ClusterName())
		s.scope.SetNotReady("DeleteFailed", err.Error())
		return ctrl.Result{}, err
	}

	if !s.hasOwnershipLabels(cluster) {
		err := fmt.Errorf(
			"%w: refusing to delete SKE cluster %q not managed by this StackitManagedCluster",
			cloud.ErrConflict,
			s.scope.ClusterName(),
		)
		log.Error(err, "Refusing to delete existing SKE cluster without matching ownership labels", "clusterName", s.scope.ClusterName())
		s.scope.SetNotReady(ReasonClusterNotManaged, err.Error())
		return ctrl.Result{}, err
	}

	state, _ := clusterAggregatedState(cluster)
	if state != skev2.CLUSTERSTATUSSTATE_STATE_DELETING {
		log.Info("Deleting SKE cluster", "clusterName", s.scope.ClusterName(), "region", s.scope.Region())
		if _, deleteErr := s.skeClient.DeleteCluster(ctx, s.scope.ProjectID(), s.scope.Region(), s.scope.ClusterName()).Execute(); deleteErr != nil {
			deleteErr = cloud.ClassifySDKError("delete SKE cluster", deleteErr)
			if !cloud.IsNotFound(deleteErr) && !cloud.IsConflict(deleteErr) {
				log.Error(deleteErr, "Failed to delete SKE cluster", "clusterName", s.scope.ClusterName())
				s.scope.SetNotReady("DeleteFailed", deleteErr.Error())
				return ctrl.Result{}, deleteErr
			}
		}
	}

	s.scope.SetNotReady(ReasonDeleting, "SKE cluster deletion in progress")
	return ctrl.Result{RequeueAfter: requeueAfter}, nil
}

// validateChildNodePools validates active (non-deleting) child pools for
// duplicate or reserved names while allowing deleting duplicate/reserved CRs to
// progress immediately to Reason="Deleted" without deadlocking finalizers or
// deleting an external pool still claimed by an active CR.
func (s *Service) validateChildNodePools() (string, string) {
	pools := s.scope.ManagedNodePools()
	if len(pools) == 0 {
		return "", ""
	}

	activeByName := make(map[string][]ManagedNodePoolState, len(pools))
	var invalidMsg string
	for _, pool := range pools {
		name := pool.Desired.Name
		if pool.Deleting {
			if name == BootstrapNodePoolName && !pool.Paused {
				s.scope.SetNodePoolNotReady(
					pool.ResourceName,
					ReasonDeleted,
					"reserved bootstrap node pool name is not managed by user StackitManagedNodePool",
				)
			}
			continue
		}
		if name == BootstrapNodePoolName {
			msg := fmt.Sprintf("node pool name %q is reserved for the provider bootstrap node pool", BootstrapNodePoolName)
			if !pool.Paused {
				s.scope.SetNodePoolNotReady(pool.ResourceName, ReasonInvalidNodePoolConfiguration, msg)
			}
			if invalidMsg == "" {
				invalidMsg = msg
			}
			continue
		}
		activeByName[name] = append(activeByName[name], pool)
	}

	// If a deleting CR shares its SKE node-pool name with at least one remaining
	// active CR, release the deleting duplicate CR (Reason=Deleted) without
	// removing the external SKE node pool owned by the active CR.
	for _, pool := range pools {
		if !pool.Deleting || pool.Paused || pool.Desired.Name == BootstrapNodePoolName {
			continue
		}
		if len(activeByName[pool.Desired.Name]) > 0 {
			s.scope.SetNodePoolNotReady(
				pool.ResourceName,
				ReasonDeleted,
				fmt.Sprintf("duplicate StackitManagedNodePool for %q released in favor of active resource", pool.Desired.Name),
			)
		}
	}

	var duplicateMsg string
	for name, group := range activeByName {
		if len(group) <= 1 {
			continue
		}
		msg := fmt.Sprintf("multiple active StackitManagedNodePools in cluster declare SKE node pool name %q", name)
		for _, pool := range group {
			if !pool.Paused {
				s.scope.SetNodePoolNotReady(pool.ResourceName, ReasonDuplicateNodePoolName, msg)
			}
		}
		if duplicateMsg == "" {
			duplicateMsg = msg
		}
	}

	if duplicateMsg != "" {
		return ReasonDuplicateNodePoolName, duplicateMsg
	}
	if invalidMsg != "" {
		return ReasonInvalidNodePoolConfiguration, invalidMsg
	}
	return "", ""
}

func (s *Service) activeNodePoolNames() map[string]bool {
	names := make(map[string]bool)
	for _, pool := range s.scope.ManagedNodePools() {
		if !pool.Deleting && pool.Desired.Name != BootstrapNodePoolName {
			names[pool.Desired.Name] = true
		}
	}
	return names
}

func (s *Service) initialNodePools() []ManagedNodePoolDesiredState {
	var active []ManagedNodePoolDesiredState
	for _, pool := range s.scope.ManagedNodePools() {
		if pool.Deleting || pool.Paused || !pool.Finalized || pool.Desired.Name == BootstrapNodePoolName {
			continue
		}
		active = append(active, pool.Desired)
	}
	sortDesiredPools(active)

	if len(active) == 0 {
		return []ManagedNodePoolDesiredState{s.defaultBootstrapNodePool()}
	}
	for _, p := range active {
		if p.AllowsSystemComponents() {
			return active
		}
	}
	withBootstrap := append(slices.Clone(active), s.defaultBootstrapNodePool())
	sortDesiredPools(withBootstrap)
	return withBootstrap
}

func (s *Service) computeDesiredNodePools(
	observedByName map[string]skev2.Nodepool,
	state skev2.ClusterStatusState,
) []ManagedNodePoolDesiredState {
	activeNames := s.activeNodePoolNames()
	var active, deleting []ManagedNodePoolDesiredState

	for _, pool := range s.scope.ManagedNodePools() {
		name := pool.Desired.Name
		if name == BootstrapNodePoolName {
			continue
		}
		if pool.Paused {
			// A paused pool that already exists in SKE is preserved verbatim in
			// desired state so neither spec mutations nor standalone deletions are
			// pushed to SKE while paused. A paused pool that does not yet exist in
			// SKE is not created while paused.
			if obs, exists := observedByName[name]; exists {
				active = append(active, NodePoolDesiredStateFromSDK(obs))
			}
			continue
		}
		if pool.Deleting {
			// If an active CR still claims the same SKE pool name, do not treat
			// the pool as deleting in SKE.
			if !activeNames[name] {
				deleting = append(deleting, pool.Desired)
			}
			continue
		}
		if pool.Finalized {
			active = append(active, pool.Desired)
		}
	}
	sortDesiredPools(active)
	sortDesiredPools(deleting)

	bootstrap := s.defaultBootstrapNodePool()

	hasActiveSystemPool := false
	allActiveConverged := len(active) > 0
	hasConvergedActiveSystemPool := false
	for _, p := range active {
		obs, exists := observedByName[p.Name]
		converged := exists && NodePoolMatchesObserved(p, obs)
		if !converged {
			allActiveConverged = false
		}
		if p.AllowsSystemComponents() {
			hasActiveSystemPool = true
			if converged && state == skev2.CLUSTERSTATUSSTATE_STATE_HEALTHY {
				hasConvergedActiveSystemPool = true
			}
		}
	}

	if hasActiveSystemPool {
		result := slices.Clone(active)
		// Forward handoff Stage 2: only remove the provider bootstrap pool (or a
		// deleting system pool being replaced) once all active user pools are
		// converged and at least one system-capable user pool is healthy in SKE.
		if !allActiveConverged || !hasConvergedActiveSystemPool {
			if _, bootstrapPresent := observedByName[BootstrapNodePoolName]; bootstrapPresent {
				result = append(result, bootstrap)
			} else {
				for _, d := range deleting {
					if obs, ok := observedByName[d.Name]; ok && observedAllowsSystemComponents(obs) {
						result = append(result, NodePoolDesiredStateFromSDK(obs))
					}
				}
			}
		}
		sortDesiredPools(result)
		return result
	}

	// Reverse handoff / fallback: zero active system-capable user pools remain,
	// so the provider bootstrap pool must be present and healthy before the
	// last system-capable user pool can be removed from SKE.
	result := append(slices.Clone(active), bootstrap)
	obsBootstrap, bootstrapPresent := observedByName[BootstrapNodePoolName]
	bootstrapHealthy := bootstrapPresent &&
		NodePoolMatchesObserved(bootstrap, obsBootstrap) &&
		state == skev2.CLUSTERSTATUSSTATE_STATE_HEALTHY

	if !bootstrapHealthy {
		for _, d := range deleting {
			if obs, ok := observedByName[d.Name]; ok && observedAllowsSystemComponents(obs) {
				result = append(result, NodePoolDesiredStateFromSDK(obs))
			}
		}
	}
	sortDesiredPools(result)
	return result
}

func (s *Service) projectNodePoolStatuses(
	cluster *skev2.Cluster,
	observedByName map[string]skev2.Nodepool,
	state skev2.ClusterStatusState,
	stateOK bool,
	updateTriggered bool,
	updateFailureReason string,
	updateFailureMessage string,
) {
	activeNames := s.activeNodePoolNames()
	for _, pool := range s.scope.ManagedNodePools() {
		if pool.Paused {
			continue
		}
		name := pool.Desired.Name
		obs, existsInObserved := observedByName[name]

		if pool.Deleting {
			if name == BootstrapNodePoolName || activeNames[name] {
				s.scope.SetNodePoolNotReady(
					pool.ResourceName,
					ReasonDeleted,
					fmt.Sprintf("duplicate StackitManagedNodePool for %q released in favor of active resource", name),
				)
				continue
			}
			if !existsInObserved && !updateTriggered && state != skev2.CLUSTERSTATUSSTATE_STATE_RECONCILING {
				s.scope.SetNodePoolNotReady(pool.ResourceName, ReasonDeleted, "SKE node pool has been deleted")
			} else {
				s.scope.SetNodePoolNotReady(pool.ResourceName, ReasonDeleting, "SKE node pool deletion in progress")
			}
			continue
		}

		if !pool.Finalized {
			s.scope.SetNodePoolNotReady(pool.ResourceName, ReasonProvisioning, "waiting for StackitManagedNodePool finalizer")
			continue
		}

		if updateFailureReason != "" {
			s.scope.SetNodePoolNotReady(pool.ResourceName, updateFailureReason, updateFailureMessage)
			continue
		}

		if !existsInObserved {
			s.scope.SetNodePoolNotReady(pool.ResourceName, ReasonProvisioning, "SKE node pool provisioning in progress")
			continue
		}

		if !NodePoolMatchesObserved(pool.Desired, obs) {
			s.scope.SetNodePoolNotReady(pool.ResourceName, ReasonReconciling, "SKE node pool update in progress")
			continue
		}

		if updateTriggered {
			s.scope.SetNodePoolNotReady(pool.ResourceName, ReasonReconciling, "SKE cluster reconciliation in progress")
			continue
		}

		if !stateOK {
			s.scope.SetNodePoolNotReady(pool.ResourceName, ReasonUnknownState, "SKE cluster status is not reported yet")
			continue
		}

		switch state {
		case skev2.CLUSTERSTATUSSTATE_STATE_HEALTHY:
			s.scope.SetNodePoolReady(pool.ResourceName)
		case skev2.CLUSTERSTATUSSTATE_STATE_CREATING:
			s.scope.SetNodePoolNotReady(pool.ResourceName, ReasonProvisioning, "SKE cluster creation in progress")
		case skev2.CLUSTERSTATUSSTATE_STATE_RECONCILING,
			skev2.CLUSTERSTATUSSTATE_STATE_WAKINGUP,
			skev2.CLUSTERSTATUSSTATE_STATE_HIBERNATING:
			s.scope.SetNodePoolNotReady(pool.ResourceName, ReasonReconciling, fmt.Sprintf("SKE cluster is in state %s", state))
		case skev2.CLUSTERSTATUSSTATE_STATE_HIBERNATED:
			s.scope.SetNodePoolNotReady(pool.ResourceName, ReasonHibernated, "SKE cluster is hibernated")
		case skev2.CLUSTERSTATUSSTATE_STATE_UNHEALTHY:
			msg := fmt.Sprintf(
				"parent SKE cluster is in cluster-wide STATE_UNHEALTHY (SKE does not report per-node-pool health): %s",
				clusterErrorMessage(cluster),
			)
			s.scope.SetNodePoolNotReady(pool.ResourceName, ReasonUnhealthy, msg)
		case skev2.CLUSTERSTATUSSTATE_STATE_DELETING:
			s.scope.SetNodePoolNotReady(pool.ResourceName, ReasonDeleting, "parent SKE cluster is deleting")
		default:
			s.scope.SetNodePoolNotReady(pool.ResourceName, ReasonUnknownState, fmt.Sprintf("SKE cluster reported unexpected state %q", state))
		}
	}
}

func (s *Service) projectAllActiveNodePoolsNotReady(reason, message string) {
	for _, pool := range s.scope.ManagedNodePools() {
		if pool.Deleting || pool.Paused {
			continue
		}
		s.scope.SetNodePoolNotReady(pool.ResourceName, reason, message)
	}
}

func (s *Service) projectAllNodePoolsNotReady(reason, message string) {
	for _, pool := range s.scope.ManagedNodePools() {
		if pool.Paused {
			continue
		}
		s.scope.SetNodePoolNotReady(pool.ResourceName, reason, message)
	}
}

func (s *Service) defaultBootstrapNodePool() ManagedNodePoolDesiredState {
	imageVersion := os.Getenv(defaultNodePoolImageVersionEnv)
	if imageVersion == "" {
		imageVersion = defaultNodePoolImageVersion
	}
	return ManagedNodePoolDesiredState{
		Name:                  BootstrapNodePoolName,
		AvailabilityZones:     []string{s.scope.Region() + "-1"},
		MachineType:           defaultNodePoolMachineType,
		ImageName:             defaultNodePoolImageName,
		ImageVersion:          imageVersion,
		Minimum:               defaultNodePoolMinimum,
		Maximum:               defaultNodePoolMaximum,
		VolumeSize:            defaultNodePoolVolumeSize,
		VolumeType:            defaultNodePoolVolumeType,
		AllowSystemComponents: new(true),
	}
}

func (s *Service) buildClusterPayload(desiredPools []ManagedNodePoolDesiredState) skev2.CreateOrUpdateClusterPayload {
	sdkPools := make([]skev2.Nodepool, len(desiredPools))
	for i, pool := range desiredPools {
		sdkPools[i] = pool.ToSKESDK()
	}

	payload := skev2.CreateOrUpdateClusterPayload{
		Kubernetes: skev2.Kubernetes{
			Version: s.scope.KubernetesVersion(),
		},
		Nodepools: sdkPools,
	}

	if networkID := s.scope.NetworkID(); networkID != "" {
		payload.Network = &skev2.Network{
			Id: new(networkID),
		}
	}

	if labels := s.scope.Labels(); len(labels) > 0 {
		payload.Labels = &labels
	}

	return payload
}

func indexObservedNodePools(pools []skev2.Nodepool) map[string]skev2.Nodepool {
	out := make(map[string]skev2.Nodepool, len(pools))
	for _, np := range pools {
		out[np.Name] = np
	}
	return out
}

func sortDesiredPools(pools []ManagedNodePoolDesiredState) {
	slices.SortFunc(pools, func(a, b ManagedNodePoolDesiredState) int {
		return cmp.Compare(a.Name, b.Name)
	})
}

func (s *Service) hasOwnershipLabels(cluster *skev2.Cluster) bool {
	if cluster == nil || !cluster.HasLabels() {
		return false
	}
	expected := s.scope.OwnershipLabels()
	if len(expected) == 0 {
		return false
	}
	actual := cluster.GetLabels()
	for k, v := range expected {
		if v == "" || actual[k] != v {
			return false
		}
	}
	return true
}

func clusterAggregatedState(cluster *skev2.Cluster) (skev2.ClusterStatusState, bool) {
	if cluster == nil || cluster.Status == nil || !cluster.Status.HasAggregated() {
		return "", false
	}
	state := cluster.Status.GetAggregated()
	if state == "" {
		return "", false
	}
	return state, true
}

func clusterErrorMessage(cluster *skev2.Cluster) string {
	if cluster != nil && cluster.Status != nil {
		if len(cluster.Status.Errors) > 0 {
			if msg := cluster.Status.Errors[0].GetMessage(); msg != "" {
				return msg
			}
		}
		if cluster.Status.Error != nil {
			if msg := cluster.Status.Error.GetMessage(); msg != "" {
				return msg
			}
		}
	}
	return "SKE cluster is unhealthy"
}
