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
	"net/netip"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	infrav1 "github.com/stackitcloud/cluster-api-provider-stackit/api/v1alpha1"
	"github.com/stackitcloud/cluster-api-provider-stackit/cloud"
	bastionservice "github.com/stackitcloud/cluster-api-provider-stackit/cloud/services/bastion"
	loadbalancerservice "github.com/stackitcloud/cluster-api-provider-stackit/cloud/services/loadbalancer"
	"github.com/stackitcloud/cluster-api-provider-stackit/scope"
	"github.com/stackitcloud/cluster-api-provider-stackit/util"
)

func (r *StackitClusterReconciler) reconcileNormal(ctx context.Context, s *scope.ClusterScope) (ctrl.Result, error) {
	log := logf.FromContext(ctx)
	sc := s.StackitCluster

	if !controllerutil.ContainsFinalizer(sc, infrav1.ClusterFinalizer) {
		controllerutil.AddFinalizer(sc, infrav1.ClusterFinalizer)
	}
	sc.Status.FailureDomains = stackitFailureDomains(sc.Spec.Region)

	cloudClient, err := util.BuildCloudClient(ctx, r.Client, r.CloudClientFactory, sc)
	if err != nil {
		sc.Status.Ready = false
		return util.CredentialFailureResult(
			&sc.Status.Conditions,
			sc.Generation,
			err,
			infrav1.ClusterCredentialsReadyCondition,
			infrav1.ClusterReadyCondition,
		)
	}
	s.SetConditions(metav1.ConditionTrue, "Available", "", infrav1.ClusterCredentialsReadyCondition)

	network, err := cloudClient.GetNetwork(ctx, sc.Spec.Network.ID)
	if err != nil {
		sc.Status.Ready = false
		return util.CloudFailureResult(
			&sc.Status.Conditions,
			sc.Generation,
			"NetworkNotFound",
			err,
			retryableErrorRequeueAfter,
			false,
			infrav1.ClusterNetworkReadyCondition,
			infrav1.ClusterReadyCondition,
		)
	}
	s.SetConditions(metav1.ConditionTrue, "Available", "", infrav1.ClusterNetworkReadyCondition)

	if sc.Spec.APIServerLoadBalancer.Enabled {
		lb, err := cloudClient.EnsureAPIServerLoadBalancer(
			ctx,
			loadbalancerservice.APIServerInput(
				sc,
				[]cloud.LoadBalancerTargetInput{loadbalancerservice.BootstrapTarget(bootstrapTargetIP(network))},
			),
		)
		if err != nil {
			sc.Status.Ready = false
			return util.CloudFailureResult(
				&sc.Status.Conditions,
				sc.Generation,
				"LoadBalancerError",
				err,
				retryableErrorRequeueAfter,
				false,
				infrav1.ClusterLoadBalancerReadyCondition,
				infrav1.ClusterReadyCondition,
			)
		}
		hadLoadBalancerID := sc.Status.APIServerLoadBalancerID != ""
		if lb != nil {
			sc.Status.APIServerLoadBalancerID = lb.ID
			if !hadLoadBalancerID && lb.ID != "" && r.Recorder != nil {
				r.Recorder.Eventf(
					sc, nil, corev1.EventTypeNormal, "LoadBalancerCreated", "Create",
					"Created API server load balancer %s", lb.ID,
				)
			}
		}
		if lb == nil || lb.IP == "" {
			s.SetNotReady("Provisioning", "waiting for API server load balancer IP address", infrav1.ClusterLoadBalancerReadyCondition, infrav1.ClusterReadyCondition)
			return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
		}
		endpoint := clusterv1.APIEndpoint{
			Host: lb.IP,
			Port: defaultAPIServerPort,
		}
		s.SetAPIServerEndpoint(endpoint)
		s.SetConditions(metav1.ConditionTrue, "Available", "", infrav1.ClusterLoadBalancerReadyCondition)
		if r.Recorder != nil {
			r.Recorder.Eventf(
				sc, nil, corev1.EventTypeNormal, "LoadBalancerReady", "SetReady",
				"API server load balancer is ready at %s", lb.IP,
			)
		}
	} else if sc.Spec.ControlPlaneEndpoint.Host != "" {
		sc.Status.APIServerEndpoint = sc.Spec.ControlPlaneEndpoint
		s.SetConditions(metav1.ConditionTrue, "Skipped", "external endpoint provided", infrav1.ClusterLoadBalancerReadyCondition)
	} else {
		s.SetNotReady("EndpointMissing", "apiServerLoadBalancer.enabled is false and controlPlaneEndpoint is empty", infrav1.ClusterLoadBalancerReadyCondition, infrav1.ClusterReadyCondition)
		return ctrl.Result{}, nil
	}

	if result, ready, err := r.reconcileBastion(ctx, cloudClient, s); err != nil {
		sc.Status.Ready = false
		return util.CloudFailureResult(
			&sc.Status.Conditions,
			sc.Generation,
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

	s.SetReady()
	log.V(1).Info("StackitCluster ready", "endpoint", sc.Status.APIServerEndpoint)
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

func (r *StackitClusterReconciler) reconcileDelete(ctx context.Context, s *scope.ClusterScope) error {
	sc := s.StackitCluster
	if sc.Status.APIServerLoadBalancerID != "" || hasBastionStatus(sc.Status.Bastion) || sc.Spec.APIServerLoadBalancer.Enabled {
		cloudClient, err := util.BuildCloudClient(ctx, r.Client, r.CloudClientFactory, sc)
		if err != nil {
			util.SetConditions(
				&sc.Status.Conditions,
				sc.Generation,
				metav1.ConditionFalse,
				"CredentialsInvalid",
				err.Error(),
				infrav1.ClusterCredentialsReadyCondition,
			)
			return err
		}
		loadBalancerID, err := loadbalancerservice.ResolveID(ctx, cloudClient, sc)
		if err != nil {
			return err
		}
		if loadBalancerID != "" {
			if err := cloudClient.DeleteAPIServerLoadBalancer(ctx, loadBalancerID); err != nil && !cloud.IsNotFound(err) {
				return err
			}
			sc.Status.APIServerLoadBalancerID = ""
			if r.Recorder != nil {
				r.Recorder.Eventf(
					sc, nil, corev1.EventTypeNormal, "LoadBalancerDeleted", "Delete",
					"Deleted API server load balancer %s", loadBalancerID,
				)
			}
		}
		if hasBastionStatus(sc.Status.Bastion) {
			if err := cloudClient.DeleteNodeSSHAccess(ctx, bastionservice.NodeSSHAccessTags(sc)); err != nil && !cloud.IsNotFound(err) {
				return err
			}
			if err := cloudClient.DeleteBastion(ctx, bastionservice.Input(sc, nil), cloud.Bastion{
				ServerID:        sc.Status.Bastion.ServerID,
				PublicIPID:      sc.Status.Bastion.PublicIPID,
				PublicIP:        sc.Status.Bastion.PublicIP,
				SecurityGroupID: sc.Status.Bastion.SecurityGroupID,
			}); err != nil && !cloud.IsNotFound(err) {
				return err
			}
			s.ClearBastionStatus()
			if r.Recorder != nil {
				r.Recorder.Eventf(sc, nil, corev1.EventTypeNormal, "BastionDeleted", "Delete", "Deleted bastion")
			}
		}
	}
	controllerutil.RemoveFinalizer(sc, infrav1.ClusterFinalizer)
	return nil
}
