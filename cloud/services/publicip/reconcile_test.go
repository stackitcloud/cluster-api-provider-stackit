/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package publicip_test

import (
	"context"
	"errors"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/scheme"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	infrav1 "github.com/stackitcloud/cluster-api-provider-stackit/api/v1alpha1"
	"github.com/stackitcloud/cluster-api-provider-stackit/cloud"
	cloudfake "github.com/stackitcloud/cluster-api-provider-stackit/cloud/fake"
	"github.com/stackitcloud/cluster-api-provider-stackit/cloud/services/publicip"
	"github.com/stackitcloud/cluster-api-provider-stackit/cloud/services/shared"
	"github.com/stackitcloud/cluster-api-provider-stackit/scope"
)

func init() {
	_ = clusterv1.AddToScheme(scheme.Scheme)
	_ = infrav1.AddToScheme(scheme.Scheme)
}

func newTestClusterScope(t *testing.T, bastionEnabled bool, cloudClient cloud.Client) *scope.ClusterScope {
	t.Helper()

	cluster := &clusterv1.Cluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-cluster",
			Namespace: "default",
		},
	}
	stackitCluster := &infrav1.StackitCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-cluster",
			Namespace: "default",
		},
		Spec: infrav1.StackitClusterSpec{
			ProjectID: "00000000-0000-0000-0000-000000000000",
			Region:    "eu01",
			Bastion: infrav1.StackitBastionSpec{
				Enabled: bastionEnabled,
			},
		},
	}

	k8sClient := fake.NewClientBuilder().
		WithScheme(scheme.Scheme).
		WithObjects(cluster, stackitCluster).
		Build()

	clusterScope, err := scope.NewClusterScope(k8sClient, cluster, stackitCluster)
	if err != nil {
		t.Fatalf("NewClusterScope() error = %v", err)
	}
	clusterScope.SetCloudClient(cloudClient)
	return clusterScope
}

