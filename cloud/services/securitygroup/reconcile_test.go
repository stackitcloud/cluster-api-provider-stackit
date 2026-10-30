/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package securitygroup_test

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

	"github.com/stackitcloud/cluster-api-provider-stackit/cloud/services/securitygroup"
	"github.com/stackitcloud/cluster-api-provider-stackit/cloud/services/shared"
)

const (
	testProjectID       = "00000000-0000-0000-0000-000000000000"
	testRegion          = "eu01"
	testSecurityGroupID = "11111111-1111-4111-8111-111111111111"
	testRemoteSGID      = "22222222-2222-4222-8222-222222222222"
	testServerID        = "33333333-3333-4333-8333-333333333333"
	testRuleID1         = "66666666-6666-4666-8666-666666666666"
	testRuleID2         = "77777777-7777-4777-8777-777777777777"
)

type testScope struct {
	projectID  string
	region     string
	iaasClient *iaas.APIClient
	specs      []securitygroup.SecurityGroupSpec
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

func (s *testScope) SecurityGroupSpecs() []securitygroup.SecurityGroupSpec {
	return s.specs
}

func newTestIaaSClient(t *testing.T, handler http.Handler) *iaas.APIClient {
	t.Helper()

	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	iaasClient, err := iaas.NewAPIClient(
		config.WithoutAuthentication(),
		config.WithEndpoint(server.URL),
	)
	if err != nil {
		t.Fatalf("iaas.NewAPIClient() error = %v", err)
	}
	return iaasClient
}

func newTestService(t *testing.T, specs []securitygroup.SecurityGroupSpec, handler http.Handler) *securitygroup.Service {
	t.Helper()

	return securitygroup.New(&testScope{
		projectID:  testProjectID,
		region:     testRegion,
		iaasClient: newTestIaaSClient(t, handler),
		specs:      specs,
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
	t.Run("creates security group and generic rules when not found", func(t *testing.T) {
		var (
			createdSGPayload    map[string]any
			createdRulePayloads []map[string]any
		)
		specs := []securitygroup.SecurityGroupSpec{{
			Name:        "cluster-sg",
			Description: "Generic security group",
			Tags:        map[string]string{"cluster": "test"},
			Rules: []securitygroup.Rule{
				{
					Direction: securitygroup.DirectionIngress,
					Protocol:  securitygroup.ProtocolTCP,
					PortMin:   6443,
					PortMax:   6443,
					IPRange:   "203.0.113.0/24",
				},
				{
					Direction:             securitygroup.DirectionEgress,
					Protocol:              securitygroup.ProtocolUDP,
					PortMin:               8472,
					PortMax:               8472,
					RemoteSecurityGroupID: testRemoteSGID,
				},
			},
		}}

		svc := newTestService(t, specs, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			path := r.URL.Path
			switch {
			case r.Method == http.MethodGet && strings.HasSuffix(path, "/security-groups"):
				writeJSON(t, w, map[string]any{"items": []any{}})
			case r.Method == http.MethodPost && strings.HasSuffix(path, "/security-groups"):
				createdSGPayload = readJSON(t, r)
				writeJSON(t, w, map[string]any{
					"id":   testSecurityGroupID,
					"name": "cluster-sg",
				})
			case r.Method == http.MethodGet && strings.HasSuffix(path, "/security-groups/"+testSecurityGroupID+"/rules"):
				writeJSON(t, w, map[string]any{"items": []any{}})
			case r.Method == http.MethodPost && strings.HasSuffix(path, "/security-groups/"+testSecurityGroupID+"/rules"):
				createdRulePayloads = append(createdRulePayloads, readJSON(t, r))
				writeJSON(t, w, map[string]any{"id": testRuleID1, "direction": "ingress"})
			default:
				t.Fatalf("unexpected request: %s %s", r.Method, r.URL.String())
			}
		}))

		if err := svc.Reconcile(context.Background()); err != nil {
			t.Fatalf("Reconcile() error = %v", err)
		}

		if createdSGPayload["name"] != "cluster-sg" {
			t.Fatalf("created SG name = %v, want cluster-sg", createdSGPayload["name"])
		}
		if createdSGPayload["description"] != "Generic security group" {
			t.Fatalf("created SG description = %v, want Generic security group", createdSGPayload["description"])
		}
		labels, _ := createdSGPayload["labels"].(map[string]any)
		if labels["cluster"] != "test" {
			t.Fatalf("created SG labels = %v, want cluster=test", labels)
		}
		if len(createdRulePayloads) != 2 {
			t.Fatalf("created %d rules, want 2", len(createdRulePayloads))
		}
		if createdRulePayloads[0]["direction"] != "ingress" || createdRulePayloads[0]["ipRange"] != "203.0.113.0/24" {
			t.Fatalf("first rule payload = %#v", createdRulePayloads[0])
		}
		if createdRulePayloads[1]["direction"] != "egress" || createdRulePayloads[1]["remoteSecurityGroupId"] != testRemoteSGID {
			t.Fatalf("second rule payload = %#v", createdRulePayloads[1])
		}
	})

	t.Run("reuses existing security group found by tags without creating duplicate", func(t *testing.T) {
		specs := []securitygroup.SecurityGroupSpec{{
			Name: "cluster-sg",
			Tags: map[string]string{"cluster": "test"},
		}}

		svc := newTestService(t, specs, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			path := r.URL.Path
			switch {
			case r.Method == http.MethodGet && strings.HasSuffix(path, "/security-groups"):
				writeJSON(t, w, map[string]any{"items": []any{
					map[string]any{
						"id":     testSecurityGroupID,
						"name":   "cluster-sg",
						"labels": map[string]any{"cluster": "test"},
					},
				}})
			case r.Method == http.MethodGet && strings.HasSuffix(path, "/security-groups/"+testSecurityGroupID+"/rules"):
				writeJSON(t, w, map[string]any{"items": []any{}})
			default:
				t.Fatalf("unexpected request: %s %s", r.Method, r.URL.String())
			}
		}))

		if err := svc.Reconcile(context.Background()); err != nil {
			t.Fatalf("Reconcile() error = %v", err)
		}
	})

	t.Run("returns ErrConflict when multiple security groups match tags", func(t *testing.T) {
		specs := []securitygroup.SecurityGroupSpec{{
			Name: "cluster-sg",
			Tags: map[string]string{"cluster": "test"},
		}}

		svc := newTestService(t, specs, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			writeJSON(t, w, map[string]any{"items": []any{
				map[string]any{"id": testSecurityGroupID, "name": "sg-1", "labels": map[string]any{"cluster": "test"}},
				map[string]any{"id": testRemoteSGID, "name": "sg-2", "labels": map[string]any{"cluster": "test"}},
			}})
		}))

		err := svc.Reconcile(context.Background())
		if !errors.Is(err, shared.ErrConflict) {
			t.Fatalf("Reconcile() error = %v, want ErrConflict", err)
		}
	})
}

