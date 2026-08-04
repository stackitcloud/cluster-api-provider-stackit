# Plan: fix for the deletion bug in `StackitClusterReconciler`

> Reference: [01-debug-machine-deletion-report.md](01-debug-machine-deletion-report.md) (root cause analysis) and [02-clean-up-machine-report.md](02-clean-up-machine-report.md) (manual workaround for the already-damaged `stackit-workload` cluster, already fully carried out there). This plan only fixes the root cause for future deletions, the orphaned resources of the affected cluster have already been cleaned up.

## Goal

`StackitClusterReconciler.reconcileDelete` must only remove its finalizer once no `Machine`s remain for the associated `Cluster`. This keeps the `StackitCluster` resource around for as long as the `StackitMachineReconciler` still needs it to build credentials and project context when deleting the individual worker/control plane VMs.

## 1. Root cause in short

`internal/controller/stackitcluster_controller.go:376-410` removes the finalizer unconditionally once the load balancer and bastion have been cleaned up. There is no check for still-existing `Machine`/`StackitMachine` objects. If `Cluster`, `StackitCluster`, and `Machine`s are deleted at the same time (e.g. via `kubectl delete -f cluster.yaml`), the `StackitCluster` disappears almost instantly. `internal/controller/stackitmachine_controller.go:92-99` then aborts on every reconcile with `StackitCluster not found, requeueing` and never deletes the associated VM.

## 2. Fix strategy

Two parts:

1. **Guard in `reconcileDelete`:** Before cleaning up the load balancer/bastion and removing the finalizer, check via a label selector whether `Machine`s belonging to the associated `Cluster` still exist. If any do, finish the reconcile without an error and keep the finalizer.
2. **Re-trigger via watch:** So that the `StackitCluster` does not have to rely on polling to notice that the last `Machine` is gone, a `Watches(&clusterv1.Machine{}, ...)` is added that produces a reconcile request for the associated `StackitCluster` on every `Machine` event (including its eventual disappearance). This mirrors the already existing reverse mechanism `stackitMachineRequestsForStackitCluster` in the `StackitMachineReconciler`.

This makes the deletion order work as follows: `Machine`s are fully cleaned up first (the `StackitMachine` controllers can still use the `StackitCluster` for credentials, since it does not disappear prematurely). Only once no `Machine` remains does `StackitCluster.reconcileDelete` clean up the load balancer/bastion and remove its own finalizer.

## 3. Concrete code changes

File: `internal/controller/stackitcluster_controller.go`

### 3.1 New helper function `listClusterMachines`

Filters server-side using the `clusterv1.ClusterNameLabel` (`cluster.x-k8s.io/cluster-name`) that CAPI sets on every `Machine`, instead of client-side fetching all `Machine`s in the namespace and filtering them itself.

**Client-side vs. server-side:** With a client-side filter (as in the first draft in section 2 above), the controller uses `client.InNamespace(...)` to ask the API server for all `Machine`s in the namespace, regardless of cluster. The API server sends back the complete list, and only afterward is each element checked in a loop in the controller's own Go code, discarding anything that does not match. With a server-side filter (`client.MatchingLabels{...}`), the filter is sent as part of the request to the API server. The API server evaluates the label selector itself and returns only the already-matching `Machine` objects from the start, so the controller does not need to sort anything out itself. This avoids unnecessary network/serialization overhead for data that would be discarded anyway when many clusters share the same namespace.

```go
func (r *StackitClusterReconciler) listClusterMachines(ctx context.Context, cluster *clusterv1.Cluster) ([]clusterv1.Machine, error) {
	machineList := &clusterv1.MachineList{}
	if err := r.List(ctx, machineList,
		client.InNamespace(cluster.Namespace),
		client.MatchingLabels{clusterv1.ClusterNameLabel: cluster.Name},
	); err != nil {
		return nil, fmt.Errorf("list machines for cluster %s: %w", cluster.Name, err)
	}
	return machineList.Items, nil
}
```

**Why a label selector instead of a field indexer:** A field indexer on `spec.clusterName` (`mgr.GetFieldIndexer().IndexField(...)`) would achieve the same effect, but requires a cache-backed client. The existing test suite client in `internal/controller/suite_test.go:90` is a direct, non-cached API server client (`client.New(cfg, ...)`), without a manager or cache. Switching to one would affect all existing tests in `stackitcluster_controller_test.go` and `stackitmachine_controller_test.go` and could introduce new flakiness due to cache sync delays. `client.MatchingLabels`, by contrast, is a normal API-server-side label selector, works identically against the envtest client and a real cluster, and requires no changes to `suite_test.go`. The label `cluster.x-k8s.io/cluster-name` is set by CAPI itself on every `Machine` (the test helper `createOwnerMachine` in `controller_test_helpers_test.go:94-101` already sets it too), making it a reliable filter.

