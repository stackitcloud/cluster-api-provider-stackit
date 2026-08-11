# Debug & regression notes — `main`

Entry point for this folder. Everything below happened against branch
`debug/regression-test-main` (commit `bd32c5b`), which carries **no Go changes
relative to `main`** — so all findings apply to `main`'s reconciler logic.

## Timeline

Read top to bottom; this is the order in which everything happened.

| # | When | Cluster | What | Documents |
| --- | --- | --- | --- | --- |
| **0** | 2026-07-31 … 08-03 | `stackit-workload` | Initial bug investigation, before any structured run | [deletion-bug.md](deletion-bug.md) |
| **1** | 2026-08-05 | `stackit-workload` | **Run 1** — bootstrapping → HA → deletion | [run1-1-bootstrapping.md](run1-1-bootstrapping.md) · [run1-2-ha-controlplane.md](run1-2-ha-controlplane.md) · [run1-3-deletion.md](run1-3-deletion.md) |
| **1e** | 2026-08-07 (earlier) | `stackit-capi-test` | **Run 1, extra** — bastion, on its own cluster | [run1-4-bastion.md](run1-4-bastion.md) |
| **2** | 2026-08-07 (later) | `stackit-capi-test` | **Run 2** — same three packages, from scratch | [run2-1-bootstrapping.md](run2-1-bootstrapping.md) · [run2-2-ha-controlplane.md](run2-2-ha-controlplane.md) · [run2-3-deletion.md](run2-3-deletion.md) |
| **2e** | 2026-08-07 (later) | `stackit-capi-test` | **Run 2, extra** — bastion | [run2-4-bastion.md](run2-4-bastion.md) |
| **3** | 2026-08-10 | — | Code review of both runs; defects written up | [bastion-bug.md](bastion-bug.md) · [machine-recreate-bug.md](machine-recreate-bug.md) |

Run 1 and run 2 are **independent full runs**, not a plan and a report. Each
`run*-*.md` is a complete protocol of its own run. Where the review later
corrected a conclusion, the protocol is left as recorded and an
`## Addendum` at the end points to the relevant bug document.

## Status matrix

| Topic | Run 1 (08-05/07) | Run 2 (08-07) |
| --- | --- | --- |
| Bootstrapping & networking | ✅ works — but cluster pre-existed, creation not evidenced | ✅ works — created from scratch, full evidence |
| HA control-plane | ⚠️ partial — manual remediation needed | ⚠️ partial — same, **plus** silent VM recreate found |
| Deletion & cleanup | ✅ works | ✅ works |
| Bastion / jump host | ✅ works — full access path verified | ⚠️ blocked at SSH — but found a template bug |

Where the runs differ, both are right about what they saw: run 2 bootstrapped
from zero and so evidences cluster creation that run 1 could not; run 1 got
through the whole bastion path that run 2 could not reach. Neither document
supersedes the other.

## Open bugs

| Document | Status | Summary |
| --- | --- | --- |
| [deletion-bug.md](deletion-bug.md) | ⚠️ open | Deleting `Cluster`+`StackitCluster`+`Machine`s simultaneously strands machines and orphans VMs. Root cause identified, fix specified, **not implemented**. Not reproduced in either run — the safe path (`kubectl delete cluster` alone) was used and works. |
| [bastion-bug.md](bastion-bug.md) | ⚠️ open | Four code-confirmed defects: security group attached twice (causes both "transient" `BastionError`s and delays the public IP), `allowedCIDRs` rules never removed (**security-relevant**), `bastionNeedsRecreate` only watches cloud-init, and `cluster-template-bastion.yaml` hardcodes `replicas: 3`. |
| [machine-recreate-bug.md](machine-recreate-bug.md) | ⚠️ open | `ensureServer()` recreates a missing server unconditionally, even for a machine that had already joined; the replacement can never rejoin and just consumes a VM + volume until an operator intervenes. |

**Not a code defect, but a gap:** no cluster template ships a
`MachineHealthCheck`, so a node whose VM dies out-of-band is never
automatically remediated at the Kubernetes level. Remediation is intentionally
opt-in upstream — this is a decision to make, not a bug. Seen in both runs.

