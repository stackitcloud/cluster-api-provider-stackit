/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package bastion_test

import (
	"context"
	"errors"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	infrav1 "github.com/stackitcloud/cluster-api-provider-stackit/api/v1alpha1"
	"github.com/stackitcloud/cluster-api-provider-stackit/cloud"
	cloudfake "github.com/stackitcloud/cluster-api-provider-stackit/cloud/fake"
	"github.com/stackitcloud/cluster-api-provider-stackit/cloud/services/bastion"
	"github.com/stackitcloud/cluster-api-provider-stackit/cloud/services/network"
	"github.com/stackitcloud/cluster-api-provider-stackit/cloud/services/publicip"
	"github.com/stackitcloud/cluster-api-provider-stackit/cloud/services/securitygroup"
	"github.com/stackitcloud/cluster-api-provider-stackit/cloud/services/server"
	"github.com/stackitcloud/cluster-api-provider-stackit/cloud/services/shared"
)

type testScope struct {
	cloud.Client
	shared.NoOpReconcileScope
	enabled         bool
	cloudInit       []byte
	cloudInitErr    error
	cloudInitHash   string
	serverID        string
	publicIPID      string
	publicIP        string
	securityGroupID string
	networkID       string
	conditionReason string
	conditionStatus metav1.ConditionStatus
	notReadyReason  string
	savedCloudHash  string
	existingCond    *metav1.Condition
}

func (s *testScope) BastionEnabled() bool {
	return s.enabled
}

func (s *testScope) ResolveBastionCloudInit(_ context.Context) ([]byte, error) {
	return s.cloudInit, s.cloudInitErr
}

func (s *testScope) SetBastionCloudInit(cloudInit []byte, cloudInitHash string) {
	s.cloudInit = cloudInit
	s.savedCloudHash = cloudInitHash
}

func (s *testScope) BastionServerID() string {
	return s.serverID
}

func (s *testScope) BastionCloudInitHash() string {
	return s.cloudInitHash
}

func (s *testScope) BastionPublicIP() string {
	return s.publicIP
}

func (s *testScope) HasBastionStatus() bool {
	return s.serverID != "" || s.publicIPID != "" || s.securityGroupID != ""
}

func (s *testScope) ServerSpec() server.ServerSpec {
	return server.ServerSpec{
		ID:          s.serverID,
		Name:        "cluster-bastion",
		MachineType: "t1.1",
		ImageID:     "img-1",
		NetworkID:   s.networkID,
		Tags:        map[string]string{"cluster": "c1", "role": "bastion"},
		UserData:    s.cloudInit,
	}
}

func (s *testScope) SetServer(srv *server.Server, created bool) {
	if srv == nil {
		s.serverID = ""
		return
	}
	s.serverID = srv.ID
	if created || s.cloudInitHash == "" {
		s.cloudInitHash = s.savedCloudHash
	}
}

func (s *testScope) SecurityGroupSpecs() []securitygroup.SecurityGroupSpec {
	var serverIDs []string
	if s.serverID != "" {
		serverIDs = []string{s.serverID}
	}
	return []securitygroup.SecurityGroupSpec{
		{
			ID:        s.securityGroupID,
			Name:      "cluster-bastion-sg",
			Tags:      map[string]string{"cluster": "c1", "role": "bastion"},
			ServerIDs: serverIDs,
			Rules: []securitygroup.Rule{
				{
					Direction: securitygroup.DirectionIngress,
					Protocol:  securitygroup.ProtocolTCP,
					PortMin:   22,
					PortMax:   22,
					IPRange:   "0.0.0.0/0",
				},
			},
		},
	}
}

func (s *testScope) SetSecurityGroup(_ string, sg *securitygroup.SecurityGroup) {
	if sg == nil {
		s.securityGroupID = ""
		return
	}
	s.securityGroupID = sg.ID
}

func (s *testScope) PublicIPSpec() publicip.PublicIPSpec {
	return publicip.PublicIPSpec{
		ID:       s.publicIPID,
		ServerID: s.serverID,
		Tags:     map[string]string{"cluster": "c1", "role": "bastion"},
	}
}

func (s *testScope) SetPublicIP(pip *publicip.PublicIP) {
	if pip == nil {
		s.publicIPID = ""
		s.publicIP = ""
		return
	}
	s.publicIPID = pip.ID
	s.publicIP = pip.IP
}

func (s *testScope) FindCondition(conditionType string) *metav1.Condition {
	if conditionType == infrav1.ClusterBastionReadyCondition {
		return s.existingCond
	}
	return nil
}

func (s *testScope) SetConditions(status metav1.ConditionStatus, reason, _ string, _ ...string) {
	s.conditionStatus = status
	s.conditionReason = reason
}

func (s *testScope) SetNotReady(reason, _ string, _ ...string) {
	s.notReadyReason = reason
}

func TestReconcile(t *testing.T) {
	ctx := context.Background()

	t.Run("marks condition Skipped when bastion is disabled", func(t *testing.T) {
		client := cloudfake.New()
		scope := &testScope{
			Client:  client,
			enabled: false,
		}

		res, err := bastion.New(scope).Reconcile(ctx)
		if err != nil {
			t.Fatalf("Reconcile() error = %v", err)
		}
		if !res.IsZero() {
			t.Fatalf("Reconcile() result = %#v, want zero", res)
		}
		if scope.conditionReason != bastion.DisabledReason {
			t.Fatalf("conditionReason = %q, want %q", scope.conditionReason, bastion.DisabledReason)
		}
	})

	t.Run("provisions server, security group, and public IP when enabled", func(t *testing.T) {
		client := cloudfake.New(network.Network{
			ID:   "net-1",
			Name: "net-1",
		})

		scope := &testScope{
			Client:    client,
			enabled:   true,
			networkID: "net-1",
			cloudInit: []byte("#cloud-config"),
		}

		res, err := bastion.New(scope).Reconcile(ctx)
		if err != nil {
			t.Fatalf("Reconcile() error = %v", err)
		}
		if !res.IsZero() {
			t.Fatalf("Reconcile() result = %#v, want zero", res)
		}
		if scope.serverID == "" || scope.securityGroupID == "" || scope.publicIP == "" {
			t.Fatalf("expected serverID, securityGroupID, and publicIP to be set, got %q / %q / %q",
				scope.serverID, scope.securityGroupID, scope.publicIP)
		}
		if scope.conditionStatus != metav1.ConditionTrue || scope.conditionReason != "Available" {
			t.Fatalf("condition = (%s, %s), want (True, Available)", scope.conditionStatus, scope.conditionReason)
		}
	})

	t.Run("sets NotReady when cloudInit resolution fails", func(t *testing.T) {
		client := cloudfake.New()
		scope := &testScope{
			Client:       client,
			enabled:      true,
			cloudInitErr: errors.New("configmap not found"),
		}

		res, err := bastion.New(scope).Reconcile(ctx)
		if err != nil {
			t.Fatalf("Reconcile() error = %v, want nil", err)
		}
		if !res.IsZero() {
			t.Fatalf("Reconcile() result = %#v, want zero", res)
		}
		if scope.notReadyReason != "CloudInitRefError" {
			t.Fatalf("notReadyReason = %q, want CloudInitRefError", scope.notReadyReason)
		}
	})
}
