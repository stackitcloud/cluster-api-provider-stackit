/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

// Package ske converts CAPSTK managed-cluster and node-pool resources into a
// provider-neutral SKE desired-state representation.
//
// Managed cluster reconciliation owns mutation of the SKE cluster because SKE
// node pools are submitted as part of the cluster payload.
package ske

import (
	"cmp"
	"maps"
	"slices"
	"strings"

	skev2 "github.com/stackitcloud/stackit-sdk-go/services/ske/v2api"

	infrav1 "github.com/stackitcloud/cluster-api-provider-stackit/api/v1alpha1"
)

// ManagedClusterDesiredState holds the cluster-level fields needed by the SKE
// service layer.
type ManagedClusterDesiredState struct {
	ProjectID         string
	Region            string
	ClusterName       string
	KubernetesVersion string
	NetworkID         string
	Labels            map[string]string
}

// ManagedNodePoolDesiredState holds the node-pool-level fields needed by the
// SKE service layer.
type ManagedNodePoolDesiredState struct {
	Name                  string
	AvailabilityZones     []string
	MachineType           string
	ImageName             string
	ImageVersion          string
	Minimum               int32
	Maximum               int32
	VolumeSize            int32
	VolumeType            string
	AllowSystemComponents *bool
	Labels                map[string]string
	Taints                []ManagedNodePoolTaint
	MaxSurge              *int32
	MaxUnavailable        *int32
}

// ManagedNodePoolTaint describes a node taint in provider-neutral terms.
type ManagedNodePoolTaint struct {
	Effect string
	Key    string
	Value  string
}

// ManagedClusterInput represents the combined SKE cluster payload produced from
// a StackitManagedCluster and its associated StackitManagedNodePools.
type ManagedClusterInput struct {
	Cluster   ManagedClusterDesiredState
	NodePools []ManagedNodePoolDesiredState
}

// ClusterDesiredState converts a StackitManagedCluster into its SKE desired
// state representation.
func ClusterDesiredState(managedCluster *infrav1.StackitManagedCluster) ManagedClusterDesiredState {
	if managedCluster == nil {
		return ManagedClusterDesiredState{}
	}
	var networkID string
	if managedCluster.Spec.Network != nil {
		networkID = managedCluster.Spec.Network.ID
	}
	var labels map[string]string
	if len(managedCluster.Spec.Labels) > 0 {
		labels = maps.Clone(managedCluster.Spec.Labels)
	}
	return ManagedClusterDesiredState{
		ProjectID:         managedCluster.Spec.ProjectID,
		Region:            managedCluster.Spec.Region,
		ClusterName:       managedCluster.Spec.ClusterName,
		KubernetesVersion: managedCluster.Spec.Kubernetes.Version,
		NetworkID:         networkID,
		Labels:            labels,
	}
}

// NodePoolDesiredState converts a StackitManagedNodePool into its SKE desired
// state representation.
func NodePoolDesiredState(nodePool *infrav1.StackitManagedNodePool) ManagedNodePoolDesiredState {
	if nodePool == nil {
		return ManagedNodePoolDesiredState{}
	}
	spec := nodePool.Spec

	var labels map[string]string
	if len(spec.Labels) > 0 {
		labels = maps.Clone(spec.Labels)
	}

	var taints []ManagedNodePoolTaint
	if len(spec.Taints) > 0 {
		taints = make([]ManagedNodePoolTaint, len(spec.Taints))
		for i, taint := range spec.Taints {
			taints[i] = ManagedNodePoolTaint{
				Effect: string(taint.Effect),
				Key:    taint.Key,
				Value:  taint.Value,
			}
		}
	}

	var allowSystemComponents *bool
	if spec.AllowSystemComponents != nil {
		allowSystemComponents = new(*spec.AllowSystemComponents)
	}
	var maxSurge *int32
	if spec.MaxSurge != nil {
		maxSurge = new(*spec.MaxSurge)
	}
	var maxUnavailable *int32
	if spec.MaxUnavailable != nil {
		maxUnavailable = new(*spec.MaxUnavailable)
	}

	return ManagedNodePoolDesiredState{
		Name:                  spec.Name,
		AvailabilityZones:     slices.Clone(spec.AvailabilityZones),
		MachineType:           spec.Machine.Type,
		ImageName:             spec.Machine.Image.Name,
		ImageVersion:          spec.Machine.Image.Version,
		Minimum:               spec.Minimum,
		Maximum:               spec.Maximum,
		VolumeSize:            spec.Volume.Size,
		VolumeType:            spec.Volume.Type,
		AllowSystemComponents: allowSystemComponents,
		Labels:                labels,
		Taints:                taints,
		MaxSurge:              maxSurge,
		MaxUnavailable:        maxUnavailable,
	}
}

