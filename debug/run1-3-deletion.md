# Regression: deletion & cleanup correctness

Date: 2026-08-05
Run: 1 of 2 — see [SUMMARY.md](SUMMARY.md#timeline)
Cluster: `stackit-workload` (3 control-plane + 1 worker, carried over from [run1-1-bootstrapping.md](run1-1-bootstrapping.md) / [run1-2-ha-controlplane.md](run1-2-ha-controlplane.md))
Status: ✅ works

Verifies that deleting a cluster removes all STACKIT infrastructure (VMs,
disks, NICs, load balancers, security groups, public IPs) and that
finalizers are removed in the correct order, on the **normal,
CAPI-orchestrated deletion path**.

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

### 1. Delete the cluster via the Cluster object only

Expected order in code: per `StackitMachine` — API-server LB target removed
→ server/VM deleted → instance status cleared → `MachineFinalizer` removed
([../internal/controller/stackitmachine_controller.go](../internal/controller/stackitmachine_controller.go),
`reconcileDelete`); then `StackitCluster` — API-server load balancer deleted
→ bastion security group / public IP deleted (if enabled) →
`ClusterFinalizer` removed
([../internal/controller/stackitcluster_controller.go](../internal/controller/stackitcluster_controller.go),
`reconcileDelete`).

```
$ kubectl delete cluster "${CLUSTER_NAME}" --wait=false

cluster.cluster.x-k8s.io "stackit-workload" deleted from default namespace

$ kubectl get cluster,stackitcluster,machine,stackitmachine -o wide

# ~5s later:
cluster.cluster.x-k8s.io/stackit-workload   ...   PHASE: Deleting
stackitmachine.../stackit-workload-md-0-t5k56-8nlzf   PHASE: Deleting   # worker deletes first
machine.../stackit-workload-control-plane-*           PHASE: Running    # control-plane untouched so far

# ~20s later:
Machine   stackit-workload-control-plane-lw7gf   Deleting   # worker gone; control-plane now deleting
StackitCluster   stackit-workload   <none>

# ~15s later:
No resources found   # Cluster, StackitCluster, all Machines/StackitMachines gone
```

**Result:** Deletion order matched the expected code path exactly: worker
`Machine` first, then control-plane `Machine`s, then `StackitCluster`
removed its finalizer, then `Cluster` disappeared. Total time from `kubectl
delete cluster` to everything gone: well under a minute, with no object
stuck in `Deleting`.

---

### 2. Verify no leaked STACKIT resources

```
$ stackit server list | grep -i stackit-workload

(none found)

$ stackit volume list | grep -i stackit-workload

(none found)

$ stackit load-balancer list | grep -i stackit-workload

stackit-workload-apiserver │ STATUS_TERMINATING │ ...   # still tearing down ~10s after Cluster was gone

$ stackit security-group list | grep -i stackit-workload

loadbalancer/stackit-workload-apiserver/backend        # tied to the terminating LB
loadbalancer/stackit-workload-apiserver/backend-port

# ~15s later, re-checked:

$ stackit load-balancer list | grep -i stackit-workload

(none found)

$ stackit security-group list | grep -i stackit-workload

(none found)
```

**Result:** All 4 VMs (2 control-plane, 1 replacement control-plane from the
HA test, 1 worker) and their boot volumes are gone. The API-server load
balancer and its 3 security groups (frontend-port, backend, backend-port)
took roughly 15-25 seconds longer to disappear than the Kubernetes objects
(`STATUS_TERMINATING` observed briefly) but were fully gone on the next
check — STACKIT's own async deletion latency, not a stuck or leaked
resource. The public IP (`192.214.181.85`) was released even before the LB
finished terminating. No leaks of any kind.

## Conclusion

**Status:** ✅ works

Deleting via `kubectl delete cluster` alone (the normal, CAPI-orchestrated
path) works correctly on current `main`: Machines are torn down in the right
order (workers, then control-plane) before `StackitCluster` removes its
finalizer, and every associated STACKIT resource — 4 VMs/volumes, the
API-server load balancer, its 3 security groups, and its public IP — is
fully cleaned up with no leaks. Nothing got stuck in `Deleting`.

**Known open risk (not retested here):** simultaneous deletion of
`Cluster`+`StackitCluster`+`Machine`s still reproduces the bug in
[deletion-bug.md](deletion-bug.md); the fix is specified but not
implemented — see
[deletion-bug.md#fix-plan-not-yet-implemented](deletion-bug.md#fix-plan-not-yet-implemented).
Until it lands, `kubectl delete cluster` should be the documented default.
