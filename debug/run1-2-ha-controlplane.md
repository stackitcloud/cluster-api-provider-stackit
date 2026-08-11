# Regression: HA control-plane (3 nodes, leader deletion, re-election)

Date: 2026-08-05
Run: 1 of 2 — see [SUMMARY.md](SUMMARY.md#timeline)
Cluster: `stackit-workload` (scaled up from the 1-CP cluster used in [run1-1-bootstrapping.md](run1-1-bootstrapping.md))
Status: ⚠️ partial — works, but only *after* manual remediation (no `MachineHealthCheck` ships with any template)

Verifies that 3 control-plane nodes can join and form an HA control plane,
and that deleting the current leader's VM leads to correct cleanup (no
leaked VM/volume/LB target), a replacement control-plane node joining, and a
new leader being elected — without losing API-server availability.

**Scope:** Leader election, etcd re-election and reassignment of the
`cluster.x-k8s.io/control-plane` label are the responsibility of the
**upstream `KubeadmControlPlane` controller**, not this provider. This
provider's only control-plane-aware logic is `isControlPlaneMachine()` in
[../internal/controller/stackitmachine_controller.go](../internal/controller/stackitmachine_controller.go),
used to add/remove that machine's IP as an API-server load-balancer target
(`reconcileAPIServerLoadBalancerTarget` / `deleteAPIServerLoadBalancerTarget`).
This is therefore a black-box, cluster-operator-perspective test — there is
no CAPSTK-internal election code to test.

