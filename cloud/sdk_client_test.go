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
	"sync"
	"testing"
)

const (
	testSDKProjectID     = "00000000-0000-0000-0000-000000000000"
	testSDKRegion        = "eu01"
	testSDKServerID      = "33333333-3333-4333-8333-333333333333"
	testSDKNetworkID     = "11111111-1111-4111-8111-111111111111"
	testSDKImageID       = "22222222-2222-4222-8222-222222222222"
	testSDKSecurityGroup = "44444444-4444-4444-8444-444444444444"
	testBoolTrue         = "true"
	testSDKPublicIP      = "203.0.113.10"
)

func TestSDKClientCreateServerUsesExpectedPayload(t *testing.T) {
	var createPayload map[string]any
	server := newSDKTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
				"id":          testSDKServerID,
				"machineType": "c2i.2",
				"name":        "node-0",
				"status":      "ACTIVE",
			})
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/servers/"+testSDKServerID+"/nics"):
			writeJSON(t, w, map[string]any{
				"items": []any{
					map[string]any{"ipv4": "10.0.0.10", "ipv6": "fd00::10"},
				},
			})
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.String())
		}
	}))

	client := newTestSDKClient(t, server.URL)
	created, err := client.CreateServer(context.Background(), CreateServerInput{
		Name:             "node-0",
		MachineType:      "c2i.2",
		ImageID:          testSDKImageID,
		AvailabilityZone: "eu01-1",
		SSHKeyName:       "default",
		NetworkID:        testSDKNetworkID,
		SecurityGroups:   []string{testSDKSecurityGroup},
		UserData:         []byte("#cloud-config\n"),
		RootVolume: RootVolumeInput{
			SizeGiB:             50,
			PerformanceClass:    "storage_premium_perf6",
			DeleteOnTermination: true,
		},
		Tags: map[string]string{"cluster": "test"},
	})
	if err != nil {
		t.Fatalf("CreateServer() error = %v", err)
	}
	if created.ID != testSDKServerID || created.Name != "node-0" || created.State != "ACTIVE" {
		t.Fatalf("CreateServer() = %#v", created)
	}
	if len(created.Addresses) != 2 {
		t.Fatalf("CreateServer() addresses = %#v, want two internal addresses", created.Addresses)
	}

	assertStringField(t, createPayload, "name", "node-0")
	assertStringField(t, createPayload, "machineType", "c2i.2")
	assertFieldAbsent(t, createPayload, "imageId")
	assertStringField(t, createPayload, "availabilityZone", "eu01-1")
	assertStringField(t, createPayload, "keypairName", "default")
	assertStringField(t, createPayload, "userData", "I2Nsb3VkLWNvbmZpZwo=")
	assertBoolField(t, createPayload, "configDrive", true)
	assertNestedStringField(t, createPayload, []string{"labels", "cluster"}, "test")
	assertNestedStringField(t, createPayload, []string{"networking", "networkId"}, testSDKNetworkID)
	assertNestedStringField(t, createPayload, []string{"bootVolume", "performanceClass"}, "storage_premium_perf6")
	assertNestedStringField(t, createPayload, []string{"bootVolume", "source", "id"}, testSDKImageID)
	assertNestedStringField(t, createPayload, []string{"bootVolume", "source", "type"}, "image")
	assertNestedNumberField(t, createPayload, []string{"bootVolume", "size"}, 50)
}

func assertFieldAbsent(t *testing.T, payload map[string]any, key string) {
	t.Helper()
	if _, ok := payload[key]; ok {
		t.Fatalf("%s present in payload, want absent: %#v", key, payload[key])
	}
}

