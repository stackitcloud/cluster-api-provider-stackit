/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package cloud

import (
	"testing"
)

func TestNewProviderID(t *testing.T) {
	got := NewProviderID("server-id")
	want := "stackit://server-id"
	if got != want {
		t.Fatalf("NewProviderID() = %q, want %q", got, want)
	}
}

func TestProviderIDMatchesCloudProviderStackitFormat(t *testing.T) {
	serverID := "321a8b81-3660-43e5-bab8-6470b65ee4e8"
	providerID := NewProviderID(serverID)

	// Verified against the local cloud-provider-stackit repository:
	// pkg/ccm/instances.go uses fmt.Sprintf("%s://%s", ProviderName, server.GetId()).
	if providerID != "stackit://"+serverID {
		t.Fatalf("NewProviderID() = %q, want cloud-provider-stackit format %q", providerID, "stackit://"+serverID)
	}
}