func TestServiceReconcile(t *testing.T) {
	t.Run("deletes existing public IP when bastion is disabled", func(t *testing.T) {
		ctx := context.Background()
		fakeCloud := cloudfake.New()
		clusterScope := newTestClusterScope(t, false, fakeCloud)

		created, err := fakeCloud.CreatePublicIP(ctx, clusterScope.BastionTags())
		if err != nil {
			t.Fatalf("CreatePublicIP() error = %v", err)
		}
		clusterScope.SetBastionPublicIP(created.ID, created.IP)

		svc := publicip.New(clusterScope)
		if err := svc.Reconcile(ctx); err != nil {
			t.Fatalf("Reconcile() error = %v", err)
		}

		if fakeCloud.PublicIPCount() != 0 {
			t.Fatalf("PublicIPCount() = %d, want 0", fakeCloud.PublicIPCount())
		}
		if clusterScope.BastionStatus().PublicIPID != "" {
			t.Fatalf("BastionStatus().PublicIPID = %q, want empty", clusterScope.BastionStatus().PublicIPID)
		}
	})

	t.Run("creates public IP and attaches it to the bastion server", func(t *testing.T) {
		ctx := context.Background()
		fakeCloud := cloudfake.New()
		clusterScope := newTestClusterScope(t, true, fakeCloud)

		server, err := fakeCloud.CreateServer(ctx, cloud.CreateServerInput{
			Name: "test-cluster-bastion",
			Tags: clusterScope.BastionTags(),
		})
		if err != nil {
			t.Fatalf("CreateServer() error = %v", err)
		}
		clusterScope.StackitCluster.Status.Bastion.ServerID = server.ID

		svc := publicip.New(clusterScope)
		if err := svc.Reconcile(ctx); err != nil {
			t.Fatalf("Reconcile() error = %v", err)
		}

		if fakeCloud.PublicIPCount() != 1 {
			t.Fatalf("PublicIPCount() = %d, want 1", fakeCloud.PublicIPCount())
		}
		if clusterScope.BastionStatus().PublicIPID == "" {
			t.Fatal("BastionStatus().PublicIPID is empty, want set")
		}
		if clusterScope.StackitCluster.Status.Bastion.PublicIP != "203.0.113.22" {
			t.Fatalf("Bastion.PublicIP = %q, want 203.0.113.22", clusterScope.StackitCluster.Status.Bastion.PublicIP)
		}

		gotIP, err := fakeCloud.GetPublicIP(ctx, clusterScope.BastionStatus().PublicIPID)
		if err != nil {
			t.Fatalf("GetPublicIP() error = %v", err)
		}
		if gotIP.NetworkInterfaceID == "" {
			t.Fatal("PublicIP NetworkInterfaceID is empty, want attached to server")
		}
	})

	t.Run("reuses existing public IP found by status ID without listing by tags", func(t *testing.T) {
		ctx := context.Background()
		fakeCloud := cloudfake.New()
		clusterScope := newTestClusterScope(t, true, fakeCloud)

		server, err := fakeCloud.CreateServer(ctx, cloud.CreateServerInput{
			Name: "test-cluster-bastion",
			Tags: clusterScope.BastionTags(),
		})
		if err != nil {
			t.Fatalf("CreateServer() error = %v", err)
		}
		clusterScope.StackitCluster.Status.Bastion.ServerID = server.ID

		existing, err := fakeCloud.CreatePublicIP(ctx, clusterScope.BastionTags())
		if err != nil {
			t.Fatalf("CreatePublicIP() error = %v", err)
		}
		clusterScope.SetBastionPublicIP(existing.ID, existing.IP)
		fakeCloud.FailNextListPublicIPs = shared.ErrTransient

		svc := publicip.New(clusterScope)
		if err := svc.Reconcile(ctx); err != nil {
			t.Fatalf("Reconcile() error = %v", err)
		}

		if fakeCloud.PublicIPCount() != 1 {
			t.Fatalf("PublicIPCount() = %d, want 1", fakeCloud.PublicIPCount())
		}
		if clusterScope.BastionStatus().PublicIPID != existing.ID {
			t.Fatalf("BastionStatus().PublicIPID = %q, want %q", clusterScope.BastionStatus().PublicIPID, existing.ID)
		}
	})

	t.Run("reuses existing public IP found by tags without creating duplicate", func(t *testing.T) {
		ctx := context.Background()
		fakeCloud := cloudfake.New()
		clusterScope := newTestClusterScope(t, true, fakeCloud)

		server, err := fakeCloud.CreateServer(ctx, cloud.CreateServerInput{
			Name: "test-cluster-bastion",
			Tags: clusterScope.BastionTags(),
		})
		if err != nil {
			t.Fatalf("CreateServer() error = %v", err)
		}
		clusterScope.StackitCluster.Status.Bastion.ServerID = server.ID

		existing, err := fakeCloud.CreatePublicIP(ctx, clusterScope.BastionTags())
		if err != nil {
			t.Fatalf("CreatePublicIP() error = %v", err)
		}

		svc := publicip.New(clusterScope)
		if err := svc.Reconcile(ctx); err != nil {
			t.Fatalf("Reconcile() error = %v", err)
		}

		if fakeCloud.PublicIPCount() != 1 {
			t.Fatalf("PublicIPCount() = %d, want 1", fakeCloud.PublicIPCount())
		}
		if clusterScope.BastionStatus().PublicIPID != existing.ID {
			t.Fatalf("BastionStatus().PublicIPID = %q, want %q", clusterScope.BastionStatus().PublicIPID, existing.ID)
		}
	})

	t.Run("tolerates conflict when attaching public IP to server", func(t *testing.T) {
		ctx := context.Background()
		fakeCloud := cloudfake.New()
		clusterScope := newTestClusterScope(t, true, fakeCloud)

		server, err := fakeCloud.CreateServer(ctx, cloud.CreateServerInput{
			Name: "test-cluster-bastion",
			Tags: clusterScope.BastionTags(),
		})
		if err != nil {
			t.Fatalf("CreateServer() error = %v", err)
		}
		clusterScope.StackitCluster.Status.Bastion.ServerID = server.ID
		fakeCloud.FailNextAddPublicIP = shared.ErrConflict

		svc := publicip.New(clusterScope)
		if err := svc.Reconcile(ctx); err != nil {
			t.Fatalf("Reconcile() error = %v, want nil on conflict", err)
		}
		if clusterScope.BastionStatus().PublicIPID == "" {
			t.Fatal("BastionStatus().PublicIPID is empty, want set")
		}
	})

	t.Run("returns conflict error when multiple public IPs match tags", func(t *testing.T) {
		ctx := context.Background()
		fakeCloud := cloudfake.New()
		clusterScope := newTestClusterScope(t, true, fakeCloud)

		if _, err := fakeCloud.CreatePublicIP(ctx, clusterScope.BastionTags()); err != nil {
			t.Fatalf("CreatePublicIP() error = %v", err)
		}
		if _, err := fakeCloud.CreatePublicIP(ctx, clusterScope.BastionTags()); err != nil {
			t.Fatalf("CreatePublicIP() error = %v", err)
		}

		svc := publicip.New(clusterScope)
		err := svc.Reconcile(ctx)
		if !errors.Is(err, shared.ErrConflict) {
			t.Fatalf("Reconcile() error = %v, want ErrConflict", err)
		}
	})

	t.Run("returns error when creating public IP fails", func(t *testing.T) {
		ctx := context.Background()
		fakeCloud := cloudfake.New()
		clusterScope := newTestClusterScope(t, true, fakeCloud)
		fakeCloud.FailNextCreatePublicIP = shared.ErrTransient

		svc := publicip.New(clusterScope)
		err := svc.Reconcile(ctx)
		if !errors.Is(err, shared.ErrTransient) {
			t.Fatalf("Reconcile() error = %v, want ErrTransient", err)
		}
	})
}