func TestServiceReconcileBastionAndNodeSSH(t *testing.T) {
	var (
		bastionSGID       string
		bastionCreated    bool
		nodeRulesCreated  []map[string]any
		attachedServerIDs []string
	)
	specs := []securitygroup.SecurityGroupSpec{
		{
			Name: "cluster-bastion-ssh",
			Tags: map[string]string{"role": "bastion"},
			SetID: func(id string) {
				bastionSGID = id
			},
			Rules: []securitygroup.Rule{
				{
					Direction: securitygroup.DirectionIngress,
					Protocol:  securitygroup.ProtocolTCP,
					PortMin:   22,
					PortMax:   22,
					IPRange:   "203.0.113.0/24",
				},
				{
					Direction: securitygroup.DirectionEgress,
				},
			},
		},
		{
			Name:      "cluster-node-ssh",
			Tags:      map[string]string{"role": "node-ssh"},
			ServerIDs: []string{testServerID},
			Rules: []securitygroup.Rule{
				{
					Direction:               securitygroup.DirectionIngress,
					Protocol:                securitygroup.ProtocolTCP,
					PortMin:                 22,
					PortMax:                 22,
					RemoteSecurityGroupTags: map[string]string{"role": "bastion"},
				},
				{
					Direction: securitygroup.DirectionEgress,
				},
			},
		},
	}

	svc := newTestService(t, specs, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(path, "/security-groups"):
			selector := r.URL.Query().Get("label_selector")
			if bastionCreated && strings.Contains(selector, "role=bastion") {
				writeJSON(t, w, map[string]any{"items": []any{
					map[string]any{
						"id":     testRemoteSGID,
						"name":   "cluster-bastion-ssh",
						"labels": map[string]any{"role": "bastion"},
					},
				}})
				return
			}
			writeJSON(t, w, map[string]any{"items": []any{}})
		case r.Method == http.MethodPost && strings.HasSuffix(path, "/security-groups"):
			payload := readJSON(t, r)
			if payload["name"] == "cluster-bastion-ssh" {
				bastionCreated = true
				writeJSON(t, w, map[string]any{"id": testRemoteSGID, "name": "cluster-bastion-ssh"})
				return
			}
			writeJSON(t, w, map[string]any{"id": testSecurityGroupID, "name": "cluster-node-ssh"})
		case r.Method == http.MethodGet && strings.HasSuffix(path, "/security-groups/"+testRemoteSGID+"/rules"):
			// Simulate STACKIT default egress rule already present.
			writeJSON(t, w, map[string]any{"items": []any{
				map[string]any{"id": testRuleID1, "direction": "egress", "ethertype": "IPv4"},
			}})
		case r.Method == http.MethodPost && strings.HasSuffix(path, "/security-groups/"+testRemoteSGID+"/rules"):
			writeJSON(t, w, map[string]any{"id": testRuleID2, "direction": "ingress"})
		case r.Method == http.MethodGet && strings.HasSuffix(path, "/security-groups/"+testSecurityGroupID+"/rules"):
			writeJSON(t, w, map[string]any{"items": []any{}})
		case r.Method == http.MethodPost && strings.HasSuffix(path, "/security-groups/"+testSecurityGroupID+"/rules"):
			nodeRulesCreated = append(nodeRulesCreated, readJSON(t, r))
			writeJSON(t, w, map[string]any{"id": testRuleID1, "direction": "ingress"})
		case r.Method == http.MethodPut && strings.HasSuffix(path, "/servers/"+testServerID+"/security-groups/"+testSecurityGroupID):
			attachedServerIDs = append(attachedServerIDs, testServerID)
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.String())
		}
	}))

	if err := svc.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	if bastionSGID != testRemoteSGID {
		t.Fatalf("bastionSGID = %q, want %q", bastionSGID, testRemoteSGID)
	}
	if len(nodeRulesCreated) != 2 {
		t.Fatalf("nodeRulesCreated = %#v, want 2 rules (ingress from bastion + egress)", nodeRulesCreated)
	}
	if nodeRulesCreated[0]["remoteSecurityGroupId"] != testRemoteSGID {
		t.Fatalf("node ingress rule remoteSecurityGroupId = %v, want %q", nodeRulesCreated[0]["remoteSecurityGroupId"], testRemoteSGID)
	}
	if nodeRulesCreated[1]["direction"] != "egress" {
		t.Fatalf("node egress rule direction = %v, want egress", nodeRulesCreated[1]["direction"])
	}
	if !slices.Equal(attachedServerIDs, []string{testServerID}) {
		t.Fatalf("attachedServerIDs = %v, want [%s]", attachedServerIDs, testServerID)
	}
}

