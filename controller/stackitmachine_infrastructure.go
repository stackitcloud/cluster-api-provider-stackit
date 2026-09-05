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

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
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

var (
	// If the server was there and is gone, cloud.ErrNotFound shouldn't be used,
	// as the state is final.
	errInstanceGone = errors.New("instance gone")
)

func (r *StackitMachineReconciler) reconcileNormal(ctx context.Context, s *scope.MachineScope) (ctrl.Result, error) {
	log := logf.FromContext(ctx)
	sm := s.StackitMachine

	if !controllerutil.ContainsFinalizer(sm, infrav1.MachineFinalizer) {
		controllerutil.AddFinalizer(sm, infrav1.MachineFinalizer)
	}

	if !s.StackitCluster.Status.Ready {
		s.SetNotReady("InfrastructureNotReady", "waiting for StackitCluster to be ready", infrav1.MachineReadyCondition)
		return ctrl.Result{}, nil
	}
	if err := validateMachineAvailabilityZone(s); err != nil {
		s.SetNotReady("InvalidFailureDomain", err.Error(), infrav1.MachineInstanceReadyCondition, infrav1.MachineReadyCondition)
		return ctrl.Result{}, nil
	}

	bootstrapData, condStatus, reason, msg := r.fetchBootstrapData(ctx, s.Machine)
	s.SetConditions(condStatus, reason, msg, infrav1.MachineBootstrapReadyCondition)
	if condStatus != metav1.ConditionTrue {
		s.SetConditions(metav1.ConditionFalse, reason, msg, infrav1.MachineReadyCondition)
		if reason == util.BootstrapReasonInvalid {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{RequeueAfter: retryableErrorRequeueAfter}, nil
	}

	cloudClient, err := util.BuildCloudClient(ctx, r.Client, r.CloudClientFactory, s.StackitCluster)
	if err != nil {
		return util.CredentialFailureResult(
			&sm.Status.Conditions,
			sm.Generation,
			err,
			infrav1.MachineCredentialsReadyCondition,
			infrav1.MachineReadyCondition,
		)
	}
	s.SetConditions(metav1.ConditionTrue, "Available", "", infrav1.MachineCredentialsReadyCondition)

	server, created, err := r.ensureServer(ctx, cloudClient, s, bootstrapData)
	if errors.Is(err, errInstanceGone) {
		s.SetNotReady("InstanceNotFound", err.Error(), infrav1.MachineInstanceReadyCondition, infrav1.MachineReadyCondition)
		if r.Recorder != nil {
			r.Recorder.Eventf(
				sm, nil, corev1.EventTypeWarning, "InstanceNotFound", "Reconcile",
				"Server %s no longer exists; the Machine must be replaced", sm.Status.InstanceID,
			)
		}
		return ctrl.Result{}, nil
	}
	if err != nil {
		s.SetNotReady("InstanceError", err.Error(), infrav1.MachineInstanceReadyCondition, infrav1.MachineReadyCondition)
		return util.CloudFailureResult(
			&sm.Status.Conditions,
			sm.Generation,
			"InstanceError",
			err,
			retryableErrorRequeueAfter,
			true,
			infrav1.MachineInstanceReadyCondition,
			infrav1.MachineReadyCondition,
		)
	}
	if created && r.Recorder != nil {
		r.Recorder.Eventf(
			sm, nil, corev1.EventTypeNormal, "InstanceCreated", "Create", "Created instance %s", server.ID,
		)
	}

	sm.Status.InstanceState = server.State
	sm.Status.Addresses = machineAddressesFromCloud(server.Addresses)
	providerID := s.SetInstance(server)

	if server.State != "" && server.State != "ACTIVE" {
		s.SetNotReady("Provisioning", fmt.Sprintf("server state is %s", server.State), infrav1.MachineInstanceReadyCondition, infrav1.MachineReadyCondition)
		return ctrl.Result{RequeueAfter: 15 * time.Second}, nil
	}

	if err := r.reconcileBastionNodeSSHAccess(ctx, cloudClient, s, server); err != nil {
		s.SetNotReady("BastionSSHAccessError", err.Error(), infrav1.MachineReadyCondition)
		return util.CloudFailureResult(
			&sm.Status.Conditions,
			sm.Generation,
			"BastionSSHAccessError",
			err,
			retryableErrorRequeueAfter,
			true,
			infrav1.MachineReadyCondition,
		)
	}

	if err := r.reconcileAPIServerLoadBalancerTarget(ctx, cloudClient, s, server); err != nil {
		s.SetNotReady("LoadBalancerTargetError", err.Error(), infrav1.MachineReadyCondition)
		return util.CloudFailureResult(
			&sm.Status.Conditions,
			sm.Generation,
			"LoadBalancerTargetError",
			err,
			retryableErrorRequeueAfter,
			true,
			infrav1.MachineReadyCondition,
		)
	}

	s.SetReady()
	log.V(1).Info("StackitMachine ready", "providerID", providerID)
	return ctrl.Result{}, nil
}

func validateMachineAvailabilityZone(s *scope.MachineScope) error {
	availabilityZone := s.StackitMachine.Spec.AvailabilityZone
	if availabilityZone == "" || len(s.StackitCluster.Status.FailureDomains) == 0 {
		return nil
	}
	for _, failureDomain := range s.StackitCluster.Status.FailureDomains {
		if failureDomain.Name == availabilityZone {
			return nil
		}
	}
	return fmt.Errorf("availabilityZone %q is not published in StackitCluster status.failureDomains", availabilityZone)
}

func (r *StackitMachineReconciler) reconcileDelete(ctx context.Context, s *scope.MachineScope) error {
	sm := s.StackitMachine
	needsLoadBalancerCleanup := isControlPlaneMachine(s.Machine) &&
		s.StackitCluster.Spec.APIServerLoadBalancer.Enabled &&
		s.StackitCluster.Status.APIServerLoadBalancerID != ""
	if sm.Status.InstanceID == "" && !needsLoadBalancerCleanup {
		controllerutil.RemoveFinalizer(sm, infrav1.MachineFinalizer)
		if r.Recorder != nil {
			r.Recorder.Eventf(sm, nil, corev1.EventTypeNormal, "InstanceDeleted", "Delete", "Deleted instance")
		}
		return nil
	}
	cloudClient, err := util.BuildCloudClient(ctx, r.Client, r.CloudClientFactory, s.StackitCluster)
	if err != nil {
		_, resultErr := util.CredentialFailureResult(
			&sm.Status.Conditions,
			sm.Generation,
			err,
			infrav1.MachineCredentialsReadyCondition,
		)
		return resultErr
	}
	if err := r.deleteAPIServerLoadBalancerTarget(ctx, cloudClient, s); err != nil {
		return err
	}
	if sm.Status.InstanceID == "" {
		controllerutil.RemoveFinalizer(sm, infrav1.MachineFinalizer)
		if r.Recorder != nil {
			r.Recorder.Eventf(sm, nil, corev1.EventTypeNormal, "InstanceDeleted", "Delete", "Deleted instance")
		}
		return nil
	}
	instanceID := sm.Status.InstanceID
	if err := cloudClient.DeleteServer(ctx, instanceID); err != nil && !cloud.IsNotFound(err) {
		return err
	}
	s.ClearInstance()
	controllerutil.RemoveFinalizer(sm, infrav1.MachineFinalizer)
	if r.Recorder != nil {
		r.Recorder.Eventf(
			sm, nil, corev1.EventTypeNormal, "InstanceDeleted", "Delete", "Deleted instance %s", instanceID,
		)
	}
	return nil
}

func (r *StackitMachineReconciler) fetchBootstrapData(ctx context.Context, machine *clusterv1.Machine) ([]byte, metav1.ConditionStatus, string, string) {
	if machine.Spec.Bootstrap.DataSecretName == nil || *machine.Spec.Bootstrap.DataSecretName == "" {
		return nil, metav1.ConditionFalse, "BootstrapDataSecretMissing", "Machine.spec.bootstrap.dataSecretName is empty"
	}
	secret := &corev1.Secret{}
	key := types.NamespacedName{Namespace: machine.Namespace, Name: *machine.Spec.Bootstrap.DataSecretName}
	if err := r.Get(ctx, key, secret); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, metav1.ConditionFalse, "BootstrapDataSecretNotFound", fmt.Sprintf("bootstrap secret %s not found", key)
		}
		return nil, metav1.ConditionFalse, "BootstrapDataSecretError", err.Error()
	}
	data, err := util.ExtractBootstrapData(secret)
	if err != nil {
		return nil, metav1.ConditionFalse, util.BootstrapReasonInvalid, err.Error()
	}
	return data, metav1.ConditionTrue, "Available", ""
}

