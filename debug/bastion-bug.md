# Bugs: bastion provisioning path

Date: 2026-08-10, updated 2026-08-11 after the refactor run
Source: code review of the bastion path after run main2 — see [SUMMARY.md](SUMMARY.md#timeline)
Status: ⚠️ **partially fixed** — defects 1, 2 (2026-08-12) and 4 (2026-08-14)
are fixed; only defect 3 (`bastionNeedsRecreate`) remains open. Paths below are
the refactored ones.

**Note on the code snippets below:** they show the state **before** the fixes of
2026-08-12, together with the line numbers of that state. They document what the
defect looked like; the current code differs for defects 1 and 2.

Collects the defects found by reading the bastion code paths against the
evidence from [run-main1-4-bastion.md](run-main1-4-bastion.md) and
[run-main2-4-bastion.md](run-main2-4-bastion.md). All four are confirmed by reading the
source, not inferred from behaviour alone.

[run-refactor1-4-bastion.md](run-refactor1-4-bastion.md) re-ran the package
against the refactored provider: defects 1 and 4 reproduced identically,
defects 2 and 3 were not exercised (they need a spec change rather than a
normal run). The refactor moved the code but changed none of it.

**Why this document exists:** both bastion run protocols classify the two
recurring `BastionError` messages as harmless, self-resolving STACKIT-API
races and explicitly tell the reader not to worry about them. That
classification is wrong — see bug 1. The protocols themselves are left as
written (they record what was observed); the corrections live here.

## 1. The bastion security group is attached twice

**Location** — the file was moved by the refactor but its content is
byte-identical, so the line numbers match on both branches:

| Branch | Path |
| --- | --- |
| `main` | `pkg/cloud/sdk_client.go`, `EnsureBastion`, lines 247-265 |
| `refactor` | [`cloud/sdk_client.go`](../cloud/sdk_client.go), `EnsureBastion`, lines 247-265 |

```go
server, err := c.CreateServer(ctx, CreateServerInput{
    ...
    SecurityGroups: []string{securityGroup.ID},   // (1) already set here
    ...
})
if err != nil {
    return nil, err
}
if err := c.addSecurityGroupToServer(ctx, server.ID, securityGroup.ID); err != nil {  // (2) same SG again
    return nil, err
}
```

`CreateServer` writes the security group into the create payload
(`payload.SetSecurityGroups(...)`, same file, lines 167-169). The call at (2)
attaches the *same* group a second time. That produces exactly the two errors
seen in both runs:

| Server state | Error observed | Cause |
| --- | --- | --- |
| still `CREATING` | `404 … could not be found as device id on any ports` | no network port exists yet, so (2) fails |
| `ACTIVE` | `400 … Duplicate items in the list: 'f1d8050f-…'` | (1) already attached it, so (2) is a duplicate |

The `400 Duplicate items` error from
[run-main2-4-bastion.md](run-main2-4-bastion.md) is direct proof that the group was
already attached at that point — the call is genuinely redundant, not merely
defensive.

`addSecurityGroupToServer` (same file, lines 802-812) only swallows
`IsConflict`:

```go
if !IsConflict(err) {
    return err       // both 400 and 404 propagate
}
```

**Additional effect:** when (2) fails, `EnsureBastion` returns immediately, so
`ensurePublicIP()` and `AddPublicIpToServer()` (lines 267-284) never run in that
reconcile. The bastion's public IP is therefore assigned a full reconcile cycle
later than necessary — part of the delay complained about in
[run-main2-4-bastion.md](run-main2-4-bastion.md) needs no external explanation.

**Reproduced on the refactor branch, and both effects visible at once:**
[run-refactor1-4-bastion.md](run-refactor1-4-bastion.md) step 3 shows the 404
variant for the first ~60 seconds of bastion provisioning — and
`status.bastion.publicIP` staying empty for exactly that window, appearing only
at t+80s once the error had cleared. Identical to `main`.

**Fix — ✅ done (2026-08-14), after a first attempt was wrong.**

The first fix simply removed the call. That was a regression: `CreateServer`
short-circuits on an existing server found by tags, so this call is the **only**
path that re-attaches a group detached out of band — for instance by an
interrupted `DeleteBastion`. Without it the bastion reports `Ready` while none
of its SSH rules are in effect. The asymmetry gives it away: the public IP right
below is re-attached conditionally and tolerates `Conflict`.

The call is therefore back, and `addSecurityGroupToServer` now tolerates
`IsConflict`, `IsInvalidInput` (the 400 duplicate) and `IsNotFound` (no port
yet) — the second option named above, which was passed over the first time.
Guarded by `TestSDKClientEnsureBastionToleratesDuplicateSecurityGroupAttach`,
where the mock returns the real 400 duplicate body and the test requires
`EnsureBastion` to complete **and** assign the public IP.

---

## 2. Changing `allowedCIDRs` never revokes the old access

**Location** — same file move, identical line numbers:

| Branch | Path |
| --- | --- |
| `main` | `pkg/cloud/sdk_client.go`, `ensureBastionSecurityGroupRules`, lines 692-720 |
| `refactor` | [`cloud/sdk_client.go`](../cloud/sdk_client.go), `ensureBastionSecurityGroupRules`, lines 692-720 |

```go
for _, cidr := range cidrs {
    if hasSSHRule(existingRules, cidr) {
        continue
    }
    ... CreateSecurityGroupRule(...)   // additive only — nothing is ever removed
}
```

The function adds missing rules but never removes rules for CIDRs that are no
longer in the spec. Changing `STACKIT_BASTION_ALLOWED_CIDRS` — because the
egress IP rotated (a Zscaler-style proxy is called out as a risk in
[run-main1-4-bastion.md](run-main1-4-bastion.md)), or deliberately to restrict access —
leaves the **old** SSH rule in place permanently.

**Why this matters for the test results:** [run-main1-4-bastion.md](run-main1-4-bastion.md)
step 5 and [run-refactor1-4-bastion.md](run-refactor1-4-bastion.md) step 7 both
certify that the CIDR restriction works. Both were run from a mobile network
that had never been in any rule, so **neither can detect this bug** — they only
prove "an IP that was never allowed is refused", not "a revoked IP is refused".

### How to actually prove this bug

Narrow the CIDR and test **from the network that was previously allowed**:

1. Bastion is up with `allowedCIDRs = <your IP>/32`; confirm SSH works.
2. Set `STACKIT_BASTION_ALLOWED_CIDRS` to a foreign range (e.g. `192.0.2.0/24`,
   TEST-NET-1), re-apply, and wait for the new rule to appear in
   `stackit security-group rule list --security-group-id "${SG}"`.
3. SSH again **from the same machine as in step 1**.

On current code the stale rule for your own IP is still there, so **SSH
succeeds** even though your IP is no longer in the spec. The expected timeout
does not happen — the test fails, and that failure *is* the proof. Listing the
rules in step 2 shows both CIDRs side by side as direct evidence.

This is the cheapest way to demonstrate the defect: it needs no second network,
unlike the "access from outside" test the runs used.

**Do not** combine narrowing with testing from a never-allowed network — that
combination times out for the wrong reason and looks like a pass. That is the
one variant that would mislead, and it is why the runs above could not catch
this.

**Fix — ✅ done (2026-08-12).** `ensureBastionSecurityGroupRules` now reconciles
in both directions: after creating missing rules it deletes every SSH rule whose
`ipRange` is no longer in the desired list. Guarded by
`TestSDKClientEnsureBastionRevokesRemovedCIDR` in `cloud/sdk_client_test.go`.
The manual procedure above should now produce the timeout it previously failed
to produce — worth confirming once against real infrastructure.

**⚠️ Still open — CIDRs are compared as strings.** The revoke loop matches
`desired[rule.GetIpRange()]` against what the API returns. A non-canonical
prefix such as `203.0.113.5/24`, which `validateBastionSpec` accepts and the API
stores masked, therefore never matches: the rule is deleted and recreated on
**every** reconcile. Separately, `isSSHRule` matches direction, port and
protocol but not `ipRange`, so a rule with an empty `ipRange` (remote-security-group
based) counts as "not desired" and would be deleted — latent today, since such
rules only live on the node security group. Suggested: normalise via
`netip.ParsePrefix(...).Masked()` before comparing, and skip rules without an
`ipRange`.

---

## 5. Disabling the bastion did not tear it down — ✅ fixed (2026-08-14)

**Location:** [`controller/stackitcluster_bastion.go`](../controller/stackitcluster_bastion.go),
`reconcileBastion`.

The intent-vs-status fix in
[deletion-bug.md](deletion-bug.md#b-cluster-cleanup-skipped-entirely-when-bastion-status-is-empty)
was applied to `reconcileDelete` only. The `spec.bastion.enabled: false` path
here stayed gated on `hasBastionStatus()` alone, so a bastion whose status patch
never landed kept running — **with port 22 open** — while the condition reported
`Skipped: bastion disabled`.

**Fix:** teardown now runs when the bastion status is present **or the
`BastionReady` condition is missing**. Both live in the same status subresource,
so the condition is absent in exactly the case where the status was lost.

The trigger matters for cost: this path runs on every reconcile of every cluster
*without* a bastion, which is most of them. An unconditional tag sweep would add
about four STACKIT API calls per reconcile permanently; with the condition as
trigger the sweep runs once per cluster.

Guarded by *"tears the bastion down when disabled even if its status was never
persisted"*.

---

## 3. `bastionNeedsRecreate` only reacts to cloud-init changes

**Location** — the refactor split the cluster controller, so this function
moved file *and* line; the logic is unchanged:

| Branch | Path |
| --- | --- |
| `main` | `internal/controller/stackitcluster_controller.go`, `bastionNeedsRecreate`, lines 470-476 |
| `refactor` | [`controller/stackitcluster_bastion.go`](../controller/stackitcluster_bastion.go), `bastionNeedsRecreate`, lines 136-141 |

```go
func bastionNeedsRecreate(sc *infrav1.StackitCluster, cloudInit []byte) bool {
	if !hasBastionStatus(sc.Status.Bastion) {
		return false
	}
	return sc.Status.Bastion.CloudInitHash != bastionCloudInitHash(cloudInit)
}
```

Changes to `sshKeyName`, `imageID`, `machineType` or `rootVolume` do **not**
trigger a recreate. A running bastion with the wrong SSH key cannot be repaired
by fixing the manifest — only by deleting the server or by touching the
cloud-init ConfigMap.

**Relevance:** [run-main1-4-bastion.md](run-main1-4-bastion.md) step 2 hit a stale
`STACKIT_BASTION_SSH_KEY_NAME` and fixed it by regenerating and re-applying.
That worked only because the bastion had never been created (`keypair not
found`). Had it come up with an existing-but-wrong key, re-applying would not
have helped — a trap documented nowhere else.

**Fix:** include the relevant spec fields in the recreate decision, or document
the limitation prominently.

---

## 4. `cluster-template-bastion.yaml` ignores `WORKER_MACHINE_COUNT`

**Location** — a template, untouched by the refactor, so identical on both
branches: [`templates/cluster-template-bastion.yaml`](../templates/cluster-template-bastion.yaml),
line 160.

```
$ grep -n replicas templates/cluster-template.yaml

49:  replicas: ${CONTROL_PLANE_MACHINE_COUNT}
123:  replicas: ${WORKER_MACHINE_COUNT}      # correctly parameterized

$ grep -n replicas templates/cluster-template-bastion.yaml

85:  replicas: ${CONTROL_PLANE_MACHINE_COUNT}
160:  replicas: 3                            # hardcoded
```

Every bastion-enabled cluster gets 3 workers regardless of what is requested.
Found and confirmed in [run-main2-4-bastion.md](run-main2-4-bastion.md); run
main1 ran with 4 nodes and did not recognise the discrepancy. Reproduced
unchanged in [run-refactor1-4-bastion.md](run-refactor1-4-bastion.md) step 2
(`WORKER_MACHINE_COUNT=1` exported, `spec.replicas` came back as `3`).

**Fix — ✅ done (2026-08-14).** Now `replicas: ${WORKER_MACHINE_COUNT}`, matching
the base template. Verified by rendering the template with
`WORKER_MACHINE_COUNT=1` and `=3` and checking the resulting
`MachineDeployment`. Not coverable by the e2e suite — it renders its own
fixtures and never reads this template.

---

## Resolved, and not a code defect: the SSH failures

**Status: confirmed on 2026-08-11 by [run-refactor1-4-bastion.md](run-refactor1-4-bastion.md).**
The prediction below was made before that run and held — see the end of this
section.

The two `main` runs gave contradictory explanations for bastions refusing SSH
(TCP connects, no banner):

- [run-main1-4-bastion.md](run-main1-4-bastion.md): "transient, tied to that specific
  public IP, unconfirmed" — 1 of 3 bastions affected.
- [run-main2-4-bastion.md](run-main2-4-bastion.md): "environment blocks TCP/22 to STACKIT
  ranges" — 3 of 3 affected.

Neither holds up: run main2's explanation cannot be right because run main1
reached STACKIT bastions over SSH from the same environment; run main1's cannot
be right because run main2 reproduced it across two different IPs.

Taking the IPs recorded in both documents together, a single consistent pattern
emerges:

| IP | Run | Result |
| --- | --- | --- |
| `213.17.20.79` | main1 | ✅ SSH ok |
| `213.17.23.100` | main1 | ✅ SSH ok |
| `192.214.188.106` | main1 | ❌ no banner |
| `188.34.73.218` | main2 | ❌ no banner (two different VMs) |
| `192.214.181.43` | main2 | ❌ no banner |
| `213.17.21.135` | **refactor1** | ✅ **SSH ok, first attempt** |

`213.17.x.x` works, `192.214.x.x` and `188.34.x.x` do not — independently of
the VM. The same split appears outside port 22: in run main2 the workload API
was reachable on `213.17.23.218` and `213.17.23.143`, while the first
bootstrapping attempt over `188.34.84.212` failed with the same kind of
connection teardown. Run refactor1 continued the pattern on other ports: its
workload API endpoints `213.17.21.81` and `213.17.23.103` were reachable
throughout.

That points to one shared cause — certain STACKIT public IP ranges are
unreachable from this environment, regardless of port — rather than two
unrelated ones. "TCP connect succeeds, zero bytes returned, immediate close" is
the signature of a transparently intercepting proxy.

**The falsifiable test has been run.** The prediction was: recreate the bastion
until a `213.17.x.x` address is assigned, then retry SSH; if it succeeds, the
cause is IP-range reachability and **not** port 22.
[run-refactor1-4-bastion.md](run-refactor1-4-bastion.md) got `213.17.21.135`
and SSH worked on the first attempt, followed by a successful jump to a
workload node and a full remote CNI install. **Conclusion: an environment
IP-range reachability issue, not a provider defect and not a port-22 block.**

**Consequence for future runs:** a bastion that refuses SSH from this
environment is not evidence of a bug. Check the assigned public IP first — if
it is outside `213.17.x.x`, force a recreate (delete the bastion server and its
public IP, then nudge a reconcile) until a reachable address is allocated.
