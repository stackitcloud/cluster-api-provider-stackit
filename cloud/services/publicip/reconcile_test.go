/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package publicip_test

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

	"github.com/stackitcloud/cluster-api-provider-stackit/cloud/services/publicip"
	"github.com/stackitcloud/cluster-api-provider-stackit/cloud/services/shared"
)

const (
	testProjectID        = "00000000-0000-0000-0000-000000000000"
	testRegion           = "eu01"
	testPublicIPID       = "11111111-1111-4111-8111-111111111111"
	testOtherPublicIPID  = "22222222-2222-4222-8222-222222222222"
	testMissingPublicIP  = "99999999-9999-4999-8999-999999999999"
	testServerID         = "33333333-3333-4333-8333-333333333333"
	testNetworkInterface = "[VISA_REDACTED]-8444-444444444444"
	testPublicIPAddress  = "203.0.113.22"
)

type testScope struct {
	projectID  string
	region     string
	iaasClient *iaas.APIClient
	spec       publicip.PublicIPSpec
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

func (s *testScope) PublicIPSpec() publicip.PublicIPSpec {
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

func newTestService(t *testing.T, spec publicip.PublicIPSpec, handler http.Handler) *publicip.Service {
	t.Helper()

	return publicip.New(&testScope{
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

func TestServiceReconcile(t *testing.T) {
	t.Run("creates public IP and attaches it to server when not found", func(t *testing.T) {
		var (
			createPayload map[string]any
			attachedPair  string
			recordedID    string
			recordedIP    string
		)
		spec := publicip.PublicIPSpec{
			Tags:     map[string]string{"cluster": "test"},
			ServerID: testServerID,
			SetPublicIP: func(id, ip string) {
				recordedID = id
				recordedIP = ip
			},
		}

		svc := newTestService(t, spec, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			path := r.URL.Path
			switch {
			case r.Method == http.MethodGet && strings.HasSuffix(path, "/public-ips"):
				writeJSON(t, w, map[string]any{"items": []any{}})
			case r.Method == http.MethodPost && strings.HasSuffix(path, "/public-ips"):
				createPayload = readJSON(t, r)
				writeJSON(t, w, map[string]any{
					"id": testPublicIPID,
					"ip": testPublicIPAddress,
				})
			case r.Method == http.MethodPut && strings.HasSuffix(path, "/servers/"+testServerID+"/public-ips/"+testPublicIPID):
				attachedPair = testServerID + ":" + testPublicIPID
				w.WriteHeader(http.StatusNoContent)
			case r.Method == http.MethodGet && strings.HasSuffix(path, "/public-ips/"+testPublicIPID):
				writeJSON(t, w, map[string]any{
					"id":               testPublicIPID,
					"ip":               testPublicIPAddress,
					"networkInterface": testNetworkInterface,
				})
			default:
				t.Fatalf("unexpected request: %s %s", r.Method, r.URL.String())
			}
		}))

		if err := svc.Reconcile(context.Background()); err != nil {
			t.Fatalf("Reconcile() error = %v", err)
		}
		if attachedPair != testServerID+":"+testPublicIPID {
			t.Fatalf("attachedPair = %q, want %s:%s", attachedPair, testServerID, testPublicIPID)
		}
		if recordedID != testPublicIPID || recordedIP != testPublicIPAddress {
			t.Fatalf("recorded = (%q, %q), want (%q, %q)", recordedID, recordedIP, testPublicIPID, testPublicIPAddress)
		}
		labels, _ := createPayload["labels"].(map[string]any)
		if labels["cluster"] != "test" {
			t.Fatalf("createPayload labels = %#v, want cluster=test", labels)
		}
	})

	t.Run("reuses existing public IP found by ID without listing by tags", func(t *testing.T) {
		var recordedID, recordedIP string
		spec := publicip.PublicIPSpec{
			ID:       testPublicIPID,
			Tags:     map[string]string{"cluster": "test"},
			ServerID: testServerID,
			SetPublicIP: func(id, ip string) {
				recordedID = id
				recordedIP = ip
			},
		}

		svc := newTestService(t, spec, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/public-ips/"+testPublicIPID) {
				writeJSON(t, w, map[string]any{
					"id":               testPublicIPID,
					"ip":               testPublicIPAddress,
					"networkInterface": testNetworkInterface,
				})
				return
			}
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.String())
		}))

		if err := svc.Reconcile(context.Background()); err != nil {
			t.Fatalf("Reconcile() error = %v", err)
		}
		if recordedID != testPublicIPID || recordedIP != testPublicIPAddress {
			t.Fatalf("recorded = (%q, %q), want (%q, %q)", recordedID, recordedIP, testPublicIPID, testPublicIPAddress)
		}
	})
}

