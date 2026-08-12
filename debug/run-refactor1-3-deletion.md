# Regression: deletion & cleanup correctness

Date: 2026-08-11
Run: refactor 1 — see [SUMMARY.md](SUMMARY.md#timeline)
Branch: `refactor/code-cleanup-and-proper-abstraction` (commit `93eb06c`)
Cluster: `stackit-capi-test` (3 control-plane + 1 worker, carried over from [run-refactor1-1-bootstrapping.md](run-refactor1-1-bootstrapping.md) / [run-refactor1-2-ha-controlplane.md](run-refactor1-2-ha-controlplane.md))
Status: ✅ works — behaviour identical to `main`

Verifies that deleting a cluster removes all STACKIT infrastructure and that
finalizers are removed in the correct order, on the **normal,
CAPI-orchestrated deletion path**, against the refactored provider.

**Only the working path is exercised here.** Deleting `Cluster`,
`StackitCluster` and all `Machine`s *simultaneously* triggers the known,
still-unfixed bug in [deletion-bug.md](deletion-bug.md). That bug is
deliberately not reproduced; if hit by accident, follow
[deletion-bug.md#manual-cleanup-protocol](deletion-bug.md#manual-cleanup-protocol).

Reference: [run-main1-3-deletion.md](run-main1-3-deletion.md) and
[run-main2-3-deletion.md](run-main2-3-deletion.md).

## Test steps

### 1. Snapshot the pre-deletion state

```
$ kubectl get machines -o wide

stackit-capi-test-control-plane-8sbqz   stackit-capi-test   stackit-capi-test-control-plane-8sbqz
stackit-capi-test-control-plane-m6lrw   stackit-capi-test   stackit-capi-test-control-plane-m6lrw
stackit-capi-test-control-plane-sgg88   stackit-capi-test   stackit-capi-test-control-plane-sgg88
stackit-capi-test-md-0-bx9jt-whjms      stackit-capi-test   stackit-capi-test-md-0-bx9jt-whjms

$ stackit server list | grep -i "${CLUSTER_NAME}"

d9051b35-4252-42f1-8d7e-af8844cef136 │ stackit-capi-test-apiserver-prul5hsk4j54lm33-1668d │ ACTIVE │ t1.2  │ eu01-1 │ 10.42.0.173
f6f90c15-5b5c-4337-8e08-43797790dc93 │ stackit-capi-test-apiserver-prul5hsk4j54lm33-0a931 │ ACTIVE │ t1.2  │ eu01-2 │ 10.42.0.4
84150eab-e6e8-498f-bc2b-23dfacaa08c0 │ stackit-capi-test-md-0-bx9jt-whjms                 │ ACTIVE │ c2i.4 │ eu01-m │ 10.42.0.51
ef485f5f-dc42-4aef-98af-a34c08f5e4b4 │ stackit-capi-test-control-plane-8sbqz              │ ACTIVE │ c2i.4 │ eu01-m │ 10.42.0.30
64358e6b-1e25-494a-989a-c195f4d722fe │ stackit-capi-test-control-plane-m6lrw              │ ACTIVE │ c2i.4 │ eu01-m │ 10.42.0.75
c04294b9-7c29-4c5f-a957-7a75a663729d │ stackit-capi-test-control-plane-sgg88              │ ACTIVE │ c2i.4 │ eu01-m │ 10.42.0.163
```

**Result:** 4 cluster VMs (3 control-plane, 1 worker) plus the 2
STACKIT-managed API-server load-balancer instances, all `ACTIVE`.

**Vergleich main:** same composition as `run-main2` (which also had 4 cluster
VMs plus 2 LB instances).

---

### 2. Delete the cluster via the Cluster object only

Expected order in code: per `StackitMachine` — API-server LB target removed →
server/VM deleted → instance status cleared → `MachineFinalizer` removed
(`controller/stackitmachine_controller.go`, `reconcileDelete`); then
`StackitCluster` — load balancer deleted → bastion resources (if enabled) →
`ClusterFinalizer` removed (`controller/stackitcluster_controller.go`).

```
$ kubectl delete cluster "${CLUSTER_NAME}" --wait=false

cluster.cluster.x-k8s.io "stackit-capi-test" deleted from default namespace

$ kubectl get cluster,stackitcluster,machine,stackitmachine   # t+8s

cluster.cluster.x-k8s.io/stackit-capi-test    Deleting   59m
stackitcluster.../stackit-capi-test           true       213.17.21.81
# worker md-0-whjms already gone — deleted first
machine.../stackit-capi-test-control-plane-8sbqz
machine.../stackit-capi-test-control-plane-m6lrw
machine.../stackit-capi-test-control-plane-sgg88

$ kubectl get cluster,stackitcluster,machine,stackitmachine   # t+24s

cluster.cluster.x-k8s.io/stackit-capi-test    Deleting   59m
stackitcluster.../stackit-capi-test           true       213.17.21.81
# all Machines and StackitMachines gone; only Cluster + StackitCluster left

$ kubectl get cluster,stackitcluster,machine,stackitmachine   # t+32s

No resources found in default namespace.
```

**Result:** Deletion order matched the expected code path exactly — worker
`Machine` first (already gone by the first check at t+8s), then the three
control-plane `Machine`s, then `StackitCluster` released its finalizer, then
`Cluster` disappeared. Everything gone in **32 seconds**, nothing stuck in
`Deleting`.

**Vergleich main:** `run-main2` measured 16s for the same object set,
`run-main1` "well under a minute". Same order, same order of magnitude.

---

### 3. Verify no leaked STACKIT resources

```
$ stackit server list | grep -i "${CLUSTER_NAME}"          # t+32s

d9051b35-4252-42f1-8d7e-af8844cef136 │ stackit-capi-test-apiserver-prul5hsk4j54lm33-1668d │ DELETING

$ stackit volume list | grep -i "${CLUSTER_NAME}"          # t+32s

(keine)   # all 4 cluster boot volumes already gone

$ stackit load-balancer list | grep -i "${CLUSTER_NAME}"   # t+32s

stackit-capi-test-apiserver │ STATUS_TERMINATING │ 213.17.21.81 │ 1 │ 1

$ stackit security-group list | grep -i "${CLUSTER_NAME}"  # t+32s

loadbalancer/stackit-capi-test-apiserver/backend
loadbalancer/stackit-capi-test-apiserver/frontend-port
loadbalancer/stackit-capi-test-apiserver/backend-port

$ stackit public-ip list | grep -i "${CLUSTER_NAME}"       # t+32s

(keine)   # public IP already released

# re-checked ~40s later:

$ stackit server list | grep -i "${CLUSTER_NAME}"
$ stackit volume list | grep -i "${CLUSTER_NAME}"
$ stackit load-balancer list | grep -i "${CLUSTER_NAME}"
$ stackit security-group list | grep -i "${CLUSTER_NAME}"
$ stackit public-ip list | grep -i "${CLUSTER_NAME}"

(keine für alle fünf)
```

**Result:** All 4 cluster VMs and their boot volumes were gone immediately. The
API-server load balancer, its 2 backend instances and its 3 security groups
took roughly another 40 seconds (`DELETING`/`STATUS_TERMINATING` observed in
between) — STACKIT's own async deletion latency, not a stuck resource. The
public IP was already released at the first check. **No leaks of any kind.**

**Vergleich main:** identical pattern in both `main` runs — VMs immediate, LB
and its security groups 25-45 s behind, public IP released first.

## Conclusion

**Status:** ✅ works — no behavioural difference from `main`

Deleting via `kubectl delete cluster` alone works correctly on the refactored
provider: Machines are torn down in the right order (worker first, then
control-plane) before `StackitCluster` releases its finalizer — all CAPI
objects gone in 32 seconds — and every associated STACKIT resource (4
VMs/volumes, the load balancer with its 2 backend instances, its 3 security
groups and its public IP) is fully cleaned up within about 75 seconds total.
Nothing got stuck in `Deleting`.

Notably this also cleaned up correctly after the messy state left by
[run-refactor1-2-ha-controlplane.md](run-refactor1-2-ha-controlplane.md),
where one machine had been silently recreated and then manually remediated.

**Refactor parity for this package: confirmed.**

**Known open risk (not retested here):** simultaneous deletion of
`Cluster`+`StackitCluster`+`Machine`s still reproduces the bug in
[deletion-bug.md](deletion-bug.md); the fix is specified but not implemented —
see [deletion-bug.md#fix-plan-not-yet-implemented](deletion-bug.md#fix-plan-not-yet-implemented).
Until it lands, `kubectl delete cluster` should be the documented default.
