# Bugs: bastion provisioning path

Date: 2026-08-10
Source: code review of the bastion path after run 2 — see [SUMMARY.md](SUMMARY.md#timeline)
Status: ⚠️ **open** — four defects confirmed in code, none implemented

Collects the defects found by reading the bastion code paths against the
evidence from [run1-4-bastion.md](run1-4-bastion.md) and
[run2-4-bastion.md](run2-4-bastion.md). All four are confirmed by reading the
source, not inferred from behaviour alone.

**Why this document exists:** both bastion run protocols classify the two
recurring `BastionError` messages as harmless, self-resolving STACKIT-API
races and explicitly tell the reader not to worry about them. That
classification is wrong — see bug 1. The protocols themselves are left as
written (they record what was observed); the corrections live here.

## 1. The bastion security group is attached twice

**Location:** [../pkg/cloud/sdk_client.go](../pkg/cloud/sdk_client.go),
`EnsureBastion`, lines 247-265.

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
[run2-4-bastion.md](run2-4-bastion.md) is direct proof that the group was
already attached at that point — the call is genuinely redundant, not merely
defensive.

`addSecurityGroupToServer` (same file, lines 802-812) only swallows
`IsConflict`:

```go
if !IsConflict(err) {
    return err       // both 400 and 404 propagate
}
```

**Additional effect not noticed in either run:** when (2) fails,
`EnsureBastion` returns immediately, so `ensurePublicIP()` and
`AddPublicIpToServer()` (lines 267-284) never run in that reconcile. The
bastion's public IP is therefore assigned a full reconcile cycle later than
necessary — part of the delay complained about in
[run2-4-bastion.md](run2-4-bastion.md) needs no external explanation.

**Fix:** drop the call at lines 263-265. If it is to be kept as a defensive
re-attach for the `CreateServer`-found-existing-server path, it must also
tolerate `IsNotFound` and the duplicate-`400`.

---

## 2. Changing `allowedCIDRs` never revokes the old access

**Location:** [../pkg/cloud/sdk_client.go](../pkg/cloud/sdk_client.go),
`ensureBastionSecurityGroupRules`, lines 692-720.

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
[run1-4-bastion.md](run1-4-bastion.md)), or deliberately to restrict access —
leaves the **old** SSH rule in place permanently.

**Why this matters for the test results:** [run1-4-bastion.md](run1-4-bastion.md)
step 5 certifies that the CIDR restriction works. That test was run from a
mobile network that had never been in any rule, so it cannot detect this bug —
it only proves "an IP that was never allowed is refused", not "a revoked IP is
refused". The CIDR-narrowing variant proposed in
[run2-4-bastion.md](run2-4-bastion.md) would, on current code, **falsely
report success**: the old rule would still admit the tester.

**Fix:** reconcile the rule set in both directions — delete SSH rules whose
`ipRange` is not in the desired CIDR list.

---

## 3. `bastionNeedsRecreate` only reacts to cloud-init changes

**Location:** [../internal/controller/stackitcluster_controller.go](../internal/controller/stackitcluster_controller.go),
lines 470-476.

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

**Relevance:** [run1-4-bastion.md](run1-4-bastion.md) step 2 hit a stale
`STACKIT_BASTION_SSH_KEY_NAME` and fixed it by regenerating and re-applying.
That worked only because the bastion had never been created (`keypair not
found`). Had it come up with an existing-but-wrong key, re-applying would not
have helped — a trap documented nowhere else.

**Fix:** include the relevant spec fields in the recreate decision, or document
the limitation prominently.

---

## 4. `cluster-template-bastion.yaml` ignores `WORKER_MACHINE_COUNT`

**Location:** [../templates/cluster-template-bastion.yaml](../templates/cluster-template-bastion.yaml), line 160.

```
$ grep -n replicas templates/cluster-template.yaml

49:  replicas: ${CONTROL_PLANE_MACHINE_COUNT}
123:  replicas: ${WORKER_MACHINE_COUNT}      # correctly parameterized

$ grep -n replicas templates/cluster-template-bastion.yaml

85:  replicas: ${CONTROL_PLANE_MACHINE_COUNT}
160:  replicas: 3                            # hardcoded
```

Every bastion-enabled cluster gets 3 workers regardless of what is requested.
Found and confirmed in [run2-4-bastion.md](run2-4-bastion.md); run 1 ran with
4 nodes and did not recognise the discrepancy.

**Fix:** `replicas: ${WORKER_MACHINE_COUNT}`, matching the base template.

---

## Open, not a code defect: the SSH failures

The two runs give contradictory explanations for bastions refusing SSH
(TCP connects, no banner):

- [run1-4-bastion.md](run1-4-bastion.md): "transient, tied to that specific
  public IP, unconfirmed" — 1 of 3 bastions affected.
- [run2-4-bastion.md](run2-4-bastion.md): "environment blocks TCP/22 to STACKIT
  ranges" — 3 of 3 affected.

Neither holds up: run 2's explanation cannot be right because run 1 reached
STACKIT bastions over SSH from the same environment; run 1's cannot be right
because run 2 reproduced it across two different IPs.

Taking the IPs recorded in both documents together, a single consistent pattern
emerges:

| IP | Run | Result |
| --- | --- | --- |
| `213.17.20.79` | 1 | ✅ SSH ok |
| `213.17.23.100` | 1 | ✅ SSH ok |
| `192.214.188.106` | 1 | ❌ no banner |
| `188.34.73.218` | 2 | ❌ no banner (two different VMs) |
| `192.214.181.43` | 2 | ❌ no banner |

`213.17.x.x` works, `192.214.x.x` and `188.34.x.x` do not — independently of
the VM. The same split appears outside port 22: in run 2 the workload API was
reachable on `213.17.23.218` and `213.17.23.143`, while the first bootstrapping
attempt over `188.34.84.212` failed with the same kind of connection teardown
(recorded as a transient outage in [SUMMARY.md](SUMMARY.md#timeline)).

That points to one shared cause — certain STACKIT public IP ranges are
unreachable from this environment, regardless of port — rather than two
unrelated ones. "TCP connect succeeds, zero bytes returned, immediate close" is
the signature of a transparently intercepting proxy.

**Falsifiable test for the next run:** recreate the bastion until a
`213.17.x.x` address is assigned, then retry SSH. If it succeeds, the cause is
IP-range reachability and **not** port 22.
