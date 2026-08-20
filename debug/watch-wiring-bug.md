# Bugs: watch wiring — objects that never get re-reconciled

Date: 2026-08-14, updated 2026-08-20
Source: Copilot review on [PR #1](https://github.com/stackitcloud/cluster-api-provider-stackit/pull/1), verified against the code here
Status: ✅ **fixed** — defect 2 on 2026-08-19, defect 1 on 2026-08-20

Two defects where a change that *should* trigger a reconcile does not, so the
affected object stays in a stale state until something unrelated wakes it up.
Both were found by reading the watch setup; neither is visible in any of the
manual runs in this folder, because both need a specific configuration or
recovery sequence to show up.

## 1. Correcting an invalid credentials Secret does not re-reconcile the cluster — ✅ fixed (2026-08-20)

**Location:** [`controller/stackitcluster_controller.go`](../controller/stackitcluster_controller.go),
`SetupWithManager`, and `stackitClusterRequestsForCloudInitRef` (same file).

The investigation below describes the state **before** the fix; see
[Fix as implemented](#fix-as-implemented-2026-08-20) at the end of this section.

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

Checked against CAPI v1.13.2 read from the module cache, plus CAPA, CAPG, CAPO,
CAPV and CAPH. Four things change how this should be fixed:

- **There is no upstream helper.** `util.SecretToInfrastructureMapFunc` does not
  exist in v1.13.2 (verified by grep); `util/util.go` offers only
  `ClusterToInfrastructureMapFunc`, `MachineToInfrastructureMapFunc` and
  `ClusterToTypedObjectsMapper`. The mapper has to be written here.
- **No provider watches its credentials Secret** — not CAPA, CAPG, CAPO, CAPV or
  CAPH. This is an ecosystem-wide gap rather than an oversight in this repo.
  CAPA is worse off: `pkg/cloud/scope/session.go` caches sessions with no
  invalidation on Secret change, so a corrected Secret may not take effect there
  even after a reconcile. Do not copy any of them.
- **The reference implementation lives in CAPI itself** — the
  `ClusterResourceSet` controller
  (`internal/controllers/clusterresourceset/clusterresourceset_controller.go:74-113`)
  watches Secrets through
  `WatchesRawSource(source.Kind(partialSecretCache, &metav1.PartialObjectMetadata{…}))`
  gated by `predicates.TypedResourceIsChanged`, against a dedicated
  metadata-only cache built in CAPI's `main.go:515-542`. Only names, labels and
  resourceVersion are cached; credential bytes never enter the informer.
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

### Fix as implemented (2026-08-20)

A second `Watches(&corev1.Secret{}, …)` on the cluster controller, backed by a
new mapper `stackitClusterRequestsForCredentialsSecret`. The existing cloud-init
mapper was left untouched, so nothing about the bastion path changed.

The mapper lists StackitClusters **without a namespace filter** and compares
each one's `util.CredentialsSecretKey` against the Secret's key — that is the
one thing the cloud-init mapper gets wrong for this purpose, and reusing
`CredentialsSecretKey` also inherits the `credentialsSecretRef.namespace`
defaulting for free. `config/rbac/role.yaml` is already a `ClusterRole` with
`secrets` and `stackitclusters` at `get;list;watch`, so `make manifests`
produces no diff.

Guarded by three specs in `controller/stackitcluster_controller_test.go` —
*"maps credentials Secret events to StackitCluster reconcile requests"*, *"maps
credentials Secret events from a different namespace than the StackitCluster"*
and *"ignores Secret events that no StackitCluster uses as credentials"*. Both
positive specs were proven to fail when pointed at the old cloud-init mapper.

**Two deliberate omissions.**

- *No Secret watch on the machine side*, contrary to the note above. The machine
  controller already watches its `StackitCluster`, and this fix makes a
  corrected Secret reconcile that object — which bumps its `resourceVersion` via
  `clusterScope.PatchObject` — so `stackitMachineRequestsForStackitCluster`
  re-enqueues the affected Machines through the existing chain. Adding a second
  path would be redundant. Note this is an inference from the watch wiring: the
  envtest suite calls `Reconcile` directly and never exercises the manager's
  informers, so it cannot prove the chain end to end.
- *No `PartialObjectMetadata` watch and no label selector.* The Secret-caching
  item (15) was fixed first and independently, in `cmd/manager/main.go`: a
  `cache.Options` `Transform` nils out `Data` and the managed fields before
  anything enters the informer, and `client.Options.Cache.DisableFor` sends the
  reads that need the real bytes to the API server. That covers every Secret
  this provider watches, not just the credentials one, so switching this watch
  to metadata-only would add little. CAPI's label selector was deliberately
  **not** copied: it works there because CAPI only watches Secrets it labels
  itself, whereas the credentials Secret here is created by the user with
  `kubectl create secret generic` and carries no labels — a selector would have
  silently disabled this very watch. See
  [SUMMARY.md](SUMMARY.md#how-the-other-providers-solve-the-secret-cache) for
  the full comparison against CAPA, CAPG and CAPI core, and what the trade costs.

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
