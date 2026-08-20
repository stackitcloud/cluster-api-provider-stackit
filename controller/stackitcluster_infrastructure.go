/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package controller

import (
	"context"
	"fmt"
	"net/netip"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	infrav1 "github.com/stackitcloud/cluster-api-provider-stackit/api/v1alpha1"
	"github.com/stackitcloud/cluster-api-provider-stackit/cloud"
	bastionservice "github.com/stackitcloud/cluster-api-provider-stackit/cloud/services/bastion"
	loadbalancerservice "github.com/stackitcloud/cluster-api-provider-stackit/cloud/services/loadbalancer"
	"github.com/stackitcloud/cluster-api-provider-stackit/scope"
	"github.com/stackitcloud/cluster-api-provider-stackit/util"
)

func (r *StackitClusterReconciler) reconcileNormal(ctx context.Context, clusterScope *scope.ClusterScope) (ctrl.Result, error) {
	log := logf.FromContext(ctx)
	stackitCluster := clusterScope.StackitCluster

	if !controllerutil.ContainsFinalizer(stackitCluster, infrav1.ClusterFinalizer) {
		controllerutil.AddFinalizer(stackitCluster, infrav1.ClusterFinalizer)
	}
	stackitCluster.Status.FailureDomains = stackitFailureDomains(stackitCluster.Spec.Region)

	cloudClient, err := util.BuildCloudClient(ctx, r.Client, r.CloudClientFactory, stackitCluster)
	if err != nil {
		stackitCluster.Status.Ready = false
		return util.CredentialFailureResult(
			&stackitCluster.Status.Conditions,
			stackitCluster.Generation,
			err,
			infrav1.ClusterCredentialsReadyCondition,
			infrav1.ClusterReadyCondition,
		)
	}
	clusterScope.SetConditions(
		metav1.ConditionTrue,
		"Available",
		"",
		infrav1.ClusterCredentialsReadyCondition,
	)

	network, err := cloudClient.GetNetwork(ctx, stackitCluster.Spec.Network.ID)
	if err != nil {
		stackitCluster.Status.Ready = false
		return util.CloudFailureResult(
			&stackitCluster.Status.Conditions,
			stackitCluster.Generation,
			"NetworkNotFound",
			err,
			retryableErrorRequeueAfter,
			false,
			infrav1.ClusterNetworkReadyCondition,
			infrav1.ClusterReadyCondition,
		)
	}
	clusterScope.SetConditions(
		metav1.ConditionTrue,
		"Available",
		"",
		infrav1.ClusterNetworkReadyCondition,
	)

	if stackitCluster.Spec.APIServerLoadBalancer.Enabled {
		loadBalancer, err := cloudClient.EnsureAPIServerLoadBalancer(
			ctx,
			loadbalancerservice.APIServerInput(
				stackitCluster,
				[]cloud.LoadBalancerTargetInput{loadbalancerservice.BootstrapTarget(bootstrapTargetIP(network))},
			),
		)
		if err != nil {
			stackitCluster.Status.Ready = false
			return util.CloudFailureResult(
				&stackitCluster.Status.Conditions,
				stackitCluster.Generation,
				"LoadBalancerError",
				err,
				retryableErrorRequeueAfter,
				false,
				infrav1.ClusterLoadBalancerReadyCondition,
				infrav1.ClusterReadyCondition,
			)
		}
		hadLoadBalancerID := stackitCluster.Status.APIServerLoadBalancerID != ""
		if loadBalancer != nil {
			stackitCluster.Status.APIServerLoadBalancerID = loadBalancer.ID
			if !hadLoadBalancerID && loadBalancer.ID != "" && r.Recorder != nil {
				r.Recorder.Eventf(
					stackitCluster, nil, corev1.EventTypeNormal, "LoadBalancerCreated", "Create",
					"Created API server load balancer %s", loadBalancer.ID,
				)
			}
		}
		if loadBalancer == nil || loadBalancer.IP == "" {
			clusterScope.SetNotReady(
				"Provisioning",
				"waiting for API server load balancer IP address",
				infrav1.ClusterLoadBalancerReadyCondition,
				infrav1.ClusterReadyCondition,
			)
			return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
		}
		endpoint := clusterv1.APIEndpoint{
			Host: loadBalancer.IP,
			Port: defaultAPIServerPort,
		}
		clusterScope.SetAPIServerEndpoint(endpoint)
		clusterScope.SetConditions(
			metav1.ConditionTrue,
			"Available",
			"",
			infrav1.ClusterLoadBalancerReadyCondition,
		)
		if r.Recorder != nil {
			r.Recorder.Eventf(
				stackitCluster, nil, corev1.EventTypeNormal, "LoadBalancerReady", "SetReady",
				"API server load balancer is ready at %s", loadBalancer.IP,
			)
		}
	} else if stackitCluster.Spec.ControlPlaneEndpoint.Host != "" {
		stackitCluster.Status.APIServerEndpoint = stackitCluster.Spec.ControlPlaneEndpoint
		clusterScope.SetConditions(
			metav1.ConditionTrue,
			"Skipped",
			"external endpoint provided",
			infrav1.ClusterLoadBalancerReadyCondition,
		)
	} else {
		clusterScope.SetNotReady(
			"EndpointMissing",
			"apiServerLoadBalancer.enabled is false and controlPlaneEndpoint is empty",
			infrav1.ClusterLoadBalancerReadyCondition,
			infrav1.ClusterReadyCondition,
		)
		return ctrl.Result{}, nil
	}

	if result, ready, err := r.reconcileBastion(ctx, cloudClient, clusterScope); err != nil {
		stackitCluster.Status.Ready = false
		return util.CloudFailureResult(
			&stackitCluster.Status.Conditions,
			stackitCluster.Generation,
			"BastionError",
			err,
			retryableErrorRequeueAfter,
			false,
			infrav1.ClusterBastionReadyCondition,
			infrav1.ClusterReadyCondition,
		)
	} else if !ready {
		return result, nil
	}

	clusterScope.SetReady()
	log.V(1).Info("StackitCluster ready", "endpoint", stackitCluster.Status.APIServerEndpoint)
	return ctrl.Result{}, nil
}