func TestSDKClientEnsureAPIServerLoadBalancerCreatesExpectedPayload(t *testing.T) {
	var createPayload map[string]any
	server := newSDKTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/load-balancers"):
			writeJSON(t, w, map[string]any{"loadBalancers": []any{}})
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/load-balancers"):
			createPayload = readJSON(t, r)
			writeJSON(t, w, sdkLoadBalancerJSON("apiserver-test", "203.0.113.10", []any{
				map[string]any{"name": apiserverTargetPoolName, "targetPort": 6443, "targets": []any{
					map[string]any{"displayName": "cp-0", "ip": "10.0.0.10"},
				}},
			}))
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.String())
		}
	}))

	client := newTestSDKClient(t, server.URL)
	loadBalancer, err := client.EnsureAPIServerLoadBalancer(context.Background(), LoadBalancerInput{
		Name:      "apiserver-test",
		Region:    testSDKRegion,
		NetworkID: testSDKNetworkID,
		Port:      6443,
		Targets: []LoadBalancerTargetInput{{
			Name: "cp-0",
			IP:   "10.0.0.10",
		}},
		Tags: map[string]string{
			"cluster.x-k8s.io/cluster-name":        "test",
			"cluster-api-provider-stackit/test-id": "run-1",
		},
	})
	if err != nil {
		t.Fatalf("EnsureAPIServerLoadBalancer() error = %v", err)
	}
	if loadBalancer.ID != "apiserver-test" || loadBalancer.IP != testSDKPublicIP || loadBalancer.Port != 6443 {
		t.Fatalf("EnsureAPIServerLoadBalancer() = %#v", loadBalancer)
	}

	assertStringField(t, createPayload, "name", "apiserver-test")
	assertStringField(t, createPayload, "region", testSDKRegion)
	assertNestedStringField(t, createPayload, []string{"labels", "cluster.x-k8s.io.cluster-name"}, "test")
	assertNestedStringField(t, createPayload, []string{"labels", "cluster-api-provider-stackit.test-id"}, "run-1")
	assertNestedBoolField(t, createPayload, []string{"options", "ephemeralAddress"}, true)
	assertNestedStringField(t, createPayload, []string{"networks", "0", "networkId"}, testSDKNetworkID)
	assertNestedStringField(t, createPayload, []string{"networks", "0", "role"}, "ROLE_LISTENERS_AND_TARGETS")
	assertNestedStringField(t, createPayload, []string{"targetPools", "0", "name"}, apiserverTargetPoolName)
	assertNestedNumberField(t, createPayload, []string{"targetPools", "0", "targetPort"}, 6443)
	assertNestedStringField(t, createPayload, []string{"targetPools", "0", "targets", "0", "displayName"}, "cp-0")
	assertNestedStringField(t, createPayload, []string{"targetPools", "0", "targets", "0", "ip"}, "10.0.0.10")
	assertNestedStringField(t, createPayload, []string{"listeners", "0", "targetPool"}, apiserverTargetPoolName)
	assertNestedNumberField(t, createPayload, []string{"listeners", "0", "port"}, 6443)
}

func TestSDKClientEnsureAPIServerLoadBalancerUsesBootstrapTargetWhenInitialTargetsAreEmpty(t *testing.T) {
	var createPayload map[string]any
	server := newSDKTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/load-balancers"):
			writeJSON(t, w, map[string]any{"loadBalancers": []any{}})
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/load-balancers"):
			createPayload = readJSON(t, r)
			writeJSON(t, w, sdkLoadBalancerJSON("apiserver-test", "203.0.113.10", []any{
				map[string]any{"name": apiserverTargetPoolName, "targetPort": 6443, "targets": []any{
					map[string]any{"displayName": bootstrapTargetName, "ip": bootstrapTargetIP},
				}},
			}))
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.String())
		}
	}))

	client := newTestSDKClient(t, server.URL)
	loadBalancer, err := client.EnsureAPIServerLoadBalancer(context.Background(), LoadBalancerInput{
		Name:      "apiserver-test",
		Region:    testSDKRegion,
		NetworkID: testSDKNetworkID,
		Port:      6443,
		Tags: map[string]string{
			"cluster.x-k8s.io/cluster-name": "test",
		},
	})
	if err != nil {
		t.Fatalf("EnsureAPIServerLoadBalancer() error = %v", err)
	}
	if loadBalancer.ID != "apiserver-test" || loadBalancer.IP != "203.0.113.10" || loadBalancer.Port != 6443 {
		t.Fatalf("EnsureAPIServerLoadBalancer() = %#v", loadBalancer)
	}

	assertNestedStringField(
		t,
		createPayload,
		[]string{"targetPools", "0", "targets", "0", "displayName"},
		bootstrapTargetName,
	)
	assertNestedStringField(t, createPayload, []string{"targetPools", "0", "targets", "0", "ip"}, bootstrapTargetIP)
	assertNestedStringField(t, createPayload, []string{"listeners", "0", "targetPool"}, apiserverTargetPoolName)
}

