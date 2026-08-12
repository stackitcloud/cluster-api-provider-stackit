# e2e suite: first execution on the refactor branch

Date: 2026-08-12
Run: refactor 2 — see [SUMMARY.md](SUMMARY.md#timeline)
Branch: `refactor/code-cleanup-and-proper-abstraction` (commit `ef6b6e5`)
Status: ✅ works — all executed specs green, no leaks

First time the e2e suite has been executed on this branch at all — including
the free specs. Covers the four free specs plus the three billable specs whose
behaviour overlaps with the documented defects: cluster lifecycle, providerID
alignment, and the bastion path.

**Deliberately out of scope:** the upgrade, ClusterClass-topology and scale
packages. They remain unrun on any branch — see
[SUMMARY.md](SUMMARY.md#not-covered).

Reference for the manual equivalents: [run-refactor1-1-bootstrapping.md](run-refactor1-1-bootstrapping.md)
through [run-refactor1-4-bastion.md](run-refactor1-4-bastion.md).

## Test steps

### 1. Six barriers before the suite would run at all

The single most useful finding of this run. None of these is a code defect,
but together they explain why the billable specs had apparently never been
executed.

```
$ GOTOOLCHAIN=auto go vet -tags=e2e ./test/...

(exit 0)
```

**Result:** the suite compiles cleanly — the refactor only rewrote import paths
(`pkg/cloud` → `cloud`, `pkg/util` → `util`). Everything below is environment
and wiring, not code.

| # | Barrier | Symptom | Fix |
| --- | --- | --- | --- |
| 1 | `STACKIT_AVAILABILITY_ZONE` missing from `.envrc` but `requiredEnv` in the suite | spec aborts immediately | add it (`eu01-1`, one of the published failure domains) |
| 2 | `STACKIT_CLOUD_CONTROLLER_MANAGER_IMAGE` from `.envrc` pins minor 1.35; the suite asserts the image minor matches the Kubernetes version under test | hard assertion failure before anything runs | do not export it for e2e — the suite has per-minor defaults |
| 3 | Container runs `golang:1.25`, `go.mod` requires 1.26.0, `GOTOOLCHAIN=local` | `make manifests/test/test-e2e` abort | rebuild the devcontainer (the branch already bumps it to `golang:1.26`), or prefix `GOTOOLCHAIN=auto` |
| 4 | Calling `go test` directly does not create the kind cluster — only the `make` targets depend on `setup-test-e2e` | `BeforeSuite` fails at "loading the manager image on Kind" | run `make setup-test-e2e` first |
| 5 | `stackit-credentials` Secret missing in the **e2e** kind cluster | `Failed to read STACKIT credentials Secret` | create it after every setup — `cleanup-test-e2e` deletes the cluster, so this is not a one-off |
| 6 | Every run rewrites a tracked file | `config/manager/kustomization.yaml` shows up modified afterwards | revert manually, or make `deploy` stop persisting the image name |

Barrier 5 is the nastiest: because the e2e kind cluster is deleted at the end
of each run, the credentials Secret must be recreated every time.

Barrier 6 is a genuine papercut worth fixing: `make deploy` runs
`cd config/manager && kustomize edit set image controller=${IMG}`
([Makefile](../Makefile)), and `kustomize edit` **writes to the file**. Anyone
running the suite and committing afterwards silently carries
`example.com/cluster-api-provider-stackit:v0.0.1` into the repository.

**Vergleich main:** no counterpart — the suite was never run there either.

---

### 2. Free specs

```
$ GOTOOLCHAIN=auto make test-e2e

Ran 5 of 14 Specs in 87.014 seconds
SUCCESS! -- 5 Passed | 0 Failed | 0 Pending | 9 Skipped
ok  	github.com/stackitcloud/cluster-api-provider-stackit/test/e2e	87.084s

$ kind get clusters

capi-stackit
```

**Result:** five free specs pass — manager runs, metrics endpoint serves,
webhook certificate Secret is provisioned, CA injection for mutating and
validating webhooks. The nine billable specs skip as designed.

The suite creates and removes its **own** kind cluster
(`cluster-api-provider-stackit-test-e2e`); the management cluster
`capi-stackit` used by the manual runs is untouched, as the listing after the
run confirms.

**Vergleich main:** not applicable — no prior execution to compare against.

---

### 3. Cluster lifecycle with leak assertion

```
$ STACKIT_E2E_TEST_ID="e2e-lifecycle-…" STACKIT_E2E_CREATE_CLUSTER=true \
  go test -timeout=90m -tags=e2e ./test/e2e -v -ginkgo.v \
  --ginkgo.focus='create and delete.*workload Cluster' --ginkgo.timeout=90m

Ran 1 of 14 Specs in 402.722 seconds
SUCCESS! -- 1 Passed | 0 Failed | 0 Pending | 13 Skipped
```

**Result:** 1 control-plane / 1 worker cluster created and deleted with the
suite's own leak assertion (`ListServersByTags` → `BeEmpty`,
`ListAPIServerLoadBalancersByTags` → `BeEmpty`). Passed in 6.7 minutes.

**Vergleich main:** matches what
[run-refactor1-3-deletion.md](run-refactor1-3-deletion.md) established manually
— correct teardown order and no leaked resources. The spec asserts the same
property automatically.

---

### 4. providerID alignment (NodeRef)

```
$ STACKIT_E2E_TEST_ID="e2e-noderef-…" STACKIT_E2E_NODE_REF=true \
  go test -timeout=90m -tags=e2e ./test/e2e -v -ginkgo.v \
  --ginkgo.focus='align StackitMachine' --ginkgo.timeout=90m

Ran 1 of 14 Specs in 830.862 seconds
SUCCESS! -- 1 Passed | 0 Failed | 0 Pending | 13 Skipped
```

**Result:** passes in 13.8 minutes. Asserts that `Machine.spec.providerID`,
`StackitMachine.status.providerID` and `Node.spec.providerID` agree across the
whole cluster.

**Vergleich main / significance:** this is precisely the invariant that the
same-IP variant of [machine-recreate-bug.md](machine-recreate-bug.md) breaks —
and the spec passes, because it never removes a VM out-of-band. It confirms the
invariant holds in normal operation, and it is the natural place to extend, see
[test-strategy.md](test-strategy.md).

---

### 5. Bastion path

```
$ STACKIT_E2E_TEST_ID="e2e-bastion-…" \
  STACKIT_E2E_CREATE_CLUSTER=true STACKIT_E2E_BASTION=true \
  go test -timeout=90m -tags=e2e ./test/e2e -v -ginkgo.v \
  --ginkgo.focus='create and delete.*workload Cluster' --ginkgo.timeout=90m

Ran 1 of 14 Specs in 562.376 seconds
SUCCESS! -- 1 Passed | 0 Failed | 0 Pending | 13 Skipped
```

**Result:** bastion, public IP and security group provisioned and torn down
correctly; passed in 9.4 minutes. Requires `STACKIT_BASTION_SSH_KEY_NAME`,
`STACKIT_BASTION_ALLOWED_CIDRS`, `STACKIT_BASTION_IMAGE_ID` and
`STACKIT_BASTION_MACHINE_TYPE` — all exported in the same shell.

**Vergleich main:** the transient `BastionError` from
[bastion-bug.md](bastion-bug.md#1-the-bastion-security-group-is-attached-twice)
did not fail the spec — the suite tolerates it, because it waits for
`BastionReady` rather than asserting an error-free path. So the defect stays
invisible at this level, which is why it belongs in a unit test
([test-strategy.md](test-strategy.md)).

---

### 6. Resource check

```
$ for r in server volume load-balancer public-ip; do
    stackit $r list | grep -ciE 'stackit-e2e|e2e-lifecycle|e2e-noderef|e2e-bastion'
  done
$ stackit security-group list | grep -ciE 'stackit-e2e|e2e-lifecycle|e2e-noderef|e2e-bastion'

0
0
0
0
0
```

**Result:** no leaked resources from any of the three billable runs. The
suite's own tagging (`stackitE2ETags`) and its `defer` cleanup work as designed;
`make cleanup-stackit` was never needed.

**Vergleich main:** same result as the manual runs, but asserted by the suite
itself rather than checked by hand afterwards.

## Conclusion

**Status:** ✅ works

The e2e suite runs green on the refactor branch: 5 free specs plus 3 billable
specs, no failures, no leaked cloud resources. Combined with
[run-refactor1-*](run-refactor1-1-bootstrapping.md), the refactor is now
verified both manually and by the suite for bootstrapping, lifecycle,
providerID alignment, bastion and deletion.

**The real finding is the barrier list in step 1.** Six separate obstacles sit
between "the suite exists" and "the suite runs", and none of them is
documented. That is the most plausible explanation for why the billable specs
had never been executed. Three of them (1, 2, 5) could be folded into
`setup-test-e2e` or the docs so a single `make` invocation suffices; barrier 6
should be fixed regardless, since it silently dirties the working tree.

**What the suite does not catch:** the transient `BastionError` (step 5) and
the recreate defect — neither is reachable at this level. The assignment of
each defect to unit, envtest or e2e is worked out in
[test-strategy.md](test-strategy.md).

**Still unrun:** upgrade, ClusterClass-topology and scale packages — on any
branch.