func stackitFailureDomains(region string) []clusterv1.FailureDomain {
	controlPlane := true
	return []clusterv1.FailureDomain{
		{
			Name:         region + "-1",
			ControlPlane: &controlPlane,
			Attributes: map[string]string{
				"region": region,
			},
		},
		{
			Name:         region + "-2",
			ControlPlane: &controlPlane,
			Attributes: map[string]string{
				"region": region,
			},
		},
		{
			Name:         region + "-3",
			ControlPlane: &controlPlane,
			Attributes: map[string]string{
				"region": region,
			},
		},
	}
}

func bootstrapTargetIP(network *cloud.Network) string {
	if network == nil {
		return "10.0.0.1"
	}
	for _, prefixValue := range network.IPv4Prefixes {
		prefix, err := netip.ParsePrefix(prefixValue)
		if err != nil || !prefix.Addr().Is4() {
			continue
		}
		address := prefix.Masked().Addr()
		for range 10 {
			address = address.Next()
		}
		if prefix.Contains(address) {
			return address.String()
		}
	}
	return "10.0.0.1"
}

// ownerClusterName reports the name of the Cluster this StackitCluster belongs
// to, without needing that Cluster to still exist.
func ownerClusterName(stackitCluster *infrav1.StackitCluster) string {
	for _, ref := range stackitCluster.OwnerReferences {
		if ref.Kind == "Cluster" {
			return ref.Name
		}
	}
	return ""
}

