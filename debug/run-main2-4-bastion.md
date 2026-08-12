# Regression: private cluster via bastion / jump host

Date: 2026-08-07
Run: 2 of 2 — see [SUMMARY.md](SUMMARY.md#timeline)
Cluster: `stackit-capi-test`
Status: ⚠️ blocked — could not verify SSH-dependent steps from this
environment; a real, unrelated template bug was found and confirmed in code

Verifies private cluster access via a provider-managed bastion: SSH to the
bastion and onward to workload nodes (jump host), kube-api reachability, and
— the substantive part — that real cluster administration works remotely
over that path by installing a CNI into a freshly bootstrapped cluster from
outside. This run could not get past the first SSH step; see the
conclusion for why, and for what was still learned along the way.

Prerequisites: see [SUMMARY.md](SUMMARY.md#shared-prerequisites), plus:

- `cilium` CLI installed locally (used by `make install-workload-cni`; the
  devcontainer installs it via `.devcontainer/post-install.sh`).
- A reachable test CIDR as the allowed range (not `0.0.0.0/0`, the e2e
  default per [../docs/src/development/testing.md](../docs/src/development/testing.md)).
  `.envrc` derives it as `ADMIN_CIDR=$(curl ifconfig.me)"/32"`.
- [../templates/cluster-template-bastion.yaml](../templates/cluster-template-bastion.yaml)
  needs **two separate** SSH key variables — `STACKIT_BASTION_SSH_KEY_NAME`
  for the bastion host and `STACKIT_SSH_KEY_NAME` for the
  control-plane/worker nodes. They may point at the same key pair, but both
  must be set (`.envrc` ships `STACKIT_SSH_KEY_NAME=""` by default):
  ```
  export STACKIT_SSH_KEY_NAME="${CLUSTER_NAME}-break-glass"
  export STACKIT_BASTION_SSH_KEY_NAME="${STACKIT_SSH_KEY_NAME}"
  export STACKIT_BASTION_ALLOWED_CIDRS="${ADMIN_CIDR}"
  export STACKIT_BASTION_IMAGE_ID="${STACKIT_IMAGE_ID}"
  export STACKIT_BASTION_MACHINE_TYPE="${STACKIT_MACHINE_TYPE}"
  ```

## Test steps

### 1. Key pair and cluster creation

```
$ stackit key-pair list

stackit-capi-test-break-glass │ cluster-api-provider-stackit: true │ ...

$ clusterctl generate cluster "${CLUSTER_NAME}" \
  --from templates/cluster-template-bastion.yaml \
  --target-namespace "${NAMESPACE}" > cluster-bastion.yaml

$ kubectl apply -f cluster-bastion.yaml

cluster.cluster.x-k8s.io/stackit-capi-test created
stackitcluster.infrastructure.cluster.x-k8s.io/stackit-capi-test created
kubeadmcontrolplane.controlplane.cluster.x-k8s.io/stackit-capi-test-control-plane created
machinedeployment.cluster.x-k8s.io/stackit-capi-test-md-0 created
...
```

**Result:** Key pair from a previous session still existed and was reused.
Cluster applied cleanly.

---

### 2. Bug found — `WORKER_MACHINE_COUNT` is ignored by the bastion template

```
$ kubectl get machinedeployment stackit-capi-test-md-0 -o jsonpath='{.spec.replicas}{"\n"}'

3
```

**Result:** `WORKER_MACHINE_COUNT=1` was exported before generating the
manifest (same as every other package in this run), yet the
`MachineDeployment` came back with `replicas: 3`. Root cause, confirmed by
inspecting the template directly:

```
$ grep -n replicas templates/cluster-template.yaml

49:  replicas: ${CONTROL_PLANE_MACHINE_COUNT}
123:  replicas: ${WORKER_MACHINE_COUNT}      # correctly parameterized

$ grep -n replicas templates/cluster-template-bastion.yaml

85:  replicas: ${CONTROL_PLANE_MACHINE_COUNT}
160:  replicas: 3                            # hardcoded — ignores WORKER_MACHINE_COUNT
```

**This is a real, reproducible bug**, independent of everything else in
this document:
[../templates/cluster-template-bastion.yaml:160](../templates/cluster-template-bastion.yaml)
hardcodes the worker `MachineDeployment` to 3 replicas instead of using
`${WORKER_MACHINE_COUNT}` like the base template does. Every bastion-enabled
cluster created from this template gets 3 workers regardless of what is
requested. Cleanup of the extra VMs was still fully correct (see step 6),
so this is a manifest/template defect, not a controller defect.

---

### 3. Bastion provisioning

```
$ kubectl get stackitcluster "${CLUSTER_NAME}" \
  -o jsonpath='{range .status.conditions[?(@.type=="BastionReady")]}{.status} {.reason} {.message}{"\n"}{end}'

False Provisioning bastion server state is CREATING   # t+100s
False BastionError not found: add security group to server: 404 Not Found ...
  could not be found as device id on any ports                            # t+100s-120s, transient
True Available                                                             # t+145s
```

**Result:** Same known transient error as documented in prior runs — the
controller attaches the security group before the server has a network
port; self-heals once the server reaches `ACTIVE`. Bastion reached `Ready`
in under 3 minutes.

---

### 4. SSH to the bastion — fails, reproduced on three independently created VMs/IPs

```
$ BASTION_IP="$(kubectl get stackitcluster "${CLUSTER_NAME}" -o jsonpath='{.status.bastion.publicIP}')"
$ ssh -i .ssh/id_ed25519 -o StrictHostKeyChecking=accept-new -o ConnectTimeout=10 ubuntu@"${BASTION_IP}" 'hostname'

Connection closed by 188.34.73.218 port 22

$ ssh -v -i .ssh/id_ed25519 ubuntu@"${BASTION_IP}" 'hostname'

...
debug1: Connection established.
debug1: Local version string SSH-2.0-OpenSSH_10.0p2 Debian-7+deb13u4
kex_exchange_identification: Connection closed by remote host
```

**Result:** TCP connects, but the remote side closes before sending any SSH
banner. Retried 3 more times over the next minute — identical every time.

---

### 5. Forcing a recreate — reveals a second, separate finding: reconcile delay after out-of-band deletion

To rule out a one-off broken VM, the bastion server was deleted directly to
force the provider's "ensure exists" reconcile (the same mechanism found in
[run-main2-2-ha-controlplane.md](run-main2-2-ha-controlplane.md)) to
provision a replacement.

```
$ BASTION_SERVER_ID="$(kubectl get stackitcluster "${CLUSTER_NAME}" -o jsonpath='{.status.bastion.serverID}')"
$ stackit server delete "${BASTION_SERVER_ID}" --assume-yes

Deleted server "stackit-capi-test-bastion"

$ kubectl get stackitcluster "${CLUSTER_NAME}" -o jsonpath='{.status.bastion.serverID}{"\n"}'   # t+15s

d76c6210-a6e5-4226-b1cf-ba556958c33b   # still the OLD, now-deleted ID — status is stale

$ kubectl get stackitcluster "${CLUSTER_NAME}" \
  -o jsonpath='{range .status.conditions[?(@.type=="BastionReady")]}{.lastTransitionTime}{"\n"}{end}'

2026-08-07T22:52:01Z   # from BEFORE the delete — no reconcile has happened yet

# waited, re-checked repeatedly — still no change after 4+ minutes;
# only after that did a new reconcile pick up the missing server:

2026-08-07T22:56:52Z False BastionError not found: add security group to server: 404 Not Found ...
  instance_id 7c8b8a93-4de5-4e3f-b34d-caa77f5b97ac could not be found as device id on any ports

# ~75s later:
True Available   # new serverID=7c8b8a93..., same reused public IP 188.34.73.218
```

**Result — second finding:** the `StackitCluster` controller took over 4
minutes to even notice the out-of-band deletion and begin recreating the
bastion (compare to the ~1 minute noticed for a control-plane
`StackitMachine` in [run-main2-2-ha-controlplane.md](run-main2-2-ha-controlplane.md)).
There is no fast active health-check for the bastion; it only picks up the
change on its next reconcile trigger. A later attempt (deleting both the
server *and* its public IP, see step 6) took over **15 minutes** with zero
reconcile activity in the controller logs until a manual
`kubectl annotate` was used to force a new reconcile — confirming this is
reconcile-scheduling latency, not a crash or error loop (the controller logs
showed no errors between successful reconciles, just no re-triggering).

---

### 6. SSH still fails on the recreated VM, and again on a third VM with a genuinely new IP

```
$ ssh -i .ssh/id_ed25519 ubuntu@"188.34.73.218" 'hostname'

Connection closed by 188.34.73.218 port 22   # same IP, genuinely new VM (new instance ID) — still fails

$ PUBLIC_IP_ID="$(kubectl get stackitcluster "${CLUSTER_NAME}" -o jsonpath='{.status.bastion.publicIPID}')"
$ BASTION_SERVER_ID="$(kubectl get stackitcluster "${CLUSTER_NAME}" -o jsonpath='{.status.bastion.serverID}')"
$ stackit server delete "${BASTION_SERVER_ID}" --assume-yes
$ stackit public-ip delete "${PUBLIC_IP_ID}" --assume-yes

Deleted server "stackit-capi-test-bastion"
Deleted public IP "188.34.73.218"

# no reconcile after 15+ minutes; forced one:
$ kubectl annotate stackitcluster "${CLUSTER_NAME}" debug.nudge="$(date +%s)" --overwrite

# self-heals through one more transient error, then succeeds with a genuinely new IP:
# BastionError invalid input: add security group to server: 400 Bad Request ...
#   Duplicate items in the list: 'f1d8050f-fa96-45c6-9e5b-ff6cbcd05a8d'
# True Available, serverID=8e6f0dc2..., publicIP=192.214.181.43

$ ssh -i .ssh/id_ed25519 ubuntu@"192.214.181.43" 'hostname'

Connection closed by 192.214.181.43 port 22   # third VM, third public IP — still fails identically
```

**Result:** Identical failure on 3 independently created VMs across 3
different public IPs. This rules out a single broken VM or a single
poisoned floating IP as the explanation.

---

### 7. Control test — general outbound SSH works fine from this environment

```
$ ssh -o StrictHostKeyChecking=accept-new -o ConnectTimeout=8 -T git@github.com

git@github.com: Permission denied (publickey).
```

**Result — decisive:** SSH to a known-good external host completes the
full protocol handshake (banner exchange, key exchange) and correctly fails
only at public-key authentication (expected — no matching key for GitHub).
This proves outbound TCP/22 is **not** generally blocked from this
environment. Combined with step 6, the most likely explanation is a
network policy in this specific devcontainer/environment that permits
outbound HTTPS to cloud ranges but drops or resets TCP/22 specifically to
STACKIT's public IP ranges (TCP handshake completes, zero bytes ever
returned — consistent with a stateful firewall allowing the SYN but killing
the connection before any payload). This is different from, and much more
thoroughly evidenced than, the "unresolved, most likely transient" SSH
finding in earlier runs — this time it reproduces 3/3 and is clearly not
VM- or IP-specific.

