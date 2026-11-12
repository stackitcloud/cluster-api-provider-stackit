/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package network_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/stackitcloud/stackit-sdk-go/core/config"
	iaas "github.com/stackitcloud/stackit-sdk-go/services/iaas/v2api"

	"github.com/stackitcloud/cluster-api-provider-stackit/cloud/services/network"
	"github.com/stackitcloud/cluster-api-provider-stackit/cloud/services/shared"
)

const (
	testProjectID = "00000000-0000-0000-0000-000000000000"
	testRegion    = "eu01"
	testNetworkID = "11111111-1111-4111-8111-111111111111"
)

type testScope struct {
	projectID  string
	region     string
	iaasClient *iaas.APIClient
	spec       network.NetworkSpec
}

func (s *testScope) ProjectID() string {
	return s.projectID
}

func (s *testScope) Region() string {
	return s.region
}

func (s *testScope) IaaSClient() *iaas.APIClient {
	return s.iaasClient
}

func (s *testScope) NetworkSpec() network.NetworkSpec {
	return s.spec
}

func newTestIaaSClient(t *testing.T, handler http.Handler) *iaas.APIClient {
	t.Helper()

	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	iaasClient, err := iaas.NewAPIClient(
		config.WithoutAuthentication(),
		config.WithEndpoint(srv.URL),
	)
	if err != nil {
		t.Fatalf("iaas.NewAPIClient() error = %v", err)
	}
	return iaasClient
}

func newTestService(t *testing.T, spec network.NetworkSpec, handler http.Handler) *network.Service {
	t.Helper()

	return network.New(&testScope{
		projectID:  testProjectID,
		region:     testRegion,
		iaasClient: newTestIaaSClient(t, handler),
		spec:       spec,
	})
}

func writeJSON(t *testing.T, w http.ResponseWriter, value any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(value); err != nil {
		t.Fatalf("encode response body: %v", err)
	}
}

func TestServiceReconcile(t *testing.T) {
	t.Run("fetches network and records IPv4 prefixes via SetNetwork", func(t *testing.T) {
		var recorded *network.Network
		spec := network.NetworkSpec{
			ID: testNetworkID,
			SetNetwork: func(n *network.Network) {
				recorded = n
			},
		}

		svc := newTestService(t, spec, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/networks/"+testNetworkID) {
				writeJSON(t, w, map[string]any{
					"id":     testNetworkID,
					"name":   "cluster-net",
					"status": "CREATED",
					"routed": true,
					"ipv4": map[string]any{
						"prefixes": []string{"10.20.0.0/24"},
					},
				})
				return
			}
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.String())
		}))

		if err := svc.Reconcile(context.Background()); err != nil {
			t.Fatalf("Reconcile() error = %v", err)
		}
		if recorded == nil || recorded.ID != testNetworkID || recorded.Name != "cluster-net" {
			t.Fatalf("recorded = %#v, want ID=%s Name=cluster-net", recorded, testNetworkID)
		}
		if !slices.Equal(recorded.IPv4Prefixes, []string{"10.20.0.0/24"}) {
			t.Fatalf("recorded.IPv4Prefixes = %v, want [10.20.0.0/24]", recorded.IPv4Prefixes)
		}
	})

	t.Run("returns ErrNotFound when network does not exist", func(t *testing.T) {
		spec := network.NetworkSpec{
			ID: testNetworkID,
		}

		svc := newTestService(t, spec, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusNotFound)
			writeJSON(t, w, map[string]any{"message": "not found"})
		}))

		err := svc.Reconcile(context.Background())
		if !errors.Is(err, shared.ErrNotFound) {
			t.Fatalf("Reconcile() error = %v, want ErrNotFound", err)
		}
	})

	t.Run("returns ErrInvalidInput when network ID is empty", func(t *testing.T) {
		svc := newTestService(t, network.NetworkSpec{}, http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.String())
		}))

		err := svc.Reconcile(context.Background())
		if !errors.Is(err, shared.ErrInvalidInput) {
			t.Fatalf("Reconcile() error = %v, want ErrInvalidInput", err)
		}
	})
}
