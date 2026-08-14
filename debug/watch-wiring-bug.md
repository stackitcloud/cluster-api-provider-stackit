# Bugs: watch wiring — objects that never get re-reconciled

Date: 2026-08-14
Source: Copilot review on [PR #1](https://github.com/stackitcloud/cluster-api-provider-stackit/pull/1), verified against the code here
Status: ⚠️ **open** — both confirmed in code, neither fixed

Two defects where a change that *should* trigger a reconcile does not, so the
affected object stays in a stale state until something unrelated wakes it up.
Both were found by reading the watch setup; neither is visible in any of the
manual runs in this folder, because both need a specific configuration or
recovery sequence to show up.

## 1. Correcting an invalid credentials Secret does not re-reconcile the cluster

**Location:** [`controller/stackitcluster_controller.go`](../controller/stackitcluster_controller.go),
`SetupWithManager`, and `stackitClusterRequestsForCloudInitRef` (same file).

```go
Watches(&corev1.Secret{}, handler.EnqueueRequestsFromMapFunc(r.stackitClusterRequestsForCloudInitRef)).
```

The `Secret` watch is wired to the **cloud-init** mapper. That mapper only
looks at `cluster.Spec.Bastion.CloudInitRef` — it never consults
`credentialsSecretRef`. So a change to the credentials Secret enqueues nothing.

That matters because of how credential failures are handled
([`util/conditions.go:84`](../util/conditions.go), `CredentialFailureResult`):

```go
if cloud.IsUnauthorized(err) || cloud.IsInvalidInput(err) || errors.Is(err, ErrCredentialsInvalid) {
    return ctrl.Result{}, nil     // no error, no requeue — deliberately
}
```

Returning without a requeue is the right call on its own: retrying invalid
credentials in a hot loop helps nobody. But it only works if *fixing* the
credentials triggers a reconcile — and that is exactly the trigger that is
missing.

**Effect:** deploy a `StackitCluster` with a bad credentials Secret, then
correct the Secret. The cluster stays `NotReady` indefinitely; nothing in the
watch set notices. A `StackitCluster` edit, a `Cluster` event or a manager
restart eventually clears it, which makes the symptom look intermittent.

**Fix:** add a mapper for `credentialsSecretRef` that also honours
`credentialsSecretRef.namespace` (the field is optional and defaults to the
`StackitCluster` namespace), and cover recovery-after-invalid-Secret in a test.

**Note:** the same reasoning applies to `StackitMachine`, which reaches its
credentials through the `StackitCluster`. Worth checking as part of the fix.

---

## 2. The StackitCluster → Machine watch matches on the wrong name

**Location:** [`controller/stackitmachine_watches.go`](../controller/stackitmachine_watches.go),
`stackitMachineRequestsForStackitCluster`.

```go
return stackitMachineRequestsForMachines(machines.Items, func(machine clusterv1.Machine) bool {
    return machine.Spec.ClusterName == stackitCluster.Name
})
```

`Machine.spec.clusterName` names the owning **CAPI `Cluster`**, not its
infrastructure object. The predicate therefore only holds while the
`StackitCluster` happens to carry the same name as the `Cluster`.

Nothing enforces that. `Cluster.spec.infrastructureRef.name` may be anything;
the templates in `templates/` merely use `${CLUSTER_NAME}` for both, which is
why every run in this folder passed without noticing.

**Effect:** with a `Cluster` named `foo` and an infrastructureRef pointing at a
`StackitCluster` named `foo-infra`, updates to that `StackitCluster` enqueue no
machines. Most visibly, the machines never learn that the cluster became ready
— and machine reconciliation is gated on exactly that
(`StackitCluster.Status.Ready`), so they stall until another event arrives.

**Fix:** resolve the `StackitCluster`'s owning `Cluster` (via its owner
reference, or by matching each `Cluster`'s `infrastructureRef`) and compare
against that name instead.

---

## Why the runs never caught either

Both need a setup the runs never produced: the first needs a
credentials-recovery sequence, the second needs a `StackitCluster` whose name
differs from its `Cluster`. Both are cheap to cover at the
[envtest level](test-strategy.md) — the fake cloud client is not involved in
either, only the watch wiring and reconcile gating.
