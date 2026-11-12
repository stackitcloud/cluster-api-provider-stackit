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
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/cluster-api/util/collections"
	"sigs.k8s.io/cluster-api/util/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	infrav1 "github.com/stackitcloud/cluster-api-provider-stackit/api/v1alpha1"
	"github.com/stackitcloud/cluster-api-provider-stackit/cloud/services/availabilityzone"
	"github.com/stackitcloud/cluster-api-provider-stackit/cloud/services/loadbalancer"
	"github.com/stackitcloud/cluster-api-provider-stackit/cloud/services/network"
	"github.com/stackitcloud/cluster-api-provider-stackit/cloud/services/publicip"
	"github.com/stackitcloud/cluster-api-provider-stackit/cloud/services/securitygroup"
	"github.com/stackitcloud/cluster-api-provider-stackit/cloud/services/server"
	"github.com/stackitcloud/cluster-api-provider-stackit/cloud/services/shared"
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
	clusterScope.SetFailureDomains(scope.DefaultFailureDomains(stackitCluster.Spec.Region))

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
	clusterScope.SetCloudClient(cloudClient)
	clusterScope.SetConditions(
		metav1.ConditionTrue,
		"Available",
		"",
		infrav1.ClusterCredentialsReadyCondition,
	)

	if err := availabilityzone.New(clusterScope).Reconcile(ctx); err != nil {
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

	if err := network.New(clusterScope).Reconcile(ctx); err != nil {
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
		targets := loadbalancer.APIServerTargets(machines.UnsortedList())

		// Seeding with the real targets lets a load balancer deleted out of band
		// come back with the control plane behind it.
		seed := targets
		if len(seed) == 0 {
			seed = []loadbalancer.Target{loadbalancer.BootstrapTarget(clusterScope.BootstrapTargetIP())}
		}
		clusterScope.SetAPIServerLoadBalancerTargets(targets, seed)

		hadLoadBalancerID := stackitCluster.Status.APIServerLoadBalancerID != ""
		previousEndpointHost := stackitCluster.Status.APIServerEndpoint.Host
		wasLBReady := meta.IsStatusConditionTrue(stackitCluster.Status.Conditions, infrav1.ClusterLoadBalancerReadyCondition)

		err = loadbalancer.New(clusterScope).Reconcile(ctx)
		if !hadLoadBalancerID && stackitCluster.Status.APIServerLoadBalancerID != "" {
			record.Eventf(
				stackitCluster, "LoadBalancerCreated",
				"Created API server load balancer %s", stackitCluster.Status.APIServerLoadBalancerID,
			)
		}
		if loadbalancer.IsTargetPoolUpdateError(err) {
			if !shared.IsRetryable(err) {
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
		if stackitCluster.Status.APIServerEndpoint.Host == "" {
			clusterScope.SetNotReady(
				"Provisioning",
				"waiting for API server load balancer IP address",
				infrav1.ClusterLoadBalancerReadyCondition,
				infrav1.ClusterReadyCondition,
			)
			return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
		}

		isLBReady := wasLBReady && previousEndpointHost == stackitCluster.Status.APIServerEndpoint.Host
		if !isLBReady {
			clusterScope.SetConditions(
				metav1.ConditionTrue,
				"Available",
				"",
				infrav1.ClusterLoadBalancerReadyCondition,
			)
			record.Eventf(
				stackitCluster, "LoadBalancerReady",
				"API server load balancer is ready at %s", stackitCluster.Status.APIServerEndpoint.Host,
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

	if result, ready, err := r.reconcileBastion(ctx, clusterScope); err != nil {
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
	// record of what exists in the cloud. The load balancer, public IP,
	// security group, and server services fall back to tag lookups and tolerate
	// NotFound.
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
	clusterScope.SetCloudClient(cloudClient)
	loadBalancerID := stackitCluster.Status.APIServerLoadBalancerID
	if err := loadbalancer.New(clusterScope).Delete(ctx); err != nil && !shared.IsNotFound(err) {
		log.Error(err, "Failed to delete API server load balancer", "cluster", stackitCluster.Name, "loadBalancerID", loadBalancerID)
		record.Warnf(stackitCluster, "FailedDeleteLoadBalancer",
			"Failed to delete API server load balancer %s: %v", loadBalancerID, err)
		return ctrl.Result{}, err
	}
	if loadBalancerID != "" {
		record.Eventf(
			stackitCluster, "LoadBalancerDeleted",
			"Deleted API server load balancer %s", loadBalancerID,
		)
	}
	hadBastion := hasBastionStatus(stackitCluster.Status.Bastion)
	if err := publicip.New(clusterScope).Delete(ctx); err != nil && !shared.IsNotFound(err) {
		log.Error(err, "Failed to delete bastion public IP", "cluster", stackitCluster.Name)
		record.Warnf(stackitCluster, "FailedDeleteBastion",
			"Failed to delete bastion public IP: %v", err)
		return ctrl.Result{}, err
	}
	if err := securitygroup.New(clusterScope).Delete(ctx); err != nil && !shared.IsNotFound(err) {
		log.Error(err, "Failed to delete bastion security groups", "cluster", stackitCluster.Name)
		record.Warnf(stackitCluster, "FailedDeleteBastion",
			"Failed to delete bastion security groups: %v", err)
		return ctrl.Result{}, err
	}
	if err := server.New(clusterScope).Delete(ctx); err != nil && !shared.IsNotFound(err) {
		log.Error(err, "Failed to delete bastion", "cluster", stackitCluster.Name)
		record.Warnf(stackitCluster, "FailedDeleteBastion",
			"Failed to delete bastion: %v", err)
		return ctrl.Result{}, err
	}
	if hadBastion {
		clusterScope.ClearBastionStatus()
		record.Event(stackitCluster, "BastionDeleted", "Deleted bastion")
	}
	controllerutil.RemoveFinalizer(stackitCluster, infrav1.ClusterFinalizer)
	return ctrl.Result{}, nil
}
