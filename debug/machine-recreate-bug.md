# Bug: StackitMachine silently recreates a server for an already-joined machine

Date: 2026-08-10
Source: run 2, HA control-plane package — see [SUMMARY.md](SUMMARY.md#timeline)
Status: ⚠️ **open** — root cause confirmed in code, fix not implemented

When a `StackitMachine`'s backing STACKIT server disappears out-of-band, the
reconciler creates a replacement server unconditionally — including for a
machine that had already bootstrapped and joined the cluster successfully. The
replacement VM boots but can never rejoin, so it consumes a VM and a volume
indefinitely while the cluster stays degraded, until an operator deletes the
`Machine`.

Observed in [run2-2-ha-controlplane.md](run2-2-ha-controlplane.md) step 4a.

## Evidence from the run

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
(see [run2-1-bootstrapping.md](run2-1-bootstrapping.md)):

```
$ kubectl --kubeconfig "${KUBECONF_WORKERCLUSTER}" get node stackit-capi-test-control-plane-bkkfk \
  -o jsonpath='{range .status.conditions[?(@.type=="Ready")]}{.lastHeartbeatTime} {.status} {.reason}{"\n"}{end}'

2026-08-07T22:34:41Z False KubeletNotReady

$ kubectl --kubeconfig "${KUBECONF_WORKERCLUSTER}" get node stackit-capi-test-control-plane-bkkfk \
  -o jsonpath='{.status.addresses}{"\n"}'

[{"address":"10.42.0.34","type":"InternalIP"}, ...]   # still the OLD VM's IP; the new VM has 10.42.0.78
```

## Root cause

[../internal/controller/stackitmachine_controller.go](../internal/controller/stackitmachine_controller.go),
`ensureServer`, lines 293-338:

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
bootstrap `UserData`. For a machine that already ran `kubeadm join` once, that
data can no longer produce a successful join, so the new VM never becomes a
node.

## Why it matters

The infrastructure layer *looks* like it is healing itself while the cluster
stays broken. That is operationally worse than a machine simply sitting in a
failed state:

- `KubeadmControlPlane` reports `3 desired, 2 ready` and does not act (there is
  no `MachineHealthCheck` in any template — see
  [run2-2-ha-controlplane.md](run2-2-ha-controlplane.md) step 4a2).
- The `Machine` stays in `phase: Running`, so nothing signals a terminal error.
- Each reconcile-triggered recreate consumes another VM and boot volume.

**Run 1 is not evidence against this.** [run1-2-ha-controlplane.md](run1-2-ha-controlplane.md)
concluded "no automatic remediation" for the same scenario, but its step 4a
never ran `stackit server list` — it only inspected Kubernetes objects and the
load balancer. The silent recreate would not have been visible. The two runs
are consistent; run 2 simply looked at the infrastructure layer.

## Fix options

1. **Guard the recreate.** Treat "instance ID was set, server is now gone" as a
   terminal condition for an already-initialised machine
   (`Status.Initialization.Provisioned` / an existing `NodeRef`) and surface a
   failure condition instead of calling `CreateServer`. That lets CAPI or a
   `MachineHealthCheck` remediate properly by replacing the `Machine`.
2. **Keep the recreate but make it correct** — request fresh bootstrap data
   before recreating. Considerably more involved, and duplicates what KCP
   already does when a `Machine` is replaced.

Option 1 matches upstream Cluster API expectations: infrastructure providers
report state, the Machine controller decides on replacement.

## Verification once fixed

- Delete a joined control-plane VM out-of-band; no replacement server may be
  created at STACKIT (`stackit server list`).
- The `StackitMachine` must surface a failure condition and the `Machine` must
  become eligible for remediation rather than staying `phase: Running`.
- With a `MachineHealthCheck` configured, the `Machine` must be replaced
  automatically and the cluster return to 3/3 without operator action.
