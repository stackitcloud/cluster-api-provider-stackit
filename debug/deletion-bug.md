# Bug: stuck cluster deletion orphans VMs

Date: 2026-08-03
Cluster: `stackit-workload`
Status: ⚠️ **open** — root cause identified, fix specified below, **not implemented**

Deleting `Cluster`, `StackitCluster` and all `Machine`s at the same time
(e.g. `kubectl delete -f cluster.yaml` on a manifest containing all of them)
strands every `Machine`/`StackitMachine` in `Deleting` forever and leaves
the backing VMs running at STACKIT. The safe path — `kubectl delete cluster
<name>` alone, letting CAPI cascade — works correctly; confirmed in both test
runs, see [run2-3-deletion.md](run2-3-deletion.md) (most recent) and
[run1-3-deletion.md](run1-3-deletion.md).

## Investigation

### 1. Overview of Machines

```
$ kubectl get machines,machinedeployments,clusters -A

NAME                                   CLUSTER            READY     PHASE      AGE
stackit-workload-control-plane-64zwk   stackit-workload   Unknown   Running    2d17h
stackit-workload-md-0-7bhgs-n4j2t      stackit-workload   False     Deleting   2d16h
stackit-workload-md-0-7bhgs-q6mbg      stackit-workload   False     Deleting   2d16h
stackit-workload-md-0-7bhgs-sjn22      stackit-workload   False     Deleting   2d17h
```

**Result:** All 3 worker Machines stuck in `PHASE=Deleting` for over 2 days.
The control-plane Machine is still running normally.

---

### 2. Details of a stuck Machine

```
$ kubectl describe machine stackit-workload-md-0-7bhgs-n4j2t

Metadata:
  Deletion Timestamp:  2026-07-31T14:38:18Z
  Finalizers:          machine.cluster.x-k8s.io
Status:
  Conditions:
    Reason:   WaitingForInfrastructureDeletion
    Message:  Waiting for StackitMachine to be deleted
    Status:   True
    Type:     Deleting
    Message:  * Deleting: Machine deletion in progress since more than 15m,
              stage: WaitingForInfrastructureDeletion
  Phase: Deleting
```

**Result:**
- `DeletionTimestamp` set for over 2 days, finalizer `machine.cluster.x-k8s.io` still present.
- Condition `Deleting`: *"Waiting for StackitMachine to be deleted"*.
- → The `Machine` is waiting for its infrastructure resource `StackitMachine`.

---

### 3. Details of the associated StackitMachine

```
$ kubectl describe stackitmachine stackit-workload-md-0-7bhgs-n4j2t

Metadata:
  Deletion Timestamp:  2026-07-31T14:38:19Z
  Finalizers:          stackitmachine.infrastructure.cluster.x-k8s.io
  Generation:          3
Status:
  Conditions:
    Observed Generation: 2      # ← all conditions still at generation 2
  Instance ID:     cec50749-6eef-4dc7-a64f-178ac8d08bcb
  Instance State:  ACTIVE
  Ready:           true
```

**Result:** `DeletionTimestamp` set and finalizer present, but `Generation:
3` while every status condition still reports `Observed Generation: 2` — the
controller has **never processed** the deletion. The instance is still
`ACTIVE` at STACKIT.

---

### 4. Is the controller pod running?

```
$ kubectl get pods -A | grep -iE "stackit|capi|cluster-api|controller"

cluster-api-provider-stackit-system   cluster-api-provider-stackit-controller-manager-fd8dfb75b-6gxw4   1/1   Running   0   2d17h
capi-system                           capi-controller-manager-7488f858b8-r9v55                          1/1   Running   0   2d17h
...
```

**Result:** All CAPI controller pods running normally, no restarts. The
controller has not crashed.

---

### 5. Controller logs

```
$ kubectl logs -n cluster-api-provider-stackit-system \
    cluster-api-provider-stackit-controller-manager-fd8dfb75b-6gxw4 --tail=200 \
    | grep -iE "error|delete|n4j2t|panic|retry"

2026-07-31T14:18:56Z  DEBUG  StackitMachine ready  {"providerID": "stackit://cec50749-..."}
...
2026-07-31T14:38:23Z  INFO   StackitCluster not found, requeueing  {"controller": "stackitmachine", ...}
2026-07-31T14:44:24Z  INFO   StackitCluster not found, requeueing  {"controller": "stackitmachine", ...}
2026-08-01T13:42:49Z  INFO   StackitCluster not found, requeueing  {"controller": "stackitmachine", ...}
2026-08-02T20:14:49Z  INFO   StackitCluster not found, requeueing  {"controller": "stackitmachine", ...}
2026-08-03T07:05:04Z  INFO   StackitCluster not found, requeueing  {"controller": "stackitmachine", ...}
```