func TestServiceReconcileRules(t *testing.T) {
	var (
		createdRules   []map[string]any
		deletedRuleIDs []string
	)
	specs := []securitygroup.SecurityGroupSpec{{
		Name: "cluster-sg",
		Tags: map[string]string{"cluster": "test"},
		Rules: []securitygroup.Rule{
			{
				Direction: securitygroup.DirectionIngress,
				Protocol:  securitygroup.ProtocolTCP,
				PortMin:   443,
				PortMax:   443,
				IPRange:   "203.0.113.0/24",
			},
			{
				Direction: securitygroup.DirectionIngress,
				Protocol:  securitygroup.ProtocolTCP,
				PortMin:   443,
				PortMax:   443,
				IPRange:   "203.0.113.0/24",
			},
			{
				Direction:             securitygroup.DirectionIngress,
				Protocol:              securitygroup.ProtocolTCP,
				PortMin:               10250,
				PortMax:               10250,
				RemoteSecurityGroupID: testRemoteSGID,
			},
		},
	}}

	svc := newTestService(t, specs, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(path, "/security-groups"):
			writeJSON(t, w, map[string]any{"items": []any{
				map[string]any{
					"id":     testSecurityGroupID,
					"name":   "cluster-sg",
					"labels": map[string]any{"cluster": "test"},
				},
			}})
		case r.Method == http.MethodGet && strings.HasSuffix(path, "/security-groups/"+testSecurityGroupID+"/rules"):
			writeJSON(t, w, map[string]any{"items": []any{
				map[string]any{
					"id":                    testRuleID1,
					"direction":             "ingress",
					"remoteSecurityGroupId": testRemoteSGID,
					"portRange":             map[string]any{"min": 10250, "max": 10250},
					"protocol":              map[string]any{"name": "tcp"},
				},
				map[string]any{
					"id":        testRuleID2,
					"direction": "ingress",
					"ipRange":   "198.51.100.0/24",
					"portRange": map[string]any{"min": 22, "max": 22},
					"protocol":  map[string]any{"name": "tcp"},
				},
			}})
		case r.Method == http.MethodPost && strings.HasSuffix(path, "/security-groups/"+testSecurityGroupID+"/rules"):
			createdRules = append(createdRules, readJSON(t, r))
			writeJSON(t, w, map[string]any{"id": "88888888-8888-4888-8888-888888888888", "direction": "ingress"})
		case r.Method == http.MethodDelete && strings.Contains(path, "/rules/"):
			parts := strings.Split(strings.TrimSuffix(path, "/"), "/")
			deletedRuleIDs = append(deletedRuleIDs, parts[len(parts)-1])
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.String())
		}
	}))

	if err := svc.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}

	if len(createdRules) != 1 || createdRules[0]["ipRange"] != "203.0.113.0/24" {
		t.Fatalf("createdRules = %#v, want single rule for 203.0.113.0/24", createdRules)
	}
	if !slices.Equal(deletedRuleIDs, []string{testRuleID2}) {
		t.Fatalf("deletedRuleIDs = %v, want [%s]", deletedRuleIDs, testRuleID2)
	}
}

