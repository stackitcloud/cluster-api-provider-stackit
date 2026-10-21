/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package network

import (
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	infrav1 "github.com/stackitcloud/cluster-api-provider-stackit/api/v1alpha1"
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
