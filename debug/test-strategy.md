# Test strategy: which defect belongs on which test level

Date: 2026-08-12
Status: 📋 decision aid — **implemented**, see [Landing the tests](#landing-the-tests)

Written while planning coverage for the three code-confirmed defects in
[bastion-bug.md](bastion-bug.md) and [machine-recreate-bug.md](machine-recreate-bug.md).
The question "unit test or envtest?" turned out to have a structural answer
rather than a preference-based one, so it is recorded here instead of being
re-derived each time.

## The three levels this repository has

| Level | Where | How | Cost | Runs in CI |
| --- | --- | --- | --- | --- |
| **Unit** | `cloud/*_test.go` | `httptest` mock of the STACKIT API; asserts the HTTP requests the client issues. No cluster. | none | yes (`make test`) |
| **envtest** | `controller/*_test.go` | real kube-apiserver + etcd via `setup-envtest`, Ginkgo, with the **fake cloud client** (`cloud/fake`). No kubelet, no real infrastructure. | none | yes (`make test`) |
| **e2e** | `test/e2e/` | kind cluster + real STACKIT resources, unlocked per env gate. | real cloud spend, 7–90 min | no (manual) |

Coverage before this work: `cloud` 39.1 %, `controller` 70.2 %, `webhook/v1alpha1` 80.6 %.
After: `cloud` **55.5 %**, `controller` **71.0 %**, webhook unchanged.

Note the controller-runtime `client/fake` is **not used anywhere** in this
repository — a pure unit test for controller logic would introduce a new
pattern.

## The asymmetry that decides it

Not every defect is reachable from every level:

- **envtest uses the fake cloud client.** That fake does not implement
  `ensureBastionSecurityGroupRules` at all, so the two cloud-layer defects are
  **structurally untestable** there.
- **The unit level (`cloud`) knows no controller logic.** The recreate defect
  lives in `ensureServer` in the `controller` package and is **unreachable**
  from there.

So this is not an either/or decision but an assignment.

## Assignment

### Defects 1 + 2 (security group attached twice, `allowedCIDRs` never revoked) → `cloud` unit tests

Both live in `cloud/sdk_client.go` and manifest as HTTP requests that are or
are not issued — exactly what `cloud/sdk_client_test.go` is built for
(`newSDKTestServer`; see `TestSDKClientCreateServerUsesExpectedPayload` as the
pattern).

- *Attach test:* run `EnsureBastion` against the mock and count how often the
  attach endpoint is hit. After the fix: **zero** extra attach requests, since
  the group is already in the create payload.
- *Revocation test:* mock `ListSecurityGroupRules` with an existing rule for
  CIDR A, call `EnsureBastion` with `AllowedCIDRs: [B]`. After the fix: a
  create for B **and a delete for A's rule**.

*Why not envtest:* impossible — the fake knows nothing about rules.
*Why not e2e:* expensive and slow, and it would additionally require extending
the `cloud.Client` interface with `ListSecurityGroupRules`, because
`SecurityGroup` only carries `ID` and `Name`.

### Defect 3 (recreate of an already-joined machine) → envtest

Lives in `ensureServer` (`controller/stackitmachine_infrastructure.go`). The
fake client already provides everything needed:

- `CreateServerCalls int` — commented in the source as
  "counts successful CreateServer calls (for idempotency…)"
- an exported `DeleteServer(ctx, id)` to simulate the disappearance

Flow: reconcile a `StackitMachine` to `Ready` (`CreateServerCalls == 1`) →
`fake.DeleteServer(instanceID)` → reconcile again → **assert `CreateServerCalls`
stays 1**. Without the guard it becomes 2; verified by temporarily reverting the
guard, which made the spec fail exactly there.

*Alternative — pure unit test:* call `ensureServer` directly with
controller-runtime's `client/fake` plus `cloud/fake`. Faster and needs no
binaries, but introduces a pattern the repo does not use, requires hand-building
a `MachineScope`, and bypasses the reconcile path where the defect actually
shows. Not recommended.

## Side by side

| | Unit (`cloud`) | envtest (`controller`) | e2e |
| --- | --- | --- | --- |
| Covers double SG attach | ✅ directly | ✖️ fake knows no rules | ⚠️ only indirectly via `BastionError` |
| Covers CIDR revocation | ✅ directly | ✖️ same | ⚠️ only with an interface extension |
| Covers recreate guard | ✖️ wrong level | ✅ directly, counter already exists | ✅ realistic but expensive |
| Cost | none | none | cloud resources |
| Duration | ms | s | 7–90 min |
| In CI | yes | yes | no |
| Deterministic | yes | yes | no (network, IP ranges, timing) |
| Realism | low | medium | high |

**Recommended combination:** defects 1+2 as `cloud` unit tests, defect 3 as an
envtest spec. All three then covered for free, fast and in CI, without a single
extra cloud resource.

**Role of e2e:** the integration statement — lifecycle, leak-freedom,
providerID alignment against real infrastructure. Confirmed by the runs in
[run-refactor2-1-e2e.md](run-refactor2-1-e2e.md). An additional e2e spec for
the out-of-band delete would be the most realistic but most expensive
safeguard; worth it once the fix exists and its effect should be shown
end-to-end.

## Optional e2e specs — deferred, and what each would need

The three defects are covered by unit and envtest specs (above), which is
enough to prevent regressions. The e2e specs below would add the
*integration* statement — that the fix also holds against real infrastructure,
with a real kubelet, real IP assignment and real CAPI controllers. They are
**deliberately deferred**: expensive, slow, non-deterministic, and not run in
CI. Recorded here so the work is not re-derived when someone wants them.

Shared prerequisites for **any** new billable spec — all six were hit in
practice, see [run-refactor2-1-e2e.md](run-refactor2-1-e2e.md) step 1:

1. `STACKIT_AVAILABILITY_ZONE` exported (missing from `.envrc`).
2. `STACKIT_CLOUD_CONTROLLER_MANAGER_IMAGE` **not** exported — the suite
   asserts the image minor matches the tested Kubernetes version.
3. Go toolchain: rebuild the devcontainer to `golang:1.26`, or prefix
   `GOTOOLCHAIN=auto`.
4. `make setup-test-e2e` before calling `go test` directly — only the make
   targets create the kind cluster.
5. `stackit-credentials` Secret recreated in the e2e kind cluster **after every
   setup**, because `cleanup-test-e2e` deletes the cluster.
6. `config/manager/kustomization.yaml` gets rewritten by `make deploy`; revert
   it afterwards.

### Spec A — out-of-band VM delete (machine-recreate-bug)

*Value:* the only way to observe the **same-IP variant**, which is what makes
the defect dangerous. The envtest spec proves no second `CreateServer` happens;
it cannot show what a real replacement VM would do to the Node object.

*Sketch:* extend or clone the existing NodeRef spec (`STACKIT_E2E_NODE_REF`).
After `waitForKubeadmWorkloadClusterReady`, take the control-plane
`StackitMachine`'s `status.instanceID`, delete the server via
`cloudClient.DeleteServer` (client from
`stackitCloudClientFromCredentialsSecret`), then assert with `Eventually`:

- no replacement appears — `ListServersByTags(leakTags)` keeps the reduced count;
- `expectProviderIDNodeRefAlignment` still holds for the remaining machines;
- every referenced providerID still resolves to an existing server — this is
  the assertion the current spec lacks and the one the same-IP variant breaks.

*Needed:* a new env gate (e.g. `STACKIT_E2E_OUT_OF_BAND_DELETE`) plus a make
target; the fixture and helpers already exist. **No production-code change.**

*Cost:* ~15 min per run, one cluster's worth of resources. The teardown must
tolerate a machine whose infrastructure is gone — reuse the existing
`cleanupCloudServersByID` / `cloud.CleanupByTags` deferred cleanup.

*Caveat:* whether the same-IP or different-IP variant occurs is **not
controllable** — STACKIT decides address assignment. The spec must therefore
assert the invariant (no recreate, providerIDs resolve), not a specific
symptom, or it will be flaky.

### Spec B — allowedCIDRs revocation (bastion-bug 2)

*Value:* proves the rule is really gone at the provider, not just that the
client issued a DELETE. The unit test asserts the request; only e2e shows the
effective state.

*Sketch:* bastion cluster with `allowedCIDRs: [A]`; confirm the rule; patch
`spec.bastion.allowedCIDRs` to `[B]`; wait for reconcile; assert rule B exists
**and rule A is gone**. Purely API-based — no SSH, therefore immune to the
IP-range reachability problem that blocked run main2.

*Needed:* **a production-code change** — `cloud.Client` exposes no rule
listing, and `SecurityGroup` carries only `ID` and `Name`. Add
`ListSecurityGroupRules(ctx, securityGroupID) ([]SecurityGroupRule, error)`
to the interface, the `SDKClient` (it already calls the SDK method internally)
and `cloud/fake`. That is the main reason this one is deferred: it widens a
production interface purely for a test.

*Alternative without an interface change:* have the spec shell out to
`stackit security-group rule list`. Cheaper to write, but drags a CLI
dependency into the suite — not recommended.

*Cost:* ~10 min per run.

### Spec C — bastion provisioning must be error-free (bastion-bug 1)

*Value:* the current bastion spec passes **even while the defect occurs**,
because it waits for `BastionReady` and never inspects the path taken —
confirmed in [run-refactor2-1-e2e.md](run-refactor2-1-e2e.md) step 5.

*Sketch:* while waiting for `BastionReady`, also assert that
`BastionReady` never goes to `False` with reason `BastionError`.

*Needed:* no production change; only polling the condition history instead of
the end state.

*Caveat:* other transient errors are legitimate (e.g. the security group
attaching before the port exists is gone with the fix, but STACKIT may still
return transient API errors). Assert on the **specific** reason, not on "no
error ever", or the spec becomes flaky.

## Landing the tests

Done on 2026-08-12: each test was merged **together with its fix**, as
recommended. All three fixes turned out to be small, as expected.

| Test | Fix | Proven effective by |
| --- | --- | --- |
| `TestSDKClientEnsureBastionAttachesSecurityGroupOnlyOnce` | removed the redundant `addSecurityGroupToServer` call | reverting the fix → *"security group attached 1 extra time(s), want 0"* |
| `TestSDKClientEnsureBastionRevokesRemovedCIDR` | `ensureBastionSecurityGroupRules` now also deletes SSH rules whose CIDR is no longer desired | reverting the fix → *"deleted rules [], want [...] — the revoked CIDR keeps its SSH access"* |
| `"does not silently recreate the server of an already-provisioned machine"` | guard in `ensureServer`: a missing server for a machine with `Status.Initialization.Provisioned` surfaces an error instead of calling `CreateServer` | reverting the guard → spec fails at exactly that assertion |

Each fix was reverted individually and the corresponding test re-run, to prove
the test actually catches the defect rather than passing for unrelated reasons.
`make test` is green with all three in place.