---

### 8. Clean up

```
$ kubectl delete cluster "${CLUSTER_NAME}" --wait=false

cluster.cluster.x-k8s.io "stackit-capi-test" deleted from default namespace

# t+64s: No resources found in default namespace.

$ stackit server list | grep -i "${CLUSTER_NAME}"
$ stackit volume list | grep -i "${CLUSTER_NAME}"
$ stackit load-balancer list | grep -i "${CLUSTER_NAME}"
$ stackit security-group list | grep -i "${CLUSTER_NAME}"
$ stackit public-ip list | grep -i "${CLUSTER_NAME}"

(none found for any of the above)
```

**Result:** Despite the extra 2 unintended worker VMs from the template bug
and the 2 extra bastion recreate cycles, cleanup was completely correct —
all VMs, volumes, the load balancer, its security groups, and all public
IPs were fully removed within about a minute of `kubectl delete cluster`.
The `stackit-capi-test-break-glass` key pair still exists, as expected —
kept intentionally for future runs.

## Conclusion

**Status:** ⚠️ blocked — SSH-dependent steps (jump to nodes, remote CNI
install, CIDR-based access test) could not be executed from this
environment and remain unverified in this run.

**What was still learned and verified:**

- **Real template bug, confirmed in code:**
  [templates/cluster-template-bastion.yaml:160](../templates/cluster-template-bastion.yaml)
  hardcodes worker replicas to `3`, ignoring `${WORKER_MACHINE_COUNT}` —
  every bastion-enabled cluster gets 3 workers regardless of what is
  requested. Should be changed to `${WORKER_MACHINE_COUNT}` to match
  [templates/cluster-template.yaml:123](../templates/cluster-template.yaml).