**Unresolved, environment-related:** bastion SSH failed in run 2 on 3 VMs
across 3 IPs while working in run 1 from the same environment. Both runs
together point at certain STACKIT public IP ranges being unreachable here,
independent of port — see
[bastion-bug.md](bastion-bug.md#open-not-a-code-defect-the-ssh-failures) for
the evidence and a falsifiable test. This also retro-explains the outbound
network failure that forced run 2's bootstrapping package to be restarted.

## What is confirmed working

Each claim notes which run evidences it.

- **Bootstrapping** (run 2, from scratch; run 1 consistent): cluster
  provisions, control-plane and worker nodes join and reach `Ready`, Cilium
  installs cleanly, deployments and pod scheduling work.
- **Networking** (both runs): cross-node pod-to-pod communication works with
  0% packet loss; in-cluster DNS resolves service names.
- **Worker scaling** (both runs): `MachineDeployment` scales 1↔2 with no
  leftover Machine/StackitMachine/Node objects. Which machine a scale-down
  removes is arbitrary — run 1 lost the original node, run 2 the newest.
- **HA control-plane** (both runs): 3-node scale-up works one machine at a
  time; killing a control-plane VM does not take the API server down; after
  the dead `Machine` is deleted manually, the LB target is removed, no
  VM/volume leaks, KCP creates a replacement and a new leader is elected.
- **Deletion** (both runs): `kubectl delete cluster` tears down machines in the
  right order (workers, then control-plane) before `StackitCluster` drops its
  finalizer; all VMs, volumes, load balancer, security groups and public IPs
  are removed with no leaks, in well under a minute. Run 2 confirmed this even
  after extra VMs and two forced bastion recreates.
- **Bastion access path** (run 1 only): SSH to the bastion, jump to a workload
  node, kube-api through the tunnel, and a full remote CNI install; access
  from a network outside the allowed CIDR is refused.

## Shared prerequisites

Apply to every run document; package-specific extras are noted in each.

- Management cluster per
  [../docs/src/getting-started/management-cluster.md](../docs/src/getting-started/management-cluster.md).
- STACKIT resources (network, security group, image, credentials secret) per
  [../docs/src/getting-started/cloud-resources.md](../docs/src/getting-started/cloud-resources.md)
  and [../docs/src/getting-started/credentials.md](../docs/src/getting-started/credentials.md).
- `stackit` CLI authenticated **once per shell** — `.envrc` sets the key path
  but does not do this itself:
  ```
  stackit auth activate-service-account --service-account-key-path "${STACKIT_SERVICE_ACCOUNT_KEY_PATH}"
  ```
- `.envrc` sourced **directly, never through a pipe** — `source .envrc | tail`
  runs it in a subshell and silently discards every export.
- `CLUSTER_NAME` must be a lowercase RFC 1123 subdomain.
- `.envrc` ships `STACKIT_SSH_KEY_NAME=""` — must be set explicitly before the
  bastion package.
- Workload kubeconfig:
  ```
  export KUBECONF_WORKERCLUSTER=/tmp/"${CLUSTER_NAME}".kubeconfig
  clusterctl get kubeconfig "${CLUSTER_NAME}" -n "${NAMESPACE}" > "${KUBECONF_WORKERCLUSTER}"
  ```

**CLI papercuts** (so they are not rediscovered): `stackit key-pair create
--public-key` needs an `@`-prefixed path; `STACKIT_BASTION_SSH_KEY_NAME="${STACKIT_SSH_KEY_NAME}"`
is a snapshot, not a live reference — re-export both together after any change.

## Next steps

1. Decide whether the unconditional server recreate in `ensureServer()` is
   intended; guard it so an already-joined machine is not silently replaced
   ([machine-recreate-bug.md](machine-recreate-bug.md)). Highest impact — it
   currently hides the moment an operator needs to step in.
2. Remove the duplicate security-group attach in `EnsureBastion`
   ([bastion-bug.md](bastion-bug.md#1-the-bastion-security-group-is-attached-twice)) —
   eliminates both recurring `BastionError`s and a reconcile of public-IP delay.
3. Make `allowedCIDRs` reconcile in both directions
   ([bastion-bug.md](bastion-bug.md#2-changing-allowedcidrs-never-revokes-the-old-access)) —
   security-relevant, and required before the CIDR-narrowing test is meaningful.
4. Fix `replicas: 3` → `${WORKER_MACHINE_COUNT}` in
   `templates/cluster-template-bastion.yaml`.
5. Land the deletion-order fix in
   [deletion-bug.md#fix-plan-not-yet-implemented](deletion-bug.md#fix-plan-not-yet-implemented);
   until then document `kubectl delete cluster` as the only supported teardown.
6. Decide whether to ship a default `MachineHealthCheck`.
7. Re-run the bastion access path from a network without the IP-range
   restriction found here, to re-verify run 1's result on current `main`.

## Convention for this folder

One flat Markdown document per topic, no subfolders.

- `run<N>-<M>-<topic>.md` — test-run protocols. `<N>` is the run, `<M>` the
  position within that run, so alphabetical order equals chronological order.
  A third run goes in as `run3-*`.
- `*-bug.md` — investigations of a specific defect; they belong to no run.
- `SUMMARY.md` — this file: timeline, status, open bugs, prerequisites.

Command blocks use a fenced block without a language tag containing `$ command`,
a blank line, then the raw output, followed by a bolded `**Result:**` paragraph
and a `---` separator.
