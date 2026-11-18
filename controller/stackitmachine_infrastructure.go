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
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	"sigs.k8s.io/cluster-api/util/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	infrav1 "github.com/stackitcloud/cluster-api-provider-stackit/api/v1alpha1"
	"github.com/stackitcloud/cluster-api-provider-stackit/cloud/services/securitygroup"
	"github.com/stackitcloud/cluster-api-provider-stackit/cloud/services/server"
	"github.com/stackitcloud/cluster-api-provider-stackit/scope"
	"github.com/stackitcloud/cluster-api-provider-stackit/util"
)

func (r *StackitMachineReconciler) reconcileNormal(ctx context.Context, machineScope *scope.MachineScope) (ctrl.Result, error) {
	log := logf.FromContext(ctx)
	stackitMachine := machineScope.StackitMachine

	if !controllerutil.ContainsFinalizer(stackitMachine, infrav1.MachineFinalizer) {
		controllerutil.AddFinalizer(stackitMachine, infrav1.MachineFinalizer)
		// Persisted immediately, before CreateServer can run, to ensure no servers
		// are running behind an object that carries no finalizer to clean it up.
		if err := machineScope.PatchObject(ctx); err != nil {
			return ctrl.Result{}, fmt.Errorf("persist finalizer: %w", err)
		}
	}

	if !machineScope.StackitCluster.Status.Ready {
		log.Info("Waiting for StackitCluster to be ready", "machine", stackitMachine.Name, "cluster", machineScope.StackitCluster.Name)
		machineScope.SetNotReady("InfrastructureNotReady", "waiting for StackitCluster to be ready", infrav1.MachineReadyCondition)
		return ctrl.Result{}, nil
	}
	if err := validateMachineAvailabilityZone(machineScope); err != nil {
		log.Error(err, "Invalid availability zone for machine", "machine", stackitMachine.Name, "availabilityZone", stackitMachine.Spec.AvailabilityZone)
		record.Warnf(stackitMachine, "InvalidFailureDomain", "%v", err)
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
		log.Error(errors.New(message), "Bootstrap data is not ready", "machine", stackitMachine.Name, "reason", reason)
		record.Warnf(stackitMachine, reason, "Bootstrap data error: %s", message)
		machineScope.SetConditions(metav1.ConditionFalse, reason, message, infrav1.MachineReadyCondition)
		if reason == util.BootstrapReasonInvalid {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{RequeueAfter: retryableErrorRequeueAfter}, nil
	}

	cloudClient, err := util.BuildCloudClient(ctx, r.Client, r.CloudClientFactory, machineScope.StackitCluster)
	if err != nil {
		log.Error(err, "Failed to build cloud client for machine", "machine", stackitMachine.Name)
		record.Warnf(stackitMachine, "InvalidCredentials", "Failed to build cloud client: %v", err)
		return machineScope.CredentialFailureResult(
			err,
			credentialsRetryRequeueAfter,
			infrav1.MachineCredentialsReadyCondition,
			infrav1.MachineReadyCondition,
		)
	}
	machineScope.SetCloudClient(cloudClient)
	machineScope.SetConditions(metav1.ConditionTrue, "Available", "", infrav1.MachineCredentialsReadyCondition)

	machineScope.SetBootstrapData(bootstrapData)
	err = server.New(machineScope).Reconcile(ctx)
	if server.IsServerGone(err) {
		machineScope.SetNotReady(
			"InstanceNotFound",
			err.Error(),
			infrav1.MachineInstanceReadyCondition,
			infrav1.MachineReadyCondition,
		)
		record.Warnf(
			stackitMachine, "InstanceNotFound",
			"Server %s no longer exists; the Machine must be replaced", stackitMachine.Status.InstanceID,
		)
		return ctrl.Result{}, nil
	}
	if err != nil {
		log.Error(err, "Failed to create server",
			"machine", stackitMachine.Name,
			"imageID", stackitMachine.Spec.ImageID,
			"networkID", machineScope.NetworkID(),
			"region", machineScope.StackitCluster.Spec.Region,
		)
		record.Warnf(
			stackitMachine, "FailedCreateInstance",
			"Failed to create server %s (imageID: %s, networkID: %s, region: %s): %v",
			stackitMachine.Name, stackitMachine.Spec.ImageID, machineScope.NetworkID(),
			machineScope.StackitCluster.Spec.Region, err,
		)
		return machineScope.CloudFailureResult(
			"InstanceError",
			err,
			retryableErrorRequeueAfter,
			true,
			infrav1.MachineInstanceReadyCondition,
			infrav1.MachineReadyCondition,
		)
	}
	if machineScope.InstanceCreated() {
		record.Eventf(
			stackitMachine, "InstanceCreated", "Created instance %s", stackitMachine.Status.InstanceID,
		)
	}

	if ready, reason, message, warn := serverStateCondition(stackitMachine.Status.InstanceState, machineScope.InstancePowerStatus()); !ready {
		previousReason := ""
		if previous := meta.FindStatusCondition(stackitMachine.Status.Conditions, infrav1.MachineInstanceReadyCondition); previous != nil {
			previousReason = previous.Reason
		}
		machineScope.SetNotReady(reason, message, infrav1.MachineInstanceReadyCondition, infrav1.MachineReadyCondition)
		// Only warn on entering the state, not on every requeue.
		if warn && previousReason != reason {
			record.Warnf(stackitMachine, reason, "Server %s: %s", stackitMachine.Status.InstanceID, message)
		}
		return ctrl.Result{RequeueAfter: 15 * time.Second}, nil
	}

	if err := securitygroup.New(machineScope).Reconcile(ctx); err != nil {
		log.Error(err, "Failed to configure bastion SSH access for node", "machine", stackitMachine.Name)
		record.Warnf(
			stackitMachine, "FailedNodeSSHAccess",
			"Failed to configure bastion SSH access for server %s: %v", stackitMachine.Status.InstanceID, err,
		)
		return machineScope.CloudFailureResult(
			"BastionSSHAccessError",
			err,
			retryableErrorRequeueAfter,
			true,
			infrav1.MachineReadyCondition,
		)
	}

	machineScope.SetReady()
	log.V(1).Info("Reconciled StackitMachine", "providerID", stackitMachine.Status.ProviderID)
	return ctrl.Result{}, nil
}

func validateMachineAvailabilityZone(machineScope *scope.MachineScope) error {
	clusterFailureDomains := machineScope.StackitCluster.Status.FailureDomains
	if len(clusterFailureDomains) == 0 {
		return nil
	}
	knownAZ := func(zone string) bool {
		for _, failureDomain := range clusterFailureDomains {
			if failureDomain.Name == zone {
				return true
			}
		}
		return false
	}
	if az := machineScope.StackitMachine.Spec.AvailabilityZone; az != "" && !knownAZ(az) {
		return fmt.Errorf("availabilityZone %q is not published in StackitCluster status.failureDomains", az)
	}
	if machineScope.Machine != nil && machineScope.Machine.Spec.FailureDomain != "" {
		fd := machineScope.Machine.Spec.FailureDomain
		if !knownAZ(fd) {
			return fmt.Errorf("machine failureDomain %q is not published in StackitCluster status.failureDomains", fd)
		}
	}
	return nil
}

func (r *StackitMachineReconciler) reconcileDelete(ctx context.Context, machineScope *scope.MachineScope) error {
	log := logf.FromContext(ctx)
	stackitMachine := machineScope.StackitMachine

	// An empty status.instanceID is not proof that no server exists, so every
	// deletion asks the cloud before dropping the finalizer.
	cloudClient, err := util.BuildCloudClient(ctx, r.Client, r.CloudClientFactory, machineScope.StackitCluster)
	if err != nil {
		// A missing credentials Secret cannot be recovered from and commonly
		// disappears first during namespace teardown, so finalize and make the
		// possible leak loud rather than stranding the Machine in Terminating.
		// Every other credentials problem is fixable and keeps retrying.
		if apierrors.IsNotFound(err) {
			log.Error(err, "Credentials secret is gone; finalizing without cloud cleanup", "machine", stackitMachine.Name)
			record.Warnf(stackitMachine, "CleanupSkipped",
				"Credentials Secret is gone; finalizing without cloud cleanup. "+
					"Any remaining STACKIT server for this machine must be removed manually: %v", err)
			controllerutil.RemoveFinalizer(stackitMachine, infrav1.MachineFinalizer)
			return nil
		}
		log.Error(err, "Failed to build cloud client for machine deletion", "machine", stackitMachine.Name)
		record.Warnf(stackitMachine, "InvalidCredentials",
			"Failed to build cloud client for deletion: %v", err)
		_, resultErr := machineScope.CredentialFailureResult(
			err,
			credentialsRetryRequeueAfter,
			infrav1.MachineCredentialsReadyCondition,
		)
		return resultErr
	}

	machineScope.SetCloudClient(cloudClient)
	instanceID := stackitMachine.Status.InstanceID
	if err := server.New(machineScope).Delete(ctx); err != nil {
		log.Error(err, "Failed to delete server", "machine", stackitMachine.Name, "instanceID", instanceID)
		record.Warnf(stackitMachine, "FailedDeleteInstance",
			"Failed to delete server %s: %v", instanceID, err)
		return err
	}
	if instanceID != "" {
		record.Eventf(
			stackitMachine, "InstanceDeleted", "Deleted instance %s", instanceID,
		)
	}
	controllerutil.RemoveFinalizer(stackitMachine, infrav1.MachineFinalizer)
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
