/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package ske

import (
	"reflect"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	infrav1 "github.com/stackitcloud/cluster-api-provider-stackit/api/v1alpha1"
)

func TestComposeManagedClusterDeterministicOrder(t *testing.T) {
	clusterObj := &infrav1.StackitManagedCluster{
		ObjectMeta: metav1.ObjectMeta{Name: "capi-cluster", Namespace: "default"},
		Spec: infrav1.StackitManagedClusterSpec{
			ProjectID:   "11111111-1111-1111-1111-111111111111",
			Region:      "eu01",
			ClusterName: "ske-cluster",
			Kubernetes: infrav1.StackitManagedClusterKubernetesSpec{
				Version: "1.35.3",
			},
			Network: &infrav1.StackitManagedClusterNetworkSpec{
				ID: "22222222-2222-2222-2222-222222222222",
			},
			Labels: map[string]string{"env": "test"},
		},
	}

	poolB := &infrav1.StackitManagedNodePool{
		ObjectMeta: metav1.ObjectMeta{Name: "k8s-pool-a", Namespace: "default"},
		Spec: infrav1.StackitManagedNodePoolSpec{
			Name:              "pool-b",
			AvailabilityZones: []string{"eu01-2"},
			Machine: infrav1.StackitManagedNodePoolMachineSpec{
				Type: "c2i.4",
				Image: infrav1.StackitManagedNodePoolImageSpec{
					Name:    "flatcar",
					Version: "4459.2.1",
				},
			},
			Minimum: 2,
			Maximum: 5,
			Volume: infrav1.StackitManagedNodePoolVolumeSpec{
				Size: 50,
				Type: "storage_premium_perf1",
			},
			Taints: []infrav1.StackitManagedNodePoolTaint{
				{
					Effect: corev1.TaintEffectNoSchedule,
					Key:    "dedicated",
					Value:  "worker",
				},
			},
		},
	}

	poolA := &infrav1.StackitManagedNodePool{
		ObjectMeta: metav1.ObjectMeta{Name: "k8s-pool-z", Namespace: "default"},
		Spec: infrav1.StackitManagedNodePoolSpec{
			Name:              "pool-a",
			AvailabilityZones: []string{"eu01-1"},
			Machine: infrav1.StackitManagedNodePoolMachineSpec{
				Type: "c2i.2",
				Image: infrav1.StackitManagedNodePoolImageSpec{
					Name:    "flatcar",
					Version: "4459.2.1",
				},
			},
			Minimum:               1,
			Maximum:               3,
			AllowSystemComponents: new(true),
			Volume: infrav1.StackitManagedNodePoolVolumeSpec{
				Size: 30,
			},
		},
	}

	clusterState := ClusterDesiredState(clusterObj)
	first, err := ComposeManagedCluster(clusterState, []ManagedNodePoolDesiredState{
		NodePoolDesiredState(poolB),
		NodePoolDesiredState(poolA),
	})
	if err != nil {
		t.Fatalf("ComposeManagedCluster() unexpected error: %v", err)
	}

	second, err := ComposeManagedCluster(clusterState, []ManagedNodePoolDesiredState{
		NodePoolDesiredState(poolA),
		NodePoolDesiredState(poolB),
	})
	if err != nil {
		t.Fatalf("ComposeManagedCluster() unexpected error: %v", err)
	}

	if !reflect.DeepEqual(first, second) {
		t.Fatalf("ComposeManagedCluster() not deterministic: %#v != %#v", first, second)
	}
	if len(first.NodePools) != 2 || first.NodePools[0].Name != "pool-a" || first.NodePools[1].Name != "pool-b" {
		t.Fatalf("ComposeManagedCluster() node pool order = [%s, %s], want [pool-a, pool-b]",
			first.NodePools[0].Name, first.NodePools[1].Name)
	}
	if first.Cluster.ClusterName != "ske-cluster" || first.Cluster.NetworkID != "22222222-2222-2222-2222-222222222222" {
		t.Fatalf("ComposeManagedCluster() cluster state = %#v", first.Cluster)
	}
}
