/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package cloud

import (
	"errors"
	"testing"
)

func TestNewProviderID(t *testing.T) {
	got := NewProviderID("server-id")
	want := "stackit://server-id"
	if got != want {
		t.Fatalf("NewProviderID() = %q, want %q", got, want)
	}
}

func TestParseProviderID(t *testing.T) {
	serverID, err := ParseProviderID("stackit://server-id")
	if err != nil {
		t.Fatalf("ParseProviderID() error = %v", err)
	}
	if serverID != "server-id" {
		t.Fatalf("ParseProviderID() = %q", serverID)
	}
}

func TestParseProviderIDInvalid(t *testing.T) {
	tests := []string{
		"",
		"aws://server-id",
		"stackit://project-id/server-id",
		"stackit:///server-id",
		"stackit://server-id/extra",
		"stackit://",
	}

	for _, tt := range tests {
		t.Run(tt, func(t *testing.T) {
			_, err := ParseProviderID(tt)
			if !errors.Is(err, ErrInvalidProviderID) {
				t.Fatalf("ParseProviderID(%q) error = %v, want ErrInvalidProviderID", tt, err)
			}
		})
	}
}

func TestProviderIDRoundTrip(t *testing.T) {
	wantServerID := "server-id"

	gotServerID, err := ParseProviderID(NewProviderID(wantServerID))
	if err != nil {
		t.Fatalf("ParseProviderID(NewProviderID()) error = %v", err)
	}
	if gotServerID != wantServerID {
		t.Fatalf("roundtrip = %q", gotServerID)
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

	parsedServerID, err := ParseProviderID(providerID)
	if err != nil {
		t.Fatalf("ParseProviderID(%q) error = %v", providerID, err)
	}
	if parsedServerID != serverID {
		t.Fatalf("ParseProviderID(%q) serverID = %q, want %q", providerID, serverID, parsedServerID)
	}
}