// ComposeManagedCluster combines the cluster desired state and node-pool
// desired states into a single SKE input, ordering node pools deterministically
// by their SKE node-pool name.
func ComposeManagedCluster(
	cluster ManagedClusterDesiredState,
	nodePools []ManagedNodePoolDesiredState,
) (ManagedClusterInput, error) {
	sortedPools := slices.Clone(nodePools)
	slices.SortFunc(sortedPools, func(a, b ManagedNodePoolDesiredState) int {
		return strings.Compare(a.Name, b.Name)
	})

	return ManagedClusterInput{
		Cluster:   cluster,
		NodePools: sortedPools,
	}, nil
}

// AllowsSystemComponents reports whether the node pool allows system
// components (defaulting to true when AllowSystemComponents is nil, matching
// SKE API defaulting).
func (np ManagedNodePoolDesiredState) AllowsSystemComponents() bool {
	return np.AllowSystemComponents == nil || *np.AllowSystemComponents
}

// ToSKESDK converts a ManagedNodePoolDesiredState into an SKE v2 SDK Nodepool.
func (np ManagedNodePoolDesiredState) ToSKESDK() skev2.Nodepool {
	zones := slices.Clone(np.AvailabilityZones)
	slices.Sort(zones)

	volumeType := canonicalVolumeType(np.VolumeType)
	out := skev2.Nodepool{
		Name:                  np.Name,
		AvailabilityZones:     zones,
		AllowSystemComponents: new(np.AllowsSystemComponents()),
		Machine: skev2.Machine{
			Type: np.MachineType,
			Image: skev2.Image{
				Name:    canonicalImageName(np.ImageName),
				Version: np.ImageVersion,
			},
		},
		Minimum: np.Minimum,
		Maximum: np.Maximum,
		Volume: skev2.Volume{
			Size: np.VolumeSize,
			Type: new(volumeType),
		},
	}

	if len(np.Labels) > 0 {
		labels := maps.Clone(np.Labels)
		out.Labels = &labels
	}
	if len(np.Taints) > 0 {
		taints := make([]skev2.Taint, len(np.Taints))
		for i, t := range np.Taints {
			taint := skev2.Taint{
				Effect: skev2.TaintEffect(t.Effect),
				Key:    t.Key,
			}
			if t.Value != "" {
				taint.Value = new(t.Value)
			}
			taints[i] = taint
		}
		out.Taints = taints
	}
	if np.MaxSurge != nil {
		out.MaxSurge = new(*np.MaxSurge)
	}
	if np.MaxUnavailable != nil {
		out.MaxUnavailable = new(*np.MaxUnavailable)
	}

	return out
}

// NodePoolDesiredStateFromSDK converts an observed SKE SDK Nodepool into a
// ManagedNodePoolDesiredState so an existing pool can be retained verbatim
// during a two-stage bootstrap handoff.
func NodePoolDesiredStateFromSDK(in skev2.Nodepool) ManagedNodePoolDesiredState {
	var labels map[string]string
	if in.HasLabels() && len(in.GetLabels()) > 0 {
		labels = maps.Clone(in.GetLabels())
	}

	var taints []ManagedNodePoolTaint
	if len(in.Taints) > 0 {
		taints = make([]ManagedNodePoolTaint, len(in.Taints))
		for i, t := range in.Taints {
			taints[i] = ManagedNodePoolTaint{
				Effect: string(t.Effect),
				Key:    t.Key,
				Value:  t.GetValue(),
			}
		}
	}

	var allowSystem *bool
	if in.HasAllowSystemComponents() {
		allowSystem = new(in.GetAllowSystemComponents())
	}
	var maxSurge *int32
	if in.HasMaxSurge() {
		maxSurge = new(in.GetMaxSurge())
	}
	var maxUnavailable *int32
	if in.HasMaxUnavailable() {
		maxUnavailable = new(in.GetMaxUnavailable())
	}

	return ManagedNodePoolDesiredState{
		Name:                  in.Name,
		AvailabilityZones:     slices.Clone(in.AvailabilityZones),
		MachineType:           in.Machine.Type,
		ImageName:             in.Machine.Image.Name,
		ImageVersion:          in.Machine.Image.Version,
		Minimum:               in.Minimum,
		Maximum:               in.Maximum,
		VolumeSize:            in.Volume.Size,
		VolumeType:            in.Volume.GetType(),
		AllowSystemComponents: allowSystem,
		Labels:                labels,
		Taints:                taints,
		MaxSurge:              maxSurge,
		MaxUnavailable:        maxUnavailable,
	}
}

