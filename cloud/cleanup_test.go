/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package cloud_test

import (
	"context"
	"testing"

	"github.com/stackitcloud/cluster-api-provider-stackit/cloud"
	cloudfake "github.com/stackitcloud/cluster-api-provider-stackit/cloud/fake"
	"github.com/stackitcloud/cluster-api-provider-stackit/cloud/services/loadbalancer"
	"github.com/stackitcloud/cluster-api-provider-stackit/cloud/services/publicip"
	"github.com/stackitcloud/cluster-api-provider-stackit/cloud/services/securitygroup"
	"github.com/stackitcloud/cluster-api-provider-stackit/cloud/services/server"
)

type testServerScope struct {
	cloud.Client
	spec server.ServerSpec
}

func (s *testServerScope) ServerSpec() server.ServerSpec {
	return s.spec
}

func createTestServer(t *testing.T, client cloud.Client, name string, tags map[string]string) {
	t.Helper()

	err := server.New(&testServerScope{
		Client: client,
		spec: server.ServerSpec{
			Name:        name,
			ImageID:     "22222222-2222-4222-8222-222222222222",
			MachineType: "c2i.2",
			NetworkID:   "11111111-1111-4111-8111-111111111111",
			Tags:        tags,
		},
	}).Reconcile(context.Background())
	if err != nil {
		t.Fatalf("server.Reconcile() error = %v", err)
	}
}

func TestCleanupByTagsDeletesMatchingCloudResources(t *testing.T) {
	ctx := context.Background()
	client := cloudfake.New()
	tags := map[string]string{
		"cluster-api-provider-stackit/e2e":     "true",
		"cluster-api-provider-stackit/test-id": "test-1",
	}
	otherTags := map[string]string{
		"cluster-api-provider-stackit/e2e":     "true",
		"cluster-api-provider-stackit/test-id": "test-2",
	}

	srvClient := server.NewClient(client)
	lbClient := loadbalancer.NewClient(client)
	pubIPClient := publicip.NewClient(client)
	sgClient := securitygroup.NewClient(client)

	createTestServer(t, client, "matched", tags)
	createTestServer(t, client, "other", otherTags)
	if _, err := client.CreatePublicIP(ctx, tags); err != nil {
		t.Fatalf("CreatePublicIP() error = %v", err)
	}
	if _, err := client.CreatePublicIP(ctx, otherTags); err != nil {
		t.Fatalf("CreatePublicIP() error = %v", err)
	}
	if _, err := client.CreateSecurityGroup(ctx, "matched-sg", tags); err != nil {
		t.Fatalf("CreateSecurityGroup() error = %v", err)
	}
	if _, err := client.CreateSecurityGroup(ctx, "other-sg", otherTags); err != nil {
		t.Fatalf("CreateSecurityGroup() error = %v", err)
	}
	if _, err := client.CreateLoadBalancer(ctx, "matched", tags); err != nil {
		t.Fatalf("CreateLoadBalancer() error = %v", err)
	}
	if _, err := client.CreateLoadBalancer(ctx, "other", otherTags); err != nil {
		t.Fatalf("CreateLoadBalancer() error = %v", err)
	}

	if err := cloud.CleanupByTags(ctx, client, tags); err != nil {
		t.Fatalf("CleanupByTags() error = %v", err)
	}

	matchedServers, err := srvClient.ListByTags(ctx, tags)
	if err != nil {
		t.Fatalf("server.ListByTags() error = %v", err)
	}
	if len(matchedServers) != 0 {
		t.Fatalf("matched server count = %d, want 0", len(matchedServers))
	}
	otherServers, err := srvClient.ListByTags(ctx, otherTags)
	if err != nil {
		t.Fatalf("server.ListByTags(other) error = %v", err)
	}
	if len(otherServers) != 1 {
		t.Fatalf("other server count = %d, want 1", len(otherServers))
	}
	matchedLoadBalancers, err := lbClient.ListByTags(ctx, tags)
	if err != nil {
		t.Fatalf("loadbalancer.ListByTags() error = %v", err)
	}
	if len(matchedLoadBalancers) != 0 {
		t.Fatalf("matched load balancer count = %d, want 0", len(matchedLoadBalancers))
	}
	otherLoadBalancers, err := lbClient.ListByTags(ctx, otherTags)
	if err != nil {
		t.Fatalf("loadbalancer.ListByTags(other) error = %v", err)
	}
	if len(otherLoadBalancers) != 1 {
		t.Fatalf("other load balancer count = %d, want 1", len(otherLoadBalancers))
	}
	matchedPublicIPs, err := pubIPClient.ListByTags(ctx, tags)
	if err != nil {
		t.Fatalf("publicip.ListByTags() error = %v", err)
	}
	if len(matchedPublicIPs) != 0 {
		t.Fatalf("matched public IP count = %d, want 0", len(matchedPublicIPs))
	}
	otherPublicIPs, err := pubIPClient.ListByTags(ctx, otherTags)
	if err != nil {
		t.Fatalf("publicip.ListByTags(other) error = %v", err)
	}
	if len(otherPublicIPs) != 1 {
		t.Fatalf("other public IP count = %d, want 1", len(otherPublicIPs))
	}
	matchedSecurityGroups, err := sgClient.ListByTags(ctx, tags)
	if err != nil {
		t.Fatalf("securitygroup.ListByTags() error = %v", err)
	}
	if len(matchedSecurityGroups) != 0 {
		t.Fatalf("matched security group count = %d, want 0", len(matchedSecurityGroups))
	}
	otherSecurityGroups, err := sgClient.ListByTags(ctx, otherTags)
	if err != nil {
		t.Fatalf("securitygroup.ListByTags(other) error = %v", err)
	}
	if len(otherSecurityGroups) != 1 {
		t.Fatalf("other security group count = %d, want 1", len(otherSecurityGroups))
	}
}
