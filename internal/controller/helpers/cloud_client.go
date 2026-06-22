/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package helpers

import (
	"context"
	"errors"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	infrav1 "github.com/stackitcloud/cluster-api-provider-stackit/api/v1alpha1"
	"github.com/stackitcloud/cluster-api-provider-stackit/pkg/cloud"
	"github.com/stackitcloud/cluster-api-provider-stackit/pkg/util"
)

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

	creds, err := util.ParseCredentialsSecret(secret, stackitCluster.Spec.ProjectID, stackitCluster.Spec.Region)
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
