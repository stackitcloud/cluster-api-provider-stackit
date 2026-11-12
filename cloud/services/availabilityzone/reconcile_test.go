/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package availabilityzone_test

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

	"github.com/stackitcloud/cluster-api-provider-stackit/cloud/services/availabilityzone"
	"github.com/stackitcloud/cluster-api-provider-stackit/cloud/services/shared"
)

const testRegion = "eu01"

type testScope struct {
	region     string
	iaasClient *iaas.APIClient
	spec       availabilityzone.AvailabilityZoneSpec
}

func (s *testScope) Region() string {
	return s.region
}

func (s *testScope) IaaSClient() *iaas.APIClient {
	return s.iaasClient
}

func (s *testScope) AvailabilityZoneSpec() availabilityzone.AvailabilityZoneSpec {
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

func newTestService(t *testing.T, spec availabilityzone.AvailabilityZoneSpec, handler http.Handler) *availabilityzone.Service {
	t.Helper()

	return availabilityzone.New(&testScope{
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
	t.Run("lists availability zones and passes them to SetZones", func(t *testing.T) {
		var recorded []string
		spec := availabilityzone.AvailabilityZoneSpec{
			SetZones: func(zones []string) {
				recorded = zones
			},
		}

		svc := newTestService(t, spec, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/availability-zones") {
				writeJSON(t, w, map[string]any{
					"items": []string{"eu01-1", "eu01-2", "eu01-3"},
				})
				return
			}
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.String())
		}))

		if err := svc.Reconcile(context.Background()); err != nil {
			t.Fatalf("Reconcile() error = %v", err)
		}
		if !slices.Equal(recorded, []string{"eu01-1", "eu01-2", "eu01-3"}) {
			t.Fatalf("recorded = %v, want [eu01-1 eu01-2 eu01-3]", recorded)
		}
	})

	t.Run("returns classified error when API fails", func(t *testing.T) {
		svc := newTestService(t, availabilityzone.AvailabilityZoneSpec{}, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
			writeJSON(t, w, map[string]any{"message": "backend error"})
		}))

		err := svc.Reconcile(context.Background())
		if !errors.Is(err, shared.ErrTransient) {
			t.Fatalf("Reconcile() error = %v, want ErrTransient", err)
		}
	})

	t.Run("Delete is a no-op", func(t *testing.T) {
		svc := newTestService(t, availabilityzone.AvailabilityZoneSpec{}, http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.String())
		}))

		if err := svc.Delete(context.Background()); err != nil {
			t.Fatalf("Delete() error = %v, want nil", err)
		}
	})
}
