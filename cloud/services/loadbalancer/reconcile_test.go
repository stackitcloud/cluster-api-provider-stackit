/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package loadbalancer_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/stackitcloud/stackit-sdk-go/core/config"
	lb "github.com/stackitcloud/stackit-sdk-go/services/loadbalancer/v2api"

	"github.com/stackitcloud/cluster-api-provider-stackit/cloud/services/loadbalancer"
	"github.com/stackitcloud/cluster-api-provider-stackit/cloud/services/shared"
)

const (
	testProjectID = "00000000-0000-0000-0000-000000000000"
	testRegion    = "eu01"
	testNetworkID = "11111111-1111-4111-8111-111111111111"
	testLBName    = "test-cluster-apiserver"
	testPublicIP  = "203.0.113.10"
)

type testScope struct {
	projectID string
	region    string
	lbClient  *lb.APIClient
	spec      loadbalancer.LoadBalancerSpec
}

func (s *testScope) ProjectID() string {
	return s.projectID
}

func (s *testScope) Region() string {
	return s.region
}

func (s *testScope) LoadBalancerClient() *lb.APIClient {
	return s.lbClient
}

func (s *testScope) LoadBalancerSpec() loadbalancer.LoadBalancerSpec {
	return s.spec
}

func newTestLBClient(t *testing.T, handler http.Handler) *lb.APIClient {
	t.Helper()

	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	lbClient, err := lb.NewAPIClient(
		config.WithoutAuthentication(),
		config.WithEndpoint(server.URL),
	)
	if err != nil {
		t.Fatalf("lb.NewAPIClient() error = %v", err)
	}
	return lbClient
}

func newTestService(t *testing.T, spec loadbalancer.LoadBalancerSpec, handler http.Handler) *loadbalancer.Service {
	t.Helper()

	return loadbalancer.New(&testScope{
		projectID: testProjectID,
		region:    testRegion,
		lbClient:  newTestLBClient(t, handler),
		spec:      spec,
	})
}

func writeJSON(t *testing.T, w http.ResponseWriter, value any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(value); err != nil {
		t.Fatalf("encode response body: %v", err)
	}
}

func readJSON(t *testing.T, r *http.Request) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.NewDecoder(r.Body).Decode(&out); err != nil {
		t.Fatalf("decode request body: %v", err)
	}
	return out
}

func sdkLoadBalancerJSON(name string, labels map[string]any, targetPools []any) map[string]any {
	m := map[string]any{
		"name":            name,
		"externalAddress": testPublicIP,
		"listeners": []any{
			map[string]any{"displayName": "apiserver", "port": 6443, "targetPool": "apiserver"},
		},
		"targetPools": targetPools,
	}
	if len(labels) > 0 {
		m["labels"] = labels
	}
	return m
}

