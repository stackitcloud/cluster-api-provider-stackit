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
	shared.NoOpReconcileScope
	projectID   string
	region      string
	iaasClient  *iaas.APIClient
	spec        network.NetworkSpec
	setNetwork  func(network *network.Network)
	eventReason string
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

func (s *testScope) SetNetwork(net *network.Network) {
	if s.setNetwork != nil {
		s.setNetwork(net)
	}
}

func (s *testScope) Eventf(reason, _ string, _ ...any) {
	s.eventReason = reason
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

func newTestServiceWithScope(t *testing.T, spec network.NetworkSpec, setNetwork func(*network.Network), handler http.Handler) (*network.Service, *testScope) {
	t.Helper()

	scope := &testScope{
		projectID:  testProjectID,
		region:     testRegion,
		iaasClient: newTestIaaSClient(t, handler),
		spec:       spec,
		setNetwork: setNetwork,
	}
	return network.New(scope), scope
}

func newTestService(t *testing.T, spec network.NetworkSpec, handler http.Handler) *network.Service {
	t.Helper()

	svc, _ := newTestServiceWithScope(t, spec, nil, handler)
	return svc
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
		}

		svc, scope := newTestServiceWithScope(t, spec, func(n *network.Network) {
			recorded = n
		}, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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

		if _, err := svc.Reconcile(context.Background()); err != nil {
			t.Fatalf("Reconcile() error = %v", err)
		}
		if recorded == nil || recorded.ID != testNetworkID || recorded.Name != "cluster-net" {
			t.Fatalf("recorded = %#v, want ID=%s Name=cluster-net", recorded, testNetworkID)
		}
		if scope.eventReason != "" {
			t.Fatalf("eventReason = %q, want empty for unmanaged network", scope.eventReason)
		}
		if !slices.Equal(recorded.IPv4Prefixes, []string{"10.20.0.0/24"}) {
			t.Fatalf("recorded.IPv4Prefixes = %v, want [10.20.0.0/24]", recorded.IPv4Prefixes)
		}
	})

	t.Run("returns ErrNotFound when network does not exist", func(t *testing.T) {
		spec := network.NetworkSpec{
			ID: testNetworkID,
		}

		svc := newTestService(t, spec, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusNotFound)
			writeJSON(t, w, map[string]any{"message": "not found"})
		}))

		_, err := svc.Reconcile(context.Background())
		if !errors.Is(err, shared.ErrNotFound) {
			t.Fatalf("Reconcile() error = %v, want ErrNotFound", err)
		}
	})

	t.Run("returns ErrInvalidInput when network ID is empty for unmanaged network", func(t *testing.T) {
		svc := newTestService(t, network.NetworkSpec{}, http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.String())
		}))

		_, err := svc.Reconcile(context.Background())
		if !errors.Is(err, shared.ErrInvalidInput) {
			t.Fatalf("Reconcile() error = %v, want ErrInvalidInput", err)
		}
	})

	t.Run("creates managed network when none exists", func(t *testing.T) {
		var (
			recorded       *network.Network
			createdPayload map[string]any
		)
		spec := network.NetworkSpec{
			Name:       "test-network",
			IPv4Prefix: network.DefaultNetworkPrefix,
			Tags:       map[string]string{"cluster": "test-cluster"},
			Managed:    true,
		}

		svc, scope := newTestServiceWithScope(t, spec, func(n *network.Network) {
			recorded = n
		}, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/networks") {
				writeJSON(t, w, map[string]any{"items": []any{}})
				return
			}
			if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/networks") {
				if err := json.NewDecoder(r.Body).Decode(&createdPayload); err != nil {
					t.Fatalf("decode payload: %v", err)
				}
				writeJSON(t, w, map[string]any{
					"id":     testNetworkID,
					"name":   "test-network",
					"status": "CREATED",
					"ipv4": map[string]any{
						"prefixes": []any{"10.0.0.0/24"},
					},
				})
				return
			}
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.String())
		}))

		if _, err := svc.Reconcile(context.Background()); err != nil {
			t.Fatalf("Reconcile() error = %v", err)
		}
		if recorded == nil || recorded.ID != testNetworkID {
			t.Fatalf("recorded = %#v, want ID=%s", recorded, testNetworkID)
		}
		if scope.eventReason != "NetworkCreated" {
			t.Fatalf("eventReason = %q, want NetworkCreated", scope.eventReason)
		}
		if createdPayload["name"] != "test-network" {
			t.Fatalf("payload name = %v, want test-network", createdPayload["name"])
		}
	})

	t.Run("adopts existing managed network by tags without creating duplicate", func(t *testing.T) {
		var recorded *network.Network
		spec := network.NetworkSpec{
			Name:       "existing-network",
			IPv4Prefix: network.DefaultNetworkPrefix,
			Tags:       map[string]string{"cluster": "test-cluster"},
			Managed:    true,
		}

		svc, scope := newTestServiceWithScope(t, spec, func(n *network.Network) {
			recorded = n
		}, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/networks") {
				writeJSON(t, w, map[string]any{
					"items": []any{
						map[string]any{
							"id":     testNetworkID,
							"name":   "existing-network",
							"status": "CREATED",
							"labels": map[string]any{
								"cluster": "test-cluster",
							},
							"ipv4": map[string]any{
								"prefixes": []any{"10.0.0.0/24"},
							},
						},
					},
				})
				return
			}
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.String())
		}))

		if _, err := svc.Reconcile(context.Background()); err != nil {
			t.Fatalf("Reconcile() error = %v", err)
		}
		if recorded == nil || recorded.ID != testNetworkID {
			t.Fatalf("recorded = %#v, want ID=%s", recorded, testNetworkID)
		}
		if scope.eventReason != "" {
			t.Fatalf("eventReason = %q, want empty when adopting existing network", scope.eventReason)
		}
	})
}

