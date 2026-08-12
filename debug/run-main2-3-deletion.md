# Regression: deletion & cleanup correctness

Date: 2026-08-07
Run: 2 of 2 — see [SUMMARY.md](SUMMARY.md#timeline)
Cluster: `stackit-capi-test` (3 control-plane + 1 worker, carried over from [run-main2-1-bootstrapping.md](run-main2-1-bootstrapping.md) / [run-main2-2-ha-controlplane.md](run-main2-2-ha-controlplane.md))
Status: ✅ works

Verifies that deleting a cluster removes all STACKIT infrastructure (VMs,
disks, load balancer, security groups, public IPs) and that finalizers are
removed in the correct order, on the **normal, CAPI-orchestrated deletion
path**.

**Only the working path is exercised here.** Deleting `Cluster`,
`StackitCluster` and all `Machine`s *simultaneously* (e.g. `kubectl delete
-f cluster.yaml` on a manifest containing all of them) triggers a known,
still-unfixed bug that strands `StackitMachine`s and orphans VMs — see
[deletion-bug.md](deletion-bug.md). That bug is deliberately not reproduced
here; if it is hit by accident, follow
[deletion-bug.md#manual-cleanup-protocol](deletion-bug.md#manual-cleanup-protocol).

Prerequisites: see [SUMMARY.md](SUMMARY.md#shared-prerequisites), plus a
running workload cluster.

## Test steps

### 1. Snapshot the pre-deletion state

```
$ kubectl get cluster,stackitcluster,machine,stackitmachine -o wide

cluster.../stackit-capi-test         ... PHASE: Provisioned   CP 3/3   W 1/1
stackitcluster.../stackit-capi-test  READY: true   ENDPOINT: 213.17.23.218

machine.../stackit-capi-test-control-plane-gxz6c   Running
machine.../stackit-capi-test-control-plane-qrc5f   Running
machine.../stackit-capi-test-control-plane-vjvch   Running
machine.../stackit-capi-test-md-0-8szbn-xcc5l      Running

$ stackit server list | grep -i "${CLUSTER_NAME}"

stackit-capi-test-apiserver-3h246mftgov5dy2w-c566a   ACTIVE   # LB-managed API-server node
stackit-capi-test-apiserver-3h246mftgov5dy2w-815a3   ACTIVE   # LB-managed API-server node
stackit-capi-test-md-0-8szbn-xcc5l                   ACTIVE
stackit-capi-test-control-plane-gxz6c                ACTIVE
stackit-capi-test-control-plane-qrc5f                ACTIVE
stackit-capi-test-control-plane-vjvch                ACTIVE
```

**Result:** 4 cluster VMs (3 control-plane, 1 worker) plus 2 STACKIT-managed
API-server load-balancer instances, all `ACTIVE`/`Running` before deletion.

---

### 2. Delete the cluster via the Cluster object only

Expected order in code: per `StackitMachine` — API-server LB target removed
→ server/VM deleted → instance status cleared → `MachineFinalizer` removed
(`internal/controller/stackitmachine_controller.go`,
`reconcileDelete`); then `StackitCluster` — API-server load balancer deleted
→ bastion security group / public IP deleted (if enabled) →
`ClusterFinalizer` removed
(`internal/controller/stackitcluster_controller.go`,
`reconcileDelete`).

```
$ kubectl delete cluster "${CLUSTER_NAME}" --wait=false

cluster.cluster.x-k8s.io "stackit-capi-test" deleted from default namespace

$ kubectl get cluster,stackitcluster,machine,stackitmachine -o wide   # t+8s

cluster.../stackit-capi-test   PHASE: Deleting   CP 0/3 ready
stackitcluster.../stackit-capi-test   READY: true   (finalizer still present)
# worker md-0-xcc5l already gone — deleted first
machine.../stackit-capi-test-control-plane-gxz6c   Deleting
machine.../stackit-capi-test-control-plane-qrc5f   Deleting
machine.../stackit-capi-test-control-plane-vjvch   Deleting

$ kubectl get cluster,stackitcluster,machine,stackitmachine -o wide   # t+16s

No resources found in default namespace.
```

**Result:** Deletion order matched the expected code path: worker `Machine`
deleted first (already gone by the first check at t+8s), then all 3
control-plane `Machine`s in parallel, then `StackitCluster` removed its
finalizer, then `Cluster` disappeared. Total time from `kubectl delete
cluster` to everything gone: **16 seconds**, with no object stuck in
`Deleting`.

---

### 3. Verify no leaked STACKIT resources

```
$ stackit server list | grep -i "${CLUSTER_NAME}"       # t+16s

stackit-capi-test-apiserver-3h246mftgov5dy2w-c566a │ DELETING   # API-server LB node, still tearing down

$ stackit volume list | grep -i "${CLUSTER_NAME}"        # t+16s

(none found)   # all 4 cluster-VM boot volumes already gone

$ stackit load-balancer list | grep -i "${CLUSTER_NAME}"  # t+16s

stackit-capi-test-apiserver │ STATUS_TERMINATING │ 213.17.23.218 │ 1 │ 1

$ stackit security-group list | grep -i "${CLUSTER_NAME}" # t+16s

loadbalancer/stackit-capi-test-apiserver/frontend-port
loadbalancer/stackit-capi-test-apiserver/backend-port
loadbalancer/stackit-capi-test-apiserver/backend

$ stackit public-ip list | grep -i "${CLUSTER_NAME}"      # t+16s

(none found)   # public IP already released

# re-checked ~25s later:

$ stackit server list | grep -i "${CLUSTER_NAME}"

(none found)

$ stackit security-group list | grep -i "${CLUSTER_NAME}"

(none found)

$ stackit load-balancer list | grep -i "${CLUSTER_NAME}"   # ~45s after delete cluster

(none found)
```

**Result:** All 4 cluster VMs and their boot volumes were gone essentially
immediately (by t+16s). The 2 STACKIT-managed API-server load-balancer
backend instances, the load balancer itself, and its 3 security groups
(frontend-port, backend, backend-port) took roughly 25-45 seconds longer to
fully disappear (`DELETING`/`STATUS_TERMINATING` observed in between) —
STACKIT's own async deletion latency, not a stuck or leaked resource. The
public IP was already released by the first check. No leaks of any kind.

## Conclusion

**Status:** ✅ works

Deleting via `kubectl delete cluster` alone (the normal, CAPI-orchestrated
path) works correctly on current `main`: Machines are torn down in the right
order (worker first, then control-plane) before `StackitCluster` removes its
finalizer — all CAPI objects gone in 16 seconds — and every associated
STACKIT resource (4 VMs/volumes, the API-server load balancer and its 2
backend instances, its 3 security groups, and its public IP) is fully
cleaned up with no leaks within about 45 seconds total. Nothing got stuck in
`Deleting`.

**Known open risk (not retested here):** simultaneous deletion of
`Cluster`+`StackitCluster`+`Machine`s still reproduces the bug in
[deletion-bug.md](deletion-bug.md); the fix is specified but not
implemented — see
[deletion-bug.md#fix-plan-not-yet-implemented](deletion-bug.md#fix-plan-not-yet-implemented).
Until it lands, `kubectl delete cluster` should be the documented default.
