# Bug: StackitMachine silently recreates a server for an already-joined machine

Date: 2026-08-10, updated 2026-08-11 after the refactor run
Source: HA control-plane package of run main2 and run refactor1 — see [SUMMARY.md](SUMMARY.md#timeline)
Status: ⚠️ **open** — root cause confirmed in code, unchanged on both branches, fix not implemented

When a `StackitMachine`'s backing STACKIT server disappears out-of-band, the
reconciler creates a replacement server unconditionally — including for a
machine that had already bootstrapped and joined the cluster successfully.

**The replacement is created with the original bootstrap data, and what happens
next depends on whether it is assigned the same internal IP as the VM it
replaces.** Both outcomes are bad:

| Replacement gets… | Outcome | Observed in |
| --- | --- | --- |
| a **different** IP | never rejoins; node stays `NotReady` — visibly broken, but at least obvious | [run-main2-2-ha-controlplane.md](run-main2-2-ha-controlplane.md) |
| the **same** IP | rejoins, KCP reports a healthy 3/3 — but `Machine`/`Node` permanently reference a deleted server. **Nothing surfaces this.** | [run-refactor1-2-ha-controlplane.md](run-refactor1-2-ha-controlplane.md) |

Either way a VM and a volume are consumed and the cluster needs an operator to
delete the `Machine`; the second case is harder to notice because everything
looks healthy.

## Evidence from run main2 (different-IP outcome)

The current control-plane leader's VM was deleted directly at STACKIT to
simulate a hard node failure. About a minute later:

```
$ kubectl get stackitmachine stackit-capi-test-control-plane-bkkfk \
  -o jsonpath='{.status.instanceState} {.status.instanceID}{"\n"}'

CREATING 41ae67cf-3515-470f-a5db-0ed0c3043fe2   # NEW instance ID, same StackitMachine

$ stackit server describe 41ae67cf-3515-470f-a5db-0ed0c3043fe2

BOOT VOLUME  75270e5a-fc57-4aff-9f2e-9468a26e15d6   # fresh volume, not the deleted VM's
CREATED AT   2026-08-07T22:29:14Z
```

The replacement reached `ACTIVE`, but the Kubernetes `Node` object never
recovered — its `Ready` heartbeat froze at the moment the original VM died and
had not moved 9 minutes later, far beyond the ~1-3 minutes a normal join takes
(see [run-main2-1-bootstrapping.md](run-main2-1-bootstrapping.md)):

```
$ kubectl --kubeconfig "${KUBECONF_WORKERCLUSTER}" get node stackit-capi-test-control-plane-bkkfk \
  -o jsonpath='{range .status.conditions[?(@.type=="Ready")]}{.lastHeartbeatTime} {.status} {.reason}{"\n"}{end}'

2026-08-07T22:34:41Z False KubeletNotReady

$ kubectl --kubeconfig "${KUBECONF_WORKERCLUSTER}" get node stackit-capi-test-control-plane-bkkfk \
  -o jsonpath='{.status.addresses}{"\n"}'

[{"address":"10.42.0.34","type":"InternalIP"}, ...]   # still the OLD VM's IP; the new VM has 10.42.0.78
```

## Root cause

**Location** — the refactor split the machine controller, so `ensureServer`
moved file *and* line. The logic is unchanged; only the return signature
differs, now `(server, created bool, err)`:

| Branch | Path |
| --- | --- |
| `main` | `internal/controller/stackitmachine_controller.go`, `ensureServer`, lines 293-338 |
| `refactor` | [`controller/stackitmachine_infrastructure.go`](../controller/stackitmachine_infrastructure.go), `ensureServer`, lines 205-250 |

The `main` version reads:

```go
func (r *StackitMachineReconciler) ensureServer(...) (*cloud.Server, error) {
	sm := s.StackitMachine
	if sm.Status.InstanceID != "" {
		server, err := c.GetServer(ctx, sm.Status.InstanceID)
		if err == nil {
			return server, nil
		}
		if !cloud.IsNotFound(err) {
			return nil, err
		}
		// fall through to lookup-by-tags / re-create.
	}
	if server, err := c.FindServerByTags(ctx, tags); err == nil {
		return server, nil
	} else if !cloud.IsNotFound(err) {
		return nil, err
	}
	...
	return c.CreateServer(ctx, cloud.CreateServerInput{
		...
		UserData: userData,   // the same one-time bootstrap data as the original join
		...
	})
}
```

There is no check for whether this machine had already provisioned and joined.
`GetServer` → `NotFound` leads straight to `CreateServer`, reusing the original
bootstrap `UserData` — which is pinned to the original machine's identity and
address.

Whether the replayed data still works decides which of the two outcomes occurs:
if the new VM is assigned a **different** internal IP, the join cannot succeed
and the VM never becomes a node (run main2); if it happens to get the **same**
IP, the join does succeed, but the baked-in provider ID is now stale (run
refactor1, see below). The provider never distinguishes these cases — it simply
recreates.

## Why it matters

The infrastructure layer *looks* like it is healing itself, but never actually
restores a correct cluster. That is operationally worse than a machine simply
sitting in a failed state, in both outcomes:

- **Nothing signals a terminal error.** The `Machine` stays in
  `phase: Running`, and there is no `MachineHealthCheck` in any template, so
  CAPI does not remediate on its own (see
  [run-main2-2-ha-controlplane.md](run-main2-2-ha-controlplane.md) step 4a2).
  The missing MHC is a separately tracked roadmap item — see
  [../docs/src/getting-started/overview.md](../docs/src/getting-started/overview.md),
  *"Full Cluster API Support"*.
- **Different-IP outcome:** `KubeadmControlPlane` reports `3 desired, 2 ready`
  and stays there — degraded, but at least visible.
- **Same-IP outcome:** `KubeadmControlPlane` reports `3 desired, 3 ready` and
  looks entirely healthy while carrying a dangling provider ID. Nothing is
  degraded, so nobody investigates.
- Each disappearance of the server costs one additional VM and boot volume that
  no longer corresponds to any `Machine` identity. (Only one recreate per
  disappearance was observed in either run — once `Status.InstanceID` points at
  the new server, `GetServer` succeeds and no further create happens.)

**Run main1 is not evidence against this.** [run-main1-2-ha-controlplane.md](run-main1-2-ha-controlplane.md)
concluded "no automatic remediation" for the same scenario, but its step 4a
never ran `stackit server list` — it only inspected Kubernetes objects and the
load balancer. The silent recreate would not have been visible. The runs are
consistent; run main2 simply looked at the infrastructure layer.

### The same-IP variant is worse (run refactor1, 2026-08-11)

When the replacement VM happened to receive the deleted VM's internal IP
(`10.42.0.155`), it rejoined within ~3 minutes and the cluster reported itself
fully healthy — while the identities stayed permanently split:

```
Machine.spec.providerID:          stackit://775e7fb6-…   # deleted server
StackitMachine.status.providerID: stackit://6af49941-…   # live server
Node.spec.providerID:             stackit://775e7fb6-…   # deleted server
```

`Machine.spec.providerID` is immutable in CAPI, and the node registered with
the old ID because it replayed the original bootstrap data — the same root
cause as above, just with a different symptom. `KubeadmControlPlane` reported
`3 desired, 3 ready`, there was no condition, event or degraded status, and
`stackit server describe` on the referenced ID returns nothing. Only deleting
the `Machine` restores consistency.

This makes the bug **harder to detect than previously documented**: a cluster
can carry a dangling provider ID indefinitely while looking perfectly healthy.
Anything that resolves a node by provider ID — the cloud-controller-manager's
node lifecycle handling in particular — is operating on a server that no longer
exists.

## A MachineHealthCheck is not a fix for this

Worth stating explicitly, because "just add an MHC" is the tempting answer: an
MHC would remediate the *consequence*, not the cause, and in the more dangerous
outcome it would not fire at all.

- **Different-IP outcome:** the node goes `NotReady`, so an MHC would
  eventually replace the `Machine`. Helpful, but the provider still burns one
  VM and volume per occurrence in the meantime.
- **Same-IP outcome:** there is **nothing for an MHC to detect**. The node is
  `Ready`, the lease is being renewed, KCP reports `3 desired, 3 ready`, and no
  condition is `Unknown` or `False`. The cluster only carries a dangling
  provider ID — which no MHC checks. It would run indefinitely without firing.

The cause is `ensureServer` recreating at all. Fix that, and the MHC question
becomes an independent, ordinary roadmap item
([../docs/src/getting-started/overview.md](../docs/src/getting-started/overview.md)).

## Fix options

1. **Guard the recreate.** Treat "instance ID was set, server is now gone" as a
   terminal condition for an already-initialised machine
   (`Status.Initialization.Provisioned` / an existing `NodeRef`) and surface a
   failure condition instead of calling `CreateServer`. That lets CAPI or a
   `MachineHealthCheck` remediate properly by replacing the `Machine` — and,
   crucially, makes the same-IP outcome *visible* so that remediation has
   something to act on.
2. **Keep the recreate but make it correct** — request fresh bootstrap data
   before recreating. Considerably more involved, duplicates what KCP already
   does when a `Machine` is replaced, and would still leave
   `Machine.spec.providerID` stale, since it is immutable once set. The same-IP
   variant shows this is not a theoretical concern.

Option 1 matches upstream Cluster API expectations: infrastructure providers
report state, the Machine controller decides on replacement.

## Verification once fixed

- Delete a joined control-plane VM out-of-band; no replacement server may be
  created at STACKIT (`stackit server list`).
- The `StackitMachine` must surface a failure condition and the `Machine` must
  become eligible for remediation rather than staying `phase: Running`.
- With a `MachineHealthCheck` configured, the `Machine` must be replaced
  automatically and the cluster return to 3/3 without operator action.
- **Provider-ID invariant:** `Machine.spec.providerID`,
  `StackitMachine.status.providerID` and `Node.spec.providerID` must agree and
  must reference a server that actually exists. This is the check that catches
  the same-IP variant, and the existing e2e spec *"should align StackitMachine,
  Machine, and Node providerIDs"* (`test/e2e/e2e_test.go`, gated by
  `STACKIT_E2E_NODE_REF`) already asserts exactly this — it simply never
  deletes a VM out-of-band. Extending that spec with an out-of-band delete
  would turn this bug into an automated regression test.
