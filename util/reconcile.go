/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package util

import (
	"context"
	"errors"
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	"sigs.k8s.io/cluster-api/util/annotations"
	"sigs.k8s.io/controller-runtime/pkg/client"

	infrav1 "github.com/stackitcloud/cluster-api-provider-stackit/api/v1alpha1"
	"github.com/stackitcloud/cluster-api-provider-stackit/cloud"
)

func ReconciliationPaused(cluster *clusterv1.Cluster, obj client.Object) (bool, string) {
	if cluster == nil {
		if annotations.HasPaused(obj) {
			return true, "object has the cluster.x-k8s.io/paused annotation"
		}
		return false, ""
	}

	var reasons []string
	if ptr.Deref(cluster.Spec.Paused, false) {
		reasons = append(reasons, "Cluster spec.paused is set to true")
	}
	if annotations.HasPaused(obj) {
		reasons = append(reasons, "object has the cluster.x-k8s.io/paused annotation")
	}
	if len(reasons) == 0 {
		return false, ""
	}
	return true, strings.Join(reasons, ", ")
}

func SetPausedCondition(conditions *[]metav1.Condition, generation int64, paused bool, message string) {
	if paused {
		SetConditions(conditions, generation, metav1.ConditionTrue, clusterv1.PausedReason, message, clusterv1.PausedCondition)
		return
	}
	SetConditions(conditions, generation, metav1.ConditionFalse, clusterv1.NotPausedReason, "", clusterv1.PausedCondition)
}

func BuildCloudClient(
	ctx context.Context,
	k8sClient client.Client,
	factory cloud.Factory,
	stackitCluster *infrav1.StackitCluster,
) (cloud.Client, error) {
	if factory == nil {
		return nil, errors.New("CloudClientFactory is not configured")
	}

	secret := &corev1.Secret{}
	key := CredentialsSecretKey(stackitCluster)
	if err := k8sClient.Get(ctx, key, secret); err != nil {
		return nil, fmt.Errorf("get credentials secret %s: %w", key, err)
	}

	creds, err := ParseCredentialsSecret(secret, stackitCluster.Spec.ProjectID, stackitCluster.Spec.Region)
	if err != nil {
		return nil, err
	}

	return factory(ctx, creds)
}

func CredentialsSecretKey(stackitCluster *infrav1.StackitCluster) types.NamespacedName {
	namespace := stackitCluster.Spec.CredentialsSecretRef.Namespace
	if namespace == "" {
		namespace = stackitCluster.Namespace
	}
	return types.NamespacedName{
		Namespace: namespace,
		Name:      stackitCluster.Spec.CredentialsSecretRef.Name,
	}
}
