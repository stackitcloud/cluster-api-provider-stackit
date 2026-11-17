/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package scope

import (
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	ctrl "sigs.k8s.io/controller-runtime"

	infrav1 "github.com/stackitcloud/cluster-api-provider-stackit/api/v1alpha1"
	cloudfake "github.com/stackitcloud/cluster-api-provider-stackit/cloud/fake"
	"github.com/stackitcloud/cluster-api-provider-stackit/cloud/services/loadbalancer"
	"github.com/stackitcloud/cluster-api-provider-stackit/cloud/services/network"
	"github.com/stackitcloud/cluster-api-provider-stackit/cloud/services/securitygroup"
	"github.com/stackitcloud/cluster-api-provider-stackit/cloud/services/server"
	"github.com/stackitcloud/cluster-api-provider-stackit/cloud/services/shared"
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

	spec.SetServer(nil, false)
	if clusterScope.BastionServerCreated() {
		t.Fatal("BastionServerCreated() = true after SetServer(nil, false), want false")
	}
	if state, powerStatus := clusterScope.BastionServerState(); state != "" || powerStatus != "" {
		t.Fatalf("BastionServerState() after SetServer(nil, false) = (%q, %q), want empty", state, powerStatus)
	}
	if got := clusterScope.StackitCluster.Status.Bastion.ServerID; got != "" {
		t.Fatalf("Status.Bastion.ServerID after SetServer(nil, false) = %q, want empty", got)
	}
	if got := clusterScope.StackitCluster.Status.Bastion.CloudInitHash; got != "" {
		t.Fatalf("Status.Bastion.CloudInitHash after SetServer(nil, false) = %q, want empty", got)
	}
}

func TestLoadBalancerSpecSetIDAndSetAddressClearsOnEmpty(t *testing.T) {
	clusterScope := &ClusterScope{
		StackitCluster: &infrav1.StackitCluster{},
	}

	spec := clusterScope.LoadBalancerSpec()
	spec.SetID("lb-1")
	spec.SetAddress("203.0.113.10", 6443)

	if clusterScope.StackitCluster.Status.APIServerLoadBalancerID != "lb-1" {
		t.Fatalf("Status.APIServerLoadBalancerID = %q, want lb-1", clusterScope.StackitCluster.Status.APIServerLoadBalancerID)
	}
	wantEndpoint := clusterv1.APIEndpoint{Host: "203.0.113.10", Port: 6443}
	if clusterScope.StackitCluster.Spec.ControlPlaneEndpoint != wantEndpoint {
		t.Fatalf("Spec.ControlPlaneEndpoint = %#v, want %#v", clusterScope.StackitCluster.Spec.ControlPlaneEndpoint, wantEndpoint)
	}
	if clusterScope.StackitCluster.Status.APIServerEndpoint != wantEndpoint {
		t.Fatalf("Status.APIServerEndpoint = %#v, want %#v", clusterScope.StackitCluster.Status.APIServerEndpoint, wantEndpoint)
	}

	spec.SetID("")
	spec.SetAddress("", 0)
	if clusterScope.StackitCluster.Status.APIServerLoadBalancerID != "" {
		t.Fatalf("Status.APIServerLoadBalancerID after clear = %q, want empty", clusterScope.StackitCluster.Status.APIServerLoadBalancerID)
	}
	if clusterScope.StackitCluster.Status.APIServerEndpoint != (clusterv1.APIEndpoint{}) {
		t.Fatalf("Status.APIServerEndpoint after clear = %#v, want empty", clusterScope.StackitCluster.Status.APIServerEndpoint)
	}
	if clusterScope.StackitCluster.Spec.ControlPlaneEndpoint != wantEndpoint {
		t.Fatalf("Spec.ControlPlaneEndpoint after clear = %#v, want preserved %#v", clusterScope.StackitCluster.Spec.ControlPlaneEndpoint, wantEndpoint)
	}
}