func TestServiceReconcileReuseAndErrors(t *testing.T) {
	t.Run("reuses existing public IP found by tags without creating duplicate", func(t *testing.T) {
		var recordedID string
		spec := publicip.PublicIPSpec{
			Tags: map[string]string{"cluster": "test"},
			SetPublicIP: func(id, _ string) {
				recordedID = id
			},
		}

		svc := newTestService(t, spec, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/public-ips") {
				writeJSON(t, w, map[string]any{"items": []any{
					map[string]any{
						"id":     testPublicIPID,
						"ip":     testPublicIPAddress,
						"labels": map[string]any{"cluster": "test"},
					},
				}})
				return
			}
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.String())
		}))

		if err := svc.Reconcile(context.Background()); err != nil {
			t.Fatalf("Reconcile() error = %v", err)
		}
		if recordedID != testPublicIPID {
			t.Fatalf("recordedID = %q, want %q", recordedID, testPublicIPID)
		}
	})

	t.Run("tolerates conflict when attaching public IP to server", func(t *testing.T) {
		var recordedID string
		spec := publicip.PublicIPSpec{
			ID:       testPublicIPID,
			Tags:     map[string]string{"cluster": "test"},
			ServerID: testServerID,
			SetPublicIP: func(id, _ string) {
				recordedID = id
			},
		}

		svc := newTestService(t, spec, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			path := r.URL.Path
			switch {
			case r.Method == http.MethodGet && strings.HasSuffix(path, "/public-ips/"+testPublicIPID):
				writeJSON(t, w, map[string]any{
					"id": testPublicIPID,
					"ip": testPublicIPAddress,
				})
			case r.Method == http.MethodPut && strings.HasSuffix(path, "/servers/"+testServerID+"/public-ips/"+testPublicIPID):
				w.WriteHeader(http.StatusConflict)
				writeJSON(t, w, map[string]any{"message": "conflict"})
			default:
				t.Fatalf("unexpected request: %s %s", r.Method, r.URL.String())
			}
		}))

		if err := svc.Reconcile(context.Background()); err != nil {
			t.Fatalf("Reconcile() error = %v, want nil on conflict", err)
		}
		if recordedID != testPublicIPID {
			t.Fatalf("recordedID = %q, want %q", recordedID, testPublicIPID)
		}
	})

	t.Run("returns ErrConflict when multiple public IPs match tags", func(t *testing.T) {
		spec := publicip.PublicIPSpec{
			Tags: map[string]string{"cluster": "test"},
		}

		svc := newTestService(t, spec, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			writeJSON(t, w, map[string]any{"items": []any{
				map[string]any{"id": testPublicIPID, "ip": testPublicIPAddress, "labels": map[string]any{"cluster": "test"}},
				map[string]any{"id": testOtherPublicIPID, "ip": "203.0.113.23", "labels": map[string]any{"cluster": "test"}},
			}})
		}))

		err := svc.Reconcile(context.Background())
		if !errors.Is(err, shared.ErrConflict) {
			t.Fatalf("Reconcile() error = %v, want ErrConflict", err)
		}
	})

	t.Run("returns ErrInvalidInput when tags are empty", func(t *testing.T) {
		svc := newTestService(t, publicip.PublicIPSpec{}, http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.String())
		}))

		if err := svc.Reconcile(context.Background()); !errors.Is(err, shared.ErrInvalidInput) {
			t.Fatalf("Reconcile() error = %v, want ErrInvalidInput", err)
		}
	})
}