**Result:** From `2026-07-31T14:38:23Z` — moments after the deletion
timestamps were set — the log contains nothing but `StackitCluster not
found, requeueing`, repeated for over 2 days. A deletion of the cloud
instance is **never attempted**. The controller logs this and returns `nil`
(no error, no backoff), so it loops forever.

---

### 6. Is the StackitCluster really gone?

```
$ kubectl get stackitcluster -A

No resources found

$ kubectl get cluster -A

NAMESPACE   NAME               PHASE      AGE
default     stackit-workload   Deleting   2d17h

$ kubectl get secret -A | grep -i stackit

default   stackit-credentials          Opaque                    2   2d17h
default   stackit-workload-kubeconfig  cluster.x-k8s.io/secret   1   2d17h
...
```

**Result:** The `StackitCluster` is completely gone, while the `Cluster`
still exists in `Deleting`. The credentials secret still exists — only the
`StackitCluster` CR through which the controller resolves it is missing.

---

### 7. Code analysis: why does a missing StackitCluster block deletion?

```
internal/controller/stackitmachine_controller.go:92-99
```
```go
stackitCluster, err := r.getStackitCluster(ctx, cluster)
if err != nil {
    return ctrl.Result{}, err
}
if stackitCluster == nil {
    log.Info("StackitCluster not found, requeueing")
    return ctrl.Result{}, nil
}
```

Without a live `StackitCluster`, no `MachineScope`/`cloud.Client`
(credentials) can be built. The reconcile aborts here — **before** it ever
tries to terminate the VM at STACKIT or remove the finalizer.

---

### 8. Code analysis: why is the StackitCluster gone while Machines remain?

```
internal/controller/stackitcluster_controller.go:376-410 (reconcileDelete)
```
```go
func (r *StackitClusterReconciler) reconcileDelete(ctx context.Context, s *scope.ClusterScope) error {
    ...
    controllerutil.RemoveFinalizer(sc, infrav1.ClusterFinalizer)
    return nil
}
```

`reconcileDelete` does **not** check whether `StackitMachine`/`Machine`
objects still exist for the cluster. It cleans up its own cloud
infrastructure (load balancer, bastion) and removes its finalizer
immediately, regardless of the state of the worker Machines.

**Branch parity note:** the bug exists identically on
`refactor/code-cleanup-and-proper-abstraction` (upstream
`github.com/stackitcloud/cluster-api-provider-stackit`, commit `ba2a21f`),
where `internal/controller/` was split into several files under
`controller/`. There, `reconcileDelete` lives in
`controller/stackitcluster_infrastructure.go:188-235` and ends with the same
unconditional `controllerutil.RemoveFinalizer(sc, infrav1.ClusterFinalizer)`
after its load-balancer/bastion cleanup — no check for remaining Machines.
The counterpart in `controller/stackitmachine_controller.go:86-92` is
identical to the `main` version. If that branch is merged, the fix below
must be applied there too.

## Root cause

`kubectl delete -f cluster.yaml` releases every object in the file
(`Cluster`, `StackitCluster`, `Machine`s) for deletion simultaneously,
instead of following CAPI's normal ordering — delete Machines first, then
the infrastructure cluster resource.

Because `StackitCluster.reconcileDelete` does not wait for pending
`StackitMachine`s, the `StackitCluster` is removed almost instantly, before
the worker VMs can be terminated at STACKIT. The `StackitMachine` controller
however *needs* the `StackitCluster` to build credentials/project context
for the cloud API call. With it missing, the controller loops forever on
`StackitCluster not found, requeueing` without ever deleting the VMs. The
finalizers stay, and the `Machine`/`StackitMachine` objects remain stuck in
`Deleting` indefinitely.

This produces two separate problems:

1. **Damaged cluster state:** the VMs still exist at STACKIT but are
   orphaned — there is no path left to delete them through the normal
   reconcile loop, since the credentials source (`StackitCluster`) is gone.
   → see [Manual cleanup protocol](#manual-cleanup-protocol).
2. **Provider bug:** a missing safeguard in
   `StackitClusterReconciler.reconcileDelete`, which should refuse to remove
   its own finalizer while `StackitMachine`s still exist for the cluster —
   a standard pattern for CAPI infrastructure providers.
   → see [Fix plan](#fix-plan-not-yet-implemented).

## Manual cleanup protocol

How to free an already-damaged cluster. **Always clean up the cloud side
(STACKIT) first, then remove the Kubernetes finalizers** — otherwise you
lose the VM↔object mapping before you have checked whether the VM still
exists.


### 1. Determine the instance IDs of the stuck Machines

```
$ kubectl get stackitmachine <name> -o jsonpath='{.status.instanceID}{"\n"}'
```

**Result:** Instance IDs determined for all three workers (e.g. `n4j2t` →
`cec50749-6eef-4dc7-a64f-178ac8d08bcb`).

### 2. Check whether the VMs still exist at STACKIT

```
$ stackit server list
```

If `STACKIT_PROJECT_ID` is unset it can be found in the `stackit-credentials`
secret, in `spec.projectID` of the (no longer existing) `StackitCluster`, or
in the original `cluster.yaml`.

**Result:** All three worker instances were found at STACKIT.

### 3. Delete the VMs

```
$ stackit server delete <instance-id>
```

**Result:** All 3 worker VMs deleted.

### 4. Check the boot volumes

With `rootVolume.deleteOnTermination: true` the volumes should be removed
along with the VMs.

```
$ stackit volume list
```

**Result:** No orphaned volumes remained.

### 5. Remove the finalizers on the `StackitMachine` objects

Only now, after steps 2-4 confirmed the cloud resources are gone:

```
$ kubectl patch stackitmachine <name> -p '{"metadata":{"finalizers":[]}}' --type=merge
```

**Result:** All 3 worker `StackitMachine`s removed.

**Lesson learned:** removing the finalizer on the `Machine` objects is
**not** necessary. Once the `StackitMachine` is gone, the `Machine`
controller notices the infrastructure ref disappeared and removes its own
finalizer automatically; the `Machine` objects vanish shortly after.

### 6. Watch the control-plane Machine

```
$ kubectl get machine,stackitmachine,cluster,kubeadmcontrolplane -A
```

**Result:** Confirmed — once the workers were gone, the `Cluster` controller
automatically triggered deletion of the control-plane Machine
(`deletionTimestamp: 2026-08-03T08:11:32Z` on
`stackit-workload-control-plane-64zwk`).

### 7. Repeat for the control-plane Machine

The same bug then hit the control-plane Machine — its `StackitMachine` got
stuck in the identical `StackitCluster not found, requeueing` loop. Repeat
steps 1-5 for it (instance `76803c9b-a9a6-45e0-ad65-70d60c841b1f`):

```
$ kubectl get stackitmachine stackit-workload-control-plane-64zwk -o jsonpath='{.status.instanceID}{"\n"}'
$ stackit server describe 76803c9b-a9a6-45e0-ad65-70d60c841b1f
$ stackit server delete 76803c9b-a9a6-45e0-ad65-70d60c841b1f
$ stackit volume list
$ kubectl patch stackitmachine stackit-workload-control-plane-64zwk -p '{"metadata":{"finalizers":[]}}' --type=merge
```

**Result:** Control-plane VM deleted, no orphaned volume, `StackitMachine`
and `Machine` both disappeared (`kubectl get stackitmachine,machines -A` →
`No resources found`).

### 8. Check remaining CAPI objects

```
$ kubectl get kubeadmcontrolplane,machineset,machinedeployment,kubeadmconfig -A
```

**Result:** `No resources found` — `KubeadmControlPlane`, `MachineSet`,
`MachineDeployment` and `KubeadmConfig` cleaned themselves up as expected
(they carry owner references rather than their own cloud finalizers), with
no manual finalizer removal needed.

### 9. Check the Cluster object

```
$ kubectl get cluster -A
```

**Result:** Empty output — `Cluster stackit-workload` fully gone, cleanup
complete.

## Fix plan (not yet implemented)

`StackitClusterReconciler.reconcileDelete` must only remove its finalizer
once no `Machine`s remain for the associated `Cluster`. That keeps the
`StackitCluster` alive as long as `StackitMachineReconciler` still needs it
for credentials and project context when deleting individual VMs.

### Strategy

1. **Guard in `reconcileDelete`:** before cleaning up load balancer/bastion
   and removing the finalizer, check via a label selector whether `Machine`s
   belonging to the `Cluster` still exist. If so, finish the reconcile
   without error and keep the finalizer.
2. **Re-trigger via watch:** add `Watches(&clusterv1.Machine{}, ...)` so the
   `StackitCluster` doesn't have to poll to notice the last `Machine` is
   gone. This mirrors the existing reverse mechanism
   `stackitMachineRequestsForStackitCluster` in `StackitMachineReconciler`.

Resulting order: Machines are cleaned up first (their controllers can still
use the `StackitCluster` for credentials, since it no longer disappears
prematurely); only once no `Machine` remains does
`StackitCluster.reconcileDelete` clean up load balancer/bastion and remove
its own finalizer.

### Code changes — `internal/controller/stackitcluster_controller.go`

**New helper `listClusterMachines`** — filters server-side via the
`clusterv1.ClusterNameLabel` (`cluster.x-k8s.io/cluster-name`) that CAPI
sets on every `Machine`:

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

*Server-side vs. client-side:* with `client.InNamespace(...)` alone the API
server returns every `Machine` in the namespace and the controller filters
in Go, discarding non-matches. `client.MatchingLabels` sends the selector to
the API server, which returns only matching objects — avoiding needless
network/serialization overhead when many clusters share a namespace.

*Why a label selector instead of a field indexer:* a field indexer on
`spec.clusterName` would work but requires a cache-backed client. The
existing test suite client (`internal/controller/suite_test.go:90`) is a
direct, non-cached API-server client (`client.New(cfg, ...)`) with no
manager or cache. Switching would affect all existing tests in
`stackitcluster_controller_test.go` / `stackitmachine_controller_test.go`
and risk cache-sync flakiness. `client.MatchingLabels` works identically
against envtest and a real cluster with no `suite_test.go` changes, and the
label is set by CAPI itself on every `Machine` (the test helper
`createOwnerMachine` in `controller_test_helpers_test.go:94-101` sets it
too).

**Guard at the start of `reconcileDelete`:**

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

**New map function `stackitClusterRequestsForMachine`:**

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

Uses the existing `isStackitClusterRef` (`stackitcluster_controller.go:588-592`).

**Register the watch** in `SetupWithManager` (`stackitcluster_controller.go:595-603`):

```go
Watches(&clusterv1.Machine{}, handler.EnqueueRequestsFromMapFunc(r.stackitClusterRequestsForMachine)).
```

**Add the RBAC marker** above `Reconcile` (`stackitcluster_controller.go:68-73`)
— permission to list `Machine`s is currently missing:

```go
// +kubebuilder:rbac:groups=cluster.x-k8s.io,resources=machines,verbs=get;list;watch
```

Then run `make manifests` so `config/rbac/role.yaml` picks up the new rule.

### Tests — `internal/controller/stackitcluster_controller_test.go`

1. **"keeps the finalizer while Machines still exist for the cluster"** —
   setup as in the existing `"deletes the provider-managed load balancer and
   removes the finalizer"` (line 423-442), but create a `Machine` for the
   same `clusterName` via `createOwnerMachine` before the `Delete`. After
   `k8sClient.Delete` + `reconciler.Reconcile`, expect `err == nil`, the
   finalizer still set, and `fakeCloud.LoadBalancerCount() == 1` (LB **not**
   cleaned up yet).
2. **Regression:** the existing test at line 423-442 must keep passing
   unchanged (no `Machine` exists for the cluster there).
3. **"removes the finalizer once the last Machine is gone"** — like test 1,
   but after the first reconcile delete the `Machine` and reconcile again;
   expect the finalizer removed, LB cleaned up, `StackitCluster` gone
   (analogous to the `Eventually(...IsNotFound...)` at line 438-441).
4. **"maps Machine events to StackitCluster reconcile requests"** —
   analogous to the existing `"maps owning Cluster events..."` (line
   462-468): create a `Machine` with `spec.clusterName = clusterName`, call
   `stackitClusterRequestsForMachine`, expect `[]reconcile.Request{request}`;
   plus a case where a different `clusterName` returns `nil`.

### Verification

1. `go build ./...` — compiles without new errors.
2. `go test ./internal/controller/...` — new and existing tests pass, in
   particular the four above plus all existing deletion tests in
   `stackitmachine_controller_test.go`.
3. `golangci-lint run` — no new findings.
4. `make manifests` after adding the RBAC marker; check
   `config/rbac/role.yaml` for the new `machines` entry.
5. Optional manual reproduction: on a local kind cluster with the provider,
   delete `Cluster`, `StackitCluster` and `Machine`s simultaneously via
   `kubectl delete -f`, and observe that the `StackitCluster` now stays
   until all `Machine`s are gone, then disappears correctly.
