# Debug & regression notes

Entry point for this folder. Two branches have been tested: `main` (two runs)
and `refactor/code-cleanup-and-proper-abstraction` (one run).

## Timeline

Read top to bottom; this is the order in which everything happened.

| # | When | Branch | Cluster | What | Documents |
| --- | --- | --- | --- | --- | --- |
| **0** | 2026-07-31 … 08-03 | `main` | `stackit-workload` | Initial bug investigation, before any structured run | [deletion-bug.md](deletion-bug.md) |
| **1** | 2026-08-05 | `main` | `stackit-workload` | **Run main1** — bootstrapping → HA → deletion | [run-main1-1-bootstrapping.md](run-main1-1-bootstrapping.md) · [run-main1-2-ha-controlplane.md](run-main1-2-ha-controlplane.md) · [run-main1-3-deletion.md](run-main1-3-deletion.md) |
| **1e** | 2026-08-07 (earlier) | `main` | `stackit-capi-test` | **Run main1, extra** — bastion, on its own cluster | [run-main1-4-bastion.md](run-main1-4-bastion.md) |
| **2** | 2026-08-07 (later) | `main` | `stackit-capi-test` | **Run main2** — same three packages, from scratch | [run-main2-1-bootstrapping.md](run-main2-1-bootstrapping.md) · [run-main2-2-ha-controlplane.md](run-main2-2-ha-controlplane.md) · [run-main2-3-deletion.md](run-main2-3-deletion.md) |
| **2e** | 2026-08-07 (later) | `main` | `stackit-capi-test` | **Run main2, extra** — bastion, blocked at SSH | [run-main2-4-bastion.md](run-main2-4-bastion.md) |
| **3** | 2026-08-10 | — | — | Code review of both `main` runs; defects written up | [bastion-bug.md](bastion-bug.md) · [machine-recreate-bug.md](machine-recreate-bug.md) |
| **4** | 2026-08-11 | `refactor` | `stackit-capi-test` | **Run refactor1** — all four packages against the refactored provider | [run-refactor1-1-bootstrapping.md](run-refactor1-1-bootstrapping.md) · [run-refactor1-2-ha-controlplane.md](run-refactor1-2-ha-controlplane.md) · [run-refactor1-3-deletion.md](run-refactor1-3-deletion.md) · [run-refactor1-4-bastion.md](run-refactor1-4-bastion.md) |
| **5** | 2026-08-12 | `refactor` | e2e fixtures | **Run refactor2** — first execution of the e2e suite on any branch | [run-refactor2-1-e2e.md](run-refactor2-1-e2e.md) |
| **6** | 2026-08-14 | `refactor` | — | External Copilot review on PR #1; findings verified against the code | [watch-wiring-bug.md](watch-wiring-bug.md) · [deletion-bug.md](deletion-bug.md#related-two-more-orphan-leak-paths-on-deletion-2026-08-14) |

Each `run-*` document is a complete protocol of its own run. Where a later
review corrected a conclusion, the protocol is left as recorded and an
`## Addendum` at the end points to the relevant bug document. The
`run-refactor1-*` documents additionally carry a `Vergleich main:` line per
step.

## Status matrix

| Topic | Run main1 (08-05/07) | Run main2 (08-07) | Run refactor1 (08-11) |
| --- | --- | --- | --- |
| Bootstrapping & networking | ✅ (cluster pre-existed) | ✅ (from scratch) | ✅ identical |
| HA control-plane | ⚠️ partial | ⚠️ partial + silent recreate found | ⚠️ partial + **worse variant** of the same defect |
| Deletion & cleanup | ✅ | ✅ | ✅ identical |
| Bastion / jump host | ✅ full path | ⚠️ blocked at SSH | ✅ full path + open SSH question resolved |

## Refactor parity

The refactor is a pure code move — `internal/controller/` → `controller/`,
`pkg/cloud/` → `cloud/`, `pkg/scope/` → `scope/`, `pkg/util/` → `util/`, plus a
split of the controllers into several files. **No behavioural change was
found.** Every known defect reproduces from the same, relocated code:

| Known defect | Location on `refactor` | Reproduced? |
| --- | --- | --- |
| Security group attached twice in `EnsureBastion` | `cloud/sdk_client.go:263` | ✅ yes — same transient 404 `BastionError` |
| `allowedCIDRs` rules never removed | `cloud/sdk_client.go:698` | not exercised (needs a CIDR change) |
| `bastionNeedsRecreate` only watches cloud-init | `controller/stackitcluster_bastion.go:183` | not exercised |
| `ensureServer` recreates unconditionally | `controller/stackitmachine_infrastructure.go`, `ensureServer` (guard now at :326) | ✅ yes — see below |
| Template hardcodes `replicas: 3` | `templates/cluster-template-bastion.yaml:160` | ✅ yes |
| Stuck deletion on simultaneous delete | `controller/stackitmachine_controller.go`, the owner checks in `Reconcile` | ✖️ did not trigger — the code path was unchanged at the time of this run, and **not fixed** until 2026-08-19/20 |

## Open bugs

| Document | Status | Summary |
| --- | --- | --- |
| [deletion-bug.md](deletion-bug.md) | ✅ fixed | Deleting `Cluster`+`StackitCluster`+`Machine`s simultaneously could strand machines and orphan VMs. **Fixed 2026-08-19:** `StackitCluster.reconcileDelete` now keeps its finalizer with `RequeueAfter` while Machines remain, and `StackitMachine.reconcileDelete` looks the server up by tags before dropping its own. **Three further orphan-leak paths** were added on 2026-08-14 — the first two from the Copilot review, both caused by trusting persisted status over the cloud; a third came out of reviewing [PR #4](https://github.com/stackitcloud/cluster-api-provider-stackit/pull/4) — bastion cleanup by intent and a deleted credentials Secret no longer stranding the cluster in `Terminating` are fixed as well. Of the two adjacent paths that could still block deletion from *outside* this document, the unpersisted finalizer (item 12) was fixed on 2026-08-20; the missing owner (item 11) was fixed and then reverted on 2026-08-21 as unreachable — see [How the other providers order deletion](#how-the-other-providers-order-deletion). |
| [bastion-bug.md](bastion-bug.md) | ⚠️ partially fixed | Five code-confirmed defects. **Fixed:** security group attached twice (the first fix removed the call and was itself a regression — now an idempotent re-attach), `allowedCIDRs` never revoked (**security-relevant**), hardcoded `replicas: 3`, and disabling the bastion not tearing it down. **Still open:** `bastionNeedsRecreate` only watches cloud-init, and the CIDR revoke compares prefixes as strings. |
| [watch-wiring-bug.md](watch-wiring-bug.md) | ✅ fixed | Two defects where a change that should trigger a reconcile does not. **Fixed 2026-08-19:** the StackitCluster→Machine predicate matched `Machine.spec.clusterName` against the StackitCluster name; it now resolves the owning `Cluster` first. **Fixed 2026-08-20, reworked 2026-08-21:** correcting an invalid credentials Secret now takes effect because `CredentialFailureResult` requeues instead of returning without one. The Secret watch that first solved this was dropped after review. From the Copilot review, verified in code. |
| [machine-recreate-bug.md](machine-recreate-bug.md) | ✅ fixed | `ensureServer()` recreated a missing server unconditionally, even for a machine that had already joined. Fixed 2026-08-12 with an envtest regression test; `run-refactor1-2` had found a worse variant than previously known — see below. |

**Where each defect should be covered by tests** — unit, envtest or e2e — is
worked out once in [test-strategy.md](test-strategy.md). The assignment is
structural, not a preference: envtest runs against the fake cloud client and
therefore cannot reach the two cloud-layer defects at all.

**Sharpened by run refactor1:** the outcome of the silent recreate depends on
whether the replacement VM happens to get the **same internal IP**:

- *different IP* (`run-main2`): the VM never rejoins, node stays `NotReady` —
  visibly broken.
- *same IP* (`run-refactor1`): the VM rejoins and KCP reports a healthy 3/3,
  but `Machine.spec.providerID` and `Node.spec.providerID` permanently point
  at a **deleted** server while `StackitMachine` points at the live one.
  Nothing surfaces this. A cluster in that state looks fine.

**Not a code defect, but a known gap:** no cluster template ships a
`MachineHealthCheck`, so a node whose VM dies out-of-band is never remediated
automatically at the Kubernetes level. Seen in all three runs.

This is **not an oversight and not a deliberate rejection** — the project
already tracks it:
[../docs/src/getting-started/overview.md](../docs/src/getting-started/overview.md),
section *"Full Cluster API Support"*, lists "MachineHealthCheck remediation"
among the areas still required for full CAPI support, alongside "interrupted
deletes" and "orphan cleanup" — i.e. the areas covered by
[deletion-bug.md](deletion-bug.md) and
[machine-recreate-bug.md](machine-recreate-bug.md). There is no ADR, issue or
commit discussing it beyond that roadmap entry.

**Resolved:** the bastion SSH failures in `run-main2` were **not** a provider
defect. [bastion-bug.md](bastion-bug.md#resolved-and-not-a-code-defect-the-ssh-failures)
predicted that a `213.17.x.x` bastion would be reachable while
`192.214.x.x`/`188.34.x.x` are not; `run-refactor1-4` got `213.17.21.135` and
SSH worked on the first attempt. It is an environment IP-range reachability
issue, independent of port.

## What is confirmed working

Each claim notes which run evidences it.

- **Bootstrapping** (main2, refactor1 from scratch): cluster provisions, nodes
  join and reach `Ready`, Cilium installs cleanly, scheduling works.
- **Networking** (all runs): cross-node pod-to-pod communication with 0%
  packet loss; in-cluster DNS resolves Service names.
- **Worker scaling** (all runs): 1↔2 with no leftover objects. Which machine a
  scale-down removes is arbitrary — main1 and refactor1 lost the original
  node, main2 the newest.
- **HA control-plane** (all runs): 3-node scale-up one machine at a time;
  killing a control-plane VM does not take the API server down; after the dead
  `Machine` is deleted manually the LB target is removed, nothing leaks, and
  KCP creates a replacement with a new leader.
- **Deletion** (all runs): `kubectl delete cluster` tears down in the right
  order and removes every STACKIT resource with no leaks, in about a minute.
- **Bastion access path** (main1, refactor1): SSH to the bastion, jump to a
  workload node, kube-api through the tunnel, full remote CNI install, and
  access refused from a network outside the allowed CIDR.

## Shared prerequisites

- Management cluster per
  [../docs/src/getting-started/management-cluster.md](../docs/src/getting-started/management-cluster.md).
- STACKIT resources (network, security group, image, credentials secret) per
  [../docs/src/getting-started/cloud-resources.md](../docs/src/getting-started/cloud-resources.md)
  and [../docs/src/getting-started/credentials.md](../docs/src/getting-started/credentials.md).
- `stackit` CLI authenticated **once per shell**:
  ```
  stackit auth activate-service-account --service-account-key-path "${STACKIT_SERVICE_ACCOUNT_KEY_PATH}"
  ```
- `.envrc` sourced **directly, never through a pipe** — `source .envrc | tail`
  runs it in a subshell and silently discards every export.
- `CLUSTER_NAME` must be a lowercase RFC 1123 subdomain.
- `.envrc` ships `STACKIT_SSH_KEY_NAME=""` — set it explicitly before the
  bastion package, together with the four `STACKIT_BASTION_*` variables in the
  same shell (they are snapshots, not live references).
- Workload kubeconfig:
  ```
  export KUBECONF_WORKERCLUSTER=/tmp/"${CLUSTER_NAME}".kubeconfig
  clusterctl get kubeconfig "${CLUSTER_NAME}" -n "${NAMESPACE}" > "${KUBECONF_WORKERCLUSTER}"
  ```
- **Testing a branch:** rebuild and reload the provider image first, and verify
  the image digest actually changed — otherwise the run silently re-tests
  whatever was deployed before. See
  [run-refactor1-1-bootstrapping.md](run-refactor1-1-bootstrapping.md) step 0.
- **Go toolchain:** `go env GOTOOLCHAIN` is `local` while `go.mod` on the
  refactor branch requires Go 1.26.0. Prefix local `make` targets with
  `GOTOOLCHAIN=auto`. The container build is unaffected.

**CLI papercuts:** `stackit key-pair create --public-key` needs an
`@`-prefixed path. Re-read `status.bastion.publicIP` for every new cluster —
a stale `BASTION_IP` from an earlier run produces a false-positive timeout in
the CIDR test.

## Next steps

### Done (2026-08-12)

- e2e suite made runnable and executed for the first time on any branch: free
  specs plus cluster lifecycle, NodeRef and bastion — all green, no leaks. Six
  entry barriers documented in [run-refactor2-1-e2e.md](run-refactor2-1-e2e.md).
- **2026-08-14:** two more quick-win fixes — the hardcoded worker `replicas: 3`
  in the bastion template, and bastion cleanup on deletion now following intent
  rather than persisted status (the latter with an envtest regression test).
- **2026-08-14, from reviewing [PR #4](https://github.com/stackitcloud/cluster-api-provider-stackit/pull/4)
  itself:** the security-group fix was found to be a regression of its own and
  replaced by an idempotent re-attach; disabling the bastion now tears it down
  even without persisted status; and a deleted credentials Secret finalizes the
  cluster instead of stranding it. Three further findings from the same review
  are open — items 8–10 below.
- Three defects fixed, each with a regression test that was proven to catch it
  by temporarily reverting the fix — see
  [test-strategy.md](test-strategy.md#landing-the-tests):
  duplicate security-group attach, `allowedCIDRs` never revoked, and the
  unconditional server recreate. `cloud` coverage 39.1 % → 55.5 %,
  `controller` 70.2 % → 71.0 %.
- **2026-08-19:** this branch had independently re-implemented PR #4's fixes
  (same behaviour, different code) instead of merging it; the 14 affected
  source files were replaced 1:1 with `main`'s actual merged version, and
  every `file:line` reference in this folder that pointed at current (not
  historical pre-fix) code was re-checked against the synced code and
  corrected where it had drifted. The then-open items were re-verified against
  `main` rather than assumed.
- **2026-08-19, branch `debug/regression-test-code-cleanup-and-proper-abstraction`:**
  four changes, each with a regression test proven to fail without its fix.
  Speaking variable names across the two reconcilers and both scopes (no
  behaviour change). The StackitCluster→Machine watch now resolves the owning
  `Cluster` via `clusterutil.GetOwnerCluster` and label-selects Machines
  server-side, so it no longer goes blind when the infrastructure object is
  named differently from its `Cluster` — the CAPG/CAPA mapper shape.
  `StackitCluster.reconcileDelete` keeps its finalizer with
  `RequeueAfter: 5s` while Machines remain, as CAPA, CAPO and CAPH do; the
  `Watches(&clusterv1.Machine{}, …)` the fix plan proposed was dropped, because
  it only existed to compensate for returning without a requeue. And
  `StackitMachine.reconcileDelete` now resolves the server by tags before
  dropping its finalizer, so a lost status patch no longer orphans a VM.
  Reviewing that work against the CAPI contract surfaced four further
  defects — items 11–15 below.
- **2026-08-20, same branch:** items 1, 11, 12 and 15 each landed as their own
  commit with a regression test proven to fail without its fix. Items 13 and 14
  were deliberately left out of scope. **Two of the four were reverted a day
  later after review — see the 2026-08-21 entry.** What survived:
  - *Item 12:* both `reconcileNormal` functions call `PatchObject` immediately
    after `AddFinalizer`, before any cloud call. `patch.Helper` snapshots the
    object once in `NewHelper` and re-diffs against that snapshot on every
    `Patch`, so the later deferred call stays correct. Proven strictly rather
    than after the fact: `cloud/fake.Client` gained non-consuming
    `BeforeCreateServer`/`BeforeGetNetwork` hooks, and the specs read the API
    server from inside the cloud call.
  - *Item 1:* solved, but by a requeue rather than the Secret watch this entry
    originally described — see the 2026-08-21 entry.
  - **Correction to the earlier research** in
    [watch-wiring-bug.md](watch-wiring-bug.md): the wrapped error from
    `clusterutil.GetOwnerCluster` *is* detectable with `apierrors.IsNotFound`.
    Both `github.com/pkg/errors` wrapper types implement `Unwrap()`, and
    `k8s.io/apimachinery`'s `reasonAndCodeForError` uses `errors.As`, which
    follows the chain. No `errors.Cause` unwrapping is needed.
  - A `chore:` commit also removed every `debug/` reference from Go comments:
    this folder never leaves the repository, so those pointers would dangle the
    moment the code is contributed upstream. The comments now describe the
    defect they guard against on their own.
- **2026-08-21, after review on [PR #17](https://github.com/stackitcloud/cluster-api-provider-stackit/pull/17):**
  the maintainer asked for less complexity for cases that do not occur in
  practice, and for markedly shorter comments. Verifying each objection against
  upstream code settled three of them and refuted one.
  - *Item 11 reverted.* The objection was right. CAPI's own
    `cluster_controller.go` removes the `Cluster` finalizer only after both the
    descendants and the infrastructure cluster are gone (`:62-75`, `:132-161`),
    so the `Cluster` cannot vanish while the `StackitCluster` still exists —
    the assumption the fix rested on does not hold. CAPA's
    `awsmachine_controller.go` also places all three owner checks **before** the
    deletion branch, exactly the shape this repository had before the fix. Since
    the root cause of the documented incident was the missing Machine guard,
    which is in place, item 11 was only defence-in-depth for an unreachable
    case. `ownerGone`, `missingOwner`, `ownerClusterName` and five specs went
    with it. **Back on the open list.**
  - *Item 15 reverted.* Confirmed: neither CAPA nor CAPG configures anything
    beyond `DefaultNamespaces` and `SyncPeriod`. `cmd/manager/main.go` is
    byte-identical to `main` again and `main_test.go` is deleted — the objection
    that it only exercised controller-runtime rather than this provider's logic
    is fair. **Back on the open list.**
  - *Item 1 solved differently.* The maintainer suggested reading the Secret on
    every reconcile instead of watching it. The reconcile already did read it on
    every pass; the missing piece was that `CredentialFailureResult` returned
    **without** a requeue, so nothing re-triggered a reconcile after a
    correction. It now returns `RequeueAfter: credentialsRetryRequeueAfter`
    (1 minute), and the mapper, the second watch and the three mapper specs are
    gone. Two specs cover the real scenario end to end: invalid credentials →
    requeue → correct them → `Ready`.
  - *The Machine guard stays.* The claim that no other provider has such a check
    is refuted — see
    [How the other providers order deletion](#how-the-other-providers-order-deletion).
  - *Naming left as is.* The suggestion to call the local `cluster` instead of
    `stackitCluster` rests on "we never actually work with the cluster-api
    cluster object anywhere", which is not the case: both `ClusterScope` and
    `MachineScope` hold `Cluster` **and** `StackitCluster`, and the CAPI object
    is read by `util.ReconciliationPaused`, by the Machine guard and by
    `MachineScope.Tags()`.
  - *Comments trimmed across the whole `controller` package*, not only where the
    review pointed: 42 lines out, 20 in. Rule applied throughout — at most one
    or two sentences, describing the current state, never what the code used to
    do.

### Open

Item numbers are never reused — the other documents in this folder cite them,
so a closed item is struck from this list without renumbering the rest.

2. Extend `bastionNeedsRecreate` to cover `sshKeyName`/`imageID`/`machineType`,
   or document the limitation prominently
   ([bastion-bug.md](bastion-bug.md#3-bastionneedsrecreate-only-reacts-to-cloud-init-changes)).
3. Confirm the `allowedCIDRs` fix once against real infrastructure using the
   procedure in
   [bastion-bug.md#how-to-actually-prove-this-bug](bastion-bug.md#how-to-actually-prove-this-bug)
   — the unit test asserts the request, not the effective state.
4. Reduce the e2e entry barriers: fold `STACKIT_AVAILABILITY_ZONE`, the CCM
   image handling and the credentials Secret into `setup-test-e2e` or the docs,
   and stop `make deploy` from rewriting `config/manager/kustomization.yaml`.
5. `MachineHealthCheck` remediation is already on the roadmap
   ([../docs/src/getting-started/overview.md](../docs/src/getting-started/overview.md),
   *"Full Cluster API Support"*), so the open question is narrower: should the
   **example templates** ship one, or only document the gap?
   - *For:* these are quickstart templates; without an MHC a node failure looks
     like a provider bug — all three runs stumbled over exactly that.
   - *Against:* upstream CAPI keeps remediation opt-in on purpose, sensible
     timeouts are workload-specific, and an aggressive default can replace
     healthy nodes.
   - Note an MHC would **not** have fixed
     [machine-recreate-bug.md](machine-recreate-bug.md); see there for why.
6. Give the recreate guard a terminal state
   ([machine-recreate-bug.md](machine-recreate-bug.md#fix-options)). It returns
   a retryable `cloud.ErrNotFound`, so an already-provisioned machine whose
   server is gone reconciles forever; the provider has no
   `failureReason`/`failureMessage` for CAPI to remediate on, and
   `status.instanceState`/`status.addresses` keep describing the deleted server.
   This is the unimplemented half of the fix, and it is what keeps item 5
   coupled to this bug.
7. Normalise CIDRs before comparing them in the `allowedCIDRs` revoke loop
   ([bastion-bug.md](bastion-bug.md#2-changing-allowedcidrs-never-revokes-the-old-access)).
   A non-canonical prefix such as `203.0.113.5/24` — which `validateBastionSpec`
   accepts — never matches what the API returns masked, so the rule is deleted
   and recreated on every reconcile. In the same loop, `isSSHRule` ignores
   `ipRange`, so a rule without one would be deleted (latent today).
8. Clear `status.ready` on all failure paths in `StackitMachine.reconcileNormal`
   ([machine-recreate-bug.md](machine-recreate-bug.md#follow-up-statusready-is-cleared-on-only-three-of-five-failure-paths))
   — the bootstrap-data and credentials paths still leave `ready: true` next to
   conditions saying `False`. The cluster controller already does this.
9. Route the four direct `stackitCluster.Status.Ready = false` assignments in
   `StackitCluster.reconcileNormal`
   (`controller/stackitcluster_infrastructure.go:55,73,101,168`) through
   `ClusterScope.SetNotReady` instead, mirroring item 8 on the machine side —
   a consistency-only follow-up explicitly flagged as out of scope for PR #4
   by [@Herbaert](https://github.com/stackitcloud/cluster-api-provider-stackit/pull/4#discussion_r3803129927).
10. Optional e2e specs for the fixed defects — deferred, with the required
    work written up per spec in
    [test-strategy.md](test-strategy.md#optional-e2e-specs--deferred-and-what-each-would-need).

### Open — from the CAPI contract review (2026-08-19)

Found by reviewing the provider against the [Cluster API Book](https://cluster-api.sigs.k8s.io/developer/providers/contracts/),
contract version v1beta2, with CAPI v1.13.2 read from the module cache for the
contract itself and CAPA/CAPG/CAPO consulted for how they solve the same
problems. None of these were introduced by the fixes above; all four predate
them. Item 12 was fixed on 2026-08-20; item 11 was fixed and then reverted after
review on 2026-08-21 and is open again. Numbers are never reused.

**Read these findings with the methodology caveat.** The two that did not survive
review — items 11 and 15 — were the two argued from the book's wording and from
Cluster API core's internals rather than from what the providers do. See
[Cluster API core is not a reference for this repository](#cluster-api-core-is-not-a-reference-for-this-repository).
Items 13 and 14 have not been checked against CAPA, CAPG and CAPO yet; do that
before acting on either.

11. **A missing owner blocks deletion — open by decision, not by oversight.**
    Both reconcilers check their owners before the `DeletionTimestamp` branch, so
    an object whose owner is already gone cannot finish deleting. The fix was
    reverted on 2026-08-21 because the case is unreachable through the normal
    path: CAPI's `cluster_controller.go` holds the `Cluster` finalizer until the
    infrastructure cluster is gone, and the Machine guard closes the route the
    documented incident took. CAPA orders its own owner checks the same way. Pick
    this up only with a concrete reproduction — reaching it needs a force-removed
    finalizer or a provider running without the CAPI controller.
13. **`Machine.spec.failureDomain` is ignored.** The cluster publishes three
    failure domains (`stackitcluster_infrastructure.go:51`), so KCP spreads
    control-plane Machines across them via `Machine.spec.failureDomain` — but the
    provider never reads that field. Placement uses
    `StackitMachine.spec.availabilityZone` alone, and
    `validateMachineAvailabilityZone` only checks that it appears in the
    published list. The HA spread KCP asks for is therefore silently not applied.
    The contract is directional here: `Machine.spec.failureDomain` decides the
    placement, and `status.failureDomain` on the InfraMachine (which this
    provider does not have) merely reports where the instance landed.
14. **`status.ready` is used as a lifecycle signal.**
    `stackitmachine_infrastructure.go:51` gates the whole machine reconcile on
    `StackitCluster.Status.Ready` — the v1beta1 field. The contract field is
    `status.initialization.provisioned`, which this provider maintains in
    parallel and even documents in the type as *"Cluster API v1beta2 contract
    state"*. Both are set separately and can drift; this is also the root of
    items 8 and 9.
15. **Every Secret in the management cluster is cached, including its `Data` —
    open by decision.** The cache is cluster-wide (`main.go` sets no
    `DefaultNamespaces`) and the bastion cloud-init watch pulls every Secret into
    the informer, so the service-account keys sit in memory. A `Transform` that
    nils `Data` and the managed fields plus `DisableFor` on the client closed
    this on 2026-08-20 and was reverted on 2026-08-21: neither CAPA nor CAPG
    hardens the Secret cache, and the maintainer prefers `DefaultNamespaces` and
    `SyncPeriod` like the other providers. Those two need new CLI flags and are
    therefore a feature rather than a fix — the natural next step here. See
    [How the other providers solve the Secret cache](#how-the-other-providers-solve-the-secret-cache).

### Not covered

- The **upgrade**, **ClusterClass-topology** and **scale** e2e packages are not cothe manual runs did not touch those areas
  either. A refactor regression there would go unnoticed. This is a deliberate
  scoping decision, not an oversight — run
  `make test-e2e-workload-upgrade-workers`,
  `…-upgrade-control-plane`, `…-topology` and `…-scale` to close it.

## How the other providers order deletion

Checked against upstream `main` on 2026-08-21, because the PR #17 review objected
that no other provider guards its finalizer against remaining Machines. Two of
the three largest do.

| Provider | Machine guard in the cluster's `reconcileDelete` | Owner checks before the `DeletionTimestamp` branch |
| --- | --- | --- |
| CAPO | ✅ `collections.GetFilteredMachinesForCluster` → `RequeueAfter: 5 * time.Second` | — |
| CAPA | ✅ `dependencyCount()` over the cluster-name label → `RequeueAfter: deleteRequeueAfter` | ✅ all three, before it |
| CAPG | ✖️ removes the finalizer unconditionally | — |

CAPO's comment states the same reasoning this provider uses: *"Wait for machines
to be deleted before removing the finalizer as they depend on this resource to
deprovision. Additionally it appears that allowing the Kubernetes API to vanish
too quickly will upset the capi kubeadm control plane controller."*

**The two questions are separate, which is what made the review confusing.**
Whether the `Cluster` can outlive the `StackitCluster` (item 11) and whether
`Machine`s can outlive it (the guard) have different answers. CAPI guarantees the
first ordering, so item 11 was reverted. It does not help with the second: a
`kubectl delete -f cluster.yaml` or a namespace teardown deletes the
`StackitCluster` **directly**, bypassing the `Cluster` controller entirely — the
route that stranded three worker Machines for two days in
[deletion-bug.md](deletion-bug.md). The guard stays.

The right-hand column is also the correction to the item 11 finding: CAPA does
what this repository did *before* the fix, so the book's wording alone was not
sufficient grounds for the change.

## How the other providers solve the Secret cache

Checked against upstream `main`, CAPA and CAPG on 2026-08-20 and CAPO on
2026-08-21.

| | `Transform` stripping Secret data | `DisableFor` Secrets on the client | Secret watch |
| --- | --- | --- | --- |
| CAPA | ✖️ | ✖️ | ✖️ none |
| CAPG | ✖️ | ✖️ | ✖️ none |
| CAPO | ✖️ | ✅ `ConfigMap` **and** `Secret` | ✖️ none |

**CAPO is the interesting one, and it was missed on 2026-08-20.** It sets
`DisableFor` for ConfigMaps and Secrets and nothing else — no `Transform`, no
selector. That is not a weaker version of item 15, it is a cheaper route to the
same end: with every Secret read bypassing the cache *and* no controller watching
Secrets, controller-runtime never starts a Secret informer at all, so there is
nothing to strip.

**That route is closed here**, which is the substantive difference. The bastion
cloud-init watch means a Secret informer exists whatever the client does, so
`DisableFor` alone would not keep credential bytes out of memory — it would only
add live reads. Dropping that watch is not an option; the cloud-init ConfigMap or
Secret can be edited with nothing else changing.

CAPA reads bootstrap data through the manager's **cached** client
(`machineScope.GetRawBootstrapDataWithFormat`), which makes controller-runtime
start a Secret informer lazily on the first read — so every Secret, `Data`
included, ends up in memory anyway. Reads stay cheap; a corrected credentials
Secret never triggers a reconcile, which is the ecosystem-wide gap already noted
in [watch-wiring-bug.md](watch-wiring-bug.md).

**Where the original finding came from, and why that was the flaw.** Item 15 was
derived from Cluster API core's own manager setup, not from any provider — see
[Cluster API core is not a reference for this repository](#cluster-api-core-is-not-a-reference-for-this-repository).
The `Transform` and the selector have no provider precedent, which is what the
PR #17 review said and why the fix was reverted. CAPO does establish precedent
for `DisableFor`, but on its own it does not solve anything here (see above), so
the revert stands either way.

**What the trade would have cost.** `DisableFor` turns every Secret read into a
live API call: one per cluster reconcile, two per machine reconcile (credentials
plus bootstrap data). CAPO accepts exactly that cost; the difference is that it
buys the whole benefit there and only part of it here.

**Worth knowing:** the exposure predates all of this work and is still present.
The bastion cloud-init watch pulls every Secret in the management cluster into
the informer — the cache is cluster-wide, `main.go` sets no `DefaultNamespaces` —
and `BuildCloudClient` reads through the cached client, so the service-account
keys sit in memory. Item 1 did not create that, and reverting item 15 leaves it
in place. `DefaultNamespaces` at least bounds it, which is the open follow-up.

## Cluster API core is not a reference for this repository

Recorded on 2026-08-21, because two findings in this folder were built on it and
one of them (item 15) did not survive review.

**Cluster API core is the counterparty, not a peer.** It owns the `Cluster`,
`Machine` and control-plane lifecycle and calls *into* infrastructure providers.
The Secrets it handles — kubeconfigs, bootstrap data, CA material — it creates
and labels itself, it reads some of them on a hot path, and it runs one manager
for the whole management cluster. An infrastructure provider has none of that:
its central Secret is a set of cloud credentials written by hand
(`kubectl create secret generic`, see
[../docs/src/getting-started/credentials.md](../docs/src/getting-started/credentials.md)),
unlabelled, read once per reconcile. Patterns tuned for the first situation —
the `ByObject` label selector, the `Transform`, `DisableFor` plus a second
caching client, the `PartialObjectMetadata` watch in `ClusterResourceSet` —
carry assumptions that simply do not hold here.

**Use CAPA, CAPG and CAPO instead.** They solve the same problems under the same
constraints, and where they agree that is real precedent; where they diverge (the
Machine guard: CAPA and CAPO yes, CAPG no) the disagreement itself is the useful
signal.

**Cluster API core is still authoritative for two things**, and citing it for
these is not the same mistake:

- **Its observable behaviour**, because the provider has to interoperate with it.
  That the `Cluster` controller holds its finalizer until the infrastructure
  cluster is gone is a fact about the environment, and it is what settled item 11.
- **The contract itself** — the field semantics and the book. Note the limit
  though: the book's wording alone was what motivated item 11, and it was not
  sufficient. Where the contract leaves room, check what the providers actually do
  before changing anything.

## Convention for this folder

One flat Markdown document per topic, no subfolders.

- `run-<branch><N>-<M>-<topic>.md` — test-run protocols. `<branch>` is the
  branch under test, `<N>` the run against that branch, `<M>` the position
  within the run. Alphabetical order equals chronological order. A second
  refactor run goes in as `run-refactor2-*`, a third `main` run as
  `run-main3-*`.
- `*-bug.md` — investigations of a specific defect; they belong to no run.
- `SUMMARY.md` — this file: timeline, status, parity, open bugs, prerequisites.

Command blocks use a fenced block without a language tag containing
`$ command`, a blank line, then the raw output, followed by a bolded
`**Result:**` paragraph and a `---` separator.