func TestServiceDelete(t *testing.T) {
	t.Run("detaches and deletes public IP using recorded ID and clears state", func(t *testing.T) {
		var (
			detachedPair string
			deletedID    string
			clearedID    = "not-cleared"
			clearedIP    = "not-cleared"
		)
		spec := publicip.PublicIPSpec{
			ID:       testPublicIPID,
			ServerID: testServerID,
			SetPublicIP: func(id, ip string) {
				clearedID = id
				clearedIP = ip
			},
		}

		svc := newTestService(t, spec, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			path := r.URL.Path
			switch {
			case r.Method == http.MethodDelete && strings.HasSuffix(path, "/servers/"+testServerID+"/public-ips/"+testPublicIPID):
				detachedPair = testServerID + ":" + testPublicIPID
				w.WriteHeader(http.StatusNoContent)
			case r.Method == http.MethodDelete && strings.HasSuffix(path, "/public-ips/"+testPublicIPID):
				deletedID = testPublicIPID
				w.WriteHeader(http.StatusNoContent)
			default:
				t.Fatalf("unexpected request: %s %s", r.Method, r.URL.String())
			}
		}))

		if err := svc.Delete(context.Background()); err != nil {
			t.Fatalf("Delete() error = %v", err)
		}
		if detachedPair != testServerID+":"+testPublicIPID {
			t.Fatalf("detachedPair = %q, want %s:%s", detachedPair, testServerID, testPublicIPID)
		}
		if deletedID != testPublicIPID {
			t.Fatalf("deletedID = %q, want %q", deletedID, testPublicIPID)
		}
		if clearedID != "" || clearedIP != "" {
			t.Fatalf("cleared = (%q, %q), want both empty", clearedID, clearedIP)
		}
	})

	t.Run("succeeds when no public IP exists", func(t *testing.T) {
		spec := publicip.PublicIPSpec{
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
		svc := newTestService(t, publicip.PublicIPSpec{}, http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.String())
		}))

		if err := svc.Delete(context.Background()); !errors.Is(err, shared.ErrInvalidInput) {
			t.Fatalf("Delete() error = %v, want ErrInvalidInput", err)
		}
	})
}

func TestServiceDeleteTagFallback(t *testing.T) {
	var (
		detachedPairs []string
		deletedIDs    []string
	)
	spec := publicip.PublicIPSpec{
		ID:             testMissingPublicIP,
		Tags:           map[string]string{"cluster": "test"},
		ServerSelector: map[string]string{"cluster": "test"},
	}

	svc := newTestService(t, spec, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		switch {
		case r.Method == http.MethodDelete && strings.HasSuffix(path, "/servers/"+testServerID+"/public-ips/"+testMissingPublicIP):
			w.WriteHeader(http.StatusNotFound)
			writeJSON(t, w, map[string]any{"message": "not found"})
		case r.Method == http.MethodDelete && strings.HasSuffix(path, "/public-ips/"+testMissingPublicIP):
			w.WriteHeader(http.StatusNotFound)
			writeJSON(t, w, map[string]any{"message": "not found"})
		case r.Method == http.MethodGet && strings.HasSuffix(path, "/public-ips"):
			writeJSON(t, w, map[string]any{"items": []any{
				map[string]any{
					"id":               testPublicIPID,
					"ip":               testPublicIPAddress,
					"networkInterface": testNetworkInterface,
					"labels":           map[string]any{"cluster": "test"},
				},
				map[string]any{
					"id":               testOtherPublicIPID,
					"ip":               "203.0.113.23",
					"networkInterface": testNetworkInterface,
					"labels":           map[string]any{"cluster": "test"},
				},
				map[string]any{
					"id":     "ignored-pip",
					"ip":     "203.0.113.24",
					"labels": map[string]any{"cluster": "other"},
				},
			}})
		case r.Method == http.MethodGet && strings.HasSuffix(path, "/servers"):
			writeJSON(t, w, map[string]any{"items": []any{
				map[string]any{
					"id":          testServerID,
					"name":        "bastion",
					"machineType": "c2i.1",
					"labels":      map[string]any{"cluster": "test"},
				},
			}})
		case r.Method == http.MethodDelete && strings.HasSuffix(path, "/servers/"+testServerID+"/public-ips/"+testPublicIPID):
			detachedPairs = append(detachedPairs, testServerID+":"+testPublicIPID)
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodDelete && strings.HasSuffix(path, "/servers/"+testServerID+"/public-ips/"+testOtherPublicIPID):
			detachedPairs = append(detachedPairs, testServerID+":"+testOtherPublicIPID)
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodDelete && strings.HasSuffix(path, "/public-ips/"+testPublicIPID):
			deletedIDs = append(deletedIDs, testPublicIPID)
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodDelete && strings.HasSuffix(path, "/public-ips/"+testOtherPublicIPID):
			deletedIDs = append(deletedIDs, testOtherPublicIPID)
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.String())
		}
	}))

	if err := svc.Delete(context.Background()); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}
	if len(detachedPairs) != 2 || detachedPairs[0] != testServerID+":"+testPublicIPID || detachedPairs[1] != testServerID+":"+testOtherPublicIPID {
		t.Fatalf("detachedPairs = %v, want [%s:%s %s:%s]", detachedPairs, testServerID, testPublicIPID, testServerID, testOtherPublicIPID)
	}
	if len(deletedIDs) != 2 || deletedIDs[0] != testPublicIPID || deletedIDs[1] != testOtherPublicIPID {
		t.Fatalf("deletedIDs = %v, want [%s %s]", deletedIDs, testPublicIPID, testOtherPublicIPID)
	}
}
