/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package util

import "testing"

func TestClusterTags(t *testing.T) {
	got := ClusterTags("cluster-a", "default", map[string]string{
		"environment":         "dev",
		LabelClusterName:      "override",
		LabelClusterNamespace: "override",
		LabelManagedBy:        "override",
		LabelProviderManaged:  "override",
		"another.example":     "value",
	})

	assertTag(t, got, LabelClusterName, "cluster-a")
	assertTag(t, got, LabelClusterNamespace, "default")
	assertTag(t, got, LabelManagedBy, ManagedByValue)
	assertTag(t, got, LabelProviderManaged, ProviderManagedValue)
	assertTag(t, got, "environment", "dev")
	assertTag(t, got, "another.example", "value")
}

func TestMachineTags(t *testing.T) {
	got := MachineTags("cluster-a", "default", "machine-a", "uid-a", map[string]string{
		"environment":        "dev",
		LabelMachineName:     "override",
		LabelMachineUID:      "override",
		LabelManagedBy:       "override",
		LabelProviderManaged: "override",
		LabelClusterName:     "override",
		"another.example":    "value",
	})

	assertTag(t, got, LabelClusterName, "cluster-a")
	assertTag(t, got, LabelClusterNamespace, "default")
	assertTag(t, got, LabelMachineName, "machine-a")
	assertTag(t, got, LabelMachineUID, "uid-a")
	assertTag(t, got, LabelManagedBy, ManagedByValue)
	assertTag(t, got, LabelProviderManaged, ProviderManagedValue)
	assertTag(t, got, "environment", "dev")
	assertTag(t, got, "another.example", "value")
}

func assertTag(t *testing.T, tags map[string]string, key, want string) {
	t.Helper()
	if got := tags[key]; got != want {
		t.Fatalf("tag %q = %q, want %q", key, got, want)
	}
}