- **Reconcile latency after out-of-band bastion deletion is highly
  variable** (~4 to 15+ minutes observed, vs. ~1 minute for a
  control-plane `StackitMachine` in
  [run-main2-2-ha-controlplane.md](run-main2-2-ha-controlplane.md)) — there
  is no fast active health probe for the bastion; recovery depends on
  whatever next triggers a reconcile.
- **Deletion/cleanup remained fully correct** even after 2 forced bastion
  recreate cycles and the extra worker VMs — no leaks of any kind.
- Bastion provisioning itself (`BastionReady`) is otherwise reliable,
  self-healing past the known transient "security group before network
  port" and "duplicate security group" errors within 1-2 reconciles.

**Not resolved — needs a different network to test:** SSH to the bastion's
public IP is refused identically on 3 independently created VMs across 3
different public IPs, while general outbound SSH (to github.com) works
fine from this environment. This is best explained by a network policy
specific to this devcontainer/environment (likely blocking or resetting
outbound TCP/22 to arbitrary public IP ranges while allowing HTTPS), not by
a defect in this provider. **Recommendation:** retry steps 3-6 of the
original bastion access path (SSH, jump to node, remote CNI install, CIDR
enforcement) from a different network — e.g. the maintainer's own laptop
outside this devcontainer — before concluding anything about the actual
bastion SSH path on current `main`.

