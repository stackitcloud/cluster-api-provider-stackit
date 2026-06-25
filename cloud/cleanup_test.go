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
)

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

	if _, err := client.CreateServer(ctx, cloud.CreateServerInput{Name: "matched", Tags: tags}); err != nil {
		t.Fatalf("CreateServer() error = %v", err)
	}
	if _, err := client.CreateServer(ctx, cloud.CreateServerInput{Name: "other", Tags: otherTags}); err != nil {
		t.Fatalf("CreateServer() error = %v", err)
	}
	if _, err := client.EnsureAPIServerLoadBalancer(ctx, cloud.LoadBalancerInput{
		Name:      "matched",
		Tags:      tags,
		NetworkID: "network-id",
		Port:      6443,
		Targets: []cloud.LoadBalancerTargetInput{{
			Name: "target",
			IP:   "10.0.0.10",
		}},
	}); err != nil {
		t.Fatalf("EnsureAPIServerLoadBalancer() error = %v", err)
	}
	if _, err := client.EnsureAPIServerLoadBalancer(ctx, cloud.LoadBalancerInput{
		Name:      "other",
		Tags:      otherTags,
		NetworkID: "network-id",
		Port:      6443,
		Targets: []cloud.LoadBalancerTargetInput{{
			Name: "target",
			IP:   "10.0.0.11",
		}},
	}); err != nil {
		t.Fatalf("EnsureAPIServerLoadBalancer() error = %v", err)
	}

	if err := cloud.CleanupByTags(ctx, client, tags); err != nil {
		t.Fatalf("CleanupByTags() error = %v", err)
	}

	matchedServers, err := client.ListServersByTags(ctx, tags)
	if err != nil {
		t.Fatalf("ListServersByTags() error = %v", err)
	}
	if len(matchedServers) != 0 {
		t.Fatalf("matched server count = %d, want 0", len(matchedServers))
	}
	otherServers, err := client.ListServersByTags(ctx, otherTags)
	if err != nil {
		t.Fatalf("ListServersByTags(other) error = %v", err)
	}
	if len(otherServers) != 1 {
		t.Fatalf("other server count = %d, want 1", len(otherServers))
	}
	matchedLoadBalancers, err := client.ListAPIServerLoadBalancersByTags(ctx, tags)
	if err != nil {
		t.Fatalf("ListAPIServerLoadBalancersByTags() error = %v", err)
	}
	if len(matchedLoadBalancers) != 0 {
		t.Fatalf("matched load balancer count = %d, want 0", len(matchedLoadBalancers))
	}
}
