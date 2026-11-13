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
