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
	"crypto/sha256"
	"fmt"
	"net/netip"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"

	infrav1 "github.com/stackitcloud/cluster-api-provider-stackit/api/v1alpha1"
	"github.com/stackitcloud/cluster-api-provider-stackit/cloud"
	bastionservice "github.com/stackitcloud/cluster-api-provider-stackit/cloud/services/bastion"
	"github.com/stackitcloud/cluster-api-provider-stackit/scope"
)

// bastionDisabledReason is read back to decide whether the cleanup below
// already ran, so it must not drift.
const bastionDisabledReason = "Skipped"

func (r *StackitClusterReconciler) reconcileBastion(
	ctx context.Context,
	cloudClient cloud.Client,
	clusterScope *scope.ClusterScope,
) (ctrl.Result, bool, error) {
	cluster := clusterScope.StackitCluster
	input := bastionservice.Input(cluster, nil)
	status := cloud.Bastion{
		ServerID:        cluster.Status.Bastion.ServerID,
		PublicIPID:      cluster.Status.Bastion.PublicIPID,
		PublicIP:        cluster.Status.Bastion.PublicIP,
		SecurityGroupID: cluster.Status.Bastion.SecurityGroupID,
	}

	if !cluster.Spec.Bastion.Enabled {
		// The condition carries this reason only after a cleanup has succeeded,
		// so anything else means we may still own bastion resources — including
		// the case where EnsureBastion succeeded but its status patch was lost.
		// Keying on it instead of on the status keeps the tag-based cleanup to
		// once per cluster rather than once per reconcile, which matters because
		// this path runs for every cluster without a bastion.
		condition := meta.FindStatusCondition(cluster.Status.Conditions, infrav1.ClusterBastionReadyCondition)
		if condition == nil || condition.Reason != bastionDisabledReason {
			if err := cloudClient.DeleteNodeSSHAccess(ctx, bastionservice.NodeSSHAccessTags(cluster)); err != nil {
				return ctrl.Result{}, false, err
			}
			if err := cloudClient.DeleteBastion(ctx, input, status); err != nil {
				return ctrl.Result{}, false, err
			}
			clusterScope.ClearBastionStatus()
			if r.Recorder != nil {
				r.Recorder.Eventf(cluster, nil, corev1.EventTypeNormal, "BastionDeleted", "Delete", "Deleted bastion")
			}
		}
		clusterScope.SetConditions(
			metav1.ConditionTrue,
			bastionDisabledReason,
			"bastion disabled",
			infrav1.ClusterBastionReadyCondition,
		)
		return ctrl.Result{}, true, nil
	}

	if err := validateBastionSpec(cluster.Spec.Bastion); err != nil {
		clusterScope.SetNotReady(
			"InvalidBastionSpec",
			err.Error(),
			infrav1.ClusterBastionReadyCondition,
			infrav1.ClusterReadyCondition,
		)
		return ctrl.Result{}, false, nil
	}

	cloudInit, err := r.resolveBastionCloudInit(ctx, cluster)
	if err != nil {
		clusterScope.SetNotReady(
			"CloudInitRefError",
			err.Error(),
			infrav1.ClusterBastionReadyCondition,
			infrav1.ClusterReadyCondition,
		)
		return ctrl.Result{}, false, nil
	}
	input.CloudInit = cloudInit

	if bastionNeedsRecreate(cluster, cloudInit) {
		if err := cloudClient.DeleteNodeSSHAccess(ctx, bastionservice.NodeSSHAccessTags(cluster)); err != nil && !cloud.IsNotFound(err) {
			return ctrl.Result{}, false, err
		}
		if err := cloudClient.DeleteBastion(ctx, input, status); err != nil && !cloud.IsNotFound(err) {
			return ctrl.Result{}, false, err
		}
		clusterScope.ClearBastionStatus()
		clusterScope.SetNotReady("Recreating", "recreating bastion because cloudInitRef content changed", infrav1.ClusterBastionReadyCondition, infrav1.ClusterReadyCondition)
		if r.Recorder != nil {
			r.Recorder.Eventf(
				cluster, nil, corev1.EventTypeNormal, "BastionRecreating", "Recreate",
				"Recreating bastion because cloudInitRef content changed",
			)
		}
		return ctrl.Result{RequeueAfter: retryableErrorRequeueAfter}, false, nil
	}

	hadBastionStatus := hasBastionStatus(cluster.Status.Bastion)
	bastion, err := cloudClient.EnsureBastion(ctx, input)
	if err != nil {
		return ctrl.Result{}, false, err
	}
	clusterScope.SetBastionStatus(bastion, bastionCloudInitHash(cloudInit))
	if !hadBastionStatus && r.Recorder != nil {
		r.Recorder.Eventf(
			cluster, nil, corev1.EventTypeNormal, "BastionCreated", "Create", "Created bastion %s", bastion.ServerID,
		)
	}
	if bastion.ServerState != "" && bastion.ServerState != "ACTIVE" {
		clusterScope.SetNotReady(
			"Provisioning",
			fmt.Sprintf("bastion server state is %s", bastion.ServerState),
			infrav1.ClusterBastionReadyCondition,
			infrav1.ClusterReadyCondition,
		)
		return ctrl.Result{RequeueAfter: 15 * time.Second}, false, nil
	}
	if bastion.PublicIP == "" {
		clusterScope.SetNotReady(
			"Provisioning",
			"waiting for bastion public IP address",
			infrav1.ClusterBastionReadyCondition,
			infrav1.ClusterReadyCondition,
		)
		return ctrl.Result{RequeueAfter: 10 * time.Second}, false, nil
	}
	clusterScope.SetConditions(
		metav1.ConditionTrue,
		"Available",
		"",
		infrav1.ClusterBastionReadyCondition,
	)
	return ctrl.Result{}, true, nil
}