### 3.2 Guard at the start of `reconcileDelete`

```go
func (r *StackitClusterReconciler) reconcileDelete(ctx context.Context, s *scope.ClusterScope) error {
	sc := s.StackitCluster

	machines, err := r.listClusterMachines(ctx, s.Cluster)
	if err != nil {
		return err
	}
	if len(machines) > 0 {
		logf.FromContext(ctx).Info("Waiting for Machines to be deleted before removing StackitCluster finalizer",
			"remainingMachines", len(machines))
		return nil
	}

	// ... existing load balancer/bastion cleanup and RemoveFinalizer stay unchanged ...
}
```

The rest of the function (load balancer deletion, bastion deletion, `controllerutil.RemoveFinalizer`) stays exactly as it is today, just behind this new guard.

### 3.3 New map function `stackitClusterRequestsForMachine`

```go
func (r *StackitClusterReconciler) stackitClusterRequestsForMachine(ctx context.Context, obj client.Object) []reconcile.Request {
	machine, ok := obj.(*clusterv1.Machine)
	if !ok || machine.Spec.ClusterName == "" {
		return nil
	}
	cluster := &clusterv1.Cluster{}
	key := types.NamespacedName{Namespace: machine.Namespace, Name: machine.Spec.ClusterName}
	if err := r.Get(ctx, key, cluster); err != nil {
		return nil
	}
	if !isStackitClusterRef(cluster.Spec.InfrastructureRef) {
		return nil
	}
	return []reconcile.Request{{
		NamespacedName: types.NamespacedName{
			Namespace: cluster.Namespace,
			Name:      cluster.Spec.InfrastructureRef.Name,
		},
	}}
}
```

Uses the already existing `isStackitClusterRef` (`stackitcluster_controller.go:588-592`).

### 3.4 Register the watch

Add to `SetupWithManager` (`stackitcluster_controller.go:595-603`):

```go
Watches(&clusterv1.Machine{}, handler.EnqueueRequestsFromMapFunc(r.stackitClusterRequestsForMachine)).
```

### 3.5 Add an RBAC marker

Above `Reconcile` (`stackitcluster_controller.go:68-73`), the permission to list `Machine`s is currently missing. Add:

```go
// +kubebuilder:rbac:groups=cluster.x-k8s.io,resources=machines,verbs=get;list;watch
```

Afterward run `make manifests` so `config/rbac/role.yaml` picks up the new rule entry.

## 4. Tests

File: `internal/controller/stackitcluster_controller_test.go`

1. **New test "keeps the finalizer while Machines still exist for the cluster":**
   - Setup as in `"deletes the provider-managed load balancer and removes the finalizer"` (line 423-442).
   - Additionally, before the `Delete`, create a `Machine` for the same `clusterName` via `createOwnerMachine(ctx, ..., clusterName, ...)` (helper in `controller_test_helpers_test.go:89-115`).
   - After `k8sClient.Delete(ctx, got)` and `reconciler.Reconcile(ctx, request)`, expect: `err` is `nil`, the finalizer is still set, `fakeCloud.LoadBalancerCount()` is still `1` (the load balancer has **not** been cleaned up yet).

2. **Regression test for the normal case:** The existing test `"deletes the provider-managed load balancer and removes the finalizer"` (line 423-442) must keep passing unchanged, since no `Machine` exists for the cluster there.

3. **New test "removes the finalizer once the last Machine is gone":**
   - Like test 1, but after the first reconcile (finalizer stays) delete the created `Machine` and reconcile a second time.
   - Expectation: the finalizer is now removed, the load balancer is cleaned up, the `StackitCluster` disappears (analogous to the `Eventually(...IsNotFound...)` from line 438-441).

4. **New test "maps Machine events to StackitCluster reconcile requests":**
   - Analogous to `"maps owning Cluster events to StackitCluster reconcile requests"` (line 462-468).
   - Create a `Machine` with `spec.clusterName = clusterName`, call `reconciler.stackitClusterRequestsForMachine(ctx, machine)`, expect: `[]reconcile.Request{request}`.
   - Additional case: a `Machine` with a different `clusterName` returns `nil`.

## 5. Verification

1. `go build ./...` (compiles without new errors).
2. `go test ./internal/controller/...` (new and existing tests pass, in particular the four listed above as well as all existing deletion tests in `stackitmachine_controller_test.go`).
3. `golangci-lint run` (project convention per `.golangci.yml`, no new findings).
4. Run `make manifests` after adding the RBAC marker and check `config/rbac/role.yaml` for the new `machines` entry.
5. Optional manual reproduction: run a local kind cluster with the provider, delete `Cluster`, `StackitCluster`, and `Machine`s at the same time via `kubectl delete -f`, and observe that the `StackitCluster` now stays around until all `Machine`s are gone, then correctly disappears itself.
