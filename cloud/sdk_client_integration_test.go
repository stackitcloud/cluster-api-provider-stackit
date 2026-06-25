//go:build stackit_integration

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
	"errors"
	"fmt"
	"os"
	"testing"
	"time"
)

const (
	envIntegrationProjectID          = "STACKIT_PROJECT_ID"
	envIntegrationRegion             = "STACKIT_REGION"
	envIntegrationServiceAccountFile = "STACKIT_SERVICE_ACCOUNT_JSON_FILE"
	envIntegrationNetworkID          = "STACKIT_NETWORK_ID"
	envIntegrationInitialTargetIP    = "STACKIT_TEST_INITIAL_TARGET_IP"
	envIntegrationTargetIP           = "STACKIT_TEST_TARGET_IP"
)

func TestSDKClientCredentialsIntegration(t *testing.T) {
	client := newIntegrationClient(t)

	_, err := client.FindServerByTags(context.Background(), map[string]string{
		"capistackit-test": "credential-check",
	})
	if IsNotFound(err) {
		return
	}
	if err != nil {
		t.Fatalf("FindServerByTags() error = %v", err)
	}
	t.Fatal("FindServerByTags() unexpectedly found a server matching integration-test tags")
}

func TestSDKClientListNetworksIntegration(t *testing.T) {
	client := newIntegrationSDKClient(t)

	resp, err := client.iaasClient.DefaultAPI.ListNetworks(context.Background(), client.projectID, client.region).Execute()
	if err != nil {
		t.Fatalf("ListNetworks() error = %v", err)
	}
	networks := resp.GetItems()
	if len(networks) == 0 {
		t.Skipf("ListNetworks() returned no networks in region %s", client.region)
	}
	for _, network := range networks {
		t.Logf("network id=%s name=%s", network.GetId(), network.GetName())
	}
}

func TestSDKClientLoadBalancerCreateDeleteIntegration(t *testing.T) {
	client := newIntegrationClient(t)
	networkID := requiredIntegrationEnv(t, envIntegrationNetworkID)
	targetIP := requiredIntegrationEnv(t, envIntegrationTargetIP)
	loadBalancer := createIntegrationLoadBalancer(t, client, networkID, LoadBalancerTargetInput{
		Name: "capistackit-initial-target",
		IP:   targetIP,
		Port: 6443,
	})

	if err := client.DeleteAPIServerLoadBalancer(context.Background(), loadBalancer.ID); err != nil {
		t.Fatalf("DeleteAPIServerLoadBalancer() error = %v", err)
	}
}

func TestSDKClientLoadBalancerTargetIntegration(t *testing.T) {
	client := newIntegrationClient(t)
	networkID := requiredIntegrationEnv(t, envIntegrationNetworkID)
	targetIP := requiredIntegrationEnv(t, envIntegrationTargetIP)
	loadBalancer := createIntegrationLoadBalancer(t, client, networkID, LoadBalancerTargetInput{
		Name: "capistackit-initial-target",
		IP:   integrationInitialTargetIP(targetIP),
		Port: 6443,
	})
	t.Cleanup(func() {
		if err := client.DeleteAPIServerLoadBalancer(context.Background(), loadBalancer.ID); err != nil && !IsNotFound(err) {
			t.Logf("DeleteAPIServerLoadBalancer() cleanup error = %v", err)
		}
	})

	target := LoadBalancerTargetInput{
		LoadBalancerID: loadBalancer.ID,
		Name:           "capistackit-integration-target",
		IP:             targetIP,
		Port:           6443,
	}
	if err := client.EnsureAPIServerLoadBalancerTarget(context.Background(), target); err != nil {
		t.Fatalf("EnsureAPIServerLoadBalancerTarget() error = %v", err)
	}
	if err := client.DeleteAPIServerLoadBalancerTarget(context.Background(), target); err != nil {
		t.Fatalf("DeleteAPIServerLoadBalancerTarget() error = %v", err)
	}
}

