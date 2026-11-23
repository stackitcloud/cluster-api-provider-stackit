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
	"fmt"
	"maps"
	"testing"
	"time"

	ctrl "sigs.k8s.io/controller-runtime"

	"github.com/stackitcloud/cluster-api-provider-stackit/cloud"
	cloudfake "github.com/stackitcloud/cluster-api-provider-stackit/cloud/fake"
	"github.com/stackitcloud/cluster-api-provider-stackit/cloud/services/loadbalancer"
	"github.com/stackitcloud/cluster-api-provider-stackit/cloud/services/network"
	"github.com/stackitcloud/cluster-api-provider-stackit/cloud/services/publicip"
	"github.com/stackitcloud/cluster-api-provider-stackit/cloud/services/securitygroup"
	"github.com/stackitcloud/cluster-api-provider-stackit/cloud/services/server"
	"github.com/stackitcloud/cluster-api-provider-stackit/cloud/services/shared"
	"github.com/stackitcloud/cluster-api-provider-stackit/util"
)

type testResourceScope struct {
	cloud.Client
	shared.NoOpReconcileScope
	networkSpec       network.NetworkSpec
	setNetwork        func(*network.Network)
	serverSpec        server.ServerSpec
	setServer         func(*server.Server, bool)
	publicIPSpec      publicip.PublicIPSpec
	loadBalancerSpec  loadbalancer.LoadBalancerSpec
	securityGroupSpec []securitygroup.SecurityGroupSpec
}

func (s *testResourceScope) NetworkSpec() network.NetworkSpec {
	return s.networkSpec
}

func (s *testResourceScope) SetNetwork(net *network.Network) {
	if s.setNetwork != nil {
		s.setNetwork(net)
	}
}

func (s *testResourceScope) ServerSpec() server.ServerSpec {
	return s.serverSpec
}

func (s *testResourceScope) SetServer(srv *server.Server, created bool) {
	if s.setServer != nil {
		s.setServer(srv, created)
	}
}

func (s *testResourceScope) PublicIPSpec() publicip.PublicIPSpec {
	return s.publicIPSpec
}

func (s *testResourceScope) SetPublicIP(_ *publicip.PublicIP) {}

func (s *testResourceScope) APIServerLoadBalancerEnabled() bool {
	return true
}

func (s *testResourceScope) ControlPlaneEndpointHost() string {
	return ""
}

func (s *testResourceScope) UseExternalControlPlaneEndpoint() {}

func (s *testResourceScope) APIServerEndpointHost() string {
	return ""
}

func (s *testResourceScope) ResolveLoadBalancerSpec(_ context.Context) (loadbalancer.LoadBalancerSpec, error) {
	return s.loadBalancerSpec, nil
}

func (s *testResourceScope) LoadBalancerSpec() loadbalancer.LoadBalancerSpec {
	return s.loadBalancerSpec
}

func (s *testResourceScope) SetLoadBalancer(_ *loadbalancer.LoadBalancer) {}

func (s *testResourceScope) TargetPoolFailureResult(err error, _ time.Duration) (ctrl.Result, error) {
	return ctrl.Result{}, err
}

func (s *testResourceScope) SecurityGroupSpecs() []securitygroup.SecurityGroupSpec {
	return s.securityGroupSpec
}

func (s *testResourceScope) SetSecurityGroup(_ string, _ *securitygroup.SecurityGroup) {}

