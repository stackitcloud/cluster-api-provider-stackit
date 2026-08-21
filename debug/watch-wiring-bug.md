# Bugs: watch wiring — objects that never get re-reconciled

Date: 2026-08-14, updated 2026-08-21
Source: Copilot review on [PR #1](https://github.com/stackitcloud/cluster-api-provider-stackit/pull/1), verified against the code here
Status: ✅ **fixed** — defect 2 on 2026-08-19, defect 1 on 2026-08-20 and
reworked on 2026-08-21 after the PR #17 review

Two defects where a change that *should* trigger a reconcile does not, so the
affected object stays in a stale state until something unrelated wakes it up.
Both were found by reading the watch setup; neither is visible in any of the
manual runs in this folder, because both need a specific configuration or
recovery sequence to show up.

## 1. Correcting an invalid credentials Secret does not re-reconcile the cluster — ✅ fixed (2026-08-20)

**Location:** [`controller/stackitcluster_controller.go`](../controller/stackitcluster_controller.go),
`SetupWithManager`, and `stackitClusterRequestsForCloudInitRef` (same file).

The investigation below describes the state **before** the fix; see
[Fix as implemented](#fix-as-implemented-2026-08-20-reworked-2026-08-21) at the end of this section.

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

### What the CAPI research added (2026-08-19)

Checked against CAPA, CAPG, CAPO, CAPV and CAPH, plus CAPI v1.13.2 from the
module cache for the contract and its helpers. Four things change how this should
be fixed — the fourth bullet was later struck, see there.

- **There is no upstream helper.** `util.SecretToInfrastructureMapFunc` does not
  exist in v1.13.2 (verified by grep); `util/util.go` offers only
  `ClusterToInfrastructureMapFunc`, `MachineToInfrastructureMapFunc` and
  `ClusterToTypedObjectsMapper`. The mapper has to be written here.
- **No provider watches its credentials Secret** — not CAPA, CAPG, CAPO, CAPV or
  CAPH. This is an ecosystem-wide gap rather than an oversight in this repo.
  CAPA is worse off: `pkg/cloud/scope/session.go` caches sessions with no
  invalidation on Secret change, so a corrected Secret may not take effect there
  even after a reconcile. Do not copy any of them.
- ~~**The reference implementation lives in CAPI itself**~~ — this pointed at the
  `ClusterResourceSet` controller and its metadata-only Secret cache. **Struck on
  2026-08-21:** Cluster API core is not a reference for an infrastructure
  provider, see
  [SUMMARY.md](SUMMARY.md#cluster-api-core-is-not-a-reference-for-this-repository).
  It was also the source of item 15, the other finding that did not survive
  review. The providers were the evidence that mattered, and the line above
  already stated what they do.
- **The existing cloud-init mapper has a second, undocumented defect for this
  purpose:** it lists only within `obj.GetNamespace()`
  (`stackitcluster_controller.go:139`). `CredentialsSecretRef` is a
  `corev1.SecretReference` whose `Namespace` is optional
  (`api/v1alpha1/stackitcluster_types.go:46-48`, field on line 48), so a credentials mapper has to
  search across namespaces. `util.CredentialsSecretKey`
  (`util/reconcile.go:84-93`) already resolves that defaulting and should be
  reused.

This fix therefore belongs together with the Secret-caching item in
[SUMMARY.md](SUMMARY.md) — both change the same `Watches(&corev1.Secret{}, …)`
call, and doing them separately means touching it twice.

> The conclusion above did not survive: the fix that landed touches no watch at
> all, so the two items turned out to be independent. Kept as recorded.

### Fix as implemented (2026-08-20, reworked 2026-08-21)

**What is in the tree now: a requeue, no watch.** `CredentialFailureResult`
(`util/conditions.go`) takes a `requeueAfter` parameter and returns
`ctrl.Result{RequeueAfter: …}` instead of a bare result for the invalid-credential
cases. The callers pass `credentialsRetryRequeueAfter` (1 minute), long enough not
to be a hot loop and short enough that an operator sees the effect of a
correction. Nothing else was needed: the reconcile already reads the Secret on
every pass, so the missing piece was only the trigger.

Two specs cover it, one per reconciler — *"requeues on unauthorized credentials
and recovers once they are corrected"* — reconciling with a failing credentials
factory, asserting the requeue and the `CredentialsInvalid` conditions, then
swapping in a working factory and asserting `Ready`. Both were proven to fail
with the requeue removed. They replace the previous mapper specs and test the
defect end to end rather than the watch wiring.

**The first attempt, and why it was dropped.** The original fix added a second
`Watches(&corev1.Secret{}, …)` backed by a mapper
`stackitClusterRequestsForCredentialsSecret`, which listed StackitClusters across
all namespaces and compared `util.CredentialsSecretKey` — necessary because
`credentialsSecretRef.namespace` is optional, the one thing the cloud-init mapper
gets wrong for this purpose. The PR #17 review asked to avoid that complexity and
read the Secret on every reconcile instead. Reading was never the gap, but the
conclusion held: a requeue achieves the same recovery without a mapper, a second
watch or the cross-namespace list. The watch, the mapper and its three specs are
gone.

The requeue also removes the reason the machine side was ever a question. It
reaches its credentials through the `StackitCluster`, which now retries on its
own, and the existing `stackitMachineRequestsForStackitCluster` re-enqueues the
Machines once that object changes.

**Not attempted, and now moot for this defect:** watching Secrets as
`PartialObjectMetadata`, and CAPI's label selector on the informer. The selector
would not have worked here in any case — CAPI only watches Secrets it labels
itself, whereas the credentials Secret is created by the user with
`kubectl create secret generic` and carries no labels. See
[SUMMARY.md](SUMMARY.md#how-the-other-providers-solve-the-secret-cache) for the
full comparison and what the cache trade costs.

---

## 2. The StackitCluster → Machine watch matches on the wrong name — ✅ fixed (2026-08-19)

**Location:** [`controller/stackitmachine_watches.go`](../controller/stackitmachine_watches.go),
`stackitMachineRequestsForStackitCluster`.

The code below is the state **before** the fix.

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

**Fix — ✅ done (2026-08-19).** The mapper now resolves the owning `Cluster` with
`clusterutil.GetOwnerCluster` and matches against *its* name, and it lets the API
server filter via `client.MatchingLabels{clusterv1.ClusterNameLabel: cluster.Name}`
instead of listing every `Machine` in the namespace. This is the shape CAPG uses
in `GCPClusterToGCPMachines` and CAPA in `AWSClusterToAWSMachines`.

Two CAPA details were deliberately left out as unnecessary here: a short-circuit
when the infrastructure object already carries a deletion timestamp, and a filter
on `InfrastructureRef.Kind` — the existing `stackitMachineRequestForMachine`
already does the latter.

**The defect was not hypothetical.** Every existing spec passed because
`createOwnerCluster` gives the `Cluster` and its `infrastructureRef` the same
name. A ClusterClass-generated `infrastructureRef` never does — it carries a
random suffix — so topology clusters were broken systematically, not just in
contrived naming setups. Guarded by *"maps StackitCluster events when the
StackitCluster name differs from the Cluster name"* in
`controller/stackitmachine_controller_test.go`, which was proven to fail without
the fix.

**Still missing, and worth doing with any follow-up here:** CAPA and CAPG also
register a `Cluster` watch built from
`util.ClusterToTypedObjectsMapper(r.Client, &infrav1.StackitMachineList{}, scheme)`
and gated by `predicates.ClusterPausedTransitionsOrInfrastructureProvisioned`,
so infra machines learn when the cluster becomes provisioned or is unpaused.
This provider relies on the `StackitCluster` watch alone.

**v1beta1 → v1beta2 traps** for whoever picks this up — this repo imports
`sigs.k8s.io/cluster-api/api/core/v1beta2`, so older blog posts are unusable:
`util.ClusterToObjectsMapper` was **removed** (use `ClusterToTypedObjectsMapper`,
which keeps list calls cached); `predicates.ClusterUnpausedAndInfrastructureProvisioned`
and `ClusterCreateInfraProvisioned` are **deprecated** (use
`ClusterPausedTransitionsOrInfrastructureProvisioned`);
`Cluster.Status.InfrastructureReady` became
`Status.Initialization.InfrastructureProvisioned`; and `cluster.Spec.Paused` is
now a `*bool`. CAPA and CAPG disagree here — CAPA still uses the deprecated
predicate, CAPG the current one. Follow CAPG.

---

## Why the runs never caught either

Both need a setup the runs never produced: the first needs a
credentials-recovery sequence, the second needs a `StackitCluster` whose name
differs from its `Cluster`. Both are cheap to cover at the
[envtest level](test-strategy.md) — the fake cloud client is not involved in
either, only the watch wiring and reconcile gating. Defect 2 was closed exactly
that way on 2026-08-19, defect 1 on 2026-08-20.

## Correction to the v1beta2 notes above (2026-08-20)

One claim in the *"v1beta1 → v1beta2 traps"* list needs qualifying, because
fixing item 11 in [SUMMARY.md](SUMMARY.md) depended on it: the wrapped error
`clusterutil.GetOwnerCluster` returns for a deleted owner **is** detectable with
`apierrors.IsNotFound`. Both wrapper types in `github.com/pkg/errors` v0.9.1
(`withStack` and `withMessage`) implement `Unwrap()`, and
`reasonAndCodeForError` in `k8s.io/apimachinery` v0.35.4 uses `errors.As`, which
follows the chain. Verified by reading both modules' source. No
`errors.Cause`-style unwrapping is needed anywhere for this.