func newIntegrationClient(t *testing.T) Client {
	t.Helper()

	return newIntegrationSDKClient(t)
}

func newIntegrationSDKClient(t *testing.T) *SDKClient {
	t.Helper()

	creds := integrationCredentials(t)
	client, err := NewClient(context.Background(), creds)
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	sdkClient, ok := client.(*SDKClient)
	if !ok {
		t.Fatalf("NewClient() = %T, want *SDKClient", client)
	}
	return sdkClient
}

func integrationCredentials(t *testing.T) Credentials {
	t.Helper()

	path := os.Getenv(envIntegrationServiceAccountFile)
	if path == "" {
		path = defaultIntegrationServiceAccountPath(t)
	}
	serviceAccountJSON, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("Read service account file %q: %v", path, err)
	}

	projectID := os.Getenv(envIntegrationProjectID)
	if projectID == "" {
		projectID = serviceAccountProjectID(t, serviceAccountJSON)
	}

	region := os.Getenv(envIntegrationRegion)
	if region == "" {
		region = "eu01"
	}

	return Credentials{
		ProjectID:          projectID,
		Region:             region,
		ServiceAccountJSON: serviceAccountJSON,
	}
}

func defaultIntegrationServiceAccountPath(t *testing.T) string {
	t.Helper()

	t.Fatalf("Set %s to the STACKIT service account JSON file", envIntegrationServiceAccountFile)
	return ""
}

func serviceAccountProjectID(t *testing.T, serviceAccountJSON []byte) string {
	t.Helper()

	var key struct {
		ProjectID string `json:"projectId"`
	}
	if err := json.Unmarshal(serviceAccountJSON, &key); err != nil {
		t.Fatalf("Parse service account JSON: %v", err)
	}
	if key.ProjectID == "" {
		t.Fatalf("Service account JSON does not contain projectId; set %s explicitly", envIntegrationProjectID)
	}
	return key.ProjectID
}

func createIntegrationLoadBalancer(t *testing.T, client Client, networkID string, target LoadBalancerTargetInput) *LoadBalancer {
	t.Helper()

	name := fmt.Sprintf("capistackit-it-%d", time.Now().UnixNano())
	loadBalancer, err := client.EnsureAPIServerLoadBalancer(context.Background(), LoadBalancerInput{
		Name:      name,
		Region:    integrationRegion(),
		NetworkID: networkID,
		Port:      6443,
		Targets:   []LoadBalancerTargetInput{target},
		Tags: map[string]string{
			"capistackit-test": "true",
			"capistackit-name": name,
		},
	})
	if err != nil {
		t.Fatalf("EnsureAPIServerLoadBalancer() error = %v", err)
	}
	if loadBalancer == nil || loadBalancer.ID == "" {
		t.Fatalf("EnsureAPIServerLoadBalancer() = %#v, want load balancer with ID", loadBalancer)
	}
	t.Cleanup(func() {
		if err := client.DeleteAPIServerLoadBalancer(context.Background(), loadBalancer.ID); err != nil &&
			!IsNotFound(err) &&
			!errors.Is(err, ErrInvalidInput) {
			t.Logf("DeleteAPIServerLoadBalancer() cleanup error = %v", err)
		}
	})
	return loadBalancer
}

func integrationRegion() string {
	if region := os.Getenv(envIntegrationRegion); region != "" {
		return region
	}
	return "eu01"
}

func integrationInitialTargetIP(targetIP string) string {
	if ip := os.Getenv(envIntegrationInitialTargetIP); ip != "" {
		return ip
	}
	if targetIP != "10.0.0.10" {
		return "10.0.0.10"
	}
	return "10.0.0.11"
}

func requiredIntegrationEnv(t *testing.T, key string) string {
	t.Helper()

	value := os.Getenv(key)
	if value == "" {
		t.Skipf("%s is required for this integration test", key)
	}
	return value
}
