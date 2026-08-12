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
| `allowedCIDRs` rules never removed | `cloud/sdk_client.go:692` | not exercised (needs a CIDR change) |
| `bastionNeedsRecreate` only watches cloud-init | `controller/stackitcluster_bastion.go:136` | not exercised |
| `ensureServer` recreates unconditionally | `controller/stackitmachine_infrastructure.go:205` | ✅ yes — see below |
| Template hardcodes `replicas: 3` | `templates/cluster-template-bastion.yaml:160` | ✅ yes |
| Stuck deletion on simultaneous delete | `controller/stackitmachine_controller.go:87-94` | ✖️ did not trigger — but code path unchanged, so **not fixed** |

## Open bugs

| Document | Status | Summary |
| --- | --- | --- |
| [deletion-bug.md](deletion-bug.md) | ⚠️ open | Deleting `Cluster`+`StackitCluster`+`Machine`s simultaneously can strand machines and orphan VMs. Root cause identified, fix specified, **not implemented**. `run-refactor1-4` ran `kubectl delete -f` deliberately and it completed cleanly — the race simply did not hit; the code path is verifiably unchanged. |
| [bastion-bug.md](bastion-bug.md) | ⚠️ open | Four code-confirmed defects: security group attached twice (causes the "transient" `BastionError` and delays the public IP), `allowedCIDRs` rules never removed (**security-relevant**), `bastionNeedsRecreate` only watches cloud-init, and the hardcoded `replicas: 3`. |
| [machine-recreate-bug.md](machine-recreate-bug.md) | ⚠️ open | `ensureServer()` recreates a missing server unconditionally, even for a machine that had already joined. **`run-refactor1-2` found a worse outcome than previously known** — see below. |

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

### Now: e2e coverage

The `debug/` runs are manual and cannot be repeated cheaply. The e2e suite
already covers much of the same ground and has **never been executed on the
refactor branch** — getting it running and extending it is the highest-value
next step.

1. **Make the suite runnable.** Three verified blockers, none of them a code
   defect:
   - `STACKIT_AVAILABILITY_ZONE` is `requiredEnv` (`test/e2e/e2e_test.go`) but
     missing from `.envrc`.
   - `STACKIT_CLOUD_CONTROLLER_MANAGER_IMAGE` from `.envrc` pins minor 1.35,
     while the suite asserts the image minor matches the Kubernetes version
     under test. Do not export it for e2e — the suite has per-minor defaults.
   - The running devcontainer is `golang:1.25` while `go.mod` requires 1.26.0
     and `GOTOOLCHAIN=local`, so `make manifests/test/test-e2e` abort. The
     branch already bumps `.devcontainer/devcontainer.json` to `golang:1.26`;
     rebuild the container, or prefix `GOTOOLCHAIN=auto`.
2. **Run the relevant billable specs:** cluster lifecycle
   (`STACKIT_E2E_CREATE_CLUSTER`), NodeRef (`make test-e2e-workload-noderef`),
   bastion (`make test-e2e-workload-bastion`) — the three that cover the
   behaviour our documented defects touch.
3. **Extend the suite** with two specs that turn documented defects into
   automated regression tests. Both land red and go green with the respective
   fix, so they belong behind their own gates like every other billable spec:
   - *out-of-band VM delete* — the existing NodeRef spec already asserts the
     provider-ID invariant that [machine-recreate-bug.md](machine-recreate-bug.md)
     violates; it simply never deletes a VM out-of-band.
   - *`allowedCIDRs` revocation* — pure API assertions, no SSH, therefore
     immune to the IP-range problem that blocked run main2. See
     [bastion-bug.md#how-to-actually-prove-this-bug](bastion-bug.md#how-to-actually-prove-this-bug).

### Then: the pre-existing defects (all present on `main` too)

4. Guard the unconditional recreate in `ensureServer()`
   ([machine-recreate-bug.md](machine-recreate-bug.md)). Highest impact: the
   same-IP variant found in `run-refactor1-2` leaves a cluster that looks
   healthy while carrying a silent provider-ID mismatch.
5. Remove the duplicate security-group attach in `EnsureBastion`
   ([bastion-bug.md](bastion-bug.md#1-the-bastion-security-group-is-attached-twice)).
6. Make `allowedCIDRs` reconcile in both directions
   ([bastion-bug.md](bastion-bug.md#2-changing-allowedcidrs-never-revokes-the-old-access))
   — security-relevant. This defect has **not been demonstrated yet**: both CIDR
   tests so far ran from a never-allowed network and cannot detect it.
7. Fix `replicas: 3` → `${WORKER_MACHINE_COUNT}` in
   `templates/cluster-template-bastion.yaml`. Note this one is **not** coverable
   by e2e as it stands — the suite renders its own fixtures and never reads that
   template; a template lint would be the fitting check.
8. Land the deletion-order fix
   ([deletion-bug.md#fix-plan-not-yet-implemented](deletion-bug.md#fix-plan-not-yet-implemented));
   `kubectl delete cluster` stays the documented default until then.
9. `MachineHealthCheck` remediation is already on the roadmap
   ([../docs/src/getting-started/overview.md](../docs/src/getting-started/overview.md),
   *"Full Cluster API Support"*), so the open question is narrower: should the
   **example templates** ship one, or only document the gap?
   - *For:* these are quickstart templates; without an MHC a node failure looks
     like a provider bug — all three runs stumbled over exactly that.
   - *Against:* upstream CAPI keeps remediation opt-in on purpose, sensible
     timeouts are workload-specific, and an aggressive default can replace
     healthy nodes.
   - Note an MHC would **not** fix
     [machine-recreate-bug.md](machine-recreate-bug.md); see there for why.

### Not covered

- The **upgrade**, **ClusterClass-topology** and **scale** e2e packages are not cothe manual runs did not touch those areas
  either. A refactor regression there would go unnoticed. This is a deliberate
  scoping decision, not an oversight — run
  `make test-e2e-workload-upgrade-workers`,
  `…-upgrade-control-plane`, `…-topology` and `…-scale` to close it.

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