func TestServiceReconcile(t *testing.T) {
	t.Run("creates load balancer with expected payload and updates target pool", func(t *testing.T) {
		var (
			createPayload map[string]any
			recordedID    string
			recordedIP    string
			recordedPort  int32
		)
		spec := loadbalancer.LoadBalancerSpec{
			Name:      testLBName,
			NetworkID: testNetworkID,
			Port:      loadbalancer.DefaultAPIServerPort,
			Tags: map[string]string{
				"cluster.x-k8s.io/cluster-name":        "test",
				"cluster-api-provider-stackit/test-id": "run-1",
			},
			Targets: []loadbalancer.Target{
				{Name: "cp-0", IP: "10.0.0.10"},
			},
			SeedTargets: []loadbalancer.Target{
				{Name: "cp-0", IP: "10.0.0.10"},
			},
			SetID: func(id string) {
				recordedID = id
			},
			SetAddress: func(ip string, port int32) {
				recordedIP = ip
				recordedPort = port
			},
		}

		targetPool := []any{
			map[string]any{
				"name":       "apiserver",
				"targetPort": 6443,
				"targets": []any{
					map[string]any{"displayName": "cp-0", "ip": "10.0.0.10"},
				},
			},
		}

		svc := newTestService(t, spec, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			path := r.URL.Path
			switch {
			case r.Method == http.MethodGet && strings.HasSuffix(path, "/load-balancers"):
				writeJSON(t, w, map[string]any{"loadBalancers": []any{}})
			case r.Method == http.MethodPost && strings.HasSuffix(path, "/load-balancers"):
				createPayload = readJSON(t, r)
				writeJSON(t, w, sdkLoadBalancerJSON(testLBName, nil, targetPool))
			case r.Method == http.MethodGet && strings.HasSuffix(path, "/load-balancers/"+testLBName):
				writeJSON(t, w, sdkLoadBalancerJSON(testLBName, nil, targetPool))
			default:
				t.Fatalf("unexpected request: %s %s", r.Method, r.URL.String())
			}
		}))

		if err := svc.Reconcile(context.Background()); err != nil {
			t.Fatalf("Reconcile() error = %v", err)
		}

		if recordedID != testLBName {
			t.Fatalf("recordedID = %q, want %q", recordedID, testLBName)
		}
		if recordedIP != testPublicIP || recordedPort != 6443 {
			t.Fatalf("recorded address = %s:%d, want %s:6443", recordedIP, recordedPort, testPublicIP)
		}
		if createPayload["name"] != testLBName {
			t.Fatalf("payload name = %v, want %q", createPayload["name"], testLBName)
		}
		if createPayload["region"] != testRegion {
			t.Fatalf("payload region = %v, want %q", createPayload["region"], testRegion)
		}
		labels, _ := createPayload["labels"].(map[string]any)
		if labels["cluster.x-k8s.io.cluster-name"] != "test" || labels["cluster-api-provider-stackit.test-id"] != "run-1" {
			t.Fatalf("payload labels = %#v", labels)
		}
	})

	t.Run("uses bootstrap placeholder target when initial targets are empty", func(t *testing.T) {
		var createPayload map[string]any
		spec := loadbalancer.LoadBalancerSpec{
			Name:      testLBName,
			NetworkID: testNetworkID,
			Port:      loadbalancer.DefaultAPIServerPort,
			Tags:      map[string]string{"cluster.x-k8s.io/cluster-name": "test"},
		}

		svc := newTestService(t, spec, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			path := r.URL.Path
			switch {
			case r.Method == http.MethodGet && strings.HasSuffix(path, "/load-balancers"):
				writeJSON(t, w, map[string]any{"loadBalancers": []any{}})
			case r.Method == http.MethodPost && strings.HasSuffix(path, "/load-balancers"):
				createPayload = readJSON(t, r)
				writeJSON(t, w, sdkLoadBalancerJSON(testLBName, nil, []any{
					map[string]any{
						"name":       "apiserver",
						"targetPort": 6443,
						"targets": []any{
							map[string]any{"displayName": "capi-bootstrap-placeholder", "ip": "10.0.0.1"},
						},
					},
				}))
			default:
				t.Fatalf("unexpected request: %s %s", r.Method, r.URL.String())
			}
		}))

		if err := svc.Reconcile(context.Background()); err != nil {
			t.Fatalf("Reconcile() error = %v", err)
		}

		pools, _ := createPayload["targetPools"].([]any)
		if len(pools) != 1 {
			t.Fatalf("targetPools = %#v, want 1 pool", pools)
		}
		pool, _ := pools[0].(map[string]any)
		targets, _ := pool["targets"].([]any)
		if len(targets) != 1 {
			t.Fatalf("targets = %#v, want 1 target", targets)
		}
		target, _ := targets[0].(map[string]any)
		if target["displayName"] != "capi-bootstrap-placeholder" || target["ip"] != "10.0.0.1" {
			t.Fatalf("target = %#v, want placeholder", target)
		}
	})
}

