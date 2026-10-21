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
	"errors"
	"fmt"
	"net/netip"
	"slices"
	"strings"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	"sigs.k8s.io/cluster-api/util/collections"
	"sigs.k8s.io/cluster-api/util/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	infrav1 "github.com/stackitcloud/cluster-api-provider-stackit/api/v1alpha1"
	"github.com/stackitcloud/cluster-api-provider-stackit/cloud"
	bastionservice "github.com/stackitcloud/cluster-api-provider-stackit/cloud/services/bastion"
	loadbalancerservice "github.com/stackitcloud/cluster-api-provider-stackit/cloud/services/loadbalancer"
	networkservice "github.com/stackitcloud/cluster-api-provider-stackit/cloud/services/network"
	"github.com/stackitcloud/cluster-api-provider-stackit/scope"
	"github.com/stackitcloud/cluster-api-provider-stackit/util"
)

func (r *StackitClusterReconciler) reconcileNormal(ctx context.Context, clusterScope *scope.ClusterScope) (ctrl.Result, error) {
	log := logf.FromContext(ctx)
	stackitCluster := clusterScope.StackitCluster

	if !controllerutil.ContainsFinalizer(stackitCluster, infrav1.ClusterFinalizer) {
		controllerutil.AddFinalizer(stackitCluster, infrav1.ClusterFinalizer)
		// Persisted immediately, before any cloud resource can be created, to
		// ensure nothing is running behind an object that carries no finalizer to
		// clean it up.
		if err := clusterScope.PatchObject(ctx); err != nil {
			return ctrl.Result{}, fmt.Errorf("persist finalizer: %w", err)
		}
	}
	clusterScope.SetFailureDomains(stackitFailureDomains(stackitCluster.Spec.Region))

	cloudClient, err := util.BuildCloudClient(ctx, r.Client, r.CloudClientFactory, stackitCluster)
	if err != nil {
		log.Error(err, "Failed to build cloud client for cluster", "cluster", stackitCluster.Name)
		record.Warnf(stackitCluster, "InvalidCredentials", "Failed to build cloud client: %v", err)
		stackitCluster.Status.Ready = false
		return util.CredentialFailureResult(
			&stackitCluster.Status.Conditions,
			stackitCluster.Generation,
			err,
			credentialsRetryRequeueAfter,
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

	zones, err := cloudClient.ListAvailabilityZones(ctx)
	if err != nil {
		stackitCluster.Status.Ready = false
		return util.CloudFailureResult(
			&stackitCluster.Status.Conditions,
			stackitCluster.Generation,
			"AvailabilityZonesError",
			err,
			retryableErrorRequeueAfter,
			true,
			infrav1.ClusterReadyCondition,
		)
	}
	clusterScope.SetFailureDomains(failureDomainsFromZones(stackitCluster.Spec.Region, zones))

	var network *cloud.Network
	if stackitCluster.Spec.Network.ID != "" {
		var err error
		network, err = cloudClient.GetNetwork(ctx, stackitCluster.Spec.Network.ID)
		if err != nil {
			log.Error(err, "Failed to get network",
				"networkID", stackitCluster.Spec.Network.ID,
				"region", stackitCluster.Spec.Region,
			)
			record.Warnf(
				stackitCluster, "FailedGetNetwork",
				"Failed to get network %s in region %s: %v",
				stackitCluster.Spec.Network.ID, stackitCluster.Spec.Region, err,
			)
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
		stackitCluster.Status.Network = infrav1.StackitNetworkStatus{
			ID:      network.ID,
			Managed: false,
		}
	} else {
		// Managed network mode: resolve or create dedicated network
		var err error
		if stackitCluster.Status.Network.ID != "" {
			network, err = cloudClient.GetNetwork(ctx, stackitCluster.Status.Network.ID)
			if err != nil && !cloud.IsNotFound(err) {
				log.Error(err, "Failed to get managed network",
					"networkID", stackitCluster.Status.Network.ID,
					"region", stackitCluster.Spec.Region,
				)
				return util.CloudFailureResult(
					&stackitCluster.Status.Conditions,
					stackitCluster.Generation,
					"NetworkError",
					err,
					retryableErrorRequeueAfter,
					false,
					infrav1.ClusterNetworkReadyCondition,
					infrav1.ClusterReadyCondition,
				)
			}
		}

		if network == nil {
			networkInput := networkservice.Input(stackitCluster)
			network, err = cloudClient.CreateNetwork(ctx, networkInput)
			if err != nil {
				log.Error(err, "Failed to create managed network",
					"networkName", networkInput.Name,
					"region", stackitCluster.Spec.Region,
				)
				record.Warnf(
					stackitCluster, "FailedCreateNetwork",
					"Failed to create managed network %s in region %s: %v",
					networkInput.Name, stackitCluster.Spec.Region, err,
				)
				stackitCluster.Status.Ready = false
				return util.CloudFailureResult(
					&stackitCluster.Status.Conditions,
					stackitCluster.Generation,
					"NetworkCreateError",
					err,
					retryableErrorRequeueAfter,
					false,
					infrav1.ClusterNetworkReadyCondition,
					infrav1.ClusterReadyCondition,
				)
			}
			record.Eventf(stackitCluster, "NetworkCreated", "Created managed network %s", network.ID)
		}

		stackitCluster.Status.Network = infrav1.StackitNetworkStatus{
			ID:      network.ID,
			Managed: true,
		}
	}
	clusterScope.SetConditions(
		metav1.ConditionTrue,
		"Available",
		"",
		infrav1.ClusterNetworkReadyCondition,
	)

	if stackitCluster.Spec.APIServerLoadBalancer.Enabled {
		// STACKIT takes an explicit target list rather than label-based
		// membership, so exactly one reconciler owns the pool. Machines being
		// deleted drop out here, before the node is drained.
		machines, err := collections.GetFilteredMachinesForCluster(
			ctx,
			r.Client,
			clusterScope.Cluster,
			collections.ControlPlaneMachines(clusterScope.Cluster.Name),
			collections.ActiveMachines,
		)
		if err != nil {
			util.SetConditions(
				&stackitCluster.Status.Conditions,
				stackitCluster.Generation,
				metav1.ConditionFalse,
				"MachineListError",
				err.Error(),
				infrav1.ClusterLoadBalancerReadyCondition,
			)
			return ctrl.Result{}, fmt.Errorf("list control plane machines: %w", err)
		}
		targets := loadbalancerservice.APIServerTargets(machines.UnsortedList())

		// Seeding with the real targets lets a load balancer deleted out of band
		// come back with the control plane behind it.
		seed := targets
		if len(seed) == 0 {
			seed = []cloud.LoadBalancerTargetInput{loadbalancerservice.BootstrapTarget(bootstrapTargetIP(network))}
		}
		loadBalancer, err := cloudClient.EnsureAPIServerLoadBalancer(
			ctx,
			loadbalancerservice.APIServerInput(stackitCluster, clusterScope.NetworkID(), seed),
		)
		if err != nil {
			log.Error(err, "Failed to create API server load balancer",
				"cluster", stackitCluster.Name,
				"region", stackitCluster.Spec.Region,
			)
			record.Warnf(
				stackitCluster, "FailedCreateLoadBalancer",
				"Failed to create API server load balancer: %v", err,
			)
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
			if !hadLoadBalancerID && loadBalancer.ID != "" {
				record.Eventf(
					stackitCluster, "LoadBalancerCreated",
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

		isLBReady := meta.IsStatusConditionTrue(stackitCluster.Status.Conditions, infrav1.ClusterLoadBalancerReadyCondition) &&
			stackitCluster.Status.APIServerEndpoint.Host == loadBalancer.IP

		endpoint := clusterv1.APIEndpoint{
			Host: loadBalancer.IP,
			Port: defaultAPIServerPort,
		}
		clusterScope.SetAPIServerEndpoint(endpoint)

		// An empty set never overwrites a populated pool: cluster deletion filters
		// out every machine while the API server is still needed for drain.
		if len(targets) > 0 {
			if err := cloudClient.SetAPIServerLoadBalancerTargets(ctx, loadBalancer.ID, defaultAPIServerPort, targets); err != nil {
				if !cloud.IsRetryable(err) {
					record.Warnf(
						stackitCluster, "LoadBalancerTargetError",
						"Cannot update API server load balancer target pool: %v", err,
					)
				}
				// Scoped to the load balancer condition: failing the cluster would
				// stop the machine reconciler from replacing the broken machine.
				util.SetConditions(
					&stackitCluster.Status.Conditions,
					stackitCluster.Generation,
					metav1.ConditionFalse,
					"LoadBalancerTargetError",
					err.Error(),
					infrav1.ClusterLoadBalancerReadyCondition,
				)
				// Requeue even on a permanent error: this return skips SetReady, so
				// no machine exists yet whose events could retry it.
				return ctrl.Result{RequeueAfter: retryableErrorRequeueAfter}, nil
			}
			log.V(1).Info("Reconciled API server load balancer target pool", "targets", len(targets))
		}

		if !isLBReady {
			clusterScope.SetConditions(
				metav1.ConditionTrue,
				"Available",
				"",
				infrav1.ClusterLoadBalancerReadyCondition,
			)
			record.Eventf(
				stackitCluster, "LoadBalancerReady",
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
		log.Error(errors.New("controlPlaneEndpoint host is empty"), "API server load balancer is disabled and controlPlaneEndpoint is empty", "cluster", stackitCluster.Name)
		record.Warn(stackitCluster, "EndpointMissing", "apiServerLoadBalancer.enabled is false and controlPlaneEndpoint is empty")
		clusterScope.SetNotReady(
			"EndpointMissing",
			"apiServerLoadBalancer.enabled is false and controlPlaneEndpoint is empty",
			infrav1.ClusterLoadBalancerReadyCondition,
			infrav1.ClusterReadyCondition,
		)
		return ctrl.Result{}, nil
	}

	if result, ready, err := r.reconcileBastion(ctx, cloudClient, clusterScope); err != nil {
		log.Error(err, "Failed to reconcile bastion",
			"cluster", stackitCluster.Name,
		)
		record.Warnf(
			stackitCluster, "FailedCreateBastion",
			"Failed to reconcile bastion: %v", err,
		)
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

func failureDomainsFromZones(region string, zones []string) []clusterv1.FailureDomain {
	controlPlane := true
	failureDomains := make([]clusterv1.FailureDomain, 0, len(zones))
	for _, zone := range zones {
		// Filter out virtual/metro zones (such as eu01-m or any zone with suffix -m)
		if strings.HasSuffix(zone, "-m") {
			continue
		}
		failureDomains = append(failureDomains, clusterv1.FailureDomain{
			Name:         zone,
			ControlPlane: &controlPlane,
			Attributes: map[string]string{
				"region": region,
			},
		})
	}
	if len(failureDomains) == 0 {
		return stackitFailureDomains(region)
	}
	slices.SortFunc(failureDomains, func(a, b clusterv1.FailureDomain) int {
		return strings.Compare(a.Name, b.Name)
	})
	return failureDomains
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

func (r *StackitClusterReconciler) reconcileDelete(ctx context.Context, clusterScope *scope.ClusterScope) (ctrl.Result, error) {
	log := logf.FromContext(ctx)
	stackitCluster := clusterScope.StackitCluster

	// Machines resolve their credentials and project context through this
	// StackitCluster, so the finalizer has to stay while any of them remain.
	machines, err := collections.GetFilteredMachinesForCluster(ctx, r.Client, clusterScope.Cluster)
	if err != nil {
		log.Error(err, "Failed to list Machines for cluster during deletion", "cluster", clusterScope.Cluster.Name)
		record.Warnf(stackitCluster, "FailedListMachines",
			"Failed to list Machines for cluster %s: %v", clusterScope.Cluster.Name, err)
		return ctrl.Result{}, fmt.Errorf("list Machines for cluster %s: %w", clusterScope.Cluster.Name, err)
	}
	if machines.Len() > 0 {
		log.Info(
			"Waiting for Machines to be deleted before removing the StackitCluster finalizer",
			"remainingMachines", machines.Len(),
		)
		return ctrl.Result{RequeueAfter: deleteRequeueAfter}, nil
	}

	// Cleanup runs unconditionally: neither spec nor status is a trustworthy
	// record of what exists in the cloud. ResolveID, DeleteBastion and
	// DeleteNodeSSHAccess fall back to tag lookups and tolerate NotFound.
	cloudClient, err := util.BuildCloudClient(ctx, r.Client, r.CloudClientFactory, stackitCluster)
	if err != nil {
		// A missing credentials Secret cannot be recovered from and commonly
		// disappears first during namespace teardown, so finalize and make the
		// possible leak loud rather than stranding the cluster in Terminating.
		// Every other credentials problem is fixable and keeps retrying.
		if apierrors.IsNotFound(err) {
			log.Error(err, "Credentials secret is gone; finalizing without cloud cleanup", "cluster", stackitCluster.Name)
			record.Warnf(stackitCluster, "CleanupSkipped",
				"Credentials Secret is gone; finalizing without cloud cleanup. "+
					"Any remaining STACKIT resources for this cluster must be removed manually: %v", err)
			controllerutil.RemoveFinalizer(stackitCluster, infrav1.ClusterFinalizer)
			return ctrl.Result{}, nil
		}
		log.Error(err, "Failed to build cloud client for cluster deletion", "cluster", stackitCluster.Name)
		record.Warnf(stackitCluster, "InvalidCredentials",
			"Failed to build cloud client for deletion: %v", err)
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
		log.Error(err, "Failed to resolve API server load balancer for deletion", "cluster", stackitCluster.Name)
		record.Warnf(stackitCluster, "FailedResolveLoadBalancer",
			"Failed to resolve API server load balancer: %v", err)
		return ctrl.Result{}, err
	}
	if loadBalancerID != "" {
		if err := cloudClient.DeleteAPIServerLoadBalancer(ctx, loadBalancerID); err != nil && !cloud.IsNotFound(err) {
			log.Error(err, "Failed to delete API server load balancer", "cluster", stackitCluster.Name, "loadBalancerID", loadBalancerID)
			record.Warnf(stackitCluster, "FailedDeleteLoadBalancer",
				"Failed to delete API server load balancer %s: %v", loadBalancerID, err)
			return ctrl.Result{}, err
		}
		stackitCluster.Status.APIServerLoadBalancerID = ""
		record.Eventf(
			stackitCluster, "LoadBalancerDeleted",
			"Deleted API server load balancer %s", loadBalancerID,
		)
	}
	if err := cloudClient.DeleteNodeSSHAccess(ctx, bastionservice.NodeSSHAccessTags(stackitCluster)); err != nil && !cloud.IsNotFound(err) {
		log.Error(err, "Failed to delete bastion node SSH access", "cluster", stackitCluster.Name)
		record.Warnf(stackitCluster, "FailedDeleteNodeSSHAccess",
			"Failed to delete bastion node SSH access: %v", err)
		return ctrl.Result{}, err
	}
	if err := cloudClient.DeleteBastion(ctx, bastionservice.Input(stackitCluster, clusterScope.NetworkID(), nil), cloud.Bastion{
		ServerID:        stackitCluster.Status.Bastion.ServerID,
		PublicIPID:      stackitCluster.Status.Bastion.PublicIPID,
		PublicIP:        stackitCluster.Status.Bastion.PublicIP,
		SecurityGroupID: stackitCluster.Status.Bastion.SecurityGroupID,
	}); err != nil && !cloud.IsNotFound(err) {
		log.Error(err, "Failed to delete bastion", "cluster", stackitCluster.Name)
		record.Warnf(stackitCluster, "FailedDeleteBastion",
			"Failed to delete bastion: %v", err)
		return ctrl.Result{}, err
	}
	if hasBastionStatus(stackitCluster.Status.Bastion) {
		clusterScope.ClearBastionStatus()
		record.Event(stackitCluster, "BastionDeleted", "Deleted bastion")
	}
	if stackitCluster.Status.Network.Managed && stackitCluster.Status.Network.ID != "" {
		networkID := stackitCluster.Status.Network.ID
		if err := cloudClient.DeleteNetwork(ctx, networkID); err != nil && !cloud.IsNotFound(err) {
			if cloud.IsConflict(err) {
				log.Info("Managed network still has resources attached; requeueing deletion",
					"cluster", stackitCluster.Name,
					"networkID", networkID,
				)
				return ctrl.Result{RequeueAfter: deleteRequeueAfter}, nil
			}
			log.Error(err, "Failed to delete managed network",
				"cluster", stackitCluster.Name,
				"networkID", networkID,
			)
			record.Warnf(stackitCluster, "FailedDeleteNetwork",
				"Failed to delete managed network %s: %v", networkID, err)
			return ctrl.Result{}, err
		}
		record.Eventf(stackitCluster, "NetworkDeleted", "Deleted managed network %s", networkID)
		stackitCluster.Status.Network = infrav1.StackitNetworkStatus{}
	}

	controllerutil.RemoveFinalizer(stackitCluster, infrav1.ClusterFinalizer)
	return ctrl.Result{}, nil
}
