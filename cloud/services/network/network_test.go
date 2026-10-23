/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package network

import (
	"context"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	infrav1 "github.com/stackitcloud/cluster-api-provider-stackit/api/v1alpha1"
	cloudfake "github.com/stackitcloud/cluster-api-provider-stackit/cloud/fake"
	"github.com/stackitcloud/cluster-api-provider-stackit/util"
)

func TestNetworkInput(t *testing.T) {
	cluster := &infrav1.StackitCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-cluster",
			Namespace: "test-ns",
		},
		Spec: infrav1.StackitClusterSpec{
			AdditionalLabels: map[string]string{
				"custom-label": "custom-value",
			},
		},
	}

	input := Input(cluster)

	if input.Name != "test-cluster-network" {
		t.Errorf("Input() Name = %s, want test-cluster-network", input.Name)
	}
	if input.IPv4Prefix != DefaultNetworkPrefix {
		t.Errorf("Input() IPv4Prefix = %s, want %s", input.IPv4Prefix, DefaultNetworkPrefix)
	}
	if input.Tags[util.LabelClusterName] != "test-cluster" {
		t.Errorf("Input() missing cluster-name tag")
	}
	if input.Tags[util.LabelClusterNamespace] != "test-ns" {
		t.Errorf("Input() missing cluster-namespace tag")
	}
	if input.Tags[util.LabelManagedBy] != util.ManagedByValue {
		t.Errorf("Input() missing managed-by tag")
	}
	if input.Tags["custom-label"] != "custom-value" {
		t.Errorf("Input() missing merged additional label")
	}
}

func TestResolveManagedNetworkID(t *testing.T) {
	ctx := context.Background()

	t.Run("returns empty string for BYO network in spec", func(t *testing.T) {
		fakeClient := cloudfake.New()
		cluster := &infrav1.StackitCluster{
			ObjectMeta: metav1.ObjectMeta{Name: "byo-cluster", Namespace: "default"},
			Spec: infrav1.StackitClusterSpec{
				Network: infrav1.StackitClusterNetworkSpec{ID: "byo-net-id"},
			},
			Status: infrav1.StackitClusterStatus{
				Network: infrav1.StackitNetworkStatus{ID: "byo-net-id", Managed: false},
			},
		}

		id, err := ResolveManagedNetworkID(ctx, fakeClient, cluster)
		if err != nil {
			t.Fatalf("ResolveManagedNetworkID() error = %v", err)
		}
		if id != "" {
			t.Errorf("ResolveManagedNetworkID() = %q, want empty string for BYO network", id)
		}
	})

	t.Run("returns network ID from status when present", func(t *testing.T) {
		fakeClient := cloudfake.New()
		cluster := &infrav1.StackitCluster{
			ObjectMeta: metav1.ObjectMeta{Name: "managed-cluster", Namespace: "default"},
			Status: infrav1.StackitClusterStatus{
				Network: infrav1.StackitNetworkStatus{ID: "status-net-id", Managed: true},
			},
		}

		id, err := ResolveManagedNetworkID(ctx, fakeClient, cluster)
		if err != nil {
			t.Fatalf("ResolveManagedNetworkID() error = %v", err)
		}
		if id != "status-net-id" {
			t.Errorf("ResolveManagedNetworkID() = %q, want status-net-id", id)
		}
	})

	t.Run("falls back to tag lookup when status is empty", func(t *testing.T) {
		fakeClient := cloudfake.New()
		cluster := &infrav1.StackitCluster{
			ObjectMeta: metav1.ObjectMeta{Name: "managed-cluster", Namespace: "default"},
		}
		created, err := fakeClient.CreateNetwork(ctx, Input(cluster))
		if err != nil {
			t.Fatalf("CreateNetwork() error = %v", err)
		}

		id, err := ResolveManagedNetworkID(ctx, fakeClient, cluster)
		if err != nil {
			t.Fatalf("ResolveManagedNetworkID() error = %v", err)
		}
		if id != created.ID {
			t.Errorf("ResolveManagedNetworkID() = %q, want %q", id, created.ID)
		}
	})

	t.Run("returns empty string when no network exists in status or cloud", func(t *testing.T) {
		fakeClient := cloudfake.New()
		cluster := &infrav1.StackitCluster{
			ObjectMeta: metav1.ObjectMeta{Name: "managed-cluster", Namespace: "default"},
		}

		id, err := ResolveManagedNetworkID(ctx, fakeClient, cluster)
		if err != nil {
			t.Fatalf("ResolveManagedNetworkID() error = %v", err)
		}
		if id != "" {
			t.Errorf("ResolveManagedNetworkID() = %q, want empty string", id)
		}
	})
}