func (r *StackitClusterReconciler) reconcileDelete(ctx context.Context, clusterScope *scope.ClusterScope) (ctrl.Result, error) {
	stackitCluster := clusterScope.StackitCluster

	// Machines resolve their credentials and project context through this
	// StackitCluster, so removing the finalizer while any of them remain leaves
	// them unable to delete their own servers. Cluster API orders this correctly
	// when deletion starts at the Cluster, but a namespace teardown or a direct
	// delete of this object bypasses that ordering entirely.
	//
	// Selects the same way util/collections.GetFilteredMachinesForCluster does,
	// inlined because that package pulls in the kubeadm bootstrap API for a
	// query this short. Cluster API sets the label on every Machine it owns.
	//
	// The Cluster itself may already be gone — a namespace teardown deletes it
	// in no particular order — so the name is taken from the ownerReference,
	// which outlives it. Owner references are always same-namespace, so the
	// StackitCluster's own namespace is the right one either way.
	clusterName := ownerClusterName(stackitCluster)
	if clusterScope.Cluster != nil {
		clusterName = clusterScope.Cluster.Name
	}
	machines := &clusterv1.MachineList{}
	if err := r.List(ctx, machines,
		client.InNamespace(stackitCluster.Namespace),
		client.MatchingLabels{clusterv1.ClusterNameLabel: clusterName},
	); err != nil {
		return ctrl.Result{}, fmt.Errorf("list Machines for cluster %s: %w", clusterName, err)
	}
	if len(machines.Items) > 0 {
		logf.FromContext(ctx).Info(
			"Waiting for Machines to be deleted before removing the StackitCluster finalizer",
			"remainingMachines", len(machines.Items),
		)
		return ctrl.Result{RequeueAfter: deleteRequeueAfter}, nil
	}

	// Cleanup runs unconditionally. Neither spec nor status is a trustworthy
	// record of what exists in the cloud: a resource can be created before its
	// status patch lands, and disabling the load balancer or the bastion leaves
	// the running resource behind. ResolveID, DeleteBastion and
	// DeleteNodeSSHAccess all fall back to tag lookups and tolerate NotFound, so
	// asking for everything costs a handful of list calls once per cluster and
	// removes every combination in which a resource could be missed.
	cloudClient, err := util.BuildCloudClient(ctx, r.Client, r.CloudClientFactory, stackitCluster)
	if err != nil {
		// A missing credentials Secret can never be recovered from — it commonly
		// disappears first during namespace teardown. Blocking here would strand
		// the cluster in Terminating forever, so finalize and make the possible
		// leak loud instead. Any other credentials problem is fixable, so keep
		// retrying for those.
		if apierrors.IsNotFound(err) {
			if r.Recorder != nil {
				r.Recorder.Eventf(stackitCluster, nil, corev1.EventTypeWarning, "CleanupSkipped", "Delete",
					"Credentials Secret is gone; finalizing without cloud cleanup. "+
						"Any remaining STACKIT resources for this cluster must be removed manually: %v", err)
			}
			controllerutil.RemoveFinalizer(stackitCluster, infrav1.ClusterFinalizer)
			return ctrl.Result{}, nil
		}
		util.SetConditions(
			&stackitCluster.Status.Conditions,
			stackitCluster.Generation,
			metav1.ConditionFalse,
			"CredentialsInvalid",
			err.Error(),
			infrav1.ClusterCredentialsReadyCondition,
		)
		return ctrl.Result{}, err
	}
	loadBalancerID, err := loadbalancerservice.ResolveID(ctx, cloudClient, stackitCluster)
	if err != nil {
		return ctrl.Result{}, err
	}
	if loadBalancerID != "" {
		if err := cloudClient.DeleteAPIServerLoadBalancer(ctx, loadBalancerID); err != nil && !cloud.IsNotFound(err) {
			return ctrl.Result{}, err
		}
		stackitCluster.Status.APIServerLoadBalancerID = ""
		if r.Recorder != nil {
			r.Recorder.Eventf(
				stackitCluster, nil, corev1.EventTypeNormal, "LoadBalancerDeleted", "Delete",
				"Deleted API server load balancer %s", loadBalancerID,
			)
		}
	}
	if err := cloudClient.DeleteNodeSSHAccess(ctx, bastionservice.NodeSSHAccessTags(stackitCluster)); err != nil && !cloud.IsNotFound(err) {
		return ctrl.Result{}, err
	}
	if err := cloudClient.DeleteBastion(ctx, bastionservice.Input(stackitCluster, nil), cloud.Bastion{
		ServerID:        stackitCluster.Status.Bastion.ServerID,
		PublicIPID:      stackitCluster.Status.Bastion.PublicIPID,
		PublicIP:        stackitCluster.Status.Bastion.PublicIP,
		SecurityGroupID: stackitCluster.Status.Bastion.SecurityGroupID,
	}); err != nil && !cloud.IsNotFound(err) {
		return ctrl.Result{}, err
	}
	if hasBastionStatus(stackitCluster.Status.Bastion) {
		clusterScope.ClearBastionStatus()
		if r.Recorder != nil {
			r.Recorder.Eventf(stackitCluster, nil, corev1.EventTypeNormal, "BastionDeleted", "Delete", "Deleted bastion")
		}
	}
	controllerutil.RemoveFinalizer(stackitCluster, infrav1.ClusterFinalizer)
	return ctrl.Result{}, nil
}
