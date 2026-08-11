# Regression: HA control-plane (3 nodes, leader deletion, re-election)

Date: 2026-08-07
Run: 2 of 2 — see [SUMMARY.md](SUMMARY.md#timeline)
Cluster: `stackit-capi-test` (scaled up from the 1-CP cluster used in [run2-1-bootstrapping.md](run2-1-bootstrapping.md))
Status: ⚠️ partial — works, but only *after* manual remediation (no `MachineHealthCheck` ships with any template); a surprising intermediate self-healing behavior was found that does **not** by itself restore the cluster

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
`CONTROL_PLANE_MACHINE_COUNT=3`.

**Important:** none of the templates under `templates/` define a
`MachineHealthCheck`. Without one, deleting a control-plane VM out-of-band
is **not** automatically detected or remediated at the Kubernetes level —
see step 4a/4a2. Step 4b therefore performs a manual `kubectl delete
machine`, which simulates the expected operator remediation path, not
something CAPI does on its own.

## Test steps

### 1. Scale the existing cluster up to 3 control-plane replicas

```
$ export CONTROL_PLANE_MACHINE_COUNT=3
$ clusterctl generate cluster "${CLUSTER_NAME}" \
   --from templates/cluster-template.yaml \
   --target-namespace "${NAMESPACE}" > cluster.yaml

$ kubectl apply -f cluster.yaml

kubeadmcontrolplane.controlplane.cluster.x-k8s.io/stackit-capi-test-control-plane configured

$ kubectl get kubeadmcontrolplane stackit-capi-test-control-plane -o jsonpath='{.status.replicas} desired, {.status.readyReplicas} ready{"\n"}'   # polled every 30s

2 desired, 1 ready    # t+30s .. t+150s
3 desired, 2 ready    # t+180s .. t+330s
3 desired, 3 ready    # t+360s

$ kubectl --kubeconfig "${KUBECONF_WORKERCLUSTER}" get nodes -l node-role.kubernetes.io/control-plane

NAME                                    STATUS   ROLES           AGE     VERSION
stackit-capi-test-control-plane-bkkfk   Ready    control-plane   32m     v1.35.7
stackit-capi-test-control-plane-qrc5f   Ready    control-plane   50s     v1.35.7
stackit-capi-test-control-plane-vjvch   Ready    control-plane   3m43s   v1.35.7
```

**Result:** KCP scaled 1→3 **one machine at a time** (`...vjvch` first, then
`...qrc5f`), each becoming healthy/`Ready` before the next was created —
about 6 minutes end-to-end. All 3 control-plane nodes `Ready` with the
`control-plane` label.

---

### 2. Identify the current leader

```
$ kubectl --kubeconfig "${KUBECONF_WORKERCLUSTER}" -n kube-system get lease kube-controller-manager -o yaml

spec:
  holderIdentity: stackit-capi-test-control-plane-bkkfk_44b4c996-ec50-46a2-8013-c4d4f3fd9cee
  uid: b331d301-7ce5-4f8a-9785-cf9a27c8baeb   # the Lease object's own UID — NOT the STACKIT server ID

$ HOLDER="$(kubectl --kubeconfig "${KUBECONF_WORKERCLUSTER}" -n kube-system get lease kube-controller-manager -o jsonpath='{.spec.holderIdentity}')"
$ LEADER_NODE="${HOLDER%%_*}"
$ PROVIDER_ID="$(kubectl get machine "${LEADER_NODE}" -o jsonpath='{.spec.providerID}')"
$ SERVER_ID="${PROVIDER_ID#stackit://}"
$ echo "${LEADER_NODE} → ${SERVER_ID}"

stackit-capi-test-control-plane-bkkfk → 03f42a7c-905c-4f16-a6eb-4cfd1f97ff2e
```

**Result:** Leader is `...bkkfk` — the original first control-plane machine
from [run2-1-bootstrapping.md](run2-1-bootstrapping.md), i.e. the
machine that was **not** cordoned in that document's scale-down test.

---

### 3. Delete the leader's VM directly

Deleting the VM (not the `Machine` object) simulates a hard node failure,
independent of the graceful CAPI-driven deletion path covered in
[run2-3-deletion.md](run2-3-deletion.md).

```
$ kubectl --kubeconfig "${KUBECONF_WORKERCLUSTER}" get --raw='/readyz'

ok

$ stackit server delete 03f42a7c-905c-4f16-a6eb-4cfd1f97ff2e --assume-yes

Deleted server "stackit-capi-test-control-plane-bkkfk"

$ kubectl --kubeconfig "${KUBECONF_WORKERCLUSTER}" get --raw='/readyz'

ok
```

**Result:** VM deleted; kube-apiserver (via the load balancer, now backed by
the 2 remaining control-plane nodes) stayed reachable immediately before and
after.

---

### 4a. Unexpected finding — the infrastructure layer silently recreates the missing VM

```
$ kubectl get stackitmachine stackit-capi-test-control-plane-bkkfk \
  -o jsonpath='{.status.instanceState} {.status.instanceID}{"\n"}'   # ~1min after delete

CREATING 41ae67cf-3515-470f-a5db-0ed0c3043fe2   # a NEW instance ID, same StackitMachine object

$ stackit server list | grep capi-test-control-plane-bkkfk

41ae67cf-3515-470f-a5db-0ed0c3043fe2 │ stackit-capi-test-control-plane-bkkfk │ ACTIVE │ c2i.4 │ eu01-m │ 10.42.0.78

$ stackit server describe 41ae67cf-3515-470f-a5db-0ed0c3043fe2

BOOT VOLUME  75270e5a-fc57-4aff-9f2e-9468a26e15d6   # a fresh volume, not reused from the deleted VM
CREATED AT   2026-08-07T22:29:14Z

$ kubectl --kubeconfig "${KUBECONF_WORKERCLUSTER}" get node stackit-capi-test-control-plane-bkkfk \
  -o jsonpath='{.status.addresses}{"\n"}'   # t+9min after the new VM's creation

[{"address":"10.42.0.34","type":"InternalIP"},{"address":"stackit-capi-test-control-plane-bkkfk","type":"Hostname"}]
# ^ still the OLD IP — the Node object was never updated by a live kubelet from the new VM

$ kubectl --kubeconfig "${KUBECONF_WORKERCLUSTER}" get node stackit-capi-test-control-plane-bkkfk \
  -o jsonpath='{range .status.conditions[?(@.type=="Ready")]}{.lastHeartbeatTime} {.status} {.reason}{"\n"}{end}'

2026-08-07T22:34:41Z False KubeletNotReady   # frozen — checked again 2m40s later, unchanged
```

**Result — the most significant finding of this run:** without any
`MachineHealthCheck` and without any operator action, the
**`StackitMachine` infrastructure reconciler detected the missing STACKIT
server and silently recreated it** (new instance ID, new boot volume, new
IP) under the same `StackitMachine`/`Machine` object. This looks like
self-healing at first — but the **new VM never actually rejoined the
Kubernetes cluster**: the `Node` object's `Ready` heartbeat froze at the
moment the old VM died and never updated again, even 9 minutes after the
replacement VM reached `ACTIVE` (well over the ~1-3 minutes normal join
takes per [run2-1-bootstrapping.md](run2-1-bootstrapping.md)).

**Root cause, confirmed in code:** `ensureServer()` in
[../internal/controller/stackitmachine_controller.go:293](../internal/controller/stackitmachine_controller.go)
does, on every reconcile, `GetServer(instanceID)`; if that returns
`NotFound` it falls through to `FindServerByTags` and then straight to
`CreateServer(...)` — unconditionally, with no check for whether this
Machine had already successfully bootstrapped/joined before. The new server
is created with the **same bootstrap `UserData`** that was used for the
original join, so the freshly booted VM replays the same one-time kubeadm
join data — which, for an already-joined control-plane machine, can no
longer succeed. **Net effect: the same operational gap as "no automatic
remediation"** — a human still has to intervene — but the failure mode is
more confusing than a simple stuck/`Unknown` machine, because the
infrastructure layer is actively (and, per this code path, pointlessly)
recreating VMs in the background for any Machine whose backing server
disappears, no matter how long it had already been running.

---

### 4a2. Health detection at the Machine/KCP level

```
$ kubectl get machine stackit-capi-test-control-plane-bkkfk -o yaml

status:
  phase: Running   # still "Running", not Failed/Deleting
  conditions:
  - type: NodeHealthy
    status: "False"
    message: 'Node.AllConditions: Kubelet stopped posting node status.'
  - type: EtcdMemberHealthy
    status: "True"   # stale — from before the VM was deleted; not re-checked since

$ kubectl get machinehealthcheck -A

No resources found

$ kubectl get kubeadmcontrolplane stackit-capi-test-control-plane \
  -o jsonpath='{.status.replicas} desired, {.status.readyReplicas} ready{"\n"}'

3 desired, 2 ready
```

**Result:** Confirms the known gap from prior runs: no `MachineHealthCheck`
exists for this cluster, so CAPI has no automatic remediation for a Machine
whose Kubernetes-level health has failed — regardless of what the
infrastructure layer does underneath it. `KubeadmControlPlane` correctly
reports `2 ready` out of `3 desired` but does not act on its own.

---

### 4b. Manual remediation

```
$ kubectl delete machine stackit-capi-test-control-plane-bkkfk

machine.cluster.x-k8s.io "stackit-capi-test-control-plane-bkkfk" deleted from default namespace

$ kubectl get machines,stackitmachines   # ~10s later

# bkkfk Machine and StackitMachine both gone, no stuck finalizer;
# qrc5f, vjvch, md-0-xcc5l unaffected

$ stackit server list | grep capi-test-control-plane-bkkfk

(none found)   # the auto-recreated VM+volume from step 4a were removed along with the Machine

$ stackit load-balancer describe stackit-capi-test-apiserver -o json | grep -A15 targetPools

"targets": [
  {"displayName": "stackit-capi-test-control-plane-vjvch", "ip": "10.42.0.252"},
  {"displayName": "stackit-capi-test-control-plane-qrc5f", "ip": "10.42.0.97"}
]
# dead machine's target removed; only the 2 healthy nodes remain

$ kubectl get kubeadmcontrolplane stackit-capi-test-control-plane -o jsonpath='{.status.replicas} desired, {.status.readyReplicas} ready{"\n"}'   # polled every 20s

3 desired, 2 ready   # t+20s .. t+180s
3 desired, 3 ready   # t+200s
# → replacement Machine ...gxz6c created and joined

$ kubectl --kubeconfig "${KUBECONF_WORKERCLUSTER}" get nodes -l node-role.kubernetes.io/control-plane

NAME                                    STATUS   ROLES           AGE   VERSION
stackit-capi-test-control-plane-gxz6c   Ready    control-plane   30s   v1.35.7
stackit-capi-test-control-plane-qrc5f   Ready    control-plane   20m   v1.35.7
stackit-capi-test-control-plane-vjvch   Ready    control-plane   23m   v1.35.7

$ kubectl --kubeconfig "${KUBECONF_WORKERCLUSTER}" -n kube-system get lease kube-controller-manager -o jsonpath='{.spec.holderIdentity}'

stackit-capi-test-control-plane-vjvch_f9ab43d7-0721-47e4-beeb-57ac08d88607

$ stackit load-balancer describe stackit-capi-test-apiserver -o json | grep -A20 targetPools

"targets": [
  {"displayName": "stackit-capi-test-control-plane-vjvch", "ip": "10.42.0.252"},
  {"displayName": "stackit-capi-test-control-plane-qrc5f", "ip": "10.42.0.97"},
  {"displayName": "stackit-capi-test-control-plane-gxz6c", "ip": "10.42.0.243"}
]
# now lists all 3 current nodes

$ kubectl --kubeconfig "${KUBECONF_WORKERCLUSTER}" get --raw='/readyz'

ok
```

**Result:** Once the stuck `Machine` was deleted manually, the
`StackitMachine` controller removed its API-server LB target cleanly, and
the previously auto-recreated (but never-joined) VM and its boot volume were
removed with it — **no leaked VM/volume**. KCP created a genuine replacement
control-plane Machine (`...gxz6c`) which joined and reached `Ready` (3/3); a
new leader was elected (`...vjvch`, different from the deleted node);
the new machine's LB target was added correctly. `readyz` stayed `ok`
throughout. Recovery from `kubectl delete machine` to 3/3 ready: about 3.5
minutes.

## Conclusion

**Status:** ⚠️ partial

What works (black-box verified):

- 3-node control-plane scale-up, one machine at a time.
- Killing a control-plane VM out-of-band does not take down the API server —
  the load balancer routes around it via the remaining healthy targets.
- Once the dead `Machine` is deleted (by an operator or an MHC), this
  provider correctly removes its LB target, leaves no leaked VM/volume
  (including any VM the infrastructure layer had recreated in the
  meantime), and adds the replacement node's LB target once it is up.
- Upstream CAPI/KCP creates the replacement Machine and a new leader is
  elected once 3 are healthy again.

What does **not** work automatically, and a new finding:

- **No `MachineHealthCheck` in any cluster template** — same gap as before:
  a Machine whose backing VM disappears out-of-band is never automatically
  detected and remediated at the Kubernetes level.
- **New finding — the `StackitMachine` reconciler silently recreates a
  missing server**, even for a Machine that had already successfully
  joined and been running for a long time. On this evidence, that recreated
  VM never actually rejoins the cluster (frozen `Ready` heartbeat 9+ minutes
  after the replacement reached `ACTIVE`) — so the recreation does not
  restore the cluster, it just consumes another VM+volume silently in the
  background until an operator deletes the `Machine`. This is worth a
  closer look at
  [../internal/controller/stackitmachine_controller.go](../internal/controller/stackitmachine_controller.go)
  to decide whether "instance not found → recreate" is the intended
  behavior for an already-`Ready` machine, or whether it should instead
  surface a terminal error for CAPI/an MHC to act on.

**Recommendation:** if automatic self-healing on control-plane VM failure is
a requirement, add a `MachineHealthCheck` to the cluster templates — and
first confirm whether the silent VM-recreation behavior above is intentional,
since it currently just delays and obscures the point at which a human
needs to intervene.

---

## Addendum (2026-08-10, after code review)

The silent VM recreation found in step 4a is tracked in full — with the code
path, the operational impact and fix options — in
[machine-recreate-bug.md](machine-recreate-bug.md).