func TestSDKClientLoadBalancerTargetUpdates(t *testing.T) {
	var mu sync.Mutex
	targets := []any{
		map[string]any{"displayName": "cp-0", "ip": "10.0.0.10"},
	}
	var updatePayloads []map[string]any

	server := newSDKTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()

		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/load-balancers/apiserver-test"):
			writeJSON(t, w, sdkLoadBalancerJSON("apiserver-test", "203.0.113.10", []any{
				map[string]any{"name": apiserverTargetPoolName, "targetPort": 6443, "targets": targets},
			}))
		case r.Method == http.MethodPut &&
			strings.HasSuffix(r.URL.Path, "/load-balancers/apiserver-test/target-pools/"+apiserverTargetPoolName):
			payload := readJSON(t, r)
			updatePayloads = append(updatePayloads, payload)
			next, ok := lookup(payload, "targets").([]any)
			if !ok {
				t.Fatalf("update target payload missing targets: %#v", payload)
			}
			targets = next
			writeJSON(t, w, map[string]any{
				"name":       apiserverTargetPoolName,
				"targetPort": 6443,
				"targets":    targets,
			})
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.String())
		}
	}))

	client := newTestSDKClient(t, server.URL)
	input := LoadBalancerTargetInput{
		LoadBalancerID: "apiserver-test",
		Name:           "cp-1",
		IP:             "10.0.0.11",
		Port:           6443,
	}
	if err := client.EnsureAPIServerLoadBalancerTarget(context.Background(), input); err != nil {
		t.Fatalf("EnsureAPIServerLoadBalancerTarget() error = %v", err)
	}
	if err := client.DeleteAPIServerLoadBalancerTarget(context.Background(), input); err != nil {
		t.Fatalf("DeleteAPIServerLoadBalancerTarget() error = %v", err)
	}

	if len(updatePayloads) != 2 {
		t.Fatalf("got %d update payloads, want 2", len(updatePayloads))
	}
	assertNestedStringField(t, updatePayloads[0], []string{"targets", "1", "displayName"}, "cp-1")
	assertNestedStringField(t, updatePayloads[0], []string{"targets", "1", "ip"}, "10.0.0.11")
	if got := nestedValue(t, updatePayloads[1], []string{"targets"}).([]any); len(got) != 1 {
		t.Fatalf("delete target payload targets = %#v, want one remaining target", got)
	}
	assertNestedStringField(t, updatePayloads[1], []string{"targets", "0", "displayName"}, "cp-0")
}

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

			_, err := client.GetServer(context.Background(), testSDKServerID)
			if err == nil {
				t.Fatal("GetServer() error = nil")
			}
			if !tt.check(err) {
				t.Fatalf("GetServer() error = %v, did not match expected classifier", err)
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

func sdkLoadBalancerJSON(name, externalAddress string, targetPools []any) map[string]any {
	return map[string]any{
		"name":            name,
		"externalAddress": externalAddress,
		"listeners": []any{
			map[string]any{"displayName": apiserverListenerName, "port": 6443, "targetPool": apiserverTargetPoolName},
		},
		"targetPools": targetPools,
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

func writeJSON(t *testing.T, w http.ResponseWriter, value any) {
	t.Helper()

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(value); err != nil {
		t.Fatalf("encode response body: %v", err)
	}
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

func assertNestedStringField(t *testing.T, m map[string]any, path []string, want string) {
	t.Helper()
	if got := nestedValue(t, m, path); got != want {
		t.Fatalf("%s = %#v, want %q in %#v", strings.Join(path, "."), got, want, m)
	}
}

func assertNestedBoolField(t *testing.T, m map[string]any, path []string, want bool) {
	t.Helper()
	if got := nestedValue(t, m, path); got != want {
		t.Fatalf("%s = %#v, want %t in %#v", strings.Join(path, "."), got, want, m)
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
		case []any:
			if len(part) != 1 || part[0] < '0' || part[0] > '9' {
				t.Fatalf("path element %q cannot index %#v", part, typed)
			}
			index := int(part[0] - '0')
			if index >= len(typed) {
				t.Fatalf("path element %q out of range for %#v", part, typed)
			}
			current = typed[index]
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

// TestSDKClientEnsureBastionToleratesDuplicateSecurityGroupAttach guards the
// idempotency of the security-group attach. The group is already in the create
// payload, so the attach usually hits a duplicate ("400 Duplicate items in the
// list") or a server without a port yet ("404 ... as device id on any ports").
// Neither may abort EnsureBastion — that used to leave the public IP unassigned
// for a full reconcile cycle. The attach itself is kept because CreateServer
// short-circuits on an existing server, making this the only re-attach path.
func TestSDKClientEnsureBastionToleratesDuplicateSecurityGroupAttach(t *testing.T) {
	var (
		createPayload    map[string]any
		attachCallCount  int
		publicIPAssigned bool
	)
	server := newSDKTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(path, "/security-groups"):
			writeJSON(t, w, map[string]any{"items": []any{
				map[string]any{
					"id": testSDKSecurityGroup, "name": "bastion-ssh",
					"labels": map[string]any{"cluster": "test"},
				},
			}})
		case r.Method == http.MethodGet && strings.HasSuffix(path, "/rules"):
			writeJSON(t, w, map[string]any{"items": []any{}})
		case r.Method == http.MethodPost && strings.HasSuffix(path, "/rules"):
			writeJSON(t, w, map[string]any{"id": "77777777-7777-4777-8777-777777777777", "direction": "ingress"})
		case r.Method == http.MethodGet && strings.HasSuffix(path, "/servers"):
			writeJSON(t, w, map[string]any{"items": []any{}})
		case r.Method == http.MethodPost && strings.HasSuffix(path, "/servers"):
			createPayload = readJSON(t, r)
			writeJSON(t, w, map[string]any{
				"id": testSDKServerID, "name": "bastion", "status": "ACTIVE", "machineType": "c2i.1",
			})
		case strings.Contains(path, "/security-groups/") && strings.Contains(path, "/servers/"):
			// Answer the way the real API does for an already-attached group.
			attachCallCount++
			w.WriteHeader(http.StatusBadRequest)
			writeJSON(t, w, map[string]any{
				"code": 400,
				"msg":  "request invalid: Invalid input for security_groups. Reason: Duplicate items in the list.",
			})
		case r.Method == http.MethodGet && strings.HasSuffix(path, "/public-ips"):
			writeJSON(t, w, map[string]any{"items": []any{}})
		case r.Method == http.MethodPost && strings.HasSuffix(path, "/public-ips"):
			writeJSON(t, w, map[string]any{"id": "66666666-6666-4666-8666-666666666666", "ip": "203.0.113.10"})
		case strings.Contains(path, "/public-ips/"):
			publicIPAssigned = true
			writeJSON(t, w, map[string]any{"id": "66666666-6666-4666-8666-666666666666", "ip": "203.0.113.10"})
		case r.Method == http.MethodGet && strings.Contains(path, "/servers/"):
			writeJSON(t, w, map[string]any{
				"id": testSDKServerID, "name": "bastion", "status": "ACTIVE", "machineType": "c2i.1",
			})
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.String())
		}
	}))

	client := newTestSDKClient(t, server.URL)
	bastion, err := client.EnsureBastion(context.Background(), BastionInput{
		Name:         "bastion",
		ProjectID:    testSDKProjectID,
		Region:       testSDKRegion,
		NetworkID:    testSDKNetworkID,
		ImageID:      testSDKImageID,
		MachineType:  "c2i.1",
		SSHKeyName:   "default",
		AllowedCIDRs: []string{"203.0.113.0/24"},
		Tags:         map[string]string{"cluster": "test"},
	})
	if err != nil {
		t.Fatalf("EnsureBastion() error = %v", err)
	}

	if attachCallCount != 1 {
		t.Fatalf("attach attempted %d time(s), want exactly 1 (the idempotent re-attach)", attachCallCount)
	}
	groups, _ := createPayload["securityGroups"].([]any)
	if len(groups) != 1 || groups[0] != testSDKSecurityGroup {
		t.Fatalf("create payload securityGroups = %v, want [%s]", createPayload["securityGroups"], testSDKSecurityGroup)
	}
	if !publicIPAssigned {
		t.Fatal("public IP was never assigned to the bastion server")
	}
	if bastion.PublicIP != "203.0.113.10" {
		t.Fatalf("bastion.PublicIP = %q, want 203.0.113.10", bastion.PublicIP)
	}
}

// TestSDKClientEnsureBastionRevokesRemovedCIDR guards against security-group
// rules only ever being added. Narrowing allowedCIDRs must actually take
// access away from the previously allowed range.
// nolint:dupl // avoid lint demanding a helper, which makes tests less obvious.
func TestSDKClientEnsureBastionRevokesRemovedCIDR(t *testing.T) {
	const staleRuleID = "55555555-5555-4555-8555-555555555555"
	var (
		createdRuleCIDRs []string
		deletedRuleIDs   []string
	)
	server := newSDKTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(path, "/security-groups"):
			writeJSON(t, w, map[string]any{"items": []any{
				map[string]any{
					"id": testSDKSecurityGroup, "name": "bastion-ssh",
					"labels": map[string]any{"cluster": "test"},
				},
			}})
		case r.Method == http.MethodGet && strings.HasSuffix(path, "/rules"):
			// One pre-existing SSH rule for the CIDR that is being revoked.
			writeJSON(t, w, map[string]any{"items": []any{
				map[string]any{
					"id":        staleRuleID,
					"direction": "ingress",
					"ipRange":   "198.51.100.0/24",
					"portRange": map[string]any{"min": 22, "max": 22},
					"protocol":  map[string]any{"name": "tcp"},
				},
			}})
		case r.Method == http.MethodPost && strings.HasSuffix(path, "/rules"):
			payload := readJSON(t, r)
			if cidr, ok := payload["ipRange"].(string); ok {
				createdRuleCIDRs = append(createdRuleCIDRs, cidr)
			}
			writeJSON(t, w, map[string]any{"id": "77777777-7777-4777-8777-777777777777", "direction": "ingress"})
		case strings.Contains(path, "/security-groups/") && strings.Contains(path, "/servers/"):
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodDelete && strings.Contains(path, "/rules/"):
			parts := strings.Split(strings.TrimSuffix(path, "/"), "/")
			deletedRuleIDs = append(deletedRuleIDs, parts[len(parts)-1])
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodGet && strings.HasSuffix(path, "/servers"):
			writeJSON(t, w, map[string]any{"items": []any{
				map[string]any{
					"id": testSDKServerID, "name": "bastion", "status": "ACTIVE", "machineType": "c2i.1",
					"labels": map[string]any{"cluster": "test"},
				},
			}})
		case r.Method == http.MethodGet && strings.HasSuffix(path, "/public-ips"):
			writeJSON(t, w, map[string]any{"items": []any{
				map[string]any{
					"id": "66666666-6666-4666-8666-666666666666", "ip": "203.0.113.10", "networkInterface": "nic-1",
					"labels": map[string]any{"cluster": "test"},
				},
			}})
		case r.Method == http.MethodGet && strings.Contains(path, "/servers/"):
			writeJSON(t, w, map[string]any{
				"id": testSDKServerID, "name": "bastion", "status": "ACTIVE", "machineType": "c2i.1",
			})
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.String())
		}
	}))

	client := newTestSDKClient(t, server.URL)
	if _, err := client.EnsureBastion(context.Background(), BastionInput{
		Name:         "bastion",
		ProjectID:    testSDKProjectID,
		Region:       testSDKRegion,
		NetworkID:    testSDKNetworkID,
		ImageID:      testSDKImageID,
		MachineType:  "c2i.1",
		SSHKeyName:   "default",
		AllowedCIDRs: []string{"203.0.113.0/24"},
		Tags:         map[string]string{"cluster": "test"},
	}); err != nil {
		t.Fatalf("EnsureBastion() error = %v", err)
	}

	if len(createdRuleCIDRs) != 1 || createdRuleCIDRs[0] != "203.0.113.0/24" {
		t.Fatalf("created rules for %v, want [203.0.113.0/24]", createdRuleCIDRs)
	}
	if len(deletedRuleIDs) != 1 || deletedRuleIDs[0] != staleRuleID {
		t.Fatalf("deleted rules %v, want [%s] — the revoked CIDR keeps its SSH access", deletedRuleIDs, staleRuleID)
	}
}

