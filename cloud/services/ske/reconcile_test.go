/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package ske

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"

	"github.com/stackitcloud/stackit-sdk-go/core/config"
	"github.com/stackitcloud/stackit-sdk-go/core/oapierror"
	skev2 "github.com/stackitcloud/stackit-sdk-go/services/ske/v2api"

	"github.com/stackitcloud/cluster-api-provider-stackit/cloud"
	"github.com/stackitcloud/cluster-api-provider-stackit/util"
)

type fakeSKEAPI struct {
	skev2.DefaultAPI

	getClusterFn     func(r skev2.ApiGetClusterRequest) (*skev2.Cluster, error)
	createOrUpdateFn func(r skev2.ApiCreateOrUpdateClusterRequest) (*skev2.Cluster, error)
	deleteClusterFn  func(r skev2.ApiDeleteClusterRequest) (map[string]any, error)

	getCalls      int
	createCalls   int
	deleteCalls   int
	lastProjectID string
	lastRegion    string
	lastCluster   string
}

func (f *fakeSKEAPI) GetCluster(_ context.Context, projectID, region, clusterName string) skev2.ApiGetClusterRequest {
	f.lastProjectID = projectID
	f.lastRegion = region
	f.lastCluster = clusterName
	return skev2.ApiGetClusterRequest{
		ApiService: f,
	}
}

func (f *fakeSKEAPI) GetClusterExecute(r skev2.ApiGetClusterRequest) (*skev2.Cluster, error) {
	f.getCalls++
	if f.getClusterFn != nil {
		return f.getClusterFn(r)
	}
	return nil, &oapierror.GenericOpenAPIError{StatusCode: http.StatusNotFound, ErrorMessage: "404 Not Found"}
}

func (f *fakeSKEAPI) CreateOrUpdateCluster(_ context.Context, projectID, region, clusterName string) skev2.ApiCreateOrUpdateClusterRequest {
	f.lastProjectID = projectID
	f.lastRegion = region
	f.lastCluster = clusterName
	return skev2.ApiCreateOrUpdateClusterRequest{
		ApiService: f,
	}
}

func (f *fakeSKEAPI) CreateOrUpdateClusterExecute(r skev2.ApiCreateOrUpdateClusterRequest) (*skev2.Cluster, error) {
	f.createCalls++
	if f.createOrUpdateFn != nil {
		return f.createOrUpdateFn(r)
	}
	return &skev2.Cluster{Name: new(f.lastCluster)}, nil
}

func (f *fakeSKEAPI) DeleteCluster(_ context.Context, projectID, region, clusterName string) skev2.ApiDeleteClusterRequest {
	f.lastProjectID = projectID
	f.lastRegion = region
	f.lastCluster = clusterName
	return skev2.ApiDeleteClusterRequest{
		ApiService: f,
	}
}

func (f *fakeSKEAPI) DeleteClusterExecute(r skev2.ApiDeleteClusterRequest) (map[string]any, error) {
	f.deleteCalls++
	if f.deleteClusterFn != nil {
		return f.deleteClusterFn(r)
	}
	return map[string]any{}, nil
}

type recordedNodePoolStatus struct {
	ready       bool
	provisioned *bool
	reason      string
	message     string
}

type fakeScope struct {
	name              string
	namespace         string
	uid               string
	projectID         string
	region            string
	clusterName       string
	kubernetesVersion string
	networkID         string
	additionalLabels  map[string]string
	skeClient         skev2.DefaultAPI
	nodePools         []ManagedNodePoolState

	ready            bool
	provisioned      *bool
	reason           string
	message          string
	nodePoolStatuses map[string]recordedNodePoolStatus
}

func newFakeScope(skeClient skev2.DefaultAPI) *fakeScope {
	return &fakeScope{
		name:              "capi-mc",
		namespace:         "default",
		uid:               "uid-capi-mc-1",
		projectID:         "11111111-1111-1111-1111-111111111111",
		region:            "eu01",
		clusterName:       "ske-poc",
		kubernetesVersion: "1.35.3",
		networkID:         "22222222-2222-2222-2222-222222222222",
		additionalLabels: map[string]string{
			"env":                "poc",
			util.LabelClusterUID: "user-attempted-override",
		},
		skeClient:        skeClient,
		nodePoolStatuses: make(map[string]recordedNodePoolStatus),
	}
}

func (s *fakeScope) ProjectID() string         { return s.projectID }
func (s *fakeScope) Region() string            { return s.region }
func (s *fakeScope) ClusterName() string       { return s.clusterName }
func (s *fakeScope) KubernetesVersion() string { return s.kubernetesVersion }
func (s *fakeScope) NetworkID() string         { return s.networkID }
func (s *fakeScope) Labels() map[string]string {
	return util.ManagedClusterTags(s.name, s.namespace, s.uid, s.additionalLabels)
}
func (s *fakeScope) OwnershipLabels() map[string]string {
	return util.ManagedClusterTags(s.name, s.namespace, s.uid, nil)
}
func (s *fakeScope) SKEClient() skev2.DefaultAPI              { return s.skeClient }
func (s *fakeScope) ManagedNodePools() []ManagedNodePoolState { return s.nodePools }
func (s *fakeScope) SetReady() {
	s.ready = true
	s.provisioned = new(true)
	s.reason = ReasonAvailable
	s.message = ""
}
func (s *fakeScope) SetNotReady(reason, message string) {
	s.ready = false
	s.reason = reason
	s.message = message
}
func (s *fakeScope) SetNodePoolReady(resourceName string) {
	prev := s.nodePoolStatuses[resourceName]
	prev.ready = true
	prev.provisioned = new(true)
	prev.reason = ReasonAvailable
	prev.message = ""
	s.nodePoolStatuses[resourceName] = prev
}
func (s *fakeScope) SetNodePoolNotReady(resourceName, reason, message string) {
	prev := s.nodePoolStatuses[resourceName]
	prev.ready = false
	prev.reason = reason
	prev.message = message
	s.nodePoolStatuses[resourceName] = prev
}

