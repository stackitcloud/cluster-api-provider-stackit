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
	iaas "github.com/stackitcloud/stackit-sdk-go/services/iaas/v2api"
)

// ClusterScope holds the per-reconcile state for a StackitCluster.
type ClusterScope struct {
	Client         client.Client
	Cluster        *clusterv1.Cluster
	StackitCluster *infrav1.StackitCluster
	CloudClient    cloud.Client

	patchHelper *patch.Helper
}

// NewClusterScope constructs a ClusterScope and snapshots the original
// resource so the patchHelper can compute a minimal patch on close.
func NewClusterScope(
	k8sClient client.Client,
	cluster *clusterv1.Cluster,
	stackitCluster *infrav1.StackitCluster,
) (*ClusterScope, error) {
	patchHelper, err := patch.NewHelper(stackitCluster, k8sClient)
	if err != nil {
		return nil, err
	}
	return &ClusterScope{
		Client:         k8sClient,
		Cluster:        cluster,
		StackitCluster: stackitCluster,
		patchHelper:    patchHelper,
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
	s.StackitCluster.Status.Bastion.ServerID = bastion.ServerID
	s.StackitCluster.Status.Bastion.SecurityGroupID = bastion.SecurityGroupID
	s.StackitCluster.Status.Bastion.CloudInitHash = cloudInitHash
}

func (s *ClusterScope) ClearBastionStatus() {
	s.StackitCluster.Status.Bastion = infrav1.StackitBastionStatus{}
}

func (s *ClusterScope) SetFailureDomains(failureDomains []clusterv1.FailureDomain) {
	s.StackitCluster.Status.FailureDomains = failureDomains
}

func (s *ClusterScope) Cloud() cloud.Client {
	return s.CloudClient
}

func (s *ClusterScope) SetCloudClient(cloudClient cloud.Client) {
	s.CloudClient = cloudClient
}

func (s *ClusterScope) ProjectID() string {
	if s.StackitCluster.Spec.ProjectID != "" {
		return s.StackitCluster.Spec.ProjectID
	}
	return s.CloudClient.ProjectID()
}

func (s *ClusterScope) Region() string {
	if s.StackitCluster.Spec.Region != "" {
		return s.StackitCluster.Spec.Region
	}
	return s.CloudClient.Region()
}

func (s *ClusterScope) IaaSClient() *iaas.APIClient {
	return s.CloudClient.IaaSClient()
}

func (s *ClusterScope) BastionSpec() infrav1.StackitBastionSpec {
	return s.StackitCluster.Spec.Bastion
}

func (s *ClusterScope) BastionStatus() infrav1.StackitBastionStatus {
	return s.StackitCluster.Status.Bastion
}

func (s *ClusterScope) BastionTags() map[string]string {
	tags := util.ClusterTags(s.StackitCluster.Name, s.StackitCluster.Namespace, s.StackitCluster.Spec.AdditionalLabels)
	tags[util.LabelResourceRole] = util.ResourceRoleBastion
	return tags
}

func (s *ClusterScope) SetBastionPublicIP(id, ip string) {
	s.StackitCluster.Status.Bastion.PublicIPID = id
	s.StackitCluster.Status.Bastion.PublicIP = ip
}
