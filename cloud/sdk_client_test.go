/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package cloud

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const (
	testSDKProjectID = "00000000-0000-0000-0000-000000000000"
	testSDKRegion    = "eu01"
	testSDKNetworkID = "11111111-1111-4111-8111-111111111111"
	testBoolTrue     = "true"
)

func TestSDKClientClassifiesHTTPStatusCodes(t *testing.T) {
	tests := []struct {
		name   string
		status int
		check  func(error) bool
	}{
		{name: "not found", status: http.StatusNotFound, check: IsNotFound},
		{name: "unauthorized", status: http.StatusUnauthorized, check: IsUnauthorized},
		{name: "conflict", status: http.StatusConflict, check: IsConflict},
		{name: "invalid input", status: http.StatusBadRequest, check: IsInvalidInput},
		{name: "rate limited", status: http.StatusTooManyRequests, check: IsTransient},
		{name: "server error", status: http.StatusInternalServerError, check: IsTransient},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := newSDKTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				http.Error(w, http.StatusText(tt.status), tt.status)
			}))
			client := newTestSDKClient(t, server.URL)

			_, err := client.GetNetwork(context.Background(), testSDKNetworkID)
			if err == nil {
				t.Fatal("GetNetwork() error = nil")
			}
			if !tt.check(err) {
				t.Fatalf("GetNetwork() error = %v, did not match expected classifier", err)
			}
		})
	}
}

func newSDKTestServer(t *testing.T, handler http.Handler) *httptest.Server {
	t.Helper()

	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	return server
}

func newTestSDKClient(t *testing.T, endpoint string) *SDKClient {
	t.Helper()

	t.Setenv(stackitNoAuthEnv, testBoolTrue)
	t.Setenv(stackitIAASEndpointEnv, endpoint)
	t.Setenv(stackitLBEndpointEnv, endpoint)

	client, err := NewClient(context.Background(), Credentials{
		ProjectID: testSDKProjectID,
		Region:    testSDKRegion,
	})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	sdkClient, ok := client.(*SDKClient)
	if !ok {
		t.Fatalf("NewClient() = %T, want *SDKClient", client)
	}
	return sdkClient
}

func writeJSON(t *testing.T, w http.ResponseWriter, value any) {
	t.Helper()

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(value); err != nil {
		t.Fatalf("encode response body: %v", err)
	}
}

func TestSDKClientListAvailabilityZones(t *testing.T) {
	server := newSDKTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/availability-zones") {
			writeJSON(t, w, map[string]any{
				"items": []any{"eu01-1", "eu01-2", "eu01-3"},
			})
			return
		}
		t.Fatalf("unexpected request: %s %s", r.Method, r.URL.String())
	}))

	client := newTestSDKClient(t, server.URL)
	zones, err := client.ListAvailabilityZones(context.Background())
	if err != nil {
		t.Fatalf("ListAvailabilityZones() error = %v", err)
	}
	if len(zones) != 3 || zones[0] != "eu01-1" || zones[1] != "eu01-2" || zones[2] != "eu01-3" {
		t.Fatalf("ListAvailabilityZones() = %v, want [eu01-1 eu01-2 eu01-3]", zones)
	}
}