// TestSDKClientEnsureBastionKeepsSSHRulesWithoutIPRange guards against the
// revoke loop deleting an SSH rule that has no ipRange. Such a rule allows a
// remote security group, or it allows each source, so it does not come from
// AllowedCIDRs and another owner maintains it. The loop must delete only the
// rules it creates, otherwise the two owners fight, and the group changes
// on every reconcile.
func TestSDKClientEnsureBastionKeepsSSHRulesWithoutIPRange(t *testing.T) {
	const ingressRuleID = "99999999-9999-4999-8999-999999999999"
	var deletedRuleIDs []string
	server := newSDKTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(path, "/security-groups"):
			writeJSON(t, w, map[string]any{"items": []any{
				map[string]any{
					"id": testSDKSecurityGroup, "name": "bastion-ssh",
					"labels": map[string]any{"cluster": "test"},
				},
			}})
		case r.Method == http.MethodGet && strings.HasSuffix(path, "/rules"):
			// One pre-existing ingress rule that must remain.
			writeJSON(t, w, map[string]any{"items": []any{
				map[string]any{
					"id":        ingressRuleID,
					"direction": "ingress",
					"ipRange":   "",
					"portRange": map[string]any{"min": 22, "max": 22},
					"protocol":  map[string]any{"name": "tcp"},
				},
			}})
		case r.Method == http.MethodPost && strings.HasSuffix(path, "/rules"):
			writeJSON(t, w, map[string]any{"id": "77777777-7777-4777-8777-777777777777", "direction": "ingress"})
		case strings.Contains(path, "/security-groups/") && strings.Contains(path, "/servers/"):
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodDelete && strings.Contains(path, "/rules/"):
			parts := strings.Split(strings.TrimSuffix(path, "/"), "/")
			deletedRuleIDs = append(deletedRuleIDs, parts[len(parts)-1])
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodGet && strings.HasSuffix(path, "/servers"):
			writeJSON(t, w, map[string]any{"items": []any{
				map[string]any{
					"id": testSDKServerID, "name": "bastion", "status": "ACTIVE", "machineType": "c2i.1",
					"labels": map[string]any{"cluster": "test"},
				},
			}})
		case r.Method == http.MethodGet && strings.HasSuffix(path, "/public-ips"):
			writeJSON(t, w, map[string]any{"items": []any{
				map[string]any{
					"id": "66666666-6666-4666-8666-666666666666", "ip": "203.0.113.10", "networkInterface": "nic-1",
					"labels": map[string]any{"cluster": "test"},
				},
			}})
		case r.Method == http.MethodGet && strings.Contains(path, "/servers/"):
			writeJSON(t, w, map[string]any{
				"id": testSDKServerID, "name": "bastion", "status": "ACTIVE", "machineType": "c2i.1",
			})
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.String())
		}
	}))

	client := newTestSDKClient(t, server.URL)
	if _, err := client.EnsureBastion(context.Background(), BastionInput{
		Name:         "bastion",
		ProjectID:    testSDKProjectID,
		Region:       testSDKRegion,
		NetworkID:    testSDKNetworkID,
		ImageID:      testSDKImageID,
		MachineType:  "c2i.1",
		SSHKeyName:   "default",
		AllowedCIDRs: []string{"203.0.113.0/24"},
		Tags:         map[string]string{"cluster": "test"},
	}); err != nil {
		t.Fatalf("EnsureBastion() error = %v", err)
	}

	if len(deletedRuleIDs) > 0 {
		t.Fatalf("deleted rules %v, want 0 — the ingress rule with empty "+
			"ipRange shouldn't be deleted", deletedRuleIDs)
	}
}