func TestServiceReconcileReuseAndErrors(t *testing.T) {
	t.Run("reuses existing load balancer found by ID without listing by tags", func(t *testing.T) {
		var recordedID string
		spec := loadbalancer.LoadBalancerSpec{
			ID:        testLBName,
			Name:      testLBName,
			NetworkID: testNetworkID,
			Port:      loadbalancer.DefaultAPIServerPort,
			Tags:      map[string]string{"cluster.x-k8s.io/cluster-name": "test"},
			SetID: func(id string) {
				recordedID = id
			},
		}

		svc := newTestService(t, spec, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/load-balancers/"+testLBName) {
				writeJSON(t, w, sdkLoadBalancerJSON(testLBName, nil, nil))
				return
			}
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.String())
		}))

		if err := svc.Reconcile(context.Background()); err != nil {
			t.Fatalf("Reconcile() error = %v", err)
		}
		if recordedID != testLBName {
			t.Fatalf("recordedID = %q, want %q", recordedID, testLBName)
		}
	})

	t.Run("falls back to tag lookup when recorded ID is not found during reconcile", func(t *testing.T) {
		var (
			getByIDCalled bool
			recordedID    string
		)
		spec := loadbalancer.LoadBalancerSpec{
			ID:        "stale-lb",
			Name:      testLBName,
			NetworkID: testNetworkID,
			Port:      loadbalancer.DefaultAPIServerPort,
			Tags:      map[string]string{"cluster.x-k8s.io/cluster-name": "test"},
			SetID: func(id string) {
				recordedID = id
			},
		}

		svc := newTestService(t, spec, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			path := r.URL.Path
			switch {
			case r.Method == http.MethodGet && strings.HasSuffix(path, "/load-balancers/stale-lb"):
				getByIDCalled = true
				w.WriteHeader(http.StatusNotFound)
				writeJSON(t, w, map[string]any{"message": "not found"})
			case r.Method == http.MethodGet && strings.HasSuffix(path, "/load-balancers"):
				writeJSON(t, w, map[string]any{"loadBalancers": []any{
					sdkLoadBalancerJSON(testLBName, map[string]any{
						"cluster.x-k8s.io.cluster-name": "test",
					}, nil),
				}})
			default:
				t.Fatalf("unexpected request: %s %s", r.Method, r.URL.String())
			}
		}))

		if err := svc.Reconcile(context.Background()); err != nil {
			t.Fatalf("Reconcile() error = %v", err)
		}
		if !getByIDCalled {
			t.Fatal("expected GET by spec.ID before falling back to tag lookup")
		}
		if recordedID != testLBName {
			t.Fatalf("recordedID = %q, want %q", recordedID, testLBName)
		}
	})

	t.Run("reuses existing load balancer found by tags without creating duplicate", func(t *testing.T) {
		var recordedID string
		spec := loadbalancer.LoadBalancerSpec{
			Name:      testLBName,
			NetworkID: testNetworkID,
			Port:      loadbalancer.DefaultAPIServerPort,
			Tags:      map[string]string{"cluster.x-k8s.io/cluster-name": "test"},
			SetID: func(id string) {
				recordedID = id
			},
		}

		svc := newTestService(t, spec, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/load-balancers") {
				writeJSON(t, w, map[string]any{"loadBalancers": []any{
					sdkLoadBalancerJSON(testLBName, map[string]any{
						"cluster.x-k8s.io.cluster-name": "test",
					}, nil),
				}})
				return
			}
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.String())
		}))

		if err := svc.Reconcile(context.Background()); err != nil {
			t.Fatalf("Reconcile() error = %v", err)
		}
		if recordedID != testLBName {
			t.Fatalf("recordedID = %q, want %q", recordedID, testLBName)
		}
	})

	t.Run("returns ErrConflict when multiple load balancers match tags", func(t *testing.T) {
		spec := loadbalancer.LoadBalancerSpec{
			Name:      testLBName,
			NetworkID: testNetworkID,
			Port:      loadbalancer.DefaultAPIServerPort,
			Tags:      map[string]string{"cluster.x-k8s.io/cluster-name": "test"},
		}

		svc := newTestService(t, spec, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			writeJSON(t, w, map[string]any{"loadBalancers": []any{
				sdkLoadBalancerJSON("lb-1", map[string]any{"cluster.x-k8s.io.cluster-name": "test"}, nil),
				sdkLoadBalancerJSON("lb-2", map[string]any{"cluster.x-k8s.io.cluster-name": "test"}, nil),
			}})
		}))

		err := svc.Reconcile(context.Background())
		if !errors.Is(err, shared.ErrConflict) {
			t.Fatalf("Reconcile() error = %v, want ErrConflict", err)
		}
	})

	t.Run("wraps target pool update failures in ErrTargetPoolUpdate after setting ID and address", func(t *testing.T) {
		var (
			recordedID string
			recordedIP string
		)
		spec := loadbalancer.LoadBalancerSpec{
			Name:      testLBName,
			NetworkID: testNetworkID,
			Port:      loadbalancer.DefaultAPIServerPort,
			Tags:      map[string]string{"cluster.x-k8s.io/cluster-name": "test"},
			Targets:   []loadbalancer.Target{{Name: "cp-0", IP: "10.0.0.10"}},
			SetID: func(id string) {
				recordedID = id
			},
			SetAddress: func(ip string, _ int32) {
				recordedIP = ip
			},
		}

		svc := newTestService(t, spec, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			path := r.URL.Path
			switch {
			case r.Method == http.MethodGet && strings.HasSuffix(path, "/load-balancers"):
				writeJSON(t, w, map[string]any{"loadBalancers": []any{
					sdkLoadBalancerJSON(testLBName, map[string]any{
						"cluster.x-k8s.io.cluster-name": "test",
					}, nil),
				}})
			case r.Method == http.MethodGet && strings.HasSuffix(path, "/load-balancers/"+testLBName):
				w.WriteHeader(http.StatusInternalServerError)
				writeJSON(t, w, map[string]any{"message": "backend error"})
			default:
				t.Fatalf("unexpected request: %s %s", r.Method, r.URL.String())
			}
		}))

		err := svc.Reconcile(context.Background())
		if !loadbalancer.IsTargetPoolUpdateError(err) {
			t.Fatalf("Reconcile() error = %v, want IsTargetPoolUpdateError", err)
		}
		if !shared.IsTransient(err) {
			t.Fatalf("Reconcile() error = %v, want wrapped ErrTransient", err)
		}
		if recordedID != testLBName || recordedIP != testPublicIP {
			t.Fatalf("recordedID=%q recordedIP=%q, want both set before target error", recordedID, recordedIP)
		}
	})
}

