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
	"maps"
	"slices"
	"strings"

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
