# Regression: private cluster via bastion / jump host

Date: 2026-08-07
Run: 1 of 2 — see [SUMMARY.md](SUMMARY.md#timeline)
Cluster: `stackit-capi-test`
Status: ✅ works

Verifies private cluster access via a provider-managed bastion: SSH to the
bastion and onward to workload nodes (jump host), kube-api reachability, and
— the substantive part — that **real cluster administration works remotely
over that path** by installing a CNI into a freshly bootstrapped cluster
from outside. Both paths must be restricted to trusted CIDRs
(`${ADMIN_CIDR}`) and refused from elsewhere.

Prerequisites: see [SUMMARY.md](SUMMARY.md#shared-prerequisites), plus:

- `cilium` CLI installed locally (used by `make install-workload-cni`; the
  devcontainer installs it via `.devcontainer/post-install.sh`).
- A reachable test CIDR as the allowed range (not `0.0.0.0/0`, the e2e
  default per [../docs/src/development/testing.md](../docs/src/development/testing.md)).
  `.envrc` derives it as `ADMIN_CIDR=$(curl ifconfig.me)"/32"` — beware a
  Zscaler-style proxy with a rotating egress IP, which makes it unstable
  between runs.
- [../templates/cluster-template-bastion.yaml](../templates/cluster-template-bastion.yaml)
  needs **two separate** SSH key variables — `STACKIT_BASTION_SSH_KEY_NAME`
  for the bastion host and `STACKIT_SSH_KEY_NAME` for the
  control-plane/worker nodes. They may point at the same key pair, but both
  must be set:
  ```
  export STACKIT_SSH_KEY_NAME="${CLUSTER_NAME}-break-glass"
  export STACKIT_BASTION_SSH_KEY_NAME="${STACKIT_SSH_KEY_NAME}"
  export STACKIT_BASTION_ALLOWED_CIDRS="${ADMIN_CIDR}"
  export STACKIT_BASTION_IMAGE_ID="${STACKIT_IMAGE_ID}"
  export STACKIT_BASTION_MACHINE_TYPE="${STACKIT_MACHINE_TYPE}"
  ```

## Test steps

### 1. Import the SSH key pair

```
$ stackit key-pair create --name "${STACKIT_SSH_KEY_NAME}" \
  --public-key "@$(pwd)/.ssh/id_ed25519.pub" \
  --labels cluster-api-provider-stackit=true

Created key pair "stackit-capi-test-break-glass".
```

**Result:** Created once the `@`-prefix was used. The prefix is mandatory —
without it the CLI treats the argument as literal key material and fails
with a schema-regex `400 Bad Request`.

---

### 2. Create the cluster with bastion enabled

```
$ clusterctl generate cluster "${CLUSTER_NAME}" \
  --from templates/cluster-template-bastion.yaml \
  --target-namespace "${NAMESPACE}" > cluster-bastion.yaml

$ kubectl apply -f cluster-bastion.yaml

$ kubectl get stackitcluster "${CLUSTER_NAME}" \
  -o jsonpath='{.status.bastion.publicIP}{"\n"}{.status.bastion.serverID}{"\n"}{.status.bastion.securityGroupID}{"\n"}'
```

**Result:** Initial apply hit `BastionReady=False/BastionError: "resource
not found: keypair"` — the bastion's `sshKeyName` was baked in from a stale
`STACKIT_BASTION_SSH_KEY_NAME` value (`stackit-workload-break-glass`, left
over from an earlier session) because that variable is a shell-snapshot
assignment, not a live reference. Fixed by re-sourcing `.envrc` and
re-exporting both key variables together in the same shell, then
regenerating and re-applying. After the fix a second, transient
`BastionError: "add security group to server: 404 ... could not be found as
device id on any ports"` appeared — the controller attached the security
group before the server had a network port (state `CREATING`); it
self-healed on the next reconcile once the server reached `ACTIVE`.

**Separate, unrelated finding:** the `StackitMachine` reconciler
(`internal/controller/stackitmachine_controller.go:131`)
hard-blocks all machine provisioning bookkeeping while
`StackitCluster.Status.Ready == false`. Since the bastion errors above kept
`Ready=false` for over an hour, the 4 workload `Machine` objects sat at
`Phase: Provisioned` / `Ready: Unknown` with no `NodeRef` that whole time —
even though their VMs had already booted and joined as real Kubernetes
`Node` objects (kubeadm bootstrap on the VM doesn't depend on this gate).
Once `BastionReady` recovered, the bookkeeping caught up within a couple of
reconciles. No fix needed — correct dependency ordering, but it looks
alarming ("stuck for over an hour") without being broken.

---

### 3. SSH reachability through the bastion

```
$ BASTION_IP="$(kubectl get stackitcluster "${CLUSTER_NAME}" -o jsonpath='{.status.bastion.publicIP}')"
$ ssh -i .ssh/id_ed25519 -o StrictHostKeyChecking=accept-new ubuntu@"${BASTION_IP}" \
  'hostname; kubectl version --client'

stackit-capi-test-bastion
Client Version: v1.35.7
```

**Result (3a, direct to bastion):** Works. `kubectl version --client`
succeeding also proves the bastion's cloud-init (the
`${CLUSTER_NAME}-bastion-cloud-init` ConfigMap) ran to completion.

This took significant investigation: the *first* bastion (IP
`192.214.188.106`) refused all SSH with `kex_exchange_identification:
Connection closed by remote host` (TCP connects, no SSH banner ever sent)
despite correct `authorized_keys`, a correct security-group rule for the
current IP, and clean cloud-init. Console login and passive log inspection
were both dead ends — cloud-init sets no OS password, and sshd/journald
output isn't mirrored to the serial console on this image (it stops right
after the last cloud-init line). Injecting a temporary debug password into
the bastion cloud-init ConfigMap triggers the controller's built-in
recreate (`bastionNeedsRecreate` in
`internal/controller/stackitcluster_controller.go:470`
auto-deletes and recreates the bastion when the resolved cloud-init content
hash changes — no manual server deletion needed). The **recreated** bastion
(new IP `213.17.20.79`, later `213.17.23.100` after reverting the debug
password) accepted key-based SSH immediately. Root cause on the original IP
remains **unconfirmed** — most likely transient and tied to that specific
public IP (e.g. internet-wide scanners saturating sshd's pre-auth
`MaxStartups` on a freshly allocated floating IP), not a security-group
enforcement bug. The debug password was reverted afterward and never
committed.

**Result (3b, jump to a workload node):** Failed initially:

```
$ ssh -i .ssh/id_ed25519 -J ubuntu@"${BASTION_IP}" ubuntu@"${NODE_IP}" 'hostname'

ubuntu@213.17.23.100: Permission denied (publickey).
```

`ssh -v` showed the jump-hop authentication trying the *default* identity
files (`~/.ssh/id_rsa`, `id_ecdsa`, …) instead of the `-i` key — `-i` does
not reliably apply to the `-J` jump hop's own authentication. Loading the
key into an agent fixes it, since agent-based auth applies to every hop:

```
$ eval "$(ssh-agent -s)"
$ ssh-add .ssh/id_ed25519
$ ssh -J ubuntu@"${BASTION_IP}" ubuntu@"${NODE_IP}" 'hostname'

stackit-capi-test-control-plane-wl4jj
```

Confirms the provider-managed node-SSH security group correctly permits
bastion→node traffic; the earlier failure was purely a local SSH client
issue.

---

### 4. kube-api reachability and remote CNI install

A freshly created cluster has **no CNI** (see
[../docs/src/usage/cni.md](../docs/src/usage/cni.md)), so nodes stay
`NotReady` and nothing can run. Installing it remotely is what proves the
private-access path is usable for real administration, not just for a
`kubectl get nodes` smoke test.

Variant used: **4b** (tunnel via bastion). The API server certificate is
issued for the load-balancer IP, so `--tls-server-name` must keep pointing
at it or TLS verification fails against `127.0.0.1`.

```
$ ssh -i .ssh/id_ed25519 -f -N -L 6443:"${LB_IP}":6443 ubuntu@"${BASTION_IP}"
$ kubectl config --kubeconfig "${KUBECONF_WORKERCLUSTER}" set-cluster "${CLUSTER_NAME}" \
  --server="https://127.0.0.1:6443" --tls-server-name="${LB_IP}"

$ make install-workload-cni WORKLOAD_KUBECONFIG="${KUBECONF_WORKERCLUSTER}"

... daemon set "cilium" successfully rolled out
... daemon set "cilium-envoy" successfully rolled out
```

**Result:** CNI install completed over the bastion-tunneled path; all 4
nodes went `Ready`. Two things looked alarming right afterward but were just
startup ordering, not bugs: `cilium status` briefly reported "8 errors"
(kubelet-exec lookups failing via `1.1.1.1:53` for node hostnames), and
`stackit-cloud-controller-manager` + `coredns` pods had been stuck
(`ContainerCreating`/`Pending`) for ~95 minutes — both were simply waiting
on the CNI (`NetworkNotReady: cni plugin not initialized` for CCM,
untolerated `not-ready` taint for CoreDNS) and self-resolved within ~3
minutes of the rollout finishing.

---

### 5. Access blocked from outside the allowed CIDR

Tested from a real second network (mobile carrier, egress `80.187.66.40`,
not in `${STACKIT_BASTION_ALLOWED_CIDRS}` = `109.250.27.16/32`) rather than
the CIDR-narrowing fallback:

```
$ ssh -o ConnectTimeout=10 ubuntu@"${BASTION_IP}"

ssh: connect to host 213.17.23.100 port 22: Connection timed out

$ kubectl --kubeconfig "${KUBECONF_WORKERCLUSTER}" get nodes

The connection to the server 127.0.0.1:6443 was refused - did you specify the right host or port?
```

**Result:** SSH to the bastion times out from outside the allowed CIDR —
the security group correctly refuses non-allowlisted sources, now verified
from a genuinely different network rather than just reasoned about. The
`kubectl` refusal on `127.0.0.1:6443` is a secondary effect, not a direct
CIDR-gate test of the API server: this cluster's API is reachable only via
the bastion tunnel (variant 4b), so once SSH is blocked the local tunnel has
nothing to connect to. Same net effect (no path to the API from outside),
different mechanism than a directly CIDR-gated load balancer would show.

---

### 6. Clean up

```
$ kubectl delete cluster "${CLUSTER_NAME}" -n "${NAMESPACE}"

cluster.cluster.x-k8s.io "stackit-capi-test" deleted from default namespace

$ stackit server list | grep -i "${CLUSTER_NAME}"

(none found)

$ stackit volume list | grep -i "${CLUSTER_NAME}"

(none found)

$ stackit load-balancer list | grep -i "${CLUSTER_NAME}"

(none found)

$ stackit security-group list | grep -i "${CLUSTER_NAME}"

(none found)
```

Do **not** `kubectl delete -f cluster-bastion.yaml` — see the open bug in
[deletion-bug.md](deletion-bug.md).

**Result:** `Cluster`/`StackitCluster` gone ~15s after `kubectl delete
cluster`. No leaked servers, volumes, load balancer or security groups
(bastion + node-SSH + LB groups all cleaned up); the bastion's public IP was
released. The `stackit-capi-test-break-glass` key pair still exists, as
expected — cluster deletion doesn't touch key pairs created outside the
provider; kept intentionally for a future run.

## Conclusion

**Status:** ✅ works

Private cluster access via the provider-managed bastion works end-to-end on
current `main`: bastion + public IP provisioned and torn down correctly, SSH
to the bastion and onward to workload nodes works, the security group
correctly restricts access to the configured CIDR (verified from both an
allowed IP and a different mobile-network IP), and a CNI could be installed
remotely through the bastion tunnel — taking the cluster from unusable
(`NotReady` nodes) to fully operational.

This cluster's API server was reachable only via the bastion SSH tunnel, not
directly from the admin CIDR. Worth confirming whether that's the intended
access model or whether a direct-but-CIDR-gated LB path was also expected;
not investigated further, since the bastion path alone satisfies this test's
goal.

**Bugs found and fixed along the way** (commands above already corrected):

- `stackit key-pair create --public-key` requires an `@`-prefixed file path.
- `STACKIT_BASTION_SSH_KEY_NAME="${STACKIT_SSH_KEY_NAME}"` is a
  shell-snapshot, not a live reference — re-export both together after any
  `CLUSTER_NAME`/`.envrc` change.
- `ssh -i <key> -J user@bastion …` does not reliably apply `-i` to the jump
  hop — use an `ssh-agent`.

**Non-bugs** (self-resolving, noted so they don't cause alarm on a re-run):

- Transient `BastionError` ("add security group to server: 404 …") right
  after bastion creation — heals once the server reaches `ACTIVE`.
- `stackit-cloud-controller-manager`/`coredns` stuck for ~95 minutes while
  no CNI was installed — resolved ~3 minutes after the CNI rollout.
- `StackitMachine` reconciliation stalls entirely while
  `StackitCluster.Status.Ready == false` — catches up automatically.

**Unresolved:** root cause of the first bastion's SSH refusal
(`kex_exchange_identification` on IP `192.214.188.106`) was never confirmed;
most likely transient scanner-related `MaxStartups` saturation, not
reproduced on either bastion created afterward.

---

## Addendum (2026-08-10, after code review)

Two corrections to the conclusions above. The protocol itself is left as
recorded — these are re-interpretations, not changes to what was observed.

- The transient `BastionError` listed above under **Non-bugs** ("add security
  group to server: 404 …") is **not** a STACKIT-API race. It is caused by a
  redundant second attach of the same security group in the provider's own
  `EnsureBastion` — see [bastion-bug.md](bastion-bug.md#1-the-bastion-security-group-is-attached-twice).
- Step 5 ("Access blocked from outside the allowed CIDR") is valid for the case
  it tested — an IP that was never allowed — but it cannot detect that
  `allowedCIDRs` rules are never removed once created. A *revoked* CIDR keeps
  its SSH access indefinitely; see
  [bastion-bug.md](bastion-bug.md#2-changing-allowedcidrs-never-revokes-the-old-access).
- The unresolved SSH refusal on `192.214.188.106` fits a pattern that only
  became visible after run 2 and is no longer best explained as transient; see
  [bastion-bug.md](bastion-bug.md#resolved-and-not-a-code-defect-the-ssh-failures).