Prerequisites: see [SUMMARY.md](SUMMARY.md#shared-prerequisites), but with
`CONTROL_PLANE_MACHINE_COUNT=3`. No documented example used that value
before — this was its first validation.

**Important:** none of the templates under `templates/` define a
`MachineHealthCheck`. Without one, deleting a control-plane VM out-of-band
is **not** automatically detected or remediated — see step 4a. Step 4b
therefore performs a manual `kubectl delete machine`, which simulates the
expected operator remediation path, not something CAPI does on its own.

## Test steps

### 1. Create a 3-node control-plane cluster

```
$ export CONTROL_PLANE_MACHINE_COUNT=3
$ clusterctl generate cluster "${CLUSTER_NAME}" \
   --from templates/cluster-template.yaml \
   --target-namespace "${NAMESPACE}" > cluster.yaml

$ kubectl apply -f cluster.yaml

kubeadmcontrolplane.controlplane.cluster.x-k8s.io/stackit-workload-control-plane configured

$ kubectl get kubeadmcontrolplane stackit-workload-control-plane -o jsonpath='{.status.replicas} desired, {.status.readyReplicas} ready{"\n"}'

3 desired, 3 ready

$ kubectl --kubeconfig "${KUBECONF_WORKERCLUSTER}" get nodes -l node-role.kubernetes.io/control-plane

NAME                                   STATUS   ROLES           AGE     VERSION
stackit-workload-control-plane-6pqbg   Ready    control-plane   43s     v1.35.7
stackit-workload-control-plane-lw7gf   Ready    control-plane   4m26s   v1.35.7
stackit-workload-control-plane-qqb5n   Ready    control-plane   4h23m   v1.35.7
```

**Result:** Since the cluster already existed with 1 CP replica, applying
with `CONTROL_PLANE_MACHINE_COUNT=3` triggered an in-place scale-up rather
than a fresh create (`KubeadmControlPlane ... configured`). KCP scaled up
**one machine at a time** (`...lw7gf` first, then `...6pqbg`), each becoming
healthy/`Ready` before the next was created — roughly 8 minutes end-to-end
for 1→3. All 3 control-plane nodes `Ready` with the `control-plane` label.

**Note:** run node/lease checks against the **workload** cluster. Without
`--kubeconfig "${KUBECONF_WORKERCLUSTER}"` these commands return the kind
*management* cluster's own node (`capi-stackit-control-plane`) instead —
briefly confusing during this run.

---

### 2. Identify the current leader

```
$ kubectl --kubeconfig "${KUBECONF_WORKERCLUSTER}" -n kube-system get lease kube-controller-manager -o yaml

spec:
  holderIdentity: stackit-workload-control-plane-qqb5n_b919a6c5-fe25-4bb7-a6d5-190c2fd81c4c
  uid: 17a4a045-7180-44d6-93b2-bae051a80664   # the Lease object's own UID — NOT the STACKIT server ID

$ HOLDER="$(kubectl --kubeconfig "${KUBECONF_WORKERCLUSTER}" -n kube-system get lease kube-controller-manager -o jsonpath='{.spec.holderIdentity}')"
$ LEADER_NODE="${HOLDER%%_*}"
$ PROVIDER_ID="$(kubectl get machine "${LEADER_NODE}" -o jsonpath='{.spec.providerID}')"
$ SERVER_ID="${PROVIDER_ID#stackit://}"
$ echo "${LEADER_NODE} → ${SERVER_ID}"

stackit-workload-control-plane-qqb5n → 6f4e51c5-a12a-4fb7-99fb-bf9004182476
```

**Result:** Leader is `...qqb5n` (the original first control-plane machine).

**Gotcha:** `lease.metadata.uid` is the `Lease` object's own UID — constant
and unrelated to the holder. The current holder must be derived from
`lease.spec.holderIdentity` (format `<node-name>_<random-uid>`) and mapped
to its `Machine.spec.providerID` to get the STACKIT server ID.

---

### 3. Delete the leader's VM directly

Deleting the VM (not the `Machine` object) simulates a hard node failure,
independent of the graceful CAPI-driven deletion path covered in
[run1-3-deletion.md](run1-3-deletion.md).

```
$ kubectl --kubeconfig "${KUBECONF_WORKERCLUSTER}" get --raw='/readyz'

ok

$ stackit server delete 6f4e51c5-a12a-4fb7-99fb-bf9004182476 --assume-yes

Deleted server "stackit-workload-control-plane-qqb5n"

$ kubectl --kubeconfig "${KUBECONF_WORKERCLUSTER}" get --raw='/readyz'

ok
```

**Result:** VM deleted; kube-apiserver (via the load balancer, now backed by
the 2 remaining control-plane nodes) stayed reachable immediately before and
after.

---

### 4a. Health detection — no automatic remediation

```
$ kubectl --kubeconfig "${KUBECONF_WORKERCLUSTER}" get nodes

stackit-workload-control-plane-qqb5n   NotReady   control-plane   8h

$ kubectl get machine stackit-workload-control-plane-qqb5n -o yaml

  - message: Node is unreachable
    reason: PodInspectionFailed
    status: Unknown
    type: APIServerPodHealthy / ControllerManagerPodHealthy / EtcdPodHealthy / SchedulerPodHealthy
  - message: Node condition ... is Unknown
    reason: NodeConditionsFailed
    status: Unknown
    type: NodeHealthy
  phase: Running   # still "Running", not Failed/Deleting

$ kubectl get machinehealthcheck -A

No resources found
```

**Result — the significant finding of this package (not a defect in this
provider):** no `MachineHealthCheck` exists for this cluster. Without one,
Cluster API has no automatic remediation for a Machine whose backing
infrastructure died out-of-band: the dead `Machine` sat in `phase: Running`
with `Unknown` health conditions indefinitely (observed 6+ minutes, no
change), and the API-server load balancer **kept the dead machine as a
target** (confirmed via `stackit load-balancer describe
stackit-workload-apiserver`). So "replacement node" and "re-election" do
**not** happen on their own — they need an operator (or an MHC) to delete
the dead `Machine`.

---

### 4b. Manual remediation

```
$ kubectl delete machine stackit-workload-control-plane-qqb5n

machine.cluster.x-k8s.io "stackit-workload-control-plane-qqb5n" deleted from default namespace

$ kubectl get machines,stackitmachines

# qqb5n Machine and StackitMachine both gone immediately, no stuck finalizer

$ stackit load-balancer describe stackit-workload-apiserver -o json | grep -A20 targetPools

# dead machine's target removed; only lw7gf and 6pqbg remain

$ stackit server list

# no orphaned qqb5n VM/volume

$ kubectl get kubeadmcontrolplane stackit-workload-control-plane -o jsonpath='{.status.replicas} desired, {.status.readyReplicas} ready{"\n"}'

3 desired, 2 ready
# → replacement Machine ...6nfsm created shortly after (Provisioned → Running)

$ kubectl --kubeconfig "${KUBECONF_WORKERCLUSTER}" get nodes -l node-role.kubernetes.io/control-plane

stackit-workload-control-plane-6nfsm   Ready   control-plane   39s
stackit-workload-control-plane-6pqbg   Ready   control-plane   3h52m
stackit-workload-control-plane-lw7gf   Ready   control-plane   3h56m

$ kubectl --kubeconfig "${KUBECONF_WORKERCLUSTER}" -n kube-system get lease kube-controller-manager -o jsonpath='{.spec.holderIdentity}'

stackit-workload-control-plane-lw7gf_8ce1828e-98fd-4348-906d-1490248ada3a

$ stackit load-balancer describe stackit-workload-apiserver -o json | grep -A20 targetPools

# now lists all 3 current nodes: lw7gf, 6pqbg, 6nfsm

$ kubectl --kubeconfig "${KUBECONF_WORKERCLUSTER}" get --raw='/readyz'

ok
```

**Result:** Once the dead `Machine` was deleted manually, the StackitMachine
controller removed its API-server LB target cleanly with no leaked
VM/volume; KCP created a replacement control-plane Machine (`...6nfsm`)
which joined and reached `Ready` (3/3); a new leader was elected
(`...lw7gf`, different from the deleted node); the new machine's LB target
was added correctly. `readyz` stayed `ok` throughout. Recovery from manual
`kubectl delete machine` to 3/3 ready: roughly 2-3 minutes.

## Conclusion

**Status:** ⚠️ partial

What works (black-box verified):

- 3-node control-plane scale-up, one machine at a time.
- Killing a control-plane VM out-of-band does not take down the API server —
  the load balancer routes around it via the remaining healthy targets.
- Once the dead `Machine` is deleted (by an operator or an MHC), this
  provider correctly removes its LB target, leaves no leaked VM/volume, and
  adds the replacement node's LB target once it is up.
- Upstream CAPI/KCP creates the replacement Machine and a new leader is
  elected once 3 are healthy again.

What does **not** work automatically:

- **No `MachineHealthCheck` in any cluster template** — a Machine whose
  backing VM disappears out-of-band is never automatically detected and
  remediated. It sits indefinitely with `Unknown` health conditions while
  the API-server load balancer keeps routing to the dead target, until a
  human deletes the `Machine`. The original goal ("CAPI recreates a third
  control-plane node" / "re-election works") therefore only holds **after
  manual intervention**.

**Recommendation:** if automatic self-healing on control-plane VM failure is
a requirement, add a `MachineHealthCheck` to the cluster templates. This is
a coverage gap to decide on, not a reconciler bug — remediation is
intentionally opt-in in upstream Cluster API.

---

## Addendum (2026-08-10, after run 2)

The conclusion above ("no automatic remediation") is correct at the Kubernetes
level but **incomplete at the infrastructure level**. Step 4a here inspected
only Kubernetes objects and the load balancer; it never ran `stackit server
list`. Run 2 repeated the same scenario and found that the `StackitMachine`
reconciler had silently created a *replacement VM* that could never rejoin the
cluster — see [machine-recreate-bug.md](machine-recreate-bug.md) and
[run2-2-ha-controlplane.md](run2-2-ha-controlplane.md).

The two runs do not contradict each other; the silent recreate was simply not
visible from the commands used here.