func TestServiceReconcileValidation(t *testing.T) {
	tests := []struct {
		name string
		spec securitygroup.SecurityGroupSpec
	}{
		{
			name: "missing name",
			spec: securitygroup.SecurityGroupSpec{
				Tags: map[string]string{"cluster": "test"},
			},
		},
		{
			name: "missing tags",
			spec: securitygroup.SecurityGroupSpec{
				Name: "cluster-sg",
			},
		},
		{
			name: "invalid rule direction",
			spec: securitygroup.SecurityGroupSpec{
				Name:  "cluster-sg",
				Tags:  map[string]string{"cluster": "test"},
				Rules: []securitygroup.Rule{{Direction: "invalid", Protocol: securitygroup.ProtocolTCP, PortMin: 80, PortMax: 80, IPRange: "0.0.0.0/0"}},
			},
		},
		{
			name: "both ipRange and remoteSecurityGroupID set",
			spec: securitygroup.SecurityGroupSpec{
				Name: "cluster-sg",
				Tags: map[string]string{"cluster": "test"},
				Rules: []securitygroup.Rule{{
					Direction:             securitygroup.DirectionIngress,
					Protocol:              securitygroup.ProtocolTCP,
					PortMin:               80,
					PortMax:               80,
					IPRange:               "0.0.0.0/0",
					RemoteSecurityGroupID: testRemoteSGID,
				}},
			},
		},
		{
			name: "both ipRange and remoteSecurityGroupTags set",
			spec: securitygroup.SecurityGroupSpec{
				Name: "cluster-sg",
				Tags: map[string]string{"cluster": "test"},
				Rules: []securitygroup.Rule{{
					Direction:               securitygroup.DirectionIngress,
					Protocol:                securitygroup.ProtocolTCP,
					PortMin:                 80,
					PortMax:                 80,
					IPRange:                 "0.0.0.0/0",
					RemoteSecurityGroupTags: map[string]string{"role": "bastion"},
				}},
			},
		},
		{
			name: "invalid port range",
			spec: securitygroup.SecurityGroupSpec{
				Name:  "cluster-sg",
				Tags:  map[string]string{"cluster": "test"},
				Rules: []securitygroup.Rule{{Direction: securitygroup.DirectionIngress, Protocol: securitygroup.ProtocolTCP, PortMin: 8080, PortMax: 80, IPRange: "0.0.0.0/0"}},
			},
		},
		{
			name: "port range without protocol",
			spec: securitygroup.SecurityGroupSpec{
				Name:  "cluster-sg",
				Tags:  map[string]string{"cluster": "test"},
				Rules: []securitygroup.Rule{{Direction: securitygroup.DirectionIngress, PortMin: 80, PortMax: 80, IPRange: "0.0.0.0/0"}},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc := newTestService(t, []securitygroup.SecurityGroupSpec{tt.spec}, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				t.Fatalf("unexpected request before validation failure: %s %s", r.Method, r.URL.String())
			}))

			err := svc.Reconcile(context.Background())
			if !errors.Is(err, shared.ErrInvalidInput) {
				t.Fatalf("Reconcile() error = %v, want ErrInvalidInput", err)
			}
		})
	}
}