func createTestResources(t *testing.T, client cloud.Client, prefix string, tags map[string]string) {
	t.Helper()
	ctx := context.Background()

	var networkID string
	if _, err := network.New(&testResourceScope{
		Client: client,
		networkSpec: network.NetworkSpec{
			Name:       prefix + "-net",
			IPv4Prefix: network.DefaultNetworkPrefix,
			Tags:       tags,
			Managed:    true,
		},
		setNetwork: func(net *network.Network) {
			networkID = net.ID
		},
	}).Reconcile(ctx); err != nil {
		t.Fatalf("network.Reconcile() error = %v", err)
	}

	var workerServerID, bastionServerID string
	if _, err := server.New(&testResourceScope{
		Client: client,
		serverSpec: server.ServerSpec{
			Name:        prefix + "-worker",
			ImageID:     "22222222-2222-4222-8222-222222222222",
			MachineType: "c2i.2",
			NetworkID:   networkID,
			Tags:        tags,
		},
		setServer: func(srv *server.Server, _ bool) {
			workerServerID = srv.ID
		},
	}).Reconcile(ctx); err != nil {
		t.Fatalf("server.Reconcile(worker) error = %v", err)
	}

	bastionTags := make(map[string]string, len(tags)+1)
	maps.Copy(bastionTags, tags)
	bastionTags[util.LabelResourceRole] = util.ResourceRoleBastion
	if _, err := server.New(&testResourceScope{
		Client: client,
		serverSpec: server.ServerSpec{
			Name:        prefix + "-bastion",
			ImageID:     "22222222-2222-4222-8222-222222222222",
			MachineType: "c2i.2",
			NetworkID:   networkID,
			Tags:        bastionTags,
		},
		setServer: func(srv *server.Server, _ bool) {
			bastionServerID = srv.ID
		},
	}).Reconcile(ctx); err != nil {
		t.Fatalf("server.Reconcile(bastion) error = %v", err)
	}

	if _, err := publicip.New(&testResourceScope{
		Client: client,
		publicIPSpec: publicip.PublicIPSpec{
			Tags:     bastionTags,
			ServerID: bastionServerID,
		},
	}).Reconcile(ctx); err != nil {
		t.Fatalf("publicip.Reconcile() error = %v", err)
	}

	nodeSSHTags := make(map[string]string, len(tags)+1)
	maps.Copy(nodeSSHTags, tags)
	nodeSSHTags[util.LabelResourceRole] = util.ResourceRoleNodeSSH
	if _, err := securitygroup.New(&testResourceScope{
		Client: client,
		securityGroupSpec: []securitygroup.SecurityGroupSpec{
			{
				Name:      prefix + "-bastion-sg",
				Tags:      bastionTags,
				ServerIDs: []string{bastionServerID},
			},
			{
				Name:      prefix + "-node-ssh-sg",
				Tags:      nodeSSHTags,
				ServerIDs: []string{workerServerID},
				Rules: []securitygroup.Rule{
					{
						Direction:               securitygroup.DirectionIngress,
						Protocol:                securitygroup.ProtocolTCP,
						PortMin:                 22,
						PortMax:                 22,
						RemoteSecurityGroupTags: bastionTags,
					},
				},
			},
		},
	}).Reconcile(ctx); err != nil {
		t.Fatalf("securitygroup.Reconcile() error = %v", err)
	}

	if _, err := loadbalancer.New(&testResourceScope{
		Client: client,
		loadBalancerSpec: loadbalancer.LoadBalancerSpec{
			Name:      prefix + "-apiserver",
			NetworkID: networkID,
			Port:      loadbalancer.DefaultAPIServerPort,
			Tags:      tags,
		},
	}).Reconcile(ctx); err != nil {
		t.Fatalf("loadbalancer.Reconcile() error = %v", err)
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

	createTestResources(t, client, "matched", tags)
	createTestResources(t, client, "other", otherTags)

	if err := cloud.CleanupByTags(ctx, client, tags); err != nil {
		t.Fatalf("CleanupByTags() error = %v", err)
	}

	if got := client.ServerCount(); got != 2 {
		t.Fatalf("remaining server count = %d, want 2", got)
	}
	if got := client.LoadBalancerCount(); got != 1 {
		t.Fatalf("remaining load balancer count = %d, want 1", got)
	}
	if got := client.PublicIPCount(); got != 1 {
		t.Fatalf("remaining public IP count = %d, want 1", got)
	}
	if got := client.SecurityGroupCount(); got != 2 {
		t.Fatalf("remaining security group count = %d, want 2", got)
	}
	if sg := client.FindSecurityGroupByTags(tags); sg != nil {
		t.Fatalf("unexpected remaining matched security group: %+v", sg)
	}
	if sg := client.FindSecurityGroupByTags(otherTags); sg == nil {
		t.Fatal("expected other security group to remain")
	}
	if got := client.NetworkCount(); got != 1 {
		t.Fatalf("remaining network count = %d, want 1", got)
	}
	if net := client.FindNetworkByTags(tags); net != nil {
		t.Fatalf("unexpected remaining matched network: %+v", net)
	}
	if net := client.FindNetworkByTags(otherTags); net == nil {
		t.Fatal("expected other network to remain")
	}
}

func TestCleanupByTagsContinuesWhenIndividualDeletesFail(t *testing.T) {
	ctx := context.Background()
	client := cloudfake.New()
	tags := map[string]string{
		"cluster-api-provider-stackit/e2e":     "true",
		"cluster-api-provider-stackit/test-id": "test-partial-fail",
	}

	createTestResources(t, client, "matched", tags)

	// Inject a transient failure on the first server delete; the remaining
	// server, load balancer, public IP, unattached security group, and network
	// should still be cleaned up in the same pass.
	client.FailNextDeleteServer = fmt.Errorf("injected server delete failure")

	if err := cloud.CleanupByTags(ctx, client, tags); err == nil {
		t.Fatal("CleanupByTags() error = nil, want non-nil when server delete fails")
	}

	if got := client.ServerCount(); got != 1 {
		t.Fatalf("remaining server count = %d, want 1 (the one that failed to delete)", got)
	}
	if got := client.LoadBalancerCount(); got != 0 {
		t.Fatalf("remaining load balancer count = %d, want 0", got)
	}
	if got := client.PublicIPCount(); got != 0 {
		t.Fatalf("remaining public IP count = %d, want 0", got)
	}
	if got := client.SecurityGroupCount(); got != 0 {
		t.Fatalf("remaining security group count = %d, want 0", got)
	}
	if got := client.NetworkCount(); got != 0 {
		t.Fatalf("remaining network count = %d, want 0", got)
	}
}