func validateBastionSpec(spec infrav1.StackitBastionSpec) error {
	if spec.ImageID == "" {
		return fmt.Errorf("%w: bastion.imageID is required", cloud.ErrInvalidInput)
	}
	if spec.MachineType == "" {
		return fmt.Errorf("%w: bastion.machineType is required", cloud.ErrInvalidInput)
	}
	if spec.SSHKeyName == "" {
		return fmt.Errorf("%w: bastion.sshKeyName is required", cloud.ErrInvalidInput)
	}
	if len(spec.AllowedCIDRs) == 0 {
		return fmt.Errorf("%w: bastion.allowedCIDRs is required", cloud.ErrInvalidInput)
	}
	for _, cidr := range spec.AllowedCIDRs {
		if _, err := netip.ParsePrefix(cidr); err != nil {
			return fmt.Errorf("%w: bastion.allowedCIDRs contains invalid CIDR %q", cloud.ErrInvalidInput, cidr)
		}
	}
	return nil
}

func hasBastionStatus(status infrav1.StackitBastionStatus) bool {
	return status.ServerID != "" || status.PublicIPID != "" || status.PublicIP != "" || status.SecurityGroupID != ""
}

func bastionNeedsRecreate(sc *infrav1.StackitCluster, cloudInit []byte) bool {
	if !hasBastionStatus(sc.Status.Bastion) {
		return false
	}
	return sc.Status.Bastion.CloudInitHash != bastionCloudInitHash(cloudInit)
}

func bastionCloudInitHash(cloudInit []byte) string {
	if len(cloudInit) == 0 {
		return ""
	}
	return fmt.Sprintf("%x", sha256.Sum256(cloudInit))
}

func (r *StackitClusterReconciler) resolveBastionCloudInit(ctx context.Context, sc *infrav1.StackitCluster) ([]byte, error) {
	ref := sc.Spec.Bastion.CloudInitRef
	if ref == nil {
		return nil, nil
	}
	key := types.NamespacedName{Namespace: sc.Namespace, Name: ref.Name}
	switch ref.Kind {
	case "ConfigMap":
		configMap := &corev1.ConfigMap{}
		if err := r.Get(ctx, key, configMap); err != nil {
			if apierrors.IsNotFound(err) {
				return nil, fmt.Errorf("bastion.cloudInitRef ConfigMap %s not found", key)
			}
			return nil, err
		}
		value, ok := configMap.Data[ref.Key]
		if !ok {
			return nil, fmt.Errorf("bastion.cloudInitRef key %q not found in ConfigMap %s", ref.Key, key)
		}
		return []byte(value), nil
	case cloudInitRefKindSecret:
		secret := &corev1.Secret{}
		if err := r.Get(ctx, key, secret); err != nil {
			if apierrors.IsNotFound(err) {
				return nil, fmt.Errorf("bastion.cloudInitRef Secret %s not found", key)
			}
			return nil, err
		}
		value, ok := secret.Data[ref.Key]
		if !ok {
			return nil, fmt.Errorf("bastion.cloudInitRef key %q not found in Secret %s", ref.Key, key)
		}
		return append([]byte(nil), value...), nil
	default:
		return nil, fmt.Errorf("bastion.cloudInitRef.kind must be ConfigMap or Secret")
	}
}