// NodePoolMatchesObserved performs a canonical semantic comparison between a
// desired node pool and an observed SKE node pool, normalizing order-insensitive
// slices, nil vs empty maps/slices, and SKE API defaults.
func NodePoolMatchesObserved(desired ManagedNodePoolDesiredState, observed skev2.Nodepool) bool {
	if desired.Name != observed.Name {
		return false
	}
	if !canonicalStringSliceEqual(desired.AvailabilityZones, observed.AvailabilityZones) {
		return false
	}
	if desired.MachineType != observed.Machine.Type {
		return false
	}
	if canonicalImageName(desired.ImageName) != canonicalImageName(observed.Machine.Image.Name) ||
		desired.ImageVersion != observed.Machine.Image.Version {
		return false
	}
	if desired.Minimum != observed.Minimum || desired.Maximum != observed.Maximum {
		return false
	}
	if desired.VolumeSize != observed.Volume.Size ||
		canonicalVolumeType(desired.VolumeType) != canonicalVolumeType(observed.Volume.GetType()) {
		return false
	}
	if desired.AllowsSystemComponents() != observedAllowsSystemComponents(observed) {
		return false
	}
	if !canonicalStringMapEqual(desired.Labels, observed.GetLabels()) {
		return false
	}
	if !canonicalTaintsEqual(desired.Taints, observed.Taints) {
		return false
	}
	if desired.MaxSurge != nil && (!observed.HasMaxSurge() || observed.GetMaxSurge() != *desired.MaxSurge) {
		return false
	}
	if desired.MaxUnavailable != nil && (!observed.HasMaxUnavailable() || observed.GetMaxUnavailable() != *desired.MaxUnavailable) {
		return false
	}
	return true
}

// NodePoolListMatchesObserved reports whether the complete desired node-pool
// set is semantically equal to the observed SKE cluster node pools,
// independent of slice order.
func NodePoolListMatchesObserved(desired []ManagedNodePoolDesiredState, observed []skev2.Nodepool) bool {
	if len(desired) != len(observed) {
		return false
	}
	observedByName := make(map[string]skev2.Nodepool, len(observed))
	for _, np := range observed {
		observedByName[np.Name] = np
	}
	if len(observedByName) != len(desired) {
		return false
	}
	for _, d := range desired {
		obs, exists := observedByName[d.Name]
		if !exists || !NodePoolMatchesObserved(d, obs) {
			return false
		}
	}
	return true
}

func observedAllowsSystemComponents(np skev2.Nodepool) bool {
	if !np.HasAllowSystemComponents() {
		return true
	}
	return np.GetAllowSystemComponents()
}

func canonicalImageName(name string) string {
	if name == "" {
		return defaultNodePoolImageName
	}
	return name
}

func canonicalVolumeType(volType string) string {
	if volType == "" {
		return defaultNodePoolVolumeType
	}
	return volType
}

func canonicalStringSliceEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	if len(a) == 0 {
		return true
	}
	ca := slices.Clone(a)
	cb := slices.Clone(b)
	slices.Sort(ca)
	slices.Sort(cb)
	return slices.Equal(ca, cb)
}

func canonicalStringMapEqual(a, b map[string]string) bool {
	if len(a) == 0 && len(b) == 0 {
		return true
	}
	return maps.Equal(a, b)
}

func canonicalTaintsEqual(desired []ManagedNodePoolTaint, observed []skev2.Taint) bool {
	if len(desired) != len(observed) {
		return false
	}
	if len(desired) == 0 {
		return true
	}
	ca := slices.Clone(desired)
	cb := make([]ManagedNodePoolTaint, len(observed))
	for i, t := range observed {
		cb[i] = ManagedNodePoolTaint{
			Effect: string(t.Effect),
			Key:    t.Key,
			Value:  t.GetValue(),
		}
	}
	cmpTaint := func(x, y ManagedNodePoolTaint) int {
		if c := cmp.Compare(x.Key, y.Key); c != 0 {
			return c
		}
		if c := cmp.Compare(x.Effect, y.Effect); c != 0 {
			return c
		}
		return cmp.Compare(x.Value, y.Value)
	}
	slices.SortFunc(ca, cmpTaint)
	slices.SortFunc(cb, cmpTaint)
	return slices.Equal(ca, cb)
}