func TestSetTargets(t *testing.T) {
	var mu sync.Mutex
	targets := []any{
		map[string]any{"displayName": "cp-0", "ip": "10.0.0.10"},
	}
	var updatePayloads []map[string]any

	scope := &testScope{
		projectID: testProjectID,
		region:    testRegion,
		lbClient: newTestLBClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			defer mu.Unlock()

			switch {
			case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/load-balancers/"+testLBName):
				writeJSON(t, w, sdkLoadBalancerJSON(testLBName, nil, []any{
					map[string]any{"name": "apiserver", "targetPort": 6443, "targets": targets},
				}))
			case r.Method == http.MethodPut && strings.HasSuffix(r.URL.Path, "/load-balancers/"+testLBName+"/target-pools/apiserver"):
				payload := readJSON(t, r)
				updatePayloads = append(updatePayloads, payload)
				next, ok := payload["targets"].([]any)
				if !ok {
					t.Fatalf("update target payload missing targets: %#v", payload)
				}
				targets = next
				writeJSON(t, w, map[string]any{
					"name":       "apiserver",
					"targetPort": 6443,
					"targets":    targets,
				})
			default:
				t.Fatalf("unexpected request: %s %s", r.Method, r.URL.String())
			}
		})),
		spec: loadbalancer.LoadBalancerSpec{
			ID:        testLBName,
			Name:      testLBName,
			NetworkID: testNetworkID,
			Port:      6443,
			Tags:      map[string]string{"cluster": "test"},
		},
	}
	svc := loadbalancer.New(scope)

	desired := []loadbalancer.Target{
		{Name: "cp-0", IP: "10.0.0.10"},
		{Name: "cp-1", IP: "10.0.0.11"},
	}
	scope.spec.Targets = desired
	if err := svc.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	if len(updatePayloads) != 1 {
		t.Fatalf("got %d update payloads, want 1", len(updatePayloads))
	}

	// Unchanged pool must not trigger another write.
	if err := svc.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile() repeat error = %v", err)
	}
	if len(updatePayloads) != 1 {
		t.Fatalf("got %d update payloads after unchanged set, want 1", len(updatePayloads))
	}

	// Shrinking pool updates targets.
	scope.spec.Targets = desired[:1]
	if err := svc.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile() shrink error = %v", err)
	}
	if len(updatePayloads) != 2 {
		t.Fatalf("got %d update payloads, want 2", len(updatePayloads))
	}

	// Target missing name or IP is rejected.
	scope.spec.Targets = []loadbalancer.Target{{Name: "", IP: "10.0.0.10"}}
	if err := svc.Reconcile(context.Background()); !errors.Is(err, shared.ErrInvalidInput) {
		t.Fatalf("Reconcile(invalid target) error = %v, want ErrInvalidInput", err)
	}
}

