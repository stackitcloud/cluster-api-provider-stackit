/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package scope

import (
	"reflect"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"

	infrav1 "github.com/stackitcloud/cluster-api-provider-stackit/api/v1alpha1"
	cloudfake "github.com/stackitcloud/cluster-api-provider-stackit/cloud/fake"
	"github.com/stackitcloud/cluster-api-provider-stackit/cloud/services/loadbalancer"
	"github.com/stackitcloud/cluster-api-provider-stackit/cloud/services/network"
	"github.com/stackitcloud/cluster-api-provider-stackit/cloud/services/server"
	"github.com/stackitcloud/cluster-api-provider-stackit/util"
)

func TestDefaultAvailabilityZone(t *testing.T) {
	tests := []struct {
		name           string
		region         string
		failureDomains []clusterv1.FailureDomain
		wantAZ         string
	}{
		{
			name:   "selects first failure domain sorted alphabetically",
			region: "eu01",
			failureDomains: []clusterv1.FailureDomain{
				{Name: "eu01-3"},
				{Name: "eu01-2"},
				{Name: "eu01-1"},
			},
			wantAZ: "eu01-1",
		},
		{
			name:           "falls back to region-1 when failureDomains is empty",
			region:         "eu02",
			failureDomains: nil,
			wantAZ:         "eu02-1",
		},
		{
			name:           "returns empty when region and failureDomains are empty",
			region:         "",
			failureDomains: nil,
			wantAZ:         "",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cluster := &infrav1.StackitCluster{
				Spec: infrav1.StackitClusterSpec{
					Region: test.region,
				},
				Status: infrav1.StackitClusterStatus{
					FailureDomains: test.failureDomains,
				},
			}
			clusterScope := &ClusterScope{StackitCluster: cluster}
			if got := clusterScope.ServerSpec().AvailabilityZone; got != test.wantAZ {
				t.Fatalf("ServerSpec().AvailabilityZone = %q, want %q", got, test.wantAZ)
			}
		})
	}
}

func TestAvailabilityZoneSpecUsesResolvedRegion(t *testing.T) {
	clusterScope := &ClusterScope{
		StackitCluster: &infrav1.StackitCluster{},
		cloudClient:    cloudfake.New(),
	}

	clusterScope.AvailabilityZoneSpec().SetZones([]string{"eu01-1"})

	if len(clusterScope.StackitCluster.Status.FailureDomains) != 1 {
		t.Fatalf("FailureDomains = %#v, want 1 entry", clusterScope.StackitCluster.Status.FailureDomains)
	}
	gotRegion := clusterScope.StackitCluster.Status.FailureDomains[0].Attributes["region"]
	if gotRegion != "eu01" {
		t.Fatalf("FailureDomains[0].Attributes[region] = %q, want %q", gotRegion, "eu01")
	}
}

func TestServerSpecSetServerPreservesCloudInitHashOnAdoptedServer(t *testing.T) {
	clusterScope := &ClusterScope{
		StackitCluster: &infrav1.StackitCluster{},
	}
	clusterScope.SetBastionCloudInit([]byte("#cloud-config\npackages:\n- jq\n"), "new-hash")

	spec := clusterScope.ServerSpec()
	spec.SetServer(&server.Server{ID: "srv-adopted", State: "ACTIVE", PowerStatus: "RUNNING"}, false)

	if clusterScope.BastionServerCreated() {
		t.Fatal("BastionServerCreated() = true on adopted server, want false")
	}
	if got := clusterScope.StackitCluster.Status.Bastion.CloudInitHash; got != "" {
		t.Fatalf("Status.Bastion.CloudInitHash on adopted server = %q, want empty", got)
	}

	spec.SetServer(&server.Server{ID: "srv-created", State: "ACTIVE", PowerStatus: "RUNNING"}, true)
	if !clusterScope.BastionServerCreated() {
		t.Fatal("BastionServerCreated() = false on created server, want true")
	}
	if got := clusterScope.StackitCluster.Status.Bastion.CloudInitHash; got != "new-hash" {
		t.Fatalf("Status.Bastion.CloudInitHash on created server = %q, want %q", got, "new-hash")
	}
}

func TestNodeSSHSecurityGroupSpecIncludesAdditionalLabelsInServerSelector(t *testing.T) {
	clusterScope := &ClusterScope{
		StackitCluster: &infrav1.StackitCluster{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "my-cluster",
				Namespace: "my-ns",
			},
			Spec: infrav1.StackitClusterSpec{
				AdditionalLabels: map[string]string{
					"env": "staging",
				},
			},
		},
	}

	specs := clusterScope.SecurityGroupSpecs()
	if len(specs) != 2 {
		t.Fatalf("SecurityGroupSpecs() count = %d, want 2", len(specs))
	}

	wantSelector := util.ClusterTags("my-cluster", "my-ns", map[string]string{"env": "staging"})
	if got := specs[1].ServerSelector; !reflect.DeepEqual(got, wantSelector) {
		t.Fatalf("nodeSSHSecurityGroupSpec ServerSelector = %#v, want %#v", got, wantSelector)
	}
}