func TestMachineScopeServerSpecSetServerClearsOnNil(t *testing.T) {
	machineScope := &MachineScope{
		cluster:        &clusterv1.Cluster{},
		Machine:        &clusterv1.Machine{},
		StackitCluster: &infrav1.StackitCluster{},
		StackitMachine: &infrav1.StackitMachine{},
	}

	spec := machineScope.ServerSpec()
	spec.SetServer(&server.Server{
		ID:               "srv-1",
		State:            "ACTIVE",
		PowerStatus:      "RUNNING",
		AvailabilityZone: "eu01-1",
		Addresses:        []server.Address{{Type: "InternalIP", Address: "10.0.0.5"}},
	}, true)

	if !machineScope.InstanceCreated() || machineScope.InstancePowerStatus() != "RUNNING" {
		t.Fatalf("InstanceCreated=%v, InstancePowerStatus=%q", machineScope.InstanceCreated(), machineScope.InstancePowerStatus())
	}
	if machineScope.StackitMachine.Status.InstanceID != "srv-1" {
		t.Fatalf("Status.InstanceID = %q, want srv-1", machineScope.StackitMachine.Status.InstanceID)
	}

	spec.SetServer(nil, false)
	if machineScope.InstanceCreated() || machineScope.InstancePowerStatus() != "" {
		t.Fatalf("after SetServer(nil, false): InstanceCreated=%v, InstancePowerStatus=%q, want false and empty", machineScope.InstanceCreated(), machineScope.InstancePowerStatus())
	}
	if machineScope.StackitMachine.Status.InstanceID != "" ||
		machineScope.StackitMachine.Status.InstanceState != "" ||
		machineScope.StackitMachine.Status.AvailabilityZone != "" ||
		len(machineScope.StackitMachine.Status.Addresses) != 0 {
		t.Fatalf("Status after SetServer(nil, false) = %#v, want cleared instance fields", machineScope.StackitMachine.Status)
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
	if specs[0].Rules[0].Direction != securitygroup.DirectionIngress || specs[0].Rules[0].IPRange != "" {
		t.Fatalf("bastion SecurityGroupSpec Rules[0] = %#v, want ingress rule with empty IPRange", specs[0].Rules[0])
	}
}

func TestServerSpecRequireSSHKey(t *testing.T) {
	clusterScope := &ClusterScope{
		StackitCluster: &infrav1.StackitCluster{
			Spec: infrav1.StackitClusterSpec{
				Bastion: infrav1.StackitBastionSpec{
					SSHKeyName: "my-key",
				},
			},
		},
	}
	clusterServerSpec := clusterScope.ServerSpec()
	if !clusterServerSpec.RequireSSHKey {
		t.Fatal("ClusterScope.ServerSpec().RequireSSHKey = false, want true")
	}
	if clusterServerSpec.SSHKeyName != "my-key" {
		t.Fatalf("ClusterScope.ServerSpec().SSHKeyName = %q, want %q", clusterServerSpec.SSHKeyName, "my-key")
	}

	machineScope := &MachineScope{
		cluster:        &clusterv1.Cluster{},
		Machine:        &clusterv1.Machine{},
		StackitCluster: &infrav1.StackitCluster{},
		StackitMachine: &infrav1.StackitMachine{},
	}
	if machineScope.ServerSpec().RequireSSHKey {
		t.Fatal("MachineScope.ServerSpec().RequireSSHKey = true, want false")
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

func TestClusterScopeCredentialFailureResult(t *testing.T) {
	requeueAfter := time.Minute

	t.Run("requeues on invalid credentials and marks cluster not ready", func(t *testing.T) {
		clusterScope := &ClusterScope{
			StackitCluster: &infrav1.StackitCluster{
				ObjectMeta: metav1.ObjectMeta{Generation: 3},
				Status:     infrav1.StackitClusterStatus{Ready: true},
			},
		}
		err := fmt.Errorf("auth failed: %w", shared.ErrUnauthorized)

		result, gotErr := clusterScope.CredentialFailureResult(
			err,
			requeueAfter,
			infrav1.ClusterCredentialsReadyCondition,
			infrav1.ClusterReadyCondition,
		)
		if gotErr != nil {
			t.Fatalf("CredentialFailureResult() error = %v, want nil", gotErr)
		}
		if result != (ctrl.Result{RequeueAfter: requeueAfter}) {
			t.Fatalf("CredentialFailureResult() result = %#v, want RequeueAfter=%v", result, requeueAfter)
		}
		if clusterScope.StackitCluster.Status.Ready {
			t.Fatal("StackitCluster.Status.Ready = true, want false")
		}
		assertConditionFalse(t, clusterScope.StackitCluster.Status.Conditions, infrav1.ClusterCredentialsReadyCondition, "CredentialsInvalid", err.Error(), 3)
		assertConditionFalse(t, clusterScope.StackitCluster.Status.Conditions, infrav1.ClusterReadyCondition, "CredentialsInvalid", err.Error(), 3)
	})

	t.Run("returns error on non-credential error", func(t *testing.T) {
		clusterScope := &ClusterScope{
			StackitCluster: &infrav1.StackitCluster{
				ObjectMeta: metav1.ObjectMeta{Generation: 2},
			},
		}
		err := errors.New("k8s api error")

		result, gotErr := clusterScope.CredentialFailureResult(err, requeueAfter, infrav1.ClusterCredentialsReadyCondition)
		if !errors.Is(gotErr, err) {
			t.Fatalf("CredentialFailureResult() error = %v, want %v", gotErr, err)
		}
		if result != (ctrl.Result{}) {
			t.Fatalf("CredentialFailureResult() result = %#v, want empty", result)
		}
	})
}

func TestClusterScopeCloudFailureResult(t *testing.T) {
	requeueAfter := 5 * time.Second

	t.Run("requeues on retryable error and marks cluster not ready", func(t *testing.T) {
		clusterScope := &ClusterScope{
			StackitCluster: &infrav1.StackitCluster{
				ObjectMeta: metav1.ObjectMeta{Generation: 4},
				Status:     infrav1.StackitClusterStatus{Ready: true},
			},
		}
		err := fmt.Errorf("temporary failure: %w", shared.ErrTransient)

		result, gotErr := clusterScope.CloudFailureResult(
			"NetworkNotFound",
			err,
			requeueAfter,
			false,
			infrav1.ClusterNetworkReadyCondition,
			infrav1.ClusterReadyCondition,
		)
		if gotErr != nil {
			t.Fatalf("CloudFailureResult() error = %v, want nil", gotErr)
		}
		if result != (ctrl.Result{RequeueAfter: requeueAfter}) {
			t.Fatalf("CloudFailureResult() result = %#v, want RequeueAfter=%v", result, requeueAfter)
		}
		if clusterScope.StackitCluster.Status.Ready {
			t.Fatal("StackitCluster.Status.Ready = true, want false")
		}
		assertConditionFalse(t, clusterScope.StackitCluster.Status.Conditions, infrav1.ClusterNetworkReadyCondition, "NetworkNotFound", err.Error(), 4)
		assertConditionFalse(t, clusterScope.StackitCluster.Status.Conditions, infrav1.ClusterReadyCondition, "NetworkNotFound", err.Error(), 4)
	})

	t.Run("returns nil on non-retryable error when returnError is false", func(t *testing.T) {
		clusterScope := &ClusterScope{
			StackitCluster: &infrav1.StackitCluster{
				ObjectMeta: metav1.ObjectMeta{Generation: 1},
			},
		}
		err := fmt.Errorf("not found: %w", shared.ErrNotFound)

		result, gotErr := clusterScope.CloudFailureResult("NetworkNotFound", err, requeueAfter, false, infrav1.ClusterReadyCondition)
		if gotErr != nil {
			t.Fatalf("CloudFailureResult() error = %v, want nil", gotErr)
		}
		if result != (ctrl.Result{}) {
			t.Fatalf("CloudFailureResult() result = %#v, want empty", result)
		}
	})

	t.Run("returns error on non-retryable error when returnError is true", func(t *testing.T) {
		clusterScope := &ClusterScope{
			StackitCluster: &infrav1.StackitCluster{
				ObjectMeta: metav1.ObjectMeta{Generation: 1},
			},
		}
		err := fmt.Errorf("invalid input: %w", shared.ErrInvalidInput)

		result, gotErr := clusterScope.CloudFailureResult("AvailabilityZonesError", err, requeueAfter, true, infrav1.ClusterReadyCondition)
		if !errors.Is(gotErr, err) {
			t.Fatalf("CloudFailureResult() error = %v, want %v", gotErr, err)
		}
		if result != (ctrl.Result{}) {
			t.Fatalf("CloudFailureResult() result = %#v, want empty", result)
		}
	})
}

func TestMachineScopeFailureResults(t *testing.T) {
	requeueAfter := 5 * time.Second

	machineScope := &MachineScope{
		StackitMachine: &infrav1.StackitMachine{
			ObjectMeta: metav1.ObjectMeta{Generation: 5},
			Status:     infrav1.StackitMachineStatus{Ready: true},
		},
	}
	credErr := fmt.Errorf("invalid secret: %w", util.ErrCredentialsInvalid)
	result, gotErr := machineScope.CredentialFailureResult(
		credErr,
		requeueAfter,
		infrav1.MachineCredentialsReadyCondition,
	)
	if gotErr != nil || result != (ctrl.Result{RequeueAfter: requeueAfter}) {
		t.Fatalf("CredentialFailureResult() = (%#v, %v), want (RequeueAfter=%v, nil)", result, gotErr, requeueAfter)
	}
	if machineScope.StackitMachine.Status.Ready {
		t.Fatal("StackitMachine.Status.Ready = true after CredentialFailureResult, want false")
	}
	assertConditionFalse(t, machineScope.StackitMachine.Status.Conditions, infrav1.MachineCredentialsReadyCondition, "CredentialsInvalid", credErr.Error(), 5)

	machineScope.SetReady()
	cloudErr := fmt.Errorf("create server rejected: %w", shared.ErrInvalidInput)
	result, gotErr = machineScope.CloudFailureResult(
		"InstanceError",
		cloudErr,
		requeueAfter,
		true,
		infrav1.MachineInstanceReadyCondition,
		infrav1.MachineReadyCondition,
	)
	if !errors.Is(gotErr, cloudErr) || result != (ctrl.Result{}) {
		t.Fatalf("CloudFailureResult() = (%#v, %v), want (empty, %v)", result, gotErr, cloudErr)
	}
	if machineScope.StackitMachine.Status.Ready {
		t.Fatal("StackitMachine.Status.Ready = true after CloudFailureResult, want false")
	}
	assertConditionFalse(t, machineScope.StackitMachine.Status.Conditions, infrav1.MachineInstanceReadyCondition, "InstanceError", cloudErr.Error(), 5)
	assertConditionFalse(t, machineScope.StackitMachine.Status.Conditions, infrav1.MachineReadyCondition, "InstanceError", cloudErr.Error(), 5)
}

func TestScopeSetPausedCondition(t *testing.T) {
	clusterScope := &ClusterScope{
		StackitCluster: &infrav1.StackitCluster{
			ObjectMeta: metav1.ObjectMeta{Generation: 2},
		},
	}

	clusterScope.SetPausedCondition(true, "Cluster spec.paused is set to true")
	assertCondition(t, clusterScope.StackitCluster.Status.Conditions, clusterv1.PausedCondition, metav1.ConditionTrue, clusterv1.PausedReason, "Cluster spec.paused is set to true", 2)

	clusterScope.SetPausedCondition(false, "")
	assertCondition(t, clusterScope.StackitCluster.Status.Conditions, clusterv1.PausedCondition, metav1.ConditionFalse, clusterv1.NotPausedReason, "", 2)

	machineScope := &MachineScope{
		StackitMachine: &infrav1.StackitMachine{
			ObjectMeta: metav1.ObjectMeta{Generation: 6},
		},
	}

	machineScope.SetPausedCondition(true, "object has the cluster.x-k8s.io/paused annotation")
	assertCondition(t, machineScope.StackitMachine.Status.Conditions, clusterv1.PausedCondition, metav1.ConditionTrue, clusterv1.PausedReason, "object has the cluster.x-k8s.io/paused annotation", 6)

	machineScope.SetPausedCondition(false, "")
	assertCondition(t, machineScope.StackitMachine.Status.Conditions, clusterv1.PausedCondition, metav1.ConditionFalse, clusterv1.NotPausedReason, "", 6)
}

func assertConditionFalse(t *testing.T, conditions []metav1.Condition, condType, wantReason, wantMessage string, wantGeneration int64) {
	t.Helper()
	assertCondition(t, conditions, condType, metav1.ConditionFalse, wantReason, wantMessage, wantGeneration)
}

func assertCondition(t *testing.T, conditions []metav1.Condition, condType string, wantStatus metav1.ConditionStatus, wantReason, wantMessage string, wantGeneration int64) {
	t.Helper()
	cond := meta.FindStatusCondition(conditions, condType)
	if cond == nil {
		t.Fatalf("condition %q not found", condType)
	}
	if cond.Status != wantStatus {
		t.Fatalf("condition %q status = %q, want %q", condType, cond.Status, wantStatus)
	}
	if cond.Reason != wantReason {
		t.Fatalf("condition %q reason = %q, want %q", condType, cond.Reason, wantReason)
	}
	if cond.Message != wantMessage {
		t.Fatalf("condition %q message = %q, want %q", condType, cond.Message, wantMessage)
	}
	if cond.ObservedGeneration != wantGeneration {
		t.Fatalf("condition %q observedGeneration = %d, want %d", condType, cond.ObservedGeneration, wantGeneration)
	}
}
