# Regression: HA control-plane (3 nodes, leader deletion, re-election)

Date: 2026-08-11
Run: refactor 1 — see [SUMMARY.md](SUMMARY.md#timeline)
Branch: `refactor/code-cleanup-and-proper-abstraction` (commit `93eb06c`)
Cluster: `stackit-capi-test` (scaled up from the 1-CP cluster used in [run-refactor1-1-bootstrapping.md](run-refactor1-1-bootstrapping.md))
Status: ⚠️ partial — same defect as `main`, but this run exposed a **worse
failure mode** of it that the `main` runs had not seen

Repeats the HA package against the refactored provider. The known defect —
`ensureServer()` recreating a server unconditionally, see
[machine-recreate-bug.md](machine-recreate-bug.md) — reproduced, as expected
from the unchanged code. What differs is the *outcome* of that recreate, and
the reason turns out to be informative.

**Scope:** leader election and etcd membership are upstream
`KubeadmControlPlane` responsibilities. This provider only maintains the
API-server load-balancer targets.

Reference: [run-main1-2-ha-controlplane.md](run-main1-2-ha-controlplane.md)
and [run-main2-2-ha-controlplane.md](run-main2-2-ha-controlplane.md).

## Test steps

### 1. Scale the control plane to 3 replicas

```
$ export CONTROL_PLANE_MACHINE_COUNT=3
$ clusterctl generate cluster "${CLUSTER_NAME}" \
   --from templates/cluster-template.yaml \
   --target-namespace "${NAMESPACE}" > cluster.yaml

$ kubectl apply -f cluster.yaml

kubeadmcontrolplane.controlplane.cluster.x-k8s.io/stackit-capi-test-control-plane configured

$ kubectl get kubeadmcontrolplane stackit-capi-test-control-plane -o jsonpath='{.status.replicas} desired, {.status.readyReplicas} ready{"\n"}'   # every 30s

2 desired, 1 ready   # t+30s .. t+180s
3 desired, 2 ready   # t+210s .. t+390s
3 desired, 3 ready   # t+420s

$ kubectl --kubeconfig "${KUBECONF_WORKERCLUSTER}" get nodes -l node-role.kubernetes.io/control-plane

NAME                                    STATUS   ROLES           AGE    VERSION
stackit-capi-test-control-plane-7xp8v   Ready    control-plane   29m    v1.35.7
stackit-capi-test-control-plane-8sbqz   Ready    control-plane   4m2s   v1.35.7
stackit-capi-test-control-plane-m6lrw   Ready    control-plane   75s    v1.35.7
```

**Result:** KCP scaled 1→3 one machine at a time, each healthy before the next
was created — 7 minutes end to end. All three control-plane nodes `Ready`.

**Vergleich main:** `run-main1` took ~8 min, `run-main2` ~6 min, both one at a
time. Same behaviour.

---

### 2. Identify the current leader

```
$ kubectl --kubeconfig "${KUBECONF_WORKERCLUSTER}" -n kube-system get lease kube-controller-manager -o jsonpath='{.spec.holderIdentity}{"\n"}'

stackit-capi-test-control-plane-7xp8v_3f7fea1d-d249-4df3-ada5-c3c5475d424b

$ HOLDER="$(kubectl --kubeconfig "${KUBECONF_WORKERCLUSTER}" -n kube-system get lease kube-controller-manager -o jsonpath='{.spec.holderIdentity}')"
$ LEADER_NODE="${HOLDER%%_*}"
$ PROVIDER_ID="$(kubectl get machine "${LEADER_NODE}" -o jsonpath='{.spec.providerID}')"
$ SERVER_ID="${PROVIDER_ID#stackit://}"
$ echo "${LEADER_NODE} → ${SERVER_ID}"

stackit-capi-test-control-plane-7xp8v → 775e7fb6-3bef-4290-b017-5ebd51759399
```

**Result:** Leader is `...7xp8v`, the original control-plane machine. Its
internal IP at this point is `10.42.0.155` — remember this, it matters in
step 4a.

**Vergleich main:** identical procedure and identical outcome (the leader was
the original machine in all three runs).

---

### 3. Delete the leader's VM out-of-band

```
$ kubectl --kubeconfig "${KUBECONF_WORKERCLUSTER}" get --raw='/readyz'

ok

$ stackit server delete 775e7fb6-3bef-4290-b017-5ebd51759399 --assume-yes

Deleted server "stackit-capi-test-control-plane-7xp8v"

$ kubectl --kubeconfig "${KUBECONF_WORKERCLUSTER}" get --raw='/readyz'

ok
```

**Result:** VM gone; the API server stayed reachable through the load balancer
via the two remaining control-plane nodes.

**Vergleich main:** identical in both `main` runs.

---

### 4a. The silent recreate reproduces — and this time the VM rejoins

```
$ stackit server list | grep capi-test-control-plane   # ~75s after the delete

6af49941-dc83-40e4-a0e6-0a8952d58bba │ stackit-capi-test-control-plane-7xp8v │ CREATING │ c2i.4 │ eu01-m │
ef485f5f-dc42-4aef-98af-a34c08f5e4b4 │ stackit-capi-test-control-plane-8sbqz │ ACTIVE   │ c2i.4 │ eu01-m │ 10.42.0.30
64358e6b-1e25-494a-989a-c195f4d722fe │ stackit-capi-test-control-plane-m6lrw │ ACTIVE   │ c2i.4 │ eu01-m │ 10.42.0.75

$ kubectl get stackitmachine stackit-capi-test-control-plane-7xp8v \
  -o jsonpath='instanceState={.status.instanceState}{"\n"}instanceID={.status.instanceID}{"\n"}'

instanceState=CREATING
instanceID=6af49941-dc83-40e4-a0e6-0a8952d58bba   # NEW ID — the old one was 775e7fb6-…

$ kubectl get machinehealthcheck -A

No resources found
```

**Result:** The defect reproduces exactly as on `main` — a replacement server
is created silently, with no `MachineHealthCheck` and no operator action.

Watching what became of it (polled once a minute):

```
--- t+1min ---   instanceState=CREATING   ready=Unknown heartbeat=2026-08-11T11:10:41Z
--- t+2min ---   instanceState=ACTIVE     ready=False   heartbeat=2026-08-11T11:16:51Z
--- t+3min ---   instanceState=ACTIVE     ready=True    heartbeat=2026-08-11T11:17:52Z
--- t+8min ---   instanceState=ACTIVE     ready=True    heartbeat=2026-08-11T11:17:52Z
```

```
$ stackit server list | grep capi-test-control-plane-7xp8v

6af49941-dc83-40e4-a0e6-0a8952d58bba │ stackit-capi-test-control-plane-7xp8v │ ACTIVE │ c2i.4 │ eu01-m │ 10.42.0.155

$ kubectl --kubeconfig "${KUBECONF_WORKERCLUSTER}" get node stackit-capi-test-control-plane-7xp8v -o jsonpath='{.status.addresses}{"\n"}'

[{"address":"10.42.0.155","type":"InternalIP"},{"address":"stackit-capi-test-control-plane-7xp8v","type":"Hostname"}]

$ kubectl --kubeconfig "${KUBECONF_WORKERCLUSTER}" -n kube-node-lease get lease stackit-capi-test-control-plane-7xp8v -o jsonpath='renewTime={.spec.renewTime}{"\n"}'

renewTime=2026-08-11T11:24:22.147604Z    # 9 seconds old — a live kubelet is renewing it
```

**Result — differs from `main`:** the recreated VM **did** rejoin the cluster
and the node went back to `Ready` within about 3 minutes. The node lease being
renewed proves a live kubelet on the new VM, not a stale object.

**Why it differs — and why it is not a refactor change:** the replacement VM
was assigned the **same internal IP** as the deleted one (`10.42.0.155`). In
[run-main2-2-ha-controlplane.md](run-main2-2-ha-controlplane.md) the
replacement got a *different* IP (`10.42.0.78` vs the original `10.42.0.34`)
and never rejoined. The recreate replays the original bootstrap data, which is
pinned to the original IP — so whether the rejoin succeeds depends on whether
STACKIT happens to hand back the same address. Same code path, same defect,
outcome decided by IP allocation luck.

**And the rejoin leaves the cluster permanently inconsistent:**

```
$ echo "Machine.spec.providerID:          $(kubectl get machine stackit-capi-test-control-plane-7xp8v -o jsonpath='{.spec.providerID}')"
$ echo "StackitMachine.status.providerID: $(kubectl get stackitmachine stackit-capi-test-control-plane-7xp8v -o jsonpath='{.status.providerID}')"
$ echo "Node.spec.providerID:             $(kubectl --kubeconfig "${KUBECONF_WORKERCLUSTER}" get node stackit-capi-test-control-plane-7xp8v -o jsonpath='{.spec.providerID}')"

Machine.spec.providerID:          stackit://775e7fb6-3bef-4290-b017-5ebd51759399
StackitMachine.status.providerID: stackit://6af49941-dc83-40e4-a0e6-0a8952d58bba
Node.spec.providerID:             stackit://775e7fb6-3bef-4290-b017-5ebd51759399

$ stackit server describe 775e7fb6-3bef-4290-b017-5ebd51759399

  -> nicht mehr vorhanden

$ kubectl get kubeadmcontrolplane stackit-capi-test-control-plane -o jsonpath='{.status.replicas} desired, {.status.readyReplicas} ready{"\n"}'

3 desired, 3 ready
```

**This is arguably worse than the `main` outcome.** KCP reports a fully healthy
3/3 cluster, but the `Machine` and the `Node` both permanently reference a
server that no longer exists, while only the `StackitMachine` knows the real
one. `Machine.spec.providerID` is immutable in CAPI, and the node registered
with the old ID because it replayed the original bootstrap data. Nothing
surfaces this — there is no condition, no event, no degraded status. A cluster
in this state looks fine and will keep looking fine until something resolves a
node by provider ID.

**Vergleich main:** `run-main2` saw the same silent recreate but a permanently
`NotReady` node — visibly broken, and therefore easier to notice.
`run-main1` did not run `stackit server list` here and missed the recreate
entirely. Three runs, three different-looking outcomes from one unchanged
defect.

---

### 4b. Manual remediation

```
$ kubectl delete machine stackit-capi-test-control-plane-7xp8v

machine.cluster.x-k8s.io "stackit-capi-test-control-plane-7xp8v" deleted from default namespace

$ kubectl get machines,stackitmachines   # ~20s later

machine.cluster.x-k8s.io/stackit-capi-test-control-plane-8sbqz   …
machine.cluster.x-k8s.io/stackit-capi-test-control-plane-m6lrw   …
machine.cluster.x-k8s.io/stackit-capi-test-md-0-bx9jt-whjms      …

$ stackit server list | grep capi-test-control-plane

ef485f5f-dc42-4aef-98af-a34c08f5e4b4 │ stackit-capi-test-control-plane-8sbqz │ ACTIVE │ … │ 10.42.0.30
64358e6b-1e25-494a-989a-c195f4d722fe │ stackit-capi-test-control-plane-m6lrw │ ACTIVE │ … │ 10.42.0.75

$ kubectl get kubeadmcontrolplane stackit-capi-test-control-plane -o jsonpath='{.status.replicas} desired, {.status.readyReplicas} ready{"\n"}'   # every 30s

3 desired, 2 ready   # t+30s .. t+120s
3 desired, 3 ready   # t+150s

$ kubectl --kubeconfig "${KUBECONF_WORKERCLUSTER}" get nodes -l node-role.kubernetes.io/control-plane

NAME                                    STATUS   ROLES           AGE   VERSION
stackit-capi-test-control-plane-8sbqz   Ready    control-plane   22m   v1.35.7
stackit-capi-test-control-plane-m6lrw   Ready    control-plane   19m   v1.35.7
stackit-capi-test-control-plane-sgg88   Ready    control-plane   27s   v1.35.7

$ kubectl --kubeconfig "${KUBECONF_WORKERCLUSTER}" -n kube-system get lease kube-controller-manager -o jsonpath='{.spec.holderIdentity}{"\n"}'

stackit-capi-test-control-plane-8sbqz_53690b44-9815-424e-85eb-9d3dc040bc62

$ stackit load-balancer describe stackit-capi-test-apiserver -o json | grep -A16 '"targets"'

"displayName": "stackit-capi-test-control-plane-8sbqz"   "ip": "10.42.0.30"
"displayName": "stackit-capi-test-control-plane-m6lrw"   "ip": "10.42.0.75"
"displayName": "stackit-capi-test-control-plane-sgg88"   "ip": "10.42.0.163"

$ kubectl --kubeconfig "${KUBECONF_WORKERCLUSTER}" get --raw='/readyz'

ok

$ echo "Machine:        $(kubectl get machine stackit-capi-test-control-plane-sgg88 -o jsonpath='{.spec.providerID}')"
$ echo "StackitMachine: $(kubectl get stackitmachine stackit-capi-test-control-plane-sgg88 -o jsonpath='{.status.providerID}')"

Machine:        stackit://c04294b9-7c29-4c5f-a957-7a75a663729d
StackitMachine: stackit://c04294b9-7c29-4c5f-a957-7a75a663729d
```

**Result:** Deleting the `Machine` removed both it and its `StackitMachine`
cleanly with no stuck finalizer and no leaked VM or volume — including the
silently recreated one. KCP built a genuine replacement (`...sgg88`) that
joined in about 2.5 minutes, a new leader was elected (`...8sbqz`), the LB
target list was updated to the three current nodes, and `readyz` stayed `ok`
throughout. The replacement machine has **consistent** provider IDs again, so
manual remediation is also the fix for the inconsistency from step 4a.

**Vergleich main:** same clean remediation path and same recovery time as both
`main` runs (`run-main1` 2-3 min, `run-main2` ~3.5 min).

## Conclusion

**Status:** ⚠️ partial — unchanged from `main`, with a newly observed variant
of the known defect

Refactor parity for this package: **confirmed**. Scale-up, LB target
maintenance, remediation and re-election all behave exactly as on `main`, and
the known defect reproduces from the same code path
(`controller/stackitmachine_infrastructure.go:205`, moved from
`internal/controller/stackitmachine_controller.go:293`, logic unchanged).

**New insight into the existing bug** (not a refactor regression) — the
outcome of the silent recreate depends on whether the replacement VM gets the
same internal IP as the one it replaces:

- **different IP** (`run-main2`): the VM never rejoins, node stays `NotReady` —
  visibly broken.
- **same IP** (this run): the VM rejoins and the cluster reports 3/3 healthy,
  but `Machine.spec.providerID` and `Node.spec.providerID` permanently point
  at a deleted server while `StackitMachine` points at the live one. **Nothing
  reports this** — the cluster looks healthy while carrying a silent identity
  mismatch.

This strengthens the case for the fix proposed in
[machine-recreate-bug.md](machine-recreate-bug.md): the recreate should not
happen at all for a machine that had already provisioned. Both observed
outcomes are bad, and the second one is harder to detect.

**Unchanged gap:** no `MachineHealthCheck` in any template, so neither outcome
is remediated automatically.

---

## Addendum (2026-08-12)

The missing `MachineHealthCheck` is a **tracked roadmap item**, not an
oversight: [../docs/src/getting-started/overview.md](../docs/src/getting-started/overview.md),
section *"Full Cluster API Support"*, already lists "MachineHealthCheck
remediation" — together with "interrupted deletes" and "orphan cleanup", the
areas covered by [deletion-bug.md](deletion-bug.md) and
[machine-recreate-bug.md](machine-recreate-bug.md).

Note also that adding an MHC would **not** have helped in the same-IP outcome
above: the node was `Ready` and KCP reported `3 desired, 3 ready`, so there was
no unhealthy condition for an MHC to act on. See
[machine-recreate-bug.md#a-machinehealthcheck-is-not-a-fix-for-this](machine-recreate-bug.md#a-machinehealthcheck-is-not-a-fix-for-this).