func TestServiceDelete(t *testing.T) {
	t.Run("deletes rules and security group using ID when provided", func(t *testing.T) {
		var (
			deletedRules []string
			deletedSGID  string
		)
		specs := []securitygroup.SecurityGroupSpec{{
			ID: testSecurityGroupID,
		}}

		svc := newTestService(t, specs, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			path := r.URL.Path
			switch {
			case r.Method == http.MethodGet && strings.HasSuffix(path, "/security-groups/"+testSecurityGroupID+"/rules"):
				writeJSON(t, w, map[string]any{"items": []any{
					map[string]any{"id": testRuleID1, "direction": "ingress"},
					map[string]any{"id": testRuleID2, "direction": "egress"},
				}})
			case r.Method == http.MethodDelete && strings.Contains(path, "/rules/"):
				parts := strings.Split(strings.TrimSuffix(path, "/"), "/")
				deletedRules = append(deletedRules, parts[len(parts)-1])
				w.WriteHeader(http.StatusNoContent)
			case r.Method == http.MethodDelete && strings.HasSuffix(path, "/security-groups/"+testSecurityGroupID):
				deletedSGID = testSecurityGroupID
				w.WriteHeader(http.StatusNoContent)
			default:
				t.Fatalf("unexpected request: %s %s", r.Method, r.URL.String())
			}
		}))

		if err := svc.Delete(context.Background()); err != nil {
			t.Fatalf("Delete() error = %v", err)
		}
		if !slices.Equal(deletedRules, []string{testRuleID1, testRuleID2}) {
			t.Fatalf("deletedRules = %v, want [%s %s]", deletedRules, testRuleID1, testRuleID2)
		}
		if deletedSGID != testSecurityGroupID {
			t.Fatalf("deletedSGID = %q, want %q", deletedSGID, testSecurityGroupID)
		}
	})

	t.Run("deletes rules and security group found by tags", func(t *testing.T) {
		var (
			deletedRules []string
			deletedSGID  string
		)
		specs := []securitygroup.SecurityGroupSpec{{
			Tags: map[string]string{"cluster": "test"},
		}}

		svc := newTestService(t, specs, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			path := r.URL.Path
			switch {
			case r.Method == http.MethodGet && strings.HasSuffix(path, "/security-groups"):
				writeJSON(t, w, map[string]any{"items": []any{
					map[string]any{"id": testSecurityGroupID, "name": "cluster-sg", "labels": map[string]any{"cluster": "test"}},
				}})
			case r.Method == http.MethodGet && strings.HasSuffix(path, "/security-groups/"+testSecurityGroupID+"/rules"):
				writeJSON(t, w, map[string]any{"items": []any{
					map[string]any{"id": testRuleID1, "direction": "ingress"},
					map[string]any{"id": testRuleID2, "direction": "egress"},
				}})
			case r.Method == http.MethodDelete && strings.Contains(path, "/rules/"):
				parts := strings.Split(strings.TrimSuffix(path, "/"), "/")
				deletedRules = append(deletedRules, parts[len(parts)-1])
				w.WriteHeader(http.StatusNoContent)
			case r.Method == http.MethodDelete && strings.HasSuffix(path, "/security-groups/"+testSecurityGroupID):
				deletedSGID = testSecurityGroupID
				w.WriteHeader(http.StatusNoContent)
			default:
				t.Fatalf("unexpected request: %s %s", r.Method, r.URL.String())
			}
		}))

		if err := svc.Delete(context.Background()); err != nil {
			t.Fatalf("Delete() error = %v", err)
		}
		if !slices.Equal(deletedRules, []string{testRuleID1, testRuleID2}) {
			t.Fatalf("deletedRules = %v, want [%s %s]", deletedRules, testRuleID1, testRuleID2)
		}
		if deletedSGID != testSecurityGroupID {
			t.Fatalf("deletedSGID = %q, want %q", deletedSGID, testSecurityGroupID)
		}
	})

	t.Run("succeeds when security group does not exist", func(t *testing.T) {
		specs := []securitygroup.SecurityGroupSpec{{
			Tags: map[string]string{"cluster": "test"},
		}}

		svc := newTestService(t, specs, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			writeJSON(t, w, map[string]any{"items": []any{}})
		}))

		if err := svc.Delete(context.Background()); err != nil {
			t.Fatalf("Delete() error = %v, want nil", err)
		}
	})

	t.Run("returns ErrInvalidInput when tags are empty", func(t *testing.T) {
		svc := newTestService(t, []securitygroup.SecurityGroupSpec{{}}, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.String())
		}))

		err := svc.Delete(context.Background())
		if !errors.Is(err, shared.ErrInvalidInput) {
			t.Fatalf("Delete() error = %v, want ErrInvalidInput", err)
		}
	})
}

