/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

// Package scope bundles the resolved CAPI / Stackit objects a reconcile pass
// operates on, together with a patch helper, mirroring the convention used
// by upstream CAPI providers.
package scope

import (
	"context"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	"sigs.k8s.io/cluster-api/util/patch"

	infrav1 "github.com/stackitcloud/cluster-api-provider-stackit/api/v1alpha1"
	"github.com/stackitcloud/cluster-api-provider-stackit/cloud"
	"github.com/stackitcloud/cluster-api-provider-stackit/util"
)

// ClusterScope holds the per-reconcile state for a StackitCluster.
type ClusterScope struct {
	Client         client.Client
	Cluster        *clusterv1.Cluster
	StackitCluster *infrav1.StackitCluster

	patchHelper *patch.Helper
}

// NewClusterScope constructs a ClusterScope and snapshots the original
// resource so the patchHelper can compute a minimal patch on close.
func NewClusterScope(
	k8sClient client.Client,
	cluster *clusterv1.Cluster,
	sc *infrav1.StackitCluster,
) (*ClusterScope, error) {
	ph, err := patch.NewHelper(sc, k8sClient)
	if err != nil {
		return nil, err
	}
	return &ClusterScope{
		Client:         k8sClient,
		Cluster:        cluster,
		StackitCluster: sc,
		patchHelper:    ph,
	}, nil
}

// PatchObject writes back any spec/status changes to the StackitCluster.
func (s *ClusterScope) PatchObject(ctx context.Context) error {
	return s.patchHelper.Patch(ctx, s.StackitCluster, patch.WithOwnedConditions{Conditions: []string{
		infrav1.ClusterReadyCondition,
		infrav1.ClusterNetworkReadyCondition,
		infrav1.ClusterLoadBalancerReadyCondition,
		infrav1.ClusterCredentialsReadyCondition,
		clusterv1.PausedCondition,
	}})
}

func (s *ClusterScope) SetConditions(status metav1.ConditionStatus, reason, message string, conditionTypes ...string) {
	util.SetConditions(&s.StackitCluster.Status.Conditions, s.StackitCluster.Generation, status, reason, message, conditionTypes...)
}

func (s *ClusterScope) SetReady() {
	s.StackitCluster.Status.Ready = true
	s.StackitCluster.Status.Initialization.Provisioned = true
	s.SetConditions(metav1.ConditionTrue, "Available", "", infrav1.ClusterReadyCondition)
}

func (s *ClusterScope) SetNotReady(reason, message string, conditionTypes ...string) {
	s.StackitCluster.Status.Ready = false
	s.SetConditions(metav1.ConditionFalse, reason, message, conditionTypes...)
}

func (s *ClusterScope) SetAPIServerEndpoint(endpoint clusterv1.APIEndpoint) {
	s.StackitCluster.Spec.ControlPlaneEndpoint = endpoint
	s.StackitCluster.Status.APIServerEndpoint = endpoint
}

func (s *ClusterScope) SetBastionStatus(bastion *cloud.Bastion, cloudInitHash string) {
	s.StackitCluster.Status.Bastion = infrav1.StackitBastionStatus{
		ServerID:        bastion.ServerID,
		PublicIPID:      bastion.PublicIPID,
		PublicIP:        bastion.PublicIP,
		SecurityGroupID: bastion.SecurityGroupID,
		CloudInitHash:   cloudInitHash,
	}
}

func (s *ClusterScope) ClearBastionStatus() {
	s.StackitCluster.Status.Bastion = infrav1.StackitBastionStatus{}
}