func defaultBootstrapSDKPool() skev2.Nodepool {
	return ManagedNodePoolDesiredState{
		Name:                  BootstrapNodePoolName,
		AvailabilityZones:     []string{"eu01-1"},
		MachineType:           defaultNodePoolMachineType,
		ImageName:             defaultNodePoolImageName,
		ImageVersion:          defaultNodePoolImageVersion,
		Minimum:               defaultNodePoolMinimum,
		Maximum:               defaultNodePoolMaximum,
		VolumeSize:            defaultNodePoolVolumeSize,
		VolumeType:            defaultNodePoolVolumeType,
		AllowSystemComponents: new(true),
	}.ToSKESDK()
}

func clusterWithState(state skev2.ClusterStatusState, pools ...skev2.Nodepool) *skev2.Cluster {
	labels := util.ManagedClusterTags("capi-mc", "default", "uid-capi-mc-1", map[string]string{"env": "poc"})
	if len(pools) == 0 {
		pools = []skev2.Nodepool{defaultBootstrapSDKPool()}
	}
	return &skev2.Cluster{
		Name:      new("ske-poc"),
		Labels:    &labels,
		Nodepools: pools,
		Status: &skev2.ClusterStatus{
			Aggregated: &state,
		},
	}
}

func desiredPool(name string, allowSystem *bool) ManagedNodePoolDesiredState {
	return ManagedNodePoolDesiredState{
		Name:                  name,
		AvailabilityZones:     []string{"eu01-1"},
		MachineType:           "c2i.2",
		ImageName:             "flatcar",
		ImageVersion:          "4459.2.4",
		Minimum:               1,
		Maximum:               3,
		VolumeSize:            30,
		VolumeType:            "storage_premium_perf1",
		AllowSystemComponents: allowSystem,
	}
}