---

## Addendum (2026-08-10, after code review)

- The `BastionError` messages in steps 3 and 6, described above as a known
  transient race that self-heals, are caused by a redundant security-group
  attach in `EnsureBastion` — and the same early return also delays the public
  IP assignment by one reconcile cycle. See
  [bastion-bug.md](bastion-bug.md#1-the-bastion-security-group-is-attached-twice).
- The CIDR-narrowing variant suggested in the conclusion is **valid and in
  fact the cheapest way to expose a second defect** — provided it is run from
  the network that was previously allowed. Security-group rules are only ever
  added, never removed, so the stale rule keeps admitting the tester and the
  expected timeout never happens; that failure is the proof. Only the
  combination "narrow the CIDR *and* test from a never-allowed network" would
  mislead. See
  [bastion-bug.md](bastion-bug.md#2-changing-allowedcidrs-never-revokes-the-old-access).
- The "TCP/22 to STACKIT ranges is blocked" explanation does not survive
  comparison with [run-main1-4-bastion.md](run-main1-4-bastion.md), where SSH to STACKIT
  bastions worked from this same environment. Both runs together point at
  IP-range reachability instead, independent of port —
  [bastion-bug.md](bastion-bug.md#resolved-and-not-a-code-defect-the-ssh-failures)
  has the evidence table and a falsifiable test.
- The `WORKER_MACHINE_COUNT` finding from step 2 is tracked as
  [bastion-bug.md](bastion-bug.md#4-cluster-template-bastionyaml-ignores-worker_machine_count).