// TestSDKClientEnsureBastionDeduplicatesRepeatedCIDRs guards against a CIDR
// listed twice producing two identical rules: existingRules is a snapshot taken
// before the loop, so the second pass would not see the rule the first pass
// created and the duplicate create fails the whole reconcile.
func TestSDKClientEnsureBastionDeduplicatesRepeatedCIDRs(t *testing.T) {
	var createdRuleCIDRs []string
	server := newSDKTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(path, "/security-groups"):
			writeJSON(t, w, map[string]any{"items": []any{
				map[string]any{
					"id": testSDKSecurityGroup, "name": "bastion-ssh",
					"labels": map[string]any{"cluster": "test"},
				},
			}})
		case r.Method == http.MethodGet && strings.HasSuffix(path, "/rules"):
			writeJSON(t, w, map[string]any{"items": []any{}})
		case r.Method == http.MethodPost && strings.HasSuffix(path, "/rules"):
			payload := readJSON(t, r)
			if cidr, ok := payload["ipRange"].(string); ok {
				createdRuleCIDRs = append(createdRuleCIDRs, cidr)
			}
			writeJSON(t, w, map[string]any{"id": "77777777-7777-4777-8777-777777777777", "direction": "ingress"})
		case r.Method == http.MethodGet && strings.HasSuffix(path, "/servers"):
			writeJSON(t, w, map[string]any{"items": []any{
				map[string]any{
					"id": testSDKServerID, "name": "bastion", "status": "ACTIVE", "machineType": "c2i.1",
					"labels": map[string]any{"cluster": "test"},
				},
			}})
		case strings.Contains(path, "/security-groups/") && strings.Contains(path, "/servers/"):
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodGet && strings.HasSuffix(path, "/public-ips"):
			writeJSON(t, w, map[string]any{"items": []any{
				map[string]any{
					"id": "66666666-6666-4666-8666-666666666666", "ip": "203.0.113.10", "networkInterface": "nic-1",
					"labels": map[string]any{"cluster": "test"},
				},
			}})
		case r.Method == http.MethodGet && strings.Contains(path, "/servers/"):
			writeJSON(t, w, map[string]any{
				"id": testSDKServerID, "name": "bastion", "status": "ACTIVE", "machineType": "c2i.1",
			})
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.String())
		}
	}))

	client := newTestSDKClient(t, server.URL)
	if _, err := client.EnsureBastion(context.Background(), BastionInput{
		Name:        "bastion",
		ProjectID:   testSDKProjectID,
		Region:      testSDKRegion,
		NetworkID:   testSDKNetworkID,
		ImageID:     testSDKImageID,
		MachineType: "c2i.1",
		SSHKeyName:  "default",
		// The same CIDR twice, plus a distinct one.
		AllowedCIDRs: []string{"203.0.113.0/24", "203.0.113.0/24", "198.51.100.0/24"},
		Tags:         map[string]string{"cluster": "test"},
	}); err != nil {
		t.Fatalf("EnsureBastion() error = %v", err)
	}

	if len(createdRuleCIDRs) != 2 {
		t.Fatalf("created %d rules (%v), want 2 — the repeated CIDR was created twice",
			len(createdRuleCIDRs), createdRuleCIDRs)
	}
}