func newCapturedCreateSDKClient(t *testing.T, captured *skev2.CreateOrUpdateClusterPayload, getCalls, createCalls *int) skev2.DefaultAPI {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		expectedPath := "/v2/projects/11111111-1111-1111-1111-111111111111/regions/eu01/clusters/ske-poc"
		if r.URL.Path != expectedPath {
			t.Fatalf("unexpected request path = %q, want %q", r.URL.Path, expectedPath)
		}
		switch r.Method {
		case http.MethodGet:
			*getCalls++
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"message":"not found"}`))
		case http.MethodPut:
			*createCalls++
			if err := json.NewDecoder(r.Body).Decode(captured); err != nil {
				t.Fatalf("decode CreateOrUpdateClusterPayload: %v", err)
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write([]byte(`{"name":"ske-poc","kubernetes":{"version":"1.35.3"},"nodepools":[]}`))
		default:
			t.Fatalf("unexpected method %s", r.Method)
		}
	}))
	t.Cleanup(server.Close)

	client, err := skev2.NewAPIClient(
		config.WithoutAuthentication(),
		config.WithEndpoint(server.URL),
	)
	if err != nil {
		t.Fatalf("NewAPIClient() error: %v", err)
	}
	return client.DefaultAPI
}

func TestServiceReconcileCreatesClusterWhenNotFound(t *testing.T) {
	var (
		captured    skev2.CreateOrUpdateClusterPayload
		getCalls    int
		createCalls int
	)
	sdkClient := newCapturedCreateSDKClient(t, &captured, &getCalls, &createCalls)
	scope := newFakeScope(sdkClient)
	svc := New(scope)

	res, err := svc.Reconcile(context.Background())
	if err != nil {
		t.Fatalf("Reconcile() unexpected error: %v", err)
	}
	if res.RequeueAfter != time.Minute {
		t.Fatalf("Reconcile() RequeueAfter = %v, want %v", res.RequeueAfter, time.Minute)
	}
	if getCalls != 1 || createCalls != 1 {
		t.Fatalf("calls: get=%d create=%d, want get=1 create=1", getCalls, createCalls)
	}
	if scope.ready {
		t.Fatalf("scope.ready = true, want false while creating")
	}
	if scope.provisioned != nil {
		t.Fatalf("scope.provisioned = %v, want nil while creating", *scope.provisioned)
	}
	if scope.reason != "Creating" {
		t.Fatalf("scope.reason = %q, want Creating", scope.reason)
	}

	if captured.Kubernetes.Version != "1.35.3" {
		t.Fatalf("captured.Kubernetes.Version = %q, want 1.35.3", captured.Kubernetes.Version)
	}
	if captured.Network.GetId() != "22222222-2222-2222-2222-222222222222" {
		t.Fatalf("captured.Network.Id = %q, want 22222222-2222-2222-2222-222222222222", captured.Network.GetId())
	}
	wantLabels := map[string]string{
		util.LabelClusterName:      "capi-mc",
		util.LabelClusterNamespace: "default",
		util.LabelClusterUID:       "uid-capi-mc-1",
		util.LabelManagedBy:        util.ManagedByValue,
		"env":                      "poc",
	}
	if !reflect.DeepEqual(captured.GetLabels(), wantLabels) {
		t.Fatalf("captured.Labels = %#v, want %#v", captured.GetLabels(), wantLabels)
	}
	if len(captured.Nodepools) != 1 {
		t.Fatalf("len(captured.Nodepools) = %d, want 1", len(captured.Nodepools))
	}
	np := captured.Nodepools[0]
	if np.Name != BootstrapNodePoolName ||
		!reflect.DeepEqual(np.AvailabilityZones, []string{"eu01-1"}) ||
		np.Machine.Type != "c2i.2" ||
		np.Machine.Image.Name != "flatcar" ||
		np.Machine.Image.Version != "4593.2.5-containerd2.1.9" ||
		np.Minimum != 1 ||
		np.Maximum != 1 ||
		np.Volume.Size != 20 ||
		np.Volume.GetType() != "storage_premium_perf1" ||
		!np.GetAllowSystemComponents() {
		t.Fatalf("unexpected captured bootstrap nodepool: %#v", np)
	}
}

func TestServiceReconcileCreatesClusterWithExistingUserNodePoolsDirectly(t *testing.T) {
	var (
		captured    skev2.CreateOrUpdateClusterPayload
		getCalls    int
		createCalls int
	)
	sdkClient := newCapturedCreateSDKClient(t, &captured, &getCalls, &createCalls)
	scope := newFakeScope(sdkClient)
	scope.nodePools = []ManagedNodePoolState{
		{ResourceName: "np-b", Desired: desiredPool("pool-b", new(false)), Finalized: true},
		{ResourceName: "np-a", Desired: desiredPool("pool-a", new(true)), Finalized: true},
	}
	svc := New(scope)

	if _, err := svc.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile() unexpected error: %v", err)
	}
	if createCalls != 1 {
		t.Fatalf("createCalls = %d, want 1", createCalls)
	}
	if len(captured.Nodepools) != 2 || captured.Nodepools[0].Name != "pool-a" || captured.Nodepools[1].Name != "pool-b" {
		t.Fatalf("captured nodepools = %#v, want [pool-a, pool-b] without bootstrap pool", captured.Nodepools)
	}
	if scope.nodePoolStatuses["np-a"].reason != "Provisioning" || scope.nodePoolStatuses["np-b"].reason != "Provisioning" {
		t.Fatalf("unexpected child statuses: %#v", scope.nodePoolStatuses)
	}
}

func TestServiceReconcileCreatePayloadImageVersionOverride(t *testing.T) {
	t.Setenv(defaultNodePoolImageVersionEnv, "4459.2.9")
	var (
		captured    skev2.CreateOrUpdateClusterPayload
		getCalls    int
		createCalls int
	)
	sdkClient := newCapturedCreateSDKClient(t, &captured, &getCalls, &createCalls)
	scope := newFakeScope(sdkClient)
	svc := New(scope)

	if _, err := svc.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile() unexpected error: %v", err)
	}
	if createCalls != 1 {
		t.Fatalf("createCalls = %d, want 1", createCalls)
	}
	if got := captured.Nodepools[0].Machine.Image.Version; got != "4459.2.9" {
		t.Fatalf("captured Nodepool image version = %q, want 4459.2.9", got)
	}
}

func TestServiceBootstrapForwardHandoffAndFailureRetention(t *testing.T) {
	var (
		capturedPayloads []skev2.CreateOrUpdateClusterPayload
		observedCluster  *skev2.Cluster
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(observedCluster)
		case http.MethodPut:
			var body skev2.CreateOrUpdateClusterPayload
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("decode PUT body: %v", err)
			}
			capturedPayloads = append(capturedPayloads, body)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusAccepted)
			_ = json.NewEncoder(w).Encode(observedCluster)
		default:
			t.Fatalf("unexpected method %s", r.Method)
		}
	}))
	t.Cleanup(server.Close)

	client, err := skev2.NewAPIClient(config.WithoutAuthentication(), config.WithEndpoint(server.URL))
	if err != nil {
		t.Fatalf("NewAPIClient() error: %v", err)
	}

	scope := newFakeScope(client.DefaultAPI)
	workerDesired := desiredPool("workers", new(true))
	scope.nodePools = []ManagedNodePoolState{
		{ResourceName: "workers-cr", Desired: workerDesired, Finalized: true},
	}
	svc := New(scope)

	// 1. SKE currently has only [capstk-system] and is HEALTHY -> Stage 1 PUTs [capstk-system, workers].
	observedCluster = clusterWithState(skev2.CLUSTERSTATUSSTATE_STATE_HEALTHY, defaultBootstrapSDKPool())
	res, err := svc.Reconcile(context.Background())
	if err != nil {
		t.Fatalf("Stage 1 Reconcile() error: %v", err)
	}
	if res.RequeueAfter != time.Minute || len(capturedPayloads) != 1 {
		t.Fatalf("Stage 1: res=%v len(capturedPayloads)=%d", res, len(capturedPayloads))
	}
	if len(capturedPayloads[0].Nodepools) != 2 ||
		capturedPayloads[0].Nodepools[0].Name != BootstrapNodePoolName ||
		capturedPayloads[0].Nodepools[1].Name != "workers" {
		t.Fatalf("Stage 1 payload nodepools = %#v, want [%s, workers]", capturedPayloads[0].Nodepools, BootstrapNodePoolName)
	}

	// 2. If SKE enters STATE_UNHEALTHY while provisioning workers, capstk-system is retained (no Stage 2 PUT).
	observedCluster = clusterWithState(
		skev2.CLUSTERSTATUSSTATE_STATE_UNHEALTHY,
		defaultBootstrapSDKPool(),
		workerDesired.ToSKESDK(),
	)
	if _, err := svc.Reconcile(context.Background()); err != nil {
		t.Fatalf("Unhealthy Reconcile() error: %v", err)
	}
	if len(capturedPayloads) != 1 {
		t.Fatalf("Unhealthy state should not remove bootstrap pool: len(capturedPayloads)=%d, want 1", len(capturedPayloads))
	}
	if scope.nodePoolStatuses["workers-cr"].reason != "Unhealthy" {
		t.Fatalf("workers-cr reason = %q, want Unhealthy", scope.nodePoolStatuses["workers-cr"].reason)
	}

	// 3. Once SKE is STATE_HEALTHY with [capstk-system, workers], Stage 2 PUTs [workers] (removing capstk-system).
	observedCluster = clusterWithState(
		skev2.CLUSTERSTATUSSTATE_STATE_HEALTHY,
		defaultBootstrapSDKPool(),
		workerDesired.ToSKESDK(),
	)
	res, err = svc.Reconcile(context.Background())
	if err != nil {
		t.Fatalf("Stage 2 Reconcile() error: %v", err)
	}
	if res.RequeueAfter != time.Minute || len(capturedPayloads) != 2 {
		t.Fatalf("Stage 2: res=%v len(capturedPayloads)=%d", res, len(capturedPayloads))
	}
	if len(capturedPayloads[1].Nodepools) != 1 || capturedPayloads[1].Nodepools[0].Name != "workers" {
		t.Fatalf("Stage 2 payload nodepools = %#v, want [workers]", capturedPayloads[1].Nodepools)
	}
	if scope.ready || scope.nodePoolStatuses["workers-cr"].ready {
		t.Fatalf("cluster/pool should be Reconciling while Stage 2 PUT is in flight")
	}

	// 4. Once SKE is STATE_HEALTHY with [workers], both cluster and workers-cr are Ready=true and Provisioned=true.
	observedCluster = clusterWithState(skev2.CLUSTERSTATUSSTATE_STATE_HEALTHY, workerDesired.ToSKESDK())
	res, err = svc.Reconcile(context.Background())
	if err != nil {
		t.Fatalf("Converged Reconcile() error: %v", err)
	}
	if res.RequeueAfter != 0 || len(capturedPayloads) != 2 {
		t.Fatalf("Converged: res=%v len(capturedPayloads)=%d", res, len(capturedPayloads))
	}
	if !scope.ready || !scope.nodePoolStatuses["workers-cr"].ready ||
		scope.nodePoolStatuses["workers-cr"].provisioned == nil || !*scope.nodePoolStatuses["workers-cr"].provisioned {
		t.Fatalf("expected cluster and workers-cr Ready=true and Provisioned=true, got cluster=%v pool=%#v",
			scope.ready, scope.nodePoolStatuses["workers-cr"])
	}
}

func TestServiceReverseHandoffOnLastSystemPoolDeletion(t *testing.T) {
	var (
		capturedPayloads []skev2.CreateOrUpdateClusterPayload
		observedCluster  *skev2.Cluster
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(observedCluster)
		case http.MethodPut:
			var body skev2.CreateOrUpdateClusterPayload
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("decode PUT body: %v", err)
			}
			capturedPayloads = append(capturedPayloads, body)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusAccepted)
			_ = json.NewEncoder(w).Encode(observedCluster)
		default:
			t.Fatalf("unexpected method %s", r.Method)
		}
	}))
	t.Cleanup(server.Close)

	client, err := skev2.NewAPIClient(config.WithoutAuthentication(), config.WithEndpoint(server.URL))
	if err != nil {
		t.Fatalf("NewAPIClient() error: %v", err)
	}

	scope := newFakeScope(client.DefaultAPI)
	sysPool := desiredPool("sys-pool", new(true))
	nonSysPool := desiredPool("app-pool", new(false))
	// sys-pool is being deleted while non-system app-pool remains active.
	scope.nodePools = []ManagedNodePoolState{
		{ResourceName: "sys-cr", Desired: sysPool, Finalized: true, Deleting: true},
		{ResourceName: "app-cr", Desired: nonSysPool, Finalized: true, Deleting: false},
	}
	svc := New(scope)

	// Stage 1: SKE currently has [app-pool, sys-pool] -> introduces capstk-system while retaining sys-pool.
	observedCluster = clusterWithState(
		skev2.CLUSTERSTATUSSTATE_STATE_HEALTHY,
		nonSysPool.ToSKESDK(),
		sysPool.ToSKESDK(),
	)
	if _, err := svc.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reverse Stage 1 error: %v", err)
	}
	if len(capturedPayloads) != 1 || len(capturedPayloads[0].Nodepools) != 3 {
		t.Fatalf("Reverse Stage 1 payloads = %#v, want 3 pools [app-pool, capstk-system, sys-pool]", capturedPayloads)
	}
	if scope.nodePoolStatuses["sys-cr"].reason != ReasonDeleting {
		t.Fatalf("sys-cr reason = %q, want Deleting", scope.nodePoolStatuses["sys-cr"].reason)
	}

	// Stage 2: Once SKE is HEALTHY with [app-pool, capstk-system, sys-pool], omits sys-pool.
	observedCluster = clusterWithState(
		skev2.CLUSTERSTATUSSTATE_STATE_HEALTHY,
		nonSysPool.ToSKESDK(),
		defaultBootstrapSDKPool(),
		sysPool.ToSKESDK(),
	)
	if _, err := svc.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reverse Stage 2 error: %v", err)
	}
	if len(capturedPayloads) != 2 || len(capturedPayloads[1].Nodepools) != 2 ||
		capturedPayloads[1].Nodepools[0].Name != "app-pool" ||
		capturedPayloads[1].Nodepools[1].Name != BootstrapNodePoolName {
		t.Fatalf("Reverse Stage 2 payload = %#v, want [app-pool, %s]", capturedPayloads[1].Nodepools, BootstrapNodePoolName)
	}
	if scope.nodePoolStatuses["sys-cr"].reason != ReasonDeleting {
		t.Fatalf("sys-cr reason during Stage 2 PUT = %q, want Deleting", scope.nodePoolStatuses["sys-cr"].reason)
	}

	// Completion: Once SKE is HEALTHY with [app-pool, capstk-system] (sys-pool absent), sys-cr gets Reason="Deleted".
	observedCluster = clusterWithState(
		skev2.CLUSTERSTATUSSTATE_STATE_HEALTHY,
		nonSysPool.ToSKESDK(),
		defaultBootstrapSDKPool(),
	)
	if _, err := svc.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reverse completion error: %v", err)
	}
	if scope.nodePoolStatuses["sys-cr"].reason != "Deleted" {
		t.Fatalf("sys-cr reason = %q, want Deleted", scope.nodePoolStatuses["sys-cr"].reason)
	}
	if !scope.nodePoolStatuses["app-cr"].ready || !scope.ready {
		t.Fatalf("expected app-cr and cluster Ready=true after reverse handoff")
	}
}

func TestServiceSingleGetClusterAcross1And5And20NodePools(t *testing.T) {
	for _, count := range []int{1, 5, 20} {
		t.Run(fmt.Sprintf("%d_pools", count), func(t *testing.T) {
			sdkPools := make([]skev2.Nodepool, 0, count)
			states := make([]ManagedNodePoolState, 0, count)
			for i := range count {
				name := fmt.Sprintf("pool-%02d", i)
				resName := fmt.Sprintf("cr-%02d", i)
				d := desiredPool(name, new(true))
				sdkPools = append(sdkPools, d.ToSKESDK())
				states = append(states, ManagedNodePoolState{
					ResourceName: resName,
					Desired:      d,
					Finalized:    true,
				})
			}

			api := &fakeSKEAPI{
				getClusterFn: func(_ skev2.ApiGetClusterRequest) (*skev2.Cluster, error) {
					return clusterWithState(skev2.CLUSTERSTATUSSTATE_STATE_HEALTHY, sdkPools...), nil
				},
			}
			scope := newFakeScope(api)
			scope.nodePools = states
			svc := New(scope)

			res, err := svc.Reconcile(context.Background())
			if err != nil {
				t.Fatalf("Reconcile() error: %v", err)
			}
			if res.RequeueAfter != 0 {
				t.Fatalf("RequeueAfter = %v, want 0", res.RequeueAfter)
			}
			if api.getCalls != 1 || api.createCalls != 0 {
				t.Fatalf("SKE calls: get=%d create=%d, want get=1 create=0", api.getCalls, api.createCalls)
			}
			for i := range count {
				resName := fmt.Sprintf("cr-%02d", i)
				st := scope.nodePoolStatuses[resName]
				if !st.ready || st.provisioned == nil || !*st.provisioned || st.reason != ReasonAvailable {
					t.Fatalf("pool %s status = %#v, want Ready/Available", resName, st)
				}
			}
		})
	}
}

func TestServiceRejectsDuplicateAndReservedNodePoolNamesAndUnblocksOnDelete(t *testing.T) {
	workerDesired := desiredPool("workers", new(true))
	api := &fakeSKEAPI{
		getClusterFn: func(_ skev2.ApiGetClusterRequest) (*skev2.Cluster, error) {
			return clusterWithState(skev2.CLUSTERSTATUSSTATE_STATE_HEALTHY, workerDesired.ToSKESDK()), nil
		},
	}
	scope := newFakeScope(api)
	scope.nodePools = []ManagedNodePoolState{
		{ResourceName: "np-1", Desired: workerDesired, Finalized: true},
		{ResourceName: "np-2", Desired: workerDesired, Finalized: true},
	}
	svc := New(scope)

	res, err := svc.Reconcile(context.Background())
	if err != nil {
		t.Fatalf("Reconcile(duplicate) unexpected error: %v", err)
	}
	if res.RequeueAfter != 0 || api.getCalls != 0 || api.createCalls != 0 {
		t.Fatalf("duplicate pools should not poll or call SKE: res=%v get=%d create=%d", res, api.getCalls, api.createCalls)
	}
	if scope.reason != ReasonDuplicateNodePoolName ||
		scope.nodePoolStatuses["np-1"].reason != ReasonDuplicateNodePoolName ||
		scope.nodePoolStatuses["np-2"].reason != ReasonDuplicateNodePoolName {
		t.Fatalf("unexpected reasons: cluster=%q np-1=%q np-2=%q",
			scope.reason, scope.nodePoolStatuses["np-1"].reason, scope.nodePoolStatuses["np-2"].reason)
	}

	// Deleting np-2 resolves the duplicate conflict: np-2 gets Reason=Deleted
	// without deleting "workers" in SKE, and np-1 + cluster become Ready=true.
	scope.nodePools[1].Deleting = true
	res, err = svc.Reconcile(context.Background())
	if err != nil {
		t.Fatalf("Reconcile(after deleting duplicate np-2) error: %v", err)
	}
	if res.RequeueAfter != 0 || api.getCalls != 1 || api.createCalls != 0 {
		t.Fatalf("expected 1 GET and 0 PUTs when resolving duplicate: res=%v get=%d create=%d",
			res, api.getCalls, api.createCalls)
	}
	if scope.nodePoolStatuses["np-2"].reason != ReasonDeleted {
		t.Fatalf("deleting duplicate np-2 reason = %q, want %q", scope.nodePoolStatuses["np-2"].reason, ReasonDeleted)
	}
	if !scope.nodePoolStatuses["np-1"].ready || !scope.ready {
		t.Fatalf("expected remaining active np-1 and cluster to be Ready=true")
	}

	// Reserved bootstrap pool name is rejected when active, and immediately
	// marked Deleted when deleting.
	api.getCalls = 0
	scope.nodePools = []ManagedNodePoolState{
		{ResourceName: "np-reserved", Desired: desiredPool(BootstrapNodePoolName, new(true)), Finalized: true},
	}
	res, err = svc.Reconcile(context.Background())
	if err != nil {
		t.Fatalf("Reconcile(reserved) unexpected error: %v", err)
	}
	if res.RequeueAfter != 0 || api.getCalls != 0 || api.createCalls != 0 {
		t.Fatalf("reserved pool name should not call SKE: res=%v get=%d create=%d", res, api.getCalls, api.createCalls)
	}
	if scope.reason != ReasonInvalidNodePoolConfiguration ||
		scope.nodePoolStatuses["np-reserved"].reason != ReasonInvalidNodePoolConfiguration {
		t.Fatalf("unexpected reasons for reserved name: cluster=%q pool=%q",
			scope.reason, scope.nodePoolStatuses["np-reserved"].reason)
	}

	// Deleting the reserved-name CR marks it Deleted so its finalizer can clear.
	scope.nodePools[0].Deleting = true
	api.getClusterFn = func(_ skev2.ApiGetClusterRequest) (*skev2.Cluster, error) {
		return clusterWithState(skev2.CLUSTERSTATUSSTATE_STATE_HEALTHY, defaultBootstrapSDKPool()), nil
	}
	if _, err := svc.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile(deleting reserved) unexpected error: %v", err)
	}
	if scope.nodePoolStatuses["np-reserved"].reason != ReasonDeleted {
		t.Fatalf("deleting reserved pool reason = %q, want %q", scope.nodePoolStatuses["np-reserved"].reason, ReasonDeleted)
	}
}

func TestServicePausedNodePoolPreservesObservedSKEStateUntilUnpaused(t *testing.T) {
	var (
		capturedPayloads []skev2.CreateOrUpdateClusterPayload
		observedCluster  *skev2.Cluster
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(observedCluster)
		case http.MethodPut:
			var body skev2.CreateOrUpdateClusterPayload
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("decode PUT body: %v", err)
			}
			capturedPayloads = append(capturedPayloads, body)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusAccepted)
			_ = json.NewEncoder(w).Encode(observedCluster)
		default:
			t.Fatalf("unexpected method %s", r.Method)
		}
	}))
	t.Cleanup(server.Close)

	client, err := skev2.NewAPIClient(config.WithoutAuthentication(), config.WithEndpoint(server.URL))
	if err != nil {
		t.Fatalf("NewAPIClient() error: %v", err)
	}

	obsPoolA := desiredPool("pool-a", new(true))
	obsPoolA.Maximum = 3
	obsPoolB := desiredPool("pool-b", new(true))
	obsPoolB.Maximum = 3
	observedCluster = clusterWithState(
		skev2.CLUSTERSTATUSSTATE_STATE_HEALTHY,
		obsPoolA.ToSKESDK(),
		obsPoolB.ToSKESDK(),
	)

	// 1. pool-a is paused and its desired Maximum changed to 5; pool-b is unpaused and unchanged.
	// Expect 0 PUT calls because paused pool-a's observed state (Maximum=3) is preserved.
	modPoolA := obsPoolA
	modPoolA.Maximum = 5
	scope := newFakeScope(client.DefaultAPI)
	scope.nodePools = []ManagedNodePoolState{
		{ResourceName: "cr-a", Desired: modPoolA, Finalized: true, Paused: true},
		{ResourceName: "cr-b", Desired: obsPoolB, Finalized: true, Paused: false},
	}
	svc := New(scope)

	if _, err := svc.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile(paused pool-a) error: %v", err)
	}
	if len(capturedPayloads) != 0 {
		t.Fatalf("expected 0 PUT calls when only paused pool changed, got %d", len(capturedPayloads))
	}

	// 2. Unpaused pool-b changes Maximum to 4 while pool-a remains paused (Desired Maximum=5).
	// Expect 1 PUT where pool-a is preserved at Maximum=3 and pool-b is updated to Maximum=4.
	modPoolB := obsPoolB
	modPoolB.Maximum = 4
	scope.nodePools[1].Desired = modPoolB
	if _, err := svc.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile(pool-b updated while pool-a paused) error: %v", err)
	}
	if len(capturedPayloads) != 1 || len(capturedPayloads[0].Nodepools) != 2 {
		t.Fatalf("expected 1 PUT with 2 pools, got %#v", capturedPayloads)
	}
	if capturedPayloads[0].Nodepools[0].Maximum != 3 || capturedPayloads[0].Nodepools[1].Maximum != 4 {
		t.Fatalf("expected pool-a Maximum=3 (preserved) and pool-b Maximum=4, got %#v", capturedPayloads[0].Nodepools)
	}

	// 3. Unpause pool-a -> next reconcile applies pool-a's Maximum=5 change.
	observedCluster = clusterWithState(
		skev2.CLUSTERSTATUSSTATE_STATE_HEALTHY,
		obsPoolA.ToSKESDK(),
		modPoolB.ToSKESDK(),
	)
	scope.nodePools[0].Paused = false
	if _, err := svc.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile(unpaused pool-a) error: %v", err)
	}
	if len(capturedPayloads) != 2 || capturedPayloads[1].Nodepools[0].Maximum != 5 {
		t.Fatalf("expected pool-a Maximum=5 after unpause, got %#v", capturedPayloads)
	}
}

func TestServiceSemanticEqualityPreventsEndlessPUTSpam(t *testing.T) {
	// Desired pool has nil AllowSystemComponents, empty VolumeType, unsorted AZs and taints, nil Labels.
	desiredA := ManagedNodePoolDesiredState{
		Name:              "pool-a",
		AvailabilityZones: []string{"eu01-2", "eu01-1"},
		MachineType:       "c2i.2",
		ImageName:         "flatcar",
		ImageVersion:      "4459.2.4",
		Minimum:           1,
		Maximum:           3,
		VolumeSize:        30,
		VolumeType:        "",
		Taints: []ManagedNodePoolTaint{
			{Key: "b", Effect: "NoSchedule", Value: ""},
			{Key: "a", Effect: "NoExecute", Value: "v1"},
		},
	}
	desiredB := desiredPool("pool-b", new(false))

	// Observed SKE response returns pools in reverse order, sorted AZs, defaulted
	// allowSystemComponents=true, defaulted volume.type="storage_premium_perf1",
	// empty map labels, reversed taints, and server-defaulted CRI/MaxSurge/MaxUnavailable.
	emptyLabels := map[string]string{}
	obsA := skev2.Nodepool{
		Name:                  "pool-a",
		AvailabilityZones:     []string{"eu01-1", "eu01-2"},
		AllowSystemComponents: new(true),
		Cri:                   &skev2.CRI{Name: new(skev2.NAMEOFTHECRILIBRARY_CONTAINERD)},
		Labels:                &emptyLabels,
		Machine: skev2.Machine{
			Type:  "c2i.2",
			Image: skev2.Image{Name: "flatcar", Version: "4459.2.4"},
		},
		Minimum:        1,
		Maximum:        3,
		MaxSurge:       new(int32(1)),
		MaxUnavailable: new(int32(0)),
		Taints: []skev2.Taint{
			{Key: "a", Effect: skev2.TAINTEFFECT_NO_EXECUTE, Value: new("v1")},
			{Key: "b", Effect: skev2.TAINTEFFECT_NO_SCHEDULE},
		},
		Volume: skev2.Volume{
			Size: 30,
			Type: new("storage_premium_perf1"),
		},
	}
	obsB := desiredB.ToSKESDK()

	api := &fakeSKEAPI{
		getClusterFn: func(_ skev2.ApiGetClusterRequest) (*skev2.Cluster, error) {
			// Return [obsB, obsA] (reverse order of [desiredA, desiredB]).
			return clusterWithState(skev2.CLUSTERSTATUSSTATE_STATE_HEALTHY, obsB, obsA), nil
		},
	}
	scope := newFakeScope(api)
	scope.nodePools = []ManagedNodePoolState{
		{ResourceName: "cr-a", Desired: desiredA, Finalized: true},
		{ResourceName: "cr-b", Desired: desiredB, Finalized: true},
	}
	svc := New(scope)

	res, err := svc.Reconcile(context.Background())
	if err != nil {
		t.Fatalf("Reconcile() unexpected error: %v", err)
	}
	if api.createCalls != 0 {
		t.Fatalf("expected 0 PUT calls for semantically identical pools, got %d", api.createCalls)
	}
	if res.RequeueAfter != 0 || !scope.ready || !scope.nodePoolStatuses["cr-a"].ready || !scope.nodePoolStatuses["cr-b"].ready {
		t.Fatalf("expected Ready=true and no requeue, got res=%v clusterReady=%v pools=%#v",
			res, scope.ready, scope.nodePoolStatuses)
	}
}

func TestServiceReconcileHandlesCreateConflictIdempotently(t *testing.T) {
	api := &fakeSKEAPI{
		createOrUpdateFn: func(_ skev2.ApiCreateOrUpdateClusterRequest) (*skev2.Cluster, error) {
			return nil, &oapierror.GenericOpenAPIError{StatusCode: http.StatusConflict, ErrorMessage: "409 Conflict"}
		},
	}
	scope := newFakeScope(api)
	svc := New(scope)

	res, err := svc.Reconcile(context.Background())
	if err != nil {
		t.Fatalf("Reconcile() unexpected error on 409 conflict: %v", err)
	}
	if res.RequeueAfter != time.Minute {
		t.Fatalf("Reconcile() RequeueAfter = %v, want %v", res.RequeueAfter, time.Minute)
	}
	if scope.reason != "Creating" || scope.ready {
		t.Fatalf("scope state = (ready=%v, reason=%q), want (false, Creating)", scope.ready, scope.reason)
	}
}

func TestServiceRefusesToAdoptOrDeleteClusterWithoutMatchingOwnershipLabels(t *testing.T) {
	state := skev2.CLUSTERSTATUSSTATE_STATE_HEALTHY
	staleUIDLabels := util.ManagedClusterTags("capi-mc", "default", "uid-capi-mc-old", nil)
	for _, tc := range []struct {
		name    string
		cluster *skev2.Cluster
	}{
		{
			name: "missing labels",
			cluster: &skev2.Cluster{
				Name: new("ske-poc"),
				Status: &skev2.ClusterStatus{
					Aggregated: &state,
				},
			},
		},
		{
			name: "same name and namespace but different UID",
			cluster: &skev2.Cluster{
				Name:   new("ske-poc"),
				Labels: &staleUIDLabels,
				Status: &skev2.ClusterStatus{
					Aggregated: &state,
				},
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			api := &fakeSKEAPI{
				getClusterFn: func(_ skev2.ApiGetClusterRequest) (*skev2.Cluster, error) {
					return tc.cluster, nil
				},
			}
			scope := newFakeScope(api)
			svc := New(scope)

			if _, err := svc.Reconcile(context.Background()); !cloud.IsConflict(err) {
				t.Fatalf("Reconcile(%s) err = %v, want ErrConflict", tc.name, err)
			}
			if scope.ready || scope.provisioned != nil || scope.reason != "ClusterNotManaged" || api.createCalls != 0 {
				t.Fatalf("unexpected state on %s Reconcile: ready=%v provisioned=%v reason=%q createCalls=%d",
					tc.name, scope.ready, scope.provisioned, scope.reason, api.createCalls)
			}

			if _, err := svc.Delete(context.Background()); !cloud.IsConflict(err) {
				t.Fatalf("Delete(%s) err = %v, want ErrConflict", tc.name, err)
			}
			if api.deleteCalls != 0 || scope.reason != "ClusterNotManaged" {
				t.Fatalf("unexpected state on %s Delete: deleteCalls=%d reason=%q",
					tc.name, api.deleteCalls, scope.reason)
			}
		})
	}
}

func TestServiceReconcileRepeatedPollingAndStickyProvisioned(t *testing.T) {
	currentState := skev2.CLUSTERSTATUSSTATE_STATE_CREATING
	api := &fakeSKEAPI{
		getClusterFn: func(_ skev2.ApiGetClusterRequest) (*skev2.Cluster, error) {
			c := clusterWithState(currentState)
			if currentState == skev2.CLUSTERSTATUSSTATE_STATE_UNHEALTHY {
				c.Status.Errors = []skev2.ClusterError{
					{Message: new("node disk pressure")},
				}
			}
			return c, nil
		},
	}
	scope := newFakeScope(api)
	svc := New(scope)

	res, err := svc.Reconcile(context.Background())
	if err != nil {
		t.Fatalf("Reconcile(CREATING) unexpected error: %v", err)
	}
	if api.createCalls != 0 || res.RequeueAfter != time.Minute || scope.ready || scope.provisioned != nil {
		t.Fatalf("unexpected state during CREATING: createCalls=%d res=%v ready=%v provisioned=%v",
			api.createCalls, res, scope.ready, scope.provisioned)
	}

	currentState = skev2.CLUSTERSTATUSSTATE_STATE_RECONCILING
	res, err = svc.Reconcile(context.Background())
	if err != nil {
		t.Fatalf("Reconcile(RECONCILING) unexpected error: %v", err)
	}
	if api.createCalls != 0 || res.RequeueAfter != time.Minute || scope.reason != "Reconciling" {
		t.Fatalf("unexpected state during RECONCILING: createCalls=%d res=%v reason=%q",
			api.createCalls, res, scope.reason)
	}

	currentState = skev2.CLUSTERSTATUSSTATE_STATE_HEALTHY
	res, err = svc.Reconcile(context.Background())
	if err != nil {
		t.Fatalf("Reconcile(HEALTHY) unexpected error: %v", err)
	}
	if res.RequeueAfter != 0 || !scope.ready || scope.provisioned == nil || !*scope.provisioned {
		t.Fatalf("unexpected state during HEALTHY: res=%v ready=%v provisioned=%v",
			res, scope.ready, scope.provisioned)
	}

	currentState = skev2.CLUSTERSTATUSSTATE_STATE_UNHEALTHY
	res, err = svc.Reconcile(context.Background())
	if err != nil {
		t.Fatalf("Reconcile(UNHEALTHY) unexpected error: %v", err)
	}
	if res.RequeueAfter != time.Minute || scope.ready || scope.reason != "Unhealthy" || scope.message != "node disk pressure" {
		t.Fatalf("unexpected state during UNHEALTHY: res=%v ready=%v reason=%q message=%q",
			res, scope.ready, scope.reason, scope.message)
	}
	if scope.provisioned == nil || !*scope.provisioned {
		t.Fatalf("Provisioned was reset on UNHEALTHY: got %v, want true", scope.provisioned)
	}
}

func TestServiceReconcileHandlesUnknownStatesAndAPIFailures(t *testing.T) {
	api := &fakeSKEAPI{
		getClusterFn: func(_ skev2.ApiGetClusterRequest) (*skev2.Cluster, error) {
			return clusterWithState(skev2.CLUSTERSTATUSSTATE_UNKNOWN_DEFAULT_OPEN_API), nil
		},
	}
	scope := newFakeScope(api)
	svc := New(scope)

	res, err := svc.Reconcile(context.Background())
	if err != nil {
		t.Fatalf("Reconcile(UNKNOWN) unexpected error: %v", err)
	}
	if res.RequeueAfter != time.Minute || scope.reason != "UnknownState" || scope.ready {
		t.Fatalf("unexpected state for UNKNOWN: res=%v reason=%q ready=%v", res, scope.reason, scope.ready)
	}

	api.getClusterFn = func(_ skev2.ApiGetClusterRequest) (*skev2.Cluster, error) {
		labels := util.ManagedClusterTags("capi-mc", "default", "uid-capi-mc-1", nil)
		return &skev2.Cluster{Name: new("ske-poc"), Labels: &labels}, nil
	}
	res, err = svc.Reconcile(context.Background())
	if err != nil {
		t.Fatalf("Reconcile(nil status) unexpected error: %v", err)
	}
	if res.RequeueAfter != time.Minute || scope.reason != "UnknownState" {
		t.Fatalf("unexpected state for nil status: res=%v reason=%q", res, scope.reason)
	}

	api.getClusterFn = func(_ skev2.ApiGetClusterRequest) (*skev2.Cluster, error) {
		return nil, nil
	}
	if _, err := svc.Reconcile(context.Background()); !cloud.IsTransient(err) || api.createCalls != 0 {
		t.Fatalf("Reconcile(nil, nil) err = %v, createCalls = %d, want ErrTransient and 0 createCalls", err, api.createCalls)
	}

	api.getClusterFn = func(_ skev2.ApiGetClusterRequest) (*skev2.Cluster, error) {
		return nil, &oapierror.GenericOpenAPIError{StatusCode: http.StatusUnauthorized, ErrorMessage: "401 Unauthorized"}
	}
	if _, err := svc.Reconcile(context.Background()); !cloud.IsUnauthorized(err) || api.createCalls != 0 {
		t.Fatalf("Reconcile(401) err = %v, createCalls = %d, want ErrUnauthorized and 0 createCalls", err, api.createCalls)
	}
}

func TestServiceDeleteLifecycleAndErrorHandling(t *testing.T) {
	currentState := skev2.CLUSTERSTATUSSTATE_STATE_HEALTHY
	notFound := false
	api := &fakeSKEAPI{
		getClusterFn: func(_ skev2.ApiGetClusterRequest) (*skev2.Cluster, error) {
			if notFound {
				return nil, &oapierror.GenericOpenAPIError{StatusCode: http.StatusNotFound, ErrorMessage: "404 Not Found"}
			}
			return clusterWithState(currentState), nil
		},
	}
	scope := newFakeScope(api)
	svc := New(scope)

	res, err := svc.Delete(context.Background())
	if err != nil {
		t.Fatalf("Delete(HEALTHY) unexpected error: %v", err)
	}
	if api.deleteCalls != 1 || res.RequeueAfter != time.Minute || scope.reason != "Deleting" {
		t.Fatalf("unexpected state after initial Delete: deleteCalls=%d res=%v reason=%q",
			api.deleteCalls, res, scope.reason)
	}

	api.deleteClusterFn = func(_ skev2.ApiDeleteClusterRequest) (map[string]any, error) {
		return nil, &oapierror.GenericOpenAPIError{StatusCode: http.StatusNotFound, ErrorMessage: "404 Not Found"}
	}
	res, err = svc.Delete(context.Background())
	if err != nil {
		t.Fatalf("Delete(DeleteCluster 404) unexpected error: %v", err)
	}
	if res.RequeueAfter != time.Minute || scope.reason != "Deleting" {
		t.Fatalf("Delete(DeleteCluster 404) res=%v reason=%q, want RequeueAfter=%v and Deleting",
			res, scope.reason, time.Minute)
	}
	api.deleteClusterFn = nil

	currentState = skev2.CLUSTERSTATUSSTATE_STATE_DELETING
	res, err = svc.Delete(context.Background())
	if err != nil {
		t.Fatalf("Delete(DELETING) unexpected error: %v", err)
	}
	if api.deleteCalls != 2 || res.RequeueAfter != time.Minute {
		t.Fatalf("unexpected state while DELETING: deleteCalls=%d res=%v", api.deleteCalls, res)
	}

	notFound = true
	res, err = svc.Delete(context.Background())
	if err != nil {
		t.Fatalf("Delete(404) unexpected error: %v", err)
	}
	if res.RequeueAfter != 0 || api.deleteCalls != 2 {
		t.Fatalf("unexpected state on 404: res=%v deleteCalls=%d", res, api.deleteCalls)
	}

	api.getClusterFn = func(_ skev2.ApiGetClusterRequest) (*skev2.Cluster, error) {
		return nil, nil
	}
	if _, err := svc.Delete(context.Background()); !cloud.IsTransient(err) {
		t.Fatalf("Delete(nil, nil) err = %v, want ErrTransient", err)
	}

	api.getClusterFn = func(_ skev2.ApiGetClusterRequest) (*skev2.Cluster, error) {
		return nil, &oapierror.GenericOpenAPIError{StatusCode: http.StatusForbidden, ErrorMessage: "403 Forbidden"}
	}
	if _, err := svc.Delete(context.Background()); !cloud.IsUnauthorized(err) {
		t.Fatalf("Delete(403) err = %v, want ErrUnauthorized", err)
	}
}
