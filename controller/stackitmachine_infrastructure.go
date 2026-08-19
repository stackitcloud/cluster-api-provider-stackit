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

func (r *StackitMachineReconciler) reconcileNormal(ctx context.Context, machineScope *scope.MachineScope) (ctrl.Result, error) {
	log := logf.FromContext(ctx)
	stackitMachine := machineScope.StackitMachine

	if !controllerutil.ContainsFinalizer(stackitMachine, infrav1.MachineFinalizer) {
		controllerutil.AddFinalizer(stackitMachine, infrav1.MachineFinalizer)
	}

	if !machineScope.StackitCluster.Status.Ready {
		machineScope.SetNotReady("InfrastructureNotReady", "waiting for StackitCluster to be ready", infrav1.MachineReadyCondition)
		return ctrl.Result{}, nil
	}
	if err := validateMachineAvailabilityZone(machineScope); err != nil {
		machineScope.SetNotReady(
			"InvalidFailureDomain",
			err.Error(),
			infrav1.MachineInstanceReadyCondition,
			infrav1.MachineReadyCondition,
		)
		return ctrl.Result{}, nil
	}

	bootstrapData, conditionStatus, reason, message := r.fetchBootstrapData(ctx, machineScope.Machine)
	machineScope.SetConditions(conditionStatus, reason, message, infrav1.MachineBootstrapReadyCondition)
	if conditionStatus != metav1.ConditionTrue {
		machineScope.SetConditions(metav1.ConditionFalse, reason, message, infrav1.MachineReadyCondition)
		if reason == util.BootstrapReasonInvalid {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{RequeueAfter: retryableErrorRequeueAfter}, nil
	}

	cloudClient, err := util.BuildCloudClient(ctx, r.Client, r.CloudClientFactory, machineScope.StackitCluster)
	if err != nil {
		return util.CredentialFailureResult(
			&stackitMachine.Status.Conditions,
			stackitMachine.Generation,
			err,
			infrav1.MachineCredentialsReadyCondition,
			infrav1.MachineReadyCondition,
		)
	}
	machineScope.SetConditions(metav1.ConditionTrue, "Available", "", infrav1.MachineCredentialsReadyCondition)

	server, created, err := r.ensureServer(ctx, cloudClient, machineScope, bootstrapData)
	if err != nil {
		machineScope.SetNotReady(
			"InstanceError",
			err.Error(),
			infrav1.MachineInstanceReadyCondition,
			infrav1.MachineReadyCondition,
		)
		return util.CloudFailureResult(
			&stackitMachine.Status.Conditions,
			stackitMachine.Generation,
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
			stackitMachine, nil, corev1.EventTypeNormal, "InstanceCreated", "Create", "Created instance %s", server.ID,
		)
	}

	stackitMachine.Status.InstanceState = server.State
	stackitMachine.Status.Addresses = machineAddressesFromCloud(server.Addresses)
	providerID := machineScope.SetInstance(server)

	if server.State != "" && server.State != "ACTIVE" {
		machineScope.SetNotReady(
			"Provisioning",
			fmt.Sprintf("server state is %s", server.State),
			infrav1.MachineInstanceReadyCondition,
			infrav1.MachineReadyCondition,
		)
		return ctrl.Result{RequeueAfter: 15 * time.Second}, nil
	}

	if err := r.reconcileBastionNodeSSHAccess(ctx, cloudClient, machineScope, server); err != nil {
		machineScope.SetNotReady("BastionSSHAccessError", err.Error(), infrav1.MachineReadyCondition)
		return util.CloudFailureResult(
			&stackitMachine.Status.Conditions,
			stackitMachine.Generation,
			"BastionSSHAccessError",
			err,
			retryableErrorRequeueAfter,
			true,
			infrav1.MachineReadyCondition,
		)
	}

	if err := r.reconcileAPIServerLoadBalancerTarget(ctx, cloudClient, machineScope, server); err != nil {
		machineScope.SetNotReady("LoadBalancerTargetError", err.Error(), infrav1.MachineReadyCondition)
		return util.CloudFailureResult(
			&stackitMachine.Status.Conditions,
			stackitMachine.Generation,
			"LoadBalancerTargetError",
			err,
			retryableErrorRequeueAfter,
			true,
			infrav1.MachineReadyCondition,
		)
	}

	machineScope.SetReady()
	log.V(1).Info("StackitMachine ready", "providerID", providerID)
	return ctrl.Result{}, nil
}

func validateMachineAvailabilityZone(machineScope *scope.MachineScope) error {
	availabilityZone := machineScope.StackitMachine.Spec.AvailabilityZone
	if availabilityZone == "" || len(machineScope.StackitCluster.Status.FailureDomains) == 0 {
		return nil
	}
	for _, failureDomain := range machineScope.StackitCluster.Status.FailureDomains {
		if failureDomain.Name == availabilityZone {
			return nil
		}
	}
	return fmt.Errorf("availabilityZone %q is not published in StackitCluster status.failureDomains", availabilityZone)
}

func (r *StackitMachineReconciler) reconcileDelete(ctx context.Context, machineScope *scope.MachineScope) error {
	stackitMachine := machineScope.StackitMachine
	needsLoadBalancerCleanup := isControlPlaneMachine(machineScope.Machine) &&
		machineScope.StackitCluster.Spec.APIServerLoadBalancer.Enabled &&
		machineScope.StackitCluster.Status.APIServerLoadBalancerID != ""
	if stackitMachine.Status.InstanceID == "" && !needsLoadBalancerCleanup {
		controllerutil.RemoveFinalizer(stackitMachine, infrav1.MachineFinalizer)
		if r.Recorder != nil {
			r.Recorder.Eventf(stackitMachine, nil, corev1.EventTypeNormal, "InstanceDeleted", "Delete", "Deleted instance")
		}
		return nil
	}
	cloudClient, err := util.BuildCloudClient(ctx, r.Client, r.CloudClientFactory, machineScope.StackitCluster)
	if err != nil {
		_, resultErr := util.CredentialFailureResult(
			&stackitMachine.Status.Conditions,
			stackitMachine.Generation,
			err,
			infrav1.MachineCredentialsReadyCondition,
		)
		return resultErr
	}
	if err := r.deleteAPIServerLoadBalancerTarget(ctx, cloudClient, machineScope); err != nil {
		return err
	}
	if stackitMachine.Status.InstanceID == "" {
		controllerutil.RemoveFinalizer(stackitMachine, infrav1.MachineFinalizer)
		if r.Recorder != nil {
			r.Recorder.Eventf(stackitMachine, nil, corev1.EventTypeNormal, "InstanceDeleted", "Delete", "Deleted instance")
		}
		return nil
	}
	instanceID := stackitMachine.Status.InstanceID
	if err := cloudClient.DeleteServer(ctx, instanceID); err != nil && !cloud.IsNotFound(err) {
		return err
	}
	machineScope.ClearInstance()
	controllerutil.RemoveFinalizer(stackitMachine, infrav1.MachineFinalizer)
	if r.Recorder != nil {
		r.Recorder.Eventf(
			stackitMachine, nil, corev1.EventTypeNormal, "InstanceDeleted", "Delete", "Deleted instance %s", instanceID,
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
	cloudClient cloud.Client,
	machineScope *scope.MachineScope,
	userData []byte,
) (*cloud.Server, bool, error) {
	stackitMachine := machineScope.StackitMachine
	tags := machineScope.Tags()
	if stackitMachine.Status.InstanceID != "" {
		server, err := cloudClient.GetServer(ctx, stackitMachine.Status.InstanceID)
		if err == nil {
			return server, false, nil
		}
		if !cloud.IsNotFound(err) {
			return nil, false, err
		}
	}
	if server, err := cloudClient.FindServerByTags(ctx, tags); err == nil {
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
	if stackitMachine.Status.Initialization.Provisioned {
		return nil, false, fmt.Errorf(
			"%w: server %s for already-provisioned machine no longer exists; the Machine must be replaced",
			cloud.ErrNotFound, stackitMachine.Status.InstanceID,
		)
	}

	deleteOnTermination := true
	if stackitMachine.Spec.RootVolume.DeleteOnTermination != nil {
		deleteOnTermination = *stackitMachine.Spec.RootVolume.DeleteOnTermination
	}
	server, err := cloudClient.CreateServer(ctx, cloud.CreateServerInput{
		Name:             stackitMachine.Name,
		ProjectID:        machineScope.StackitCluster.Spec.ProjectID,
		Region:           machineScope.StackitCluster.Spec.Region,
		ImageID:          stackitMachine.Spec.ImageID,
		MachineType:      stackitMachine.Spec.MachineType,
		AvailabilityZone: stackitMachine.Spec.AvailabilityZone,
		SSHKeyName:       stackitMachine.Spec.SSHKeyName,
		NetworkID:        stackitMachine.Spec.Network.ID,
		SecurityGroups:   stackitMachine.Spec.SecurityGroups,
		UserData:         userData,
		Tags:             tags,
		RootVolume: cloud.RootVolumeInput{
			SizeGiB:             stackitMachine.Spec.RootVolume.SizeGiB,
			PerformanceClass:    stackitMachine.Spec.RootVolume.PerformanceClass,
			DeleteOnTermination: deleteOnTermination,
		},
	})
	return server, true, err
}

func (r *StackitMachineReconciler) reconcileBastionNodeSSHAccess(
	ctx context.Context,
	cloudClient cloud.Client,
	machineScope *scope.MachineScope,
	server *cloud.Server,
) error {
	if !machineScope.StackitCluster.Spec.Bastion.Enabled {
		return nil
	}
	if machineScope.StackitCluster.Status.Bastion.SecurityGroupID == "" {
		return fmt.Errorf("%w: bastion security group ID is empty", cloud.ErrTransient)
	}
	if server == nil || server.ID == "" {
		return fmt.Errorf("%w: server ID is empty", cloud.ErrTransient)
	}
	_, err := cloudClient.EnsureNodeSSHAccess(ctx, cloud.NodeSSHAccessInput{
		Name:                   machineScope.StackitCluster.Name + "-node-ssh",
		ServerID:               server.ID,
		BastionSecurityGroupID: machineScope.StackitCluster.Status.Bastion.SecurityGroupID,
		Tags:                   bastionservice.NodeSSHAccessTags(machineScope.StackitCluster),
	})
	return err
}

func (r *StackitMachineReconciler) reconcileAPIServerLoadBalancerTarget(
	ctx context.Context,
	cloudClient cloud.Client,
	machineScope *scope.MachineScope,
	server *cloud.Server,
) error {
	if !isControlPlaneMachine(machineScope.Machine) || !machineScope.StackitCluster.Spec.APIServerLoadBalancer.Enabled {
		return nil
	}
	loadBalancerID, err := loadbalancerservice.EnsureForMachine(
		ctx,
		cloudClient,
		machineScope.StackitCluster,
		machineScope.Machine.Name,
		server.Addresses,
	)
	if err != nil {
		return err
	}

	target, err := loadbalancerservice.TargetForMachine(machineScope.Machine.Name, server.Addresses)
	if err != nil {
		return err
	}
	target.LoadBalancerID = loadBalancerID
	return cloudClient.EnsureAPIServerLoadBalancerTarget(ctx, target)
}

func (r *StackitMachineReconciler) deleteAPIServerLoadBalancerTarget(
	ctx context.Context,
	cloudClient cloud.Client,
	machineScope *scope.MachineScope,
) error {
	if !isControlPlaneMachine(machineScope.Machine) || !machineScope.StackitCluster.Spec.APIServerLoadBalancer.Enabled {
		return nil
	}
	loadBalancerID, err := loadbalancerservice.ResolveID(ctx, cloudClient, machineScope.StackitCluster)
	if err != nil {
		return err
	}
	if loadBalancerID == "" {
		return nil
	}
	err = cloudClient.DeleteAPIServerLoadBalancerTarget(ctx, cloud.LoadBalancerTargetInput{
		LoadBalancerID: loadBalancerID,
		Name:           machineScope.Machine.Name,
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
