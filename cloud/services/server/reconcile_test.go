/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package server_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stackitcloud/stackit-sdk-go/core/config"
	iaas "github.com/stackitcloud/stackit-sdk-go/services/iaas/v2api"

	"github.com/stackitcloud/cluster-api-provider-stackit/cloud/services/server"
	"github.com/stackitcloud/cluster-api-provider-stackit/cloud/services/shared"
)

const (
	testProjectID       = "00000000-0000-0000-0000-000000000000"
	testRegion          = "eu01"
	testServerID        = "33333333-3333-4333-8333-333333333333"
	testOtherServerID   = "44444444-4444-4444-8444-444444444444"
	testNetworkID       = "11111111-1111-4111-8111-111111111111"
	testImageID         = "22222222-2222-4222-8222-222222222222"
	testSecurityGroupID = "55555555-5555-4555-8555-555555555555"
	testBoolTrue        = "true"
)

type testScope struct {
	projectID  string
	region     string
	iaasClient *iaas.APIClient
	spec       server.ServerSpec
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

func (s *testScope) ServerSpec() server.ServerSpec {
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

func newTestService(t *testing.T, spec server.ServerSpec, handler http.Handler) *server.Service {
	t.Helper()

	return server.New(&testScope{
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

func readJSON(t *testing.T, r *http.Request) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.NewDecoder(r.Body).Decode(&out); err != nil {
		t.Fatalf("decode request body: %v", err)
	}
	return out
}

func assertStringField(t *testing.T, m map[string]any, key, want string) {
	t.Helper()
	if got := lookup(m, key); got != want {
		t.Fatalf("%s = %#v, want %q in %#v", key, got, want, m)
	}
}

func assertBoolField(t *testing.T, m map[string]any, key string, want bool) {
	t.Helper()
	if got := lookup(m, key); got != want {
		t.Fatalf("%s = %#v, want %t in %#v", key, got, want, m)
	}
}

func assertFieldAbsent(t *testing.T, payload map[string]any, key string) {
	t.Helper()
	if _, ok := payload[key]; ok {
		t.Fatalf("%s present in payload, want absent: %#v", key, payload[key])
	}
}

func assertNestedStringField(t *testing.T, m map[string]any, path []string, want string) {
	t.Helper()
	if got := nestedValue(t, m, path); got != want {
		t.Fatalf("%s = %#v, want %q in %#v", strings.Join(path, "."), got, want, m)
	}
}

func assertNestedNumberField(t *testing.T, m map[string]any, path []string, want float64) {
	t.Helper()
	if got := nestedValue(t, m, path); got != want {
		t.Fatalf("%s = %#v, want %v in %#v", strings.Join(path, "."), got, want, m)
	}
}

func nestedValue(t *testing.T, m map[string]any, path []string) any {
	t.Helper()

	var current any = m
	for _, part := range path {
		switch typed := current.(type) {
		case map[string]any:
			current = lookup(typed, part)
		default:
			t.Fatalf("path element %q cannot descend into %#v", part, typed)
		}
	}
	return current
}

func lookup(m map[string]any, key string) any {
	for _, candidate := range []string{
		key,
		strings.ToUpper(key[:1]) + key[1:],
	} {
		if value, ok := m[candidate]; ok {
			return value
		}
	}
	return nil
}

func TestServiceReconcile(t *testing.T) {
	t.Run("creates server with bootVolume and expected payload when not found", func(t *testing.T) {
		var (
			createPayload  map[string]any
			recordedServer *server.Server
			wasCreated     bool
		)
		spec := server.ServerSpec{
			Name:             "node-0",
			MachineType:      "c2i.2",
			ImageID:          testImageID,
			AvailabilityZone: "eu01-1",
			SSHKeyName:       "default",
			NetworkID:        testNetworkID,
			SecurityGroups:   []string{testSecurityGroupID},
			UserData:         []byte("#cloud-config\n"),
			RootVolume: server.RootVolume{
				SizeGiB:             50,
				PerformanceClass:    "storage_premium_perf6",
				DeleteOnTermination: true,
			},
			Tags: map[string]string{"cluster": "test"},
			SetServer: func(srv *server.Server, created bool) {
				recordedServer = srv
				wasCreated = created
			},
		}

		svc := newTestService(t, spec, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch {
			case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/servers"):
				if got := r.URL.Query().Get("label_selector"); got != "cluster=test" {
					t.Fatalf("label_selector = %q, want cluster=test", got)
				}
				if got := r.URL.Query().Get("details"); got != testBoolTrue {
					t.Fatalf("details = %q, want true", got)
				}
				writeJSON(t, w, map[string]any{"items": []any{}})
			case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/servers"):
				createPayload = readJSON(t, r)
				writeJSON(t, w, map[string]any{
					"id":               testServerID,
					"machineType":      "c2i.2",
					"name":             "node-0",
					"status":           "ACTIVE",
					"availabilityZone": "eu01-1",
				})
			case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/servers/"+testServerID+"/nics"):
				writeJSON(t, w, map[string]any{
					"items": []any{
						map[string]any{"ipv4": "10.0.0.10", "ipv6": "fd00::10"},
					},
				})
			default:
				t.Fatalf("unexpected request: %s %s", r.Method, r.URL.String())
			}
		}))

		if err := svc.Reconcile(context.Background()); err != nil {
			t.Fatalf("Reconcile() error = %v", err)
		}
		if !wasCreated {
			t.Fatal("SetServer created = false, want true")
		}
		if recordedServer == nil || recordedServer.ID != testServerID || recordedServer.Name != "node-0" || recordedServer.State != "ACTIVE" {
			t.Fatalf("recordedServer = %#v", recordedServer)
		}
		if len(recordedServer.Addresses) != 2 {
			t.Fatalf("recordedServer.Addresses = %#v, want 2 addresses", recordedServer.Addresses)
		}

		assertStringField(t, createPayload, "name", "node-0")
		assertStringField(t, createPayload, "machineType", "c2i.2")
		assertFieldAbsent(t, createPayload, "imageId")
		assertStringField(t, createPayload, "availabilityZone", "eu01-1")
		assertStringField(t, createPayload, "keypairName", "default")
		assertStringField(t, createPayload, "userData", "I2Nsb3VkLWNvbmZpZwo=")
		assertBoolField(t, createPayload, "configDrive", true)
		assertNestedStringField(t, createPayload, []string{"labels", "cluster"}, "test")
		assertNestedStringField(t, createPayload, []string{"networking", "networkId"}, testNetworkID)
		assertNestedStringField(t, createPayload, []string{"bootVolume", "performanceClass"}, "storage_premium_perf6")
		assertNestedStringField(t, createPayload, []string{"bootVolume", "source", "id"}, testImageID)
		assertNestedStringField(t, createPayload, []string{"bootVolume", "source", "type"}, "image")
		assertNestedNumberField(t, createPayload, []string{"bootVolume", "size"}, 50)
	})

	t.Run("creates server with imageId when rootVolume size and performanceClass are unset", func(t *testing.T) {
		var createPayload map[string]any
		spec := server.ServerSpec{
			Name:        "node-0",
			MachineType: "c2i.2",
			ImageID:     testImageID,
			NetworkID:   testNetworkID,
			Tags:        map[string]string{"cluster": "test"},
		}

		svc := newTestService(t, spec, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch {
			case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/servers"):
				writeJSON(t, w, map[string]any{"items": []any{}})
			case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/servers"):
				createPayload = readJSON(t, r)
				writeJSON(t, w, map[string]any{
					"id":          testServerID,
					"machineType": "c2i.2",
					"name":        "node-0",
					"status":      "ACTIVE",
				})
			case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/servers/"+testServerID+"/nics"):
				writeJSON(t, w, map[string]any{"items": []any{}})
			default:
				t.Fatalf("unexpected request: %s %s", r.Method, r.URL.String())
			}
		}))

		if err := svc.Reconcile(context.Background()); err != nil {
			t.Fatalf("Reconcile() error = %v", err)
		}
		assertStringField(t, createPayload, "imageId", testImageID)
		assertFieldAbsent(t, createPayload, "bootVolume")
	})
}