func TestServiceDeleteStaleIDFallbackAndDetach(t *testing.T) {
	var (
		detachedPairs []string
		deletedSGIDs  []string
	)
	specs := []securitygroup.SecurityGroupSpec{
		{
			ID:   "99999999-9999-4999-8999-999999999999",
			Tags: map[string]string{"role": "bastion"},
		},
		{
			Tags:           map[string]string{"role": "node-ssh"},
			ServerSelector: map[string]string{"cluster": "test"},
		},
	}

	svc := newTestService(t, specs, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(path, "/security-groups/99999999-9999-4999-8999-999999999999/rules"):
			w.WriteHeader(http.StatusNotFound)
			writeJSON(t, w, map[string]any{"message": "not found"})
		case r.Method == http.MethodGet && strings.HasSuffix(path, "/security-groups"):
			selector := r.URL.Query().Get("label_selector")
			if strings.Contains(selector, "role=node-ssh") {
				writeJSON(t, w, map[string]any{"items": []any{
					map[string]any{"id": testSecurityGroupID, "name": "node-ssh", "labels": map[string]any{"role": "node-ssh"}},
				}})
				return
			}
			writeJSON(t, w, map[string]any{"items": []any{
				map[string]any{"id": testRemoteSGID, "name": "bastion-ssh", "labels": map[string]any{"role": "bastion"}},
			}})
		case r.Method == http.MethodGet && strings.HasSuffix(path, "/servers"):
			writeJSON(t, w, map[string]any{"items": []any{
				map[string]any{
					"id":          testServerID,
					"name":        "worker-1",
					"machineType": "c2i.1",
					"labels":      map[string]any{"cluster": "test"},
				},
			}})
		case r.Method == http.MethodDelete && strings.Contains(path, "/servers/") && strings.Contains(path, "/security-groups/"):
			parts := strings.Split(path, "/servers/")
			sub := strings.Split(parts[1], "/security-groups/")
			detachedPairs = append(detachedPairs, sub[0]+":"+sub[1])
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodGet && strings.HasSuffix(path, "/rules"):
			writeJSON(t, w, map[string]any{"items": []any{}})
		case r.Method == http.MethodDelete && strings.Contains(path, "/security-groups/"):
			id := path[strings.LastIndex(path, "/security-groups/")+len("/security-groups/"):]
			deletedSGIDs = append(deletedSGIDs, id)
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.String())
		}
	}))

	if err := svc.Delete(context.Background()); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}
	if !slices.Equal(detachedPairs, []string{testServerID + ":" + testSecurityGroupID}) {
		t.Fatalf("detachedPairs = %v, want [%s:%s]", detachedPairs, testServerID, testSecurityGroupID)
	}
	if !slices.Equal(deletedSGIDs, []string{testSecurityGroupID, testRemoteSGID}) {
		t.Fatalf("deletedSGIDs = %v, want [%s %s] (reverse order: node-ssh then bastion-ssh)", deletedSGIDs, testSecurityGroupID, testRemoteSGID)
	}
}