func TestServiceDelete(t *testing.T) {
	t.Run("does not delete unmanaged network", func(t *testing.T) {
		spec := network.NetworkSpec{
			ID:      testNetworkID,
			Managed: false,
		}
		svc := newTestService(t, spec, http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.String())
		}))
		if _, err := svc.Delete(context.Background()); err != nil {
			t.Fatalf("Delete() error = %v", err)
		}
	})

	t.Run("deletes managed network by ID and clears via SetNetwork", func(t *testing.T) {
		deletedID := ""
		cleared := false
		spec := network.NetworkSpec{
			ID:      testNetworkID,
			Tags:    map[string]string{"cluster": "test-cluster"},
			Managed: true,
		}

		svc, _ := newTestServiceWithScope(t, spec, func(n *network.Network) {
			if n == nil {
				cleared = true
			}
		}, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodDelete && strings.HasSuffix(r.URL.Path, "/networks/"+testNetworkID) {
				deletedID = testNetworkID
				w.WriteHeader(http.StatusNoContent)
				return
			}
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.String())
		}))

		if _, err := svc.Delete(context.Background()); err != nil {
			t.Fatalf("Delete() error = %v", err)
		}
		if deletedID != testNetworkID {
			t.Fatalf("deletedID = %s, want %s", deletedID, testNetworkID)
		}
		if !cleared {
			t.Fatal("expected SetNetwork(nil) to be called")
		}
	})

	t.Run("falls back to tag lookup when ID is empty on Delete", func(t *testing.T) {
		deletedID := ""
		spec := network.NetworkSpec{
			Tags:    map[string]string{"cluster": "test-cluster"},
			Managed: true,
		}

		svc := newTestService(t, spec, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/networks") {
				writeJSON(t, w, map[string]any{
					"items": []any{
						map[string]any{
							"id":     testNetworkID,
							"name":   "managed-network",
							"status": "CREATED",
							"labels": map[string]any{
								"cluster": "test-cluster",
							},
						},
					},
				})
				return
			}
			if r.Method == http.MethodDelete && strings.HasSuffix(r.URL.Path, "/networks/"+testNetworkID) {
				deletedID = testNetworkID
				w.WriteHeader(http.StatusNoContent)
				return
			}
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.String())
		}))

		if _, err := svc.Delete(context.Background()); err != nil {
			t.Fatalf("Delete() error = %v", err)
		}
		if deletedID != testNetworkID {
			t.Fatalf("deletedID = %s, want %s", deletedID, testNetworkID)
		}
	})
}