func TestServiceReconcileValidation(t *testing.T) {
	tests := []struct {
		name string
		spec loadbalancer.LoadBalancerSpec
	}{
		{
			name: "missing name",
			spec: loadbalancer.LoadBalancerSpec{
				NetworkID: testNetworkID,
				Port:      6443,
				Tags:      map[string]string{"cluster": "test"},
			},
		},
		{
			name: "missing network ID",
			spec: loadbalancer.LoadBalancerSpec{
				Name: testLBName,
				Port: 6443,
				Tags: map[string]string{"cluster": "test"},
			},
		},
		{
			name: "invalid port",
			spec: loadbalancer.LoadBalancerSpec{
				Name:      testLBName,
				NetworkID: testNetworkID,
				Tags:      map[string]string{"cluster": "test"},
			},
		},
		{
			name: "missing tags",
			spec: loadbalancer.LoadBalancerSpec{
				Name:      testLBName,
				NetworkID: testNetworkID,
				Port:      6443,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc := newTestService(t, tt.spec, http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
				t.Fatalf("unexpected request: %s %s", r.Method, r.URL.String())
			}))

			err := svc.Reconcile(context.Background())
			if !errors.Is(err, shared.ErrInvalidInput) {
				t.Fatalf("Reconcile() error = %v, want ErrInvalidInput", err)
			}
		})
	}
}