func TestServiceReconcileReuseAndErrors(t *testing.T) {
	t.Run("reuses existing server by ID without listing by tags or creating duplicate", func(t *testing.T) {
		var (
			recordedServer *server.Server
			wasCreated     = true
		)
		spec := server.ServerSpec{
			ID:          testServerID,
			Name:        "node-0",
			MachineType: "c2i.2",
			ImageID:     testImageID,
			NetworkID:   testNetworkID,
			Tags:        map[string]string{"cluster": "test"},
			SetServer: func(srv *server.Server, created bool) {
				recordedServer = srv
				wasCreated = created
			},
		}

		svc := newTestService(t, spec, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch {
			case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/servers/"+testServerID):
				writeJSON(t, w, map[string]any{
					"id":          testServerID,
					"machineType": "c2i.2",
					"name":        "node-0",
					"status":      "ACTIVE",
				})
			case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/servers/"+testServerID+"/nics"):
				writeJSON(t, w, map[string]any{"items": []any{}})
			default:
				t.Fatalf("unexpected request: %s %s", r.Method, r.URL.String())
			}
		}))

		if err := svc.Reconcile(context.Background()); err != nil {
			t.Fatalf("Reconcile() error = %v", err)
		}
		if wasCreated {
			t.Fatal("SetServer created = true, want false")
		}
		if recordedServer == nil || recordedServer.ID != testServerID {
			t.Fatalf("recordedServer = %#v, want ID %s", recordedServer, testServerID)
		}
	})

	t.Run("reuses existing server found by tags without creating duplicate", func(t *testing.T) {
		var (
			recordedServer *server.Server
			wasCreated     = true
		)
		spec := server.ServerSpec{
			Name:        "node-0",
			MachineType: "c2i.2",
			ImageID:     testImageID,
			NetworkID:   testNetworkID,
			Tags:        map[string]string{"cluster": "test"},
			SetServer: func(srv *server.Server, created bool) {
				recordedServer = srv
				wasCreated = created
			},
		}

		svc := newTestService(t, spec, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch {
			case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/servers"):
				writeJSON(t, w, map[string]any{"items": []any{
					map[string]any{
						"id":          testServerID,
						"machineType": "c2i.2",
						"name":        "node-0",
						"status":      "ACTIVE",
						"labels":      map[string]any{"cluster": "test"},
					},
				}})
			case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/servers/"+testServerID+"/nics"):
				writeJSON(t, w, map[string]any{"items": []any{}})
			default:
				t.Fatalf("unexpected request: %s %s", r.Method, r.URL.String())
			}
		}))

		if err := svc.Reconcile(context.Background()); err != nil {
			t.Fatalf("Reconcile() error = %v", err)
		}
		if wasCreated {
			t.Fatal("SetServer created = true, want false")
		}
		if recordedServer == nil || recordedServer.ID != testServerID {
			t.Fatalf("recordedServer = %#v, want ID %s", recordedServer, testServerID)
		}
	})

	t.Run("returns ErrServerGone when DisallowRecreate is true and server is missing", func(t *testing.T) {
		spec := server.ServerSpec{
			ID:               testServerID,
			Name:             "node-0",
			MachineType:      "c2i.2",
			ImageID:          testImageID,
			NetworkID:        testNetworkID,
			Tags:             map[string]string{"cluster": "test"},
			DisallowRecreate: true,
		}

		svc := newTestService(t, spec, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch {
			case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/servers/"+testServerID):
				w.WriteHeader(http.StatusNotFound)
				writeJSON(t, w, map[string]any{"message": "not found"})
			case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/servers"):
				writeJSON(t, w, map[string]any{"items": []any{}})
			default:
				t.Fatalf("unexpected request: %s %s", r.Method, r.URL.String())
			}
		}))

		err := svc.Reconcile(context.Background())
		if !server.IsServerGone(err) {
			t.Fatalf("Reconcile() error = %v, want ErrServerGone", err)
		}
	})

	t.Run("returns ErrConflict when multiple servers match tags", func(t *testing.T) {
		spec := server.ServerSpec{
			Name:        "node-0",
			MachineType: "c2i.2",
			ImageID:     testImageID,
			NetworkID:   testNetworkID,
			Tags:        map[string]string{"cluster": "test"},
		}

		svc := newTestService(t, spec, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/servers") {
				writeJSON(t, w, map[string]any{"items": []any{
					map[string]any{"id": testServerID, "name": "node-0", "machineType": "c2i.2", "labels": map[string]any{"cluster": "test"}},
					map[string]any{"id": testOtherServerID, "name": "node-1", "machineType": "c2i.2", "labels": map[string]any{"cluster": "test"}},
				}})
				return
			}
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.String())
		}))

		err := svc.Reconcile(context.Background())
		if !errors.Is(err, shared.ErrConflict) {
			t.Fatalf("Reconcile() error = %v, want ErrConflict", err)
		}
	})
}