func TestServiceDelete(t *testing.T) {
	t.Run("detaches and deletes public IP using recorded status ID", func(t *testing.T) {
		ctx := context.Background()
		fakeCloud := cloudfake.New()
		clusterScope := newTestClusterScope(t, true, fakeCloud)

		server, err := fakeCloud.CreateServer(ctx, cloud.CreateServerInput{
			Name: "test-cluster-bastion",
			Tags: clusterScope.BastionTags(),
		})
		if err != nil {
			t.Fatalf("CreateServer() error = %v", err)
		}
		clusterScope.StackitCluster.Status.Bastion.ServerID = server.ID

		created, err := fakeCloud.CreatePublicIP(ctx, clusterScope.BastionTags())
		if err != nil {
			t.Fatalf("CreatePublicIP() error = %v", err)
		}
		if err := fakeCloud.AddPublicIPToServer(ctx, server.ID, created.ID); err != nil {
			t.Fatalf("AddPublicIPToServer() error = %v", err)
		}
		clusterScope.SetBastionPublicIP(created.ID, created.IP)

		svc := publicip.New(clusterScope)
		if err := svc.Delete(ctx); err != nil {
			t.Fatalf("Delete() error = %v", err)
		}

		if fakeCloud.PublicIPCount() != 0 {
			t.Fatalf("PublicIPCount() = %d, want 0", fakeCloud.PublicIPCount())
		}
		if clusterScope.BastionStatus().PublicIPID != "" {
			t.Fatalf("BastionStatus().PublicIPID = %q, want empty", clusterScope.BastionStatus().PublicIPID)
		}
		if clusterScope.StackitCluster.Status.Bastion.PublicIP != "" {
			t.Fatalf("Bastion.PublicIP = %q, want empty", clusterScope.StackitCluster.Status.Bastion.PublicIP)
		}
	})

	t.Run("falls back to tag lookup for both public IP and server when status IDs are empty", func(t *testing.T) {
		ctx := context.Background()
		fakeCloud := cloudfake.New()
		clusterScope := newTestClusterScope(t, true, fakeCloud)

		server, err := fakeCloud.CreateServer(ctx, cloud.CreateServerInput{
			Name: "test-cluster-bastion",
			Tags: clusterScope.BastionTags(),
		})
		if err != nil {
			t.Fatalf("CreateServer() error = %v", err)
		}
		created, err := fakeCloud.CreatePublicIP(ctx, clusterScope.BastionTags())
		if err != nil {
			t.Fatalf("CreatePublicIP() error = %v", err)
		}
		if err := fakeCloud.AddPublicIPToServer(ctx, server.ID, created.ID); err != nil {
			t.Fatalf("AddPublicIPToServer() error = %v", err)
		}

		svc := publicip.New(clusterScope)
		if err := svc.Delete(ctx); err != nil {
			t.Fatalf("Delete() error = %v", err)
		}

		if fakeCloud.PublicIPCount() != 0 {
			t.Fatalf("PublicIPCount() = %d, want 0", fakeCloud.PublicIPCount())
		}
	})

	t.Run("succeeds when no public IP exists", func(t *testing.T) {
		ctx := context.Background()
		fakeCloud := cloudfake.New()
		clusterScope := newTestClusterScope(t, true, fakeCloud)

		svc := publicip.New(clusterScope)
		if err := svc.Delete(ctx); err != nil {
			t.Fatalf("Delete() error = %v, want nil", err)
		}
	})

	t.Run("returns error when DeletePublicIP fails with non-NotFound error", func(t *testing.T) {
		ctx := context.Background()
		fakeCloud := cloudfake.New()
		clusterScope := newTestClusterScope(t, true, fakeCloud)

		created, err := fakeCloud.CreatePublicIP(ctx, clusterScope.BastionTags())
		if err != nil {
			t.Fatalf("CreatePublicIP() error = %v", err)
		}
		clusterScope.SetBastionPublicIP(created.ID, created.IP)
		fakeCloud.FailNextDeletePublicIP = shared.ErrTransient

		svc := publicip.New(clusterScope)
		err = svc.Delete(ctx)
		if !errors.Is(err, shared.ErrTransient) {
			t.Fatalf("Delete() error = %v, want ErrTransient", err)
		}
		if clusterScope.BastionStatus().PublicIPID != created.ID {
			t.Fatalf("BastionStatus().PublicIPID = %q, want preserved %q on error", clusterScope.BastionStatus().PublicIPID, created.ID)
		}
	})
}
