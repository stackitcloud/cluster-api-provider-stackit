/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package scope

import (
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	infrav1 "github.com/stackitcloud/cluster-api-provider-stackit/api/v1alpha1"
)

func TestMachineScopeNetworkID(t *testing.T) {
	tests := []struct {
		name           string
		stackitMachine *infrav1.StackitMachine
		stackitCluster *infrav1.StackitCluster
		want           string
	}{
		{
			name: "machine-level network override takes precedence",
			stackitMachine: &infrav1.StackitMachine{
				Spec: infrav1.StackitMachineSpec{
					Network: infrav1.StackitMachineNetworkSpec{ID: "machine-network-id"},
				},
			},
			stackitCluster: &infrav1.StackitCluster{
				Spec: infrav1.StackitClusterSpec{
					Network: infrav1.StackitClusterNetworkSpec{ID: "cluster-spec-id"},
				},
				Status: infrav1.StackitClusterStatus{
					Network: infrav1.StackitNetworkStatus{ID: "cluster-status-id", Managed: true},
				},
			},
			want: "machine-network-id",
		},
		{
			name: "falls back to BYO cluster spec network ID when machine has none",
			stackitMachine: &infrav1.StackitMachine{
				Spec: infrav1.StackitMachineSpec{},
			},
			stackitCluster: &infrav1.StackitCluster{
				Spec: infrav1.StackitClusterSpec{
					Network: infrav1.StackitClusterNetworkSpec{ID: "cluster-byo-id"},
				},
			},
			want: "cluster-byo-id",
		},
		{
			name: "falls back to managed cluster status network ID when spec has none",
			stackitMachine: &infrav1.StackitMachine{
				Spec: infrav1.StackitMachineSpec{},
			},
			stackitCluster: &infrav1.StackitCluster{
				Spec: infrav1.StackitClusterSpec{},
				Status: infrav1.StackitClusterStatus{
					Network: infrav1.StackitNetworkStatus{ID: "cluster-managed-status-id", Managed: true},
				},
			},
			want: "cluster-managed-status-id",
		},
		{
			name: "returns empty when no network is configured anywhere",
			stackitMachine: &infrav1.StackitMachine{
				Spec: infrav1.StackitMachineSpec{},
			},
			stackitCluster: &infrav1.StackitCluster{
				Spec: infrav1.StackitClusterSpec{},
			},
			want: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			scope := &MachineScope{
				StackitMachine: tt.stackitMachine,
				StackitCluster: tt.stackitCluster,
			}
			if got := scope.NetworkID(); got != tt.want {
				t.Errorf("NetworkID() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestClusterScopeNetworkID(t *testing.T) {
	clusterBYO := &infrav1.StackitCluster{
		ObjectMeta: metav1.ObjectMeta{Name: "byo-cluster"},
		Spec: infrav1.StackitClusterSpec{
			Network: infrav1.StackitClusterNetworkSpec{ID: "byo-id"},
		},
	}
	scopeBYO := &ClusterScope{StackitCluster: clusterBYO}
	if got := scopeBYO.NetworkID(); got != "byo-id" {
		t.Errorf("ClusterScope.NetworkID() = %q, want byo-id", got)
	}

	clusterManaged := &infrav1.StackitCluster{
		ObjectMeta: metav1.ObjectMeta{Name: "managed-cluster"},
		Status: infrav1.StackitClusterStatus{
			Network: infrav1.StackitNetworkStatus{ID: "managed-status-id", Managed: true},
		},
	}
	scopeManaged := &ClusterScope{StackitCluster: clusterManaged}
	if got := scopeManaged.NetworkID(); got != "managed-status-id" {
		t.Errorf("ClusterScope.NetworkID() = %q, want managed-status-id", got)
	}
}