func (r *StackitMachineReconciler) ensureServer(
	ctx context.Context,
	c cloud.Client,
	s *scope.MachineScope,
	userData []byte,
) (*cloud.Server, bool, error) {
	sm := s.StackitMachine
	tags := s.Tags()
	if sm.Status.InstanceID != "" {
		server, err := c.GetServer(ctx, sm.Status.InstanceID)
		if err == nil {
			return server, false, nil
		}
		if !cloud.IsNotFound(err) {
			return nil, false, err
		}
	}
	if server, err := c.FindServerByTags(ctx, tags); err == nil {
		return server, false, nil
	} else if !cloud.IsNotFound(err) {
		return nil, false, err
	}

	// The machine had already been provisioned and its server has since
	// disappeared. Recreating it here would replay the original bootstrap data,
	// which is pinned to the previous identity: the replacement either never
	// rejoins (different IP) or rejoins while Machine/Node keep pointing at the
	// deleted server (same IP). Neither restores the cluster, and both consume
	// another VM silently. Surface it instead and let Cluster API decide to
	// replace the Machine.
	if sm.Status.Initialization.Provisioned {
		return nil, false, fmt.Errorf(
			"%w: server %s; the Machine must be replaced",
			errInstanceGone, sm.Status.InstanceID,
		)
	}

	deleteOnTermination := true
	if sm.Spec.RootVolume.DeleteOnTermination != nil {
		deleteOnTermination = *sm.Spec.RootVolume.DeleteOnTermination
	}
	server, err := c.CreateServer(ctx, cloud.CreateServerInput{
		Name:             sm.Name,
		ProjectID:        s.StackitCluster.Spec.ProjectID,
		Region:           s.StackitCluster.Spec.Region,
		ImageID:          sm.Spec.ImageID,
		MachineType:      sm.Spec.MachineType,
		AvailabilityZone: sm.Spec.AvailabilityZone,
		SSHKeyName:       sm.Spec.SSHKeyName,
		NetworkID:        sm.Spec.Network.ID,
		SecurityGroups:   sm.Spec.SecurityGroups,
		UserData:         userData,
		Tags:             tags,
		RootVolume: cloud.RootVolumeInput{
			SizeGiB:             sm.Spec.RootVolume.SizeGiB,
			PerformanceClass:    sm.Spec.RootVolume.PerformanceClass,
			DeleteOnTermination: deleteOnTermination,
		},
	})
	return server, true, err
}