func TestFindByTagsAndDeleteByID(t *testing.T) {
	t.Run("FindByTags returns matching security group", func(t *testing.T) {
		svc := newTestService(t, nil, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			writeJSON(t, w, map[string]any{"items": []any{
				map[string]any{"id": testSecurityGroupID, "name": "bastion-ssh", "labels": map[string]any{"cluster": "test"}},
			}})
		}))

		got, err := svc.FindByTags(context.Background(), map[string]string{"cluster": "test"})
		if err != nil {
			t.Fatalf("FindByTags() error = %v", err)
		}
		if got.ID != testSecurityGroupID {
			t.Fatalf("FindByTags().ID = %q, want %q", got.ID, testSecurityGroupID)
		}
	})

	t.Run("DeleteByID deletes rules and security group and ignores NotFound", func(t *testing.T) {
		var (
			deletedRules []string
			deletedSGID  string
		)
		svc := newTestService(t, nil, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			path := r.URL.Path
			switch {
			case r.Method == http.MethodGet && strings.HasSuffix(path, "/security-groups/"+testSecurityGroupID+"/rules"):
				writeJSON(t, w, map[string]any{"items": []any{
					map[string]any{"id": testRuleID1, "direction": "ingress"},
				}})
			case r.Method == http.MethodDelete && strings.Contains(path, "/rules/"):
				parts := strings.Split(strings.TrimSuffix(path, "/"), "/")
				deletedRules = append(deletedRules, parts[len(parts)-1])
				w.WriteHeader(http.StatusNoContent)
			case r.Method == http.MethodDelete && strings.HasSuffix(path, "/security-groups/"+testSecurityGroupID):
				deletedSGID = testSecurityGroupID
				w.WriteHeader(http.StatusNoContent)
			default:
				t.Fatalf("unexpected request: %s %s", r.Method, r.URL.String())
			}
		}))

		if err := svc.DeleteByID(context.Background(), testSecurityGroupID); err != nil {
			t.Fatalf("DeleteByID() error = %v", err)
		}
		if !slices.Equal(deletedRules, []string{testRuleID1}) {
			t.Fatalf("deletedRules = %v, want [%s]", deletedRules, testRuleID1)
		}
		if deletedSGID != testSecurityGroupID {
			t.Fatalf("deletedSGID = %q, want %q", deletedSGID, testSecurityGroupID)
		}
	})
}