func TestServiceReconcileValidation(t *testing.T) {
	tests := []struct {
		name string
		spec server.ServerSpec
	}{
		{
			name: "missing name",
			spec: server.ServerSpec{
				ImageID:     testImageID,
				MachineType: "c2i.2",
				NetworkID:   testNetworkID,
				Tags:        map[string]string{"cluster": "test"},
			},
		},
		{
			name: "missing image ID",
			spec: server.ServerSpec{
				Name:        "node-0",
				MachineType: "c2i.2",
				NetworkID:   testNetworkID,
				Tags:        map[string]string{"cluster": "test"},
			},
		},
		{
			name: "missing machine type",
			spec: server.ServerSpec{
				Name:      "node-0",
				ImageID:   testImageID,
				NetworkID: testNetworkID,
				Tags:      map[string]string{"cluster": "test"},
			},
		},
		{
			name: "missing network ID",
			spec: server.ServerSpec{
				Name:        "node-0",
				ImageID:     testImageID,
				MachineType: "c2i.2",
				Tags:        map[string]string{"cluster": "test"},
			},
		},
		{
			name: "missing tags",
			spec: server.ServerSpec{
				Name:        "node-0",
				ImageID:     testImageID,
				MachineType: "c2i.2",
				NetworkID:   testNetworkID,
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
	t.Run("deletes server by ID when provided and calls ClearServer", func(t *testing.T) {
		var (
			deletedID string
			cleared   bool
		)
		spec := server.ServerSpec{
			ID: testServerID,
			ClearServer: func() {
				cleared = true
			},
		}

		svc := newTestService(t, spec, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodDelete && strings.HasSuffix(r.URL.Path, "/servers/"+testServerID) {
				deletedID = testServerID
				w.WriteHeader(http.StatusNoContent)
				return
			}
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.String())
		}))

		if err := svc.Delete(context.Background()); err != nil {
			t.Fatalf("Delete() error = %v", err)
		}
		if deletedID != testServerID {
			t.Fatalf("deletedID = %q, want %q", deletedID, testServerID)
		}
		if !cleared {
			t.Fatal("ClearServer was not called")
		}
	})

	t.Run("falls back to tag lookup when ID is empty", func(t *testing.T) {
		var deletedID string
		spec := server.ServerSpec{
			Tags: map[string]string{"cluster": "test"},
		}

		svc := newTestService(t, spec, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch {
			case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/servers"):
				writeJSON(t, w, map[string]any{"items": []any{
					map[string]any{"id": testServerID, "name": "node-0", "machineType": "c2i.2", "labels": map[string]any{"cluster": "test"}},
				}})
			case r.Method == http.MethodDelete && strings.HasSuffix(r.URL.Path, "/servers/"+testServerID):
				deletedID = testServerID
				w.WriteHeader(http.StatusNoContent)
			default:
				t.Fatalf("unexpected request: %s %s", r.Method, r.URL.String())
			}
		}))

		if err := svc.Delete(context.Background()); err != nil {
			t.Fatalf("Delete() error = %v", err)
		}
		if deletedID != testServerID {
			t.Fatalf("deletedID = %q, want %q", deletedID, testServerID)
		}
	})

	t.Run("falls back to tag lookup when recorded ID no longer exists", func(t *testing.T) {
		var deletedID string
		spec := server.ServerSpec{
			ID:   testOtherServerID,
			Tags: map[string]string{"cluster": "test"},
		}

		svc := newTestService(t, spec, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch {
			case r.Method == http.MethodDelete && strings.HasSuffix(r.URL.Path, "/servers/"+testOtherServerID):
				w.WriteHeader(http.StatusNotFound)
				writeJSON(t, w, map[string]any{"message": "not found"})
			case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/servers"):
				writeJSON(t, w, map[string]any{"items": []any{
					map[string]any{"id": testServerID, "name": "node-0", "machineType": "c2i.2", "labels": map[string]any{"cluster": "test"}},
				}})
			case r.Method == http.MethodDelete && strings.HasSuffix(r.URL.Path, "/servers/"+testServerID):
				deletedID = testServerID
				w.WriteHeader(http.StatusNoContent)
			default:
				t.Fatalf("unexpected request: %s %s", r.Method, r.URL.String())
			}
		}))

		if err := svc.Delete(context.Background()); err != nil {
			t.Fatalf("Delete() error = %v", err)
		}
		if deletedID != testServerID {
			t.Fatalf("deletedID = %q, want %q", deletedID, testServerID)
		}
	})

	t.Run("succeeds when server is not found", func(t *testing.T) {
		spec := server.ServerSpec{
			Tags: map[string]string{"cluster": "test"},
		}

		svc := newTestService(t, spec, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			writeJSON(t, w, map[string]any{"items": []any{}})
		}))

		if err := svc.Delete(context.Background()); err != nil {
			t.Fatalf("Delete() error = %v, want nil", err)
		}
	})

	t.Run("returns ErrInvalidInput when both ID and tags are empty", func(t *testing.T) {
		svc := newTestService(t, server.ServerSpec{}, http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.String())
		}))

		if err := svc.Delete(context.Background()); !errors.Is(err, shared.ErrInvalidInput) {
			t.Fatalf("Delete() error = %v, want ErrInvalidInput", err)
		}
	})
}