func (r *StackitMachineReconciler) reconcileBastionNodeSSHAccess(
	ctx context.Context,
	c cloud.Client,
	s *scope.MachineScope,
	server *cloud.Server,
) error {
	if !s.StackitCluster.Spec.Bastion.Enabled {
		return nil
	}
	if s.StackitCluster.Status.Bastion.SecurityGroupID == "" {
		return fmt.Errorf("%w: bastion security group ID is empty", cloud.ErrTransient)
	}
	if server == nil || server.ID == "" {
		return fmt.Errorf("%w: server ID is empty", cloud.ErrTransient)
	}
	_, err := c.EnsureNodeSSHAccess(ctx, cloud.NodeSSHAccessInput{
		Name:                   s.StackitCluster.Name + "-node-ssh",
		ServerID:               server.ID,
		BastionSecurityGroupID: s.StackitCluster.Status.Bastion.SecurityGroupID,
		Tags:                   bastionservice.NodeSSHAccessTags(s.StackitCluster),
	})
	return err
}

func (r *StackitMachineReconciler) reconcileAPIServerLoadBalancerTarget(
	ctx context.Context,
	c cloud.Client,
	s *scope.MachineScope,
	server *cloud.Server,
) error {
	if !isControlPlaneMachine(s.Machine) || !s.StackitCluster.Spec.APIServerLoadBalancer.Enabled {
		return nil
	}
	loadBalancerID, err := loadbalancerservice.EnsureForMachine(
		ctx,
		c,
		s.StackitCluster,
		s.Machine.Name,
		server.Addresses,
	)
	if err != nil {
		return err
	}

	target, err := loadbalancerservice.TargetForMachine(s.Machine.Name, server.Addresses)
	if err != nil {
		return err
	}
	target.LoadBalancerID = loadBalancerID
	return c.EnsureAPIServerLoadBalancerTarget(ctx, target)
}

func (r *StackitMachineReconciler) deleteAPIServerLoadBalancerTarget(ctx context.Context, c cloud.Client, s *scope.MachineScope) error {
	if !isControlPlaneMachine(s.Machine) || !s.StackitCluster.Spec.APIServerLoadBalancer.Enabled {
		return nil
	}
	loadBalancerID, err := loadbalancerservice.ResolveID(ctx, c, s.StackitCluster)
	if err != nil {
		return err
	}
	if loadBalancerID == "" {
		return nil
	}
	err = c.DeleteAPIServerLoadBalancerTarget(ctx, cloud.LoadBalancerTargetInput{
		LoadBalancerID: loadBalancerID,
		Name:           s.Machine.Name,
		Port:           defaultAPIServerPort,
	})
	if cloud.IsNotFound(err) {
		return nil
	}
	return err
}

func (r *StackitMachineReconciler) getStackitCluster(ctx context.Context, cluster *clusterv1.Cluster) (*infrav1.StackitCluster, error) {
	if cluster.Spec.InfrastructureRef.Name == "" {
		return nil, nil
	}
	stackitCluster := &infrav1.StackitCluster{}
	key := types.NamespacedName{Namespace: cluster.Namespace, Name: cluster.Spec.InfrastructureRef.Name}
	if err := r.Get(ctx, key, stackitCluster); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("get StackitCluster %s: %w", key, err)
	}
	return stackitCluster, nil
}

func machineAddressesFromCloud(in []cloud.Address) []clusterv1.MachineAddress {
	if len(in) == 0 {
		return nil
	}

	out := make([]clusterv1.MachineAddress, len(in))
	for i, address := range in {
		out[i] = clusterv1.MachineAddress{
			Type:    clusterv1.MachineAddressType(address.Type),
			Address: address.Address,
		}
	}
	return out
}

func isControlPlaneMachine(machine *clusterv1.Machine) bool {
	if machine == nil {
		return false
	}
	_, ok := machine.Labels[clusterv1.MachineControlPlaneLabel]
	return ok
}