// TestSDKClientEnsureBastionToleratesNonCanonicalCIDR guards against a CIDR
// given in a non-canonical form (e.g. host bits set) never matching
// the canonical form STACKIT stores and returns, so the rule is deleted
// and re-created on every reconcile.
// nolint:dupl // avoid lint demanding a helper, which makes tests less obvious.
func TestSDKClientEnsureBastionToleratesNonCanonicalCIDR(t *testing.T) {
	const (
		existingRuleID   = "88888888-8888-4888-8888-888888888888"
		nonCanonicalCIDR = "203.0.113.5/24"
	)
	var (
		createdRuleCIDRs []string
		deletedRuleIDs   []string
	)
	server := newSDKTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(path, "/security-groups"):
			writeJSON(t, w, map[string]any{"items": []any{
				map[string]any{
					"id": testSDKSecurityGroup, "name": "bastion-ssh",
					"labels": map[string]any{"cluster": "test"},
				},
			}})
		case r.Method == http.MethodGet && strings.HasSuffix(path, "/rules"):
			writeJSON(t, w, map[string]any{"items": []any{
				map[string]any{
					"id":        existingRuleID,
					"direction": "ingress",
					"ipRange":   "203.0.113.0/24",
					"portRange": map[string]any{"min": 22, "max": 22},
					"protocol":  map[string]any{"name": "tcp"},
				},
			}})
		case r.Method == http.MethodPost && strings.HasSuffix(path, "/rules"):
			payload := readJSON(t, r)
			if cidr, ok := payload["ipRange"].(string); ok {
				createdRuleCIDRs = append(createdRuleCIDRs, cidr)
			}
			writeJSON(t, w, map[string]any{"id": "77777777-7777-4777-8777-777777777777", "direction": "ingress"})
		case strings.Contains(path, "/security-groups/") && strings.Contains(path, "/servers/"):
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodDelete && strings.Contains(path, "/rules/"):
			parts := strings.Split(strings.TrimSuffix(path, "/"), "/")
			deletedRuleIDs = append(deletedRuleIDs, parts[len(parts)-1])
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodGet && strings.HasSuffix(path, "/servers"):
			writeJSON(t, w, map[string]any{"items": []any{
				map[string]any{
					"id": testSDKServerID, "name": "bastion", "status": "ACTIVE", "machineType": "c2i.1",
					"labels": map[string]any{"cluster": "test"},
				},
			}})
		case r.Method == http.MethodGet && strings.HasSuffix(path, "/public-ips"):
			writeJSON(t, w, map[string]any{"items": []any{
				map[string]any{
					"id": "66666666-6666-4666-8666-666666666666", "ip": "203.0.113.10", "networkInterface": "nic-1",
					"labels": map[string]any{"cluster": "test"},
				},
			}})
		case r.Method == http.MethodGet && strings.Contains(path, "/servers/"):
			writeJSON(t, w, map[string]any{
				"id": testSDKServerID, "name": "bastion", "status": "ACTIVE", "machineType": "c2i.1",
			})
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.String())
		}
	}))

	client := newTestSDKClient(t, server.URL)
	if _, err := client.EnsureBastion(context.Background(), BastionInput{
		Name:         "bastion",
		ProjectID:    testSDKProjectID,
		Region:       testSDKRegion,
		NetworkID:    testSDKNetworkID,
		ImageID:      testSDKImageID,
		MachineType:  "c2i.1",
		SSHKeyName:   "default",
		AllowedCIDRs: []string{nonCanonicalCIDR},
		Tags:         map[string]string{"cluster": "test"},
	}); err != nil {
		t.Fatalf("EnsureBastion() error = %v", err)
	}

	if len(createdRuleCIDRs) != 0 {
		t.Fatalf("created rules for %v, want none — the CIDR %v already has a matching rule", createdRuleCIDRs, nonCanonicalCIDR)
	}
	if len(deletedRuleIDs) != 0 {
		t.Fatalf("deleted rules %v, want none — the existing rule still matches the desired CIDR", deletedRuleIDs)
	}
}