func TestServiceDelete(t *testing.T) {
	t.Run("deletes load balancer using ID when provided and clears ID", func(t *testing.T) {
		var (
			deletedID string
			clearedID = "not-cleared"
		)
		spec := loadbalancer.LoadBalancerSpec{
			ID: testLBName,
			SetID: func(id string) {
				clearedID = id
			},
		}

		svc := newTestService(t, spec, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodDelete && strings.HasSuffix(r.URL.Path, "/load-balancers/"+testLBName) {
				deletedID = testLBName
				writeJSON(t, w, map[string]any{})
				return
			}
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.String())
		}))

		if err := svc.Delete(context.Background()); err != nil {
			t.Fatalf("Delete() error = %v", err)
		}
		if deletedID != testLBName {
			t.Fatalf("deletedID = %q, want %q", deletedID, testLBName)
		}
		if clearedID != "" {
			t.Fatalf("clearedID = %q, want empty", clearedID)
		}
	})

	t.Run("falls back to tag lookup when ID is empty and deletes all matching load balancers", func(t *testing.T) {
		var deletedIDs []string
		spec := loadbalancer.LoadBalancerSpec{
			Tags: map[string]string{"cluster.x-k8s.io/cluster-name": "test"},
		}

		svc := newTestService(t, spec, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			path := r.URL.Path
			switch {
			case r.Method == http.MethodGet && strings.HasSuffix(path, "/load-balancers"):
				writeJSON(t, w, map[string]any{"loadBalancers": []any{
					sdkLoadBalancerJSON(testLBName, map[string]any{
						"cluster.x-k8s.io.cluster-name": "test",
					}, nil),
					sdkLoadBalancerJSON("second-lb", map[string]any{
						"cluster.x-k8s.io.cluster-name": "test",
					}, nil),
					sdkLoadBalancerJSON("other-lb", map[string]any{
						"cluster.x-k8s.io.cluster-name": "other",
					}, nil),
				}})
			case r.Method == http.MethodDelete && strings.HasSuffix(path, "/load-balancers/"+testLBName):
				deletedIDs = append(deletedIDs, testLBName)
				writeJSON(t, w, map[string]any{})
			case r.Method == http.MethodDelete && strings.HasSuffix(path, "/load-balancers/second-lb"):
				deletedIDs = append(deletedIDs, "second-lb")
				writeJSON(t, w, map[string]any{})
			default:
				t.Fatalf("unexpected request: %s %s", r.Method, r.URL.String())
			}
		}))

		if err := svc.Delete(context.Background()); err != nil {
			t.Fatalf("Delete() error = %v", err)
		}
		if len(deletedIDs) != 2 || deletedIDs[0] != testLBName || deletedIDs[1] != "second-lb" {
			t.Fatalf("deletedIDs = %v, want [%s second-lb]", deletedIDs, testLBName)
		}
	})

	t.Run("falls back to tag lookup when recorded ID no longer exists", func(t *testing.T) {
		var deletedID string
		spec := loadbalancer.LoadBalancerSpec{
			ID:   "stale-lb",
			Tags: map[string]string{"cluster.x-k8s.io/cluster-name": "test"},
		}

		svc := newTestService(t, spec, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			path := r.URL.Path
			switch {
			case r.Method == http.MethodDelete && strings.HasSuffix(path, "/load-balancers/stale-lb"):
				w.WriteHeader(http.StatusNotFound)
				writeJSON(t, w, map[string]any{"message": "not found"})
			case r.Method == http.MethodGet && strings.HasSuffix(path, "/load-balancers"):
				writeJSON(t, w, map[string]any{"loadBalancers": []any{
					sdkLoadBalancerJSON(testLBName, map[string]any{
						"cluster.x-k8s.io.cluster-name": "test",
					}, nil),
				}})
			case r.Method == http.MethodDelete && strings.HasSuffix(path, "/load-balancers/"+testLBName):
				deletedID = testLBName
				writeJSON(t, w, map[string]any{})
			default:
				t.Fatalf("unexpected request: %s %s", r.Method, r.URL.String())
			}
		}))

		if err := svc.Delete(context.Background()); err != nil {
			t.Fatalf("Delete() error = %v", err)
		}
		if deletedID != testLBName {
			t.Fatalf("deletedID = %q, want %q", deletedID, testLBName)
		}
	})

	t.Run("succeeds when load balancer does not exist", func(t *testing.T) {
		spec := loadbalancer.LoadBalancerSpec{
			Tags: map[string]string{"cluster.x-k8s.io/cluster-name": "test"},
		}

		svc := newTestService(t, spec, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			writeJSON(t, w, map[string]any{"loadBalancers": []any{}})
		}))

		if err := svc.Delete(context.Background()); err != nil {
			t.Fatalf("Delete() error = %v, want nil", err)
		}
	})

	t.Run("returns ErrInvalidInput when both ID and tags are empty", func(t *testing.T) {
		svc := newTestService(t, loadbalancer.LoadBalancerSpec{}, http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.String())
		}))

		if err := svc.Delete(context.Background()); !errors.Is(err, shared.ErrInvalidInput) {
			t.Fatalf("Delete() error = %v, want ErrInvalidInput", err)
		}
	})
}