func TestClusterScopeSecurityGroupSpecsIncludesEmptyIngressRuleWhenAllowedCIDRsMissing(t *testing.T) {
	clusterScope := &ClusterScope{
		StackitCluster: &infrav1.StackitCluster{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "my-cluster",
				Namespace: "my-ns",
			},
		},
	}

	specs := clusterScope.SecurityGroupSpecs()
	if len(specs) != 2 {
		t.Fatalf("SecurityGroupSpecs() count = %d, want 2", len(specs))
	}
	if len(specs[0].Rules) != 2 {
		t.Fatalf("bastion SecurityGroupSpec Rules count = %d, want 2 (1 ingress + 1 egress)", len(specs[0].Rules))
	}
	if specs[0].Rules[0].Direction != "ingress" || specs[0].Rules[0].IPRange != "" {
		t.Fatalf("bastion SecurityGroupSpec Rules[0] = %#v, want ingress rule with empty IPRange", specs[0].Rules[0])
	}
}

func TestMachineScopeSecurityGroupSpecsRespectsBastionEnabled(t *testing.T) {
	machineScope := &MachineScope{
		StackitCluster: &infrav1.StackitCluster{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "my-cluster",
				Namespace: "my-ns",
			},
			Spec: infrav1.StackitClusterSpec{
				Bastion: infrav1.StackitBastionSpec{
					Enabled: false,
				},
			},
			Status: infrav1.StackitClusterStatus{
				Bastion: infrav1.StackitBastionStatus{
					SecurityGroupID: "bastion-sg-id",
				},
			},
		},
		StackitMachine: &infrav1.StackitMachine{
			Status: infrav1.StackitMachineStatus{
				InstanceID: "srv-1",
			},
		},
	}

	if got := machineScope.SecurityGroupSpecs(); len(got) != 0 {
		t.Fatalf("SecurityGroupSpecs() when bastion disabled = %#v, want empty", got)
	}

	machineScope.StackitCluster.Spec.Bastion.Enabled = true
	got := machineScope.SecurityGroupSpecs()
	if len(got) != 1 {
		t.Fatalf("SecurityGroupSpecs() when bastion enabled count = %d, want 1", len(got))
	}
	if !reflect.DeepEqual(got[0].ServerIDs, []string{"srv-1"}) {
		t.Fatalf("SecurityGroupSpecs()[0].ServerIDs = %#v, want [srv-1]", got[0].ServerIDs)
	}
	if len(got[0].Rules) == 0 || got[0].Rules[0].RemoteSecurityGroupID != "bastion-sg-id" {
		t.Fatalf("SecurityGroupSpecs()[0].Rules = %#v, want RemoteSecurityGroupID=bastion-sg-id", got[0].Rules)
	}
}

func TestBootstrapTargetIP(t *testing.T) {
	clusterScope := &ClusterScope{
		StackitCluster: &infrav1.StackitCluster{},
	}
	if got := clusterScope.BootstrapTargetIP(); got != loadbalancer.DefaultBootstrapTargetIP {
		t.Fatalf("BootstrapTargetIP() with nil network = %q, want %q", got, loadbalancer.DefaultBootstrapTargetIP)
	}

	clusterScope.NetworkSpec().SetNetwork(&network.Network{
		ID:           "net-1",
		IPv4Prefixes: []string{"192.168.10.0/24"},
	})
	if got := clusterScope.BootstrapTargetIP(); got != "192.168.10.10" {
		t.Fatalf("BootstrapTargetIP() with /24 network = %q, want %q", got, "192.168.10.10")
	}

	clusterScope.NetworkSpec().SetNetwork(&network.Network{
		ID:           "net-2",
		IPv4Prefixes: []string{"invalid"},
	})
	if got := clusterScope.BootstrapTargetIP(); got != loadbalancer.DefaultBootstrapTargetIP {
		t.Fatalf("BootstrapTargetIP() with invalid prefix = %q, want %q", got, loadbalancer.DefaultBootstrapTargetIP)
	}
}

func TestHasBastionStatus(t *testing.T) {
	clusterScope := &ClusterScope{
		StackitCluster: &infrav1.StackitCluster{},
	}
	if clusterScope.HasBastionStatus() {
		t.Fatal("HasBastionStatus() = true on empty status, want false")
	}

	clusterScope.StackitCluster.Status.Bastion.ServerID = "srv-1"
	if !clusterScope.HasBastionStatus() {
		t.Fatal("HasBastionStatus() = false when ServerID is set, want true")
	}
}
