# Regression: private cluster via bastion / jump host

Date: 2026-08-11
Run: refactor 1 — see [SUMMARY.md](SUMMARY.md#timeline)
Branch: `refactor/code-cleanup-and-proper-abstraction` (commit `93eb06c`)
Cluster: `stackit-capi-test`
Status: ✅ works — full access path verified end to end, first time since
[run-main1-4-bastion.md](run-main1-4-bastion.md)

Verifies private cluster access via the provider-managed bastion against the
refactored provider: SSH to the bastion, jump on to a workload node, kube-api
through the tunnel, a full remote CNI install, and CIDR enforcement from a
genuinely different network. Cleanup uses the **`kubectl delete -f` variant**
from [deletion-bug.md](deletion-bug.md) rather than the safe path.

Reference: [run-main1-4-bastion.md](run-main1-4-bastion.md) (the only `main`
run that got through the whole path) and
[run-main2-4-bastion.md](run-main2-4-bastion.md) (blocked at SSH).

## Test steps

### 1. Create the cluster with bastion enabled

```
$ export STACKIT_SSH_KEY_NAME="${CLUSTER_NAME}-break-glass"
$ export STACKIT_BASTION_SSH_KEY_NAME="${STACKIT_SSH_KEY_NAME}"
$ export STACKIT_BASTION_ALLOWED_CIDRS="${ADMIN_CIDR}"
$ export STACKIT_BASTION_IMAGE_ID="${STACKIT_IMAGE_ID}"
$ export STACKIT_BASTION_MACHINE_TYPE="${STACKIT_MACHINE_TYPE}"
$ echo "ADMIN_CIDR=${ADMIN_CIDR}"

ADMIN_CIDR=109.250.24.213/32

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

**Result:** Applied cleanly. Note the admin CIDR is `109.250.24.213/32` — the
egress IP had rotated since the `main` runs (`109.250.27.16/32`), exactly the
instability warned about in [run-main1-4-bastion.md](run-main1-4-bastion.md).

**Vergleich main:** identical, except `run-main1` had to fix a stale
`STACKIT_BASTION_SSH_KEY_NAME` first. Exporting all five variables in one
shell avoided that here.

---

### 2. Known template defect reproduces

```
$ kubectl get machinedeployment stackit-capi-test-md-0 -o jsonpath='{.spec.replicas}{"\n"}'

3
```

**Result:** `WORKER_MACHINE_COUNT=1` was exported, yet the `MachineDeployment`
has 3 replicas — the hardcoded `replicas: 3` in
[../templates/cluster-template-bastion.yaml](../templates/cluster-template-bastion.yaml)
line 160, unchanged by the refactor.

**Vergleich main:** found in `run-main2`, tracked in
[bastion-bug.md](bastion-bug.md#4-cluster-template-bastionyaml-ignores-worker_machine_count).
Reproduces identically.

---

### 3. Bastion provisioning — known transient error reproduces

```
$ kubectl get stackitcluster "${CLUSTER_NAME}" \
  -o jsonpath='{range .status.conditions[?(@.type=="BastionReady")]}{.status} {.reason} {.message}{"\n"}{end}'

False BastionError not found: add security group to server: 404 Not Found, status code 404,
  Body: {"code":404,"msg":"instance_id 4cc84238-6041-4170-922c-b525152615f3 could not be found
  as device id on any ports"}                                        # t+20s .. t+60s
False Provisioning bastion server state is CREATING                  # t+80s .. t+140s
True Available                                                       # t+160s
```

**Result:** The 404 `BastionError` appeared for the first ~60 seconds and then
cleared, exactly as on `main`. Bastion `Ready` after 2m40s with public IP
`213.17.21.135`.

**Vergleich main:** same error, same self-healing, same timescale. Confirms the
double security-group attach documented in
[bastion-bug.md](bastion-bug.md#1-the-bastion-security-group-is-attached-twice)
is unchanged by the refactor.

---

### 4. SSH to the bastion — and a decisive result for the open `main` question

```
$ BASTION_IP="$(kubectl get stackitcluster "${CLUSTER_NAME}" -o jsonpath='{.status.bastion.publicIP}')"
$ echo "${BASTION_IP}"

213.17.21.135

$ ssh -i .ssh/id_ed25519 -o StrictHostKeyChecking=accept-new -o ConnectTimeout=10 ubuntu@"${BASTION_IP}" 'hostname; kubectl version --client'

stackit-capi-test-bastion
Client Version: v1.35.7
Kustomize Version: v5.7.1
```

**Result:** SSH works immediately. `kubectl version --client` succeeding also
proves the bastion cloud-init ran to completion.

**Vergleich main:** this settles the open question.
[run-main2-4-bastion.md](run-main2-4-bastion.md) failed SSH on 3 bastions
across 3 IPs and concluded the environment blocks TCP/22 to STACKIT ranges;
[run-main1-4-bastion.md](run-main1-4-bastion.md) had one failing IP and called
it transient. [bastion-bug.md](bastion-bug.md#resolved-and-not-a-code-defect-the-ssh-failures)
proposed a falsifiable test instead: retry until a `213.17.x.x` address is
assigned, since `213.17.x.x` had worked in both runs while `192.214.x.x` and
`188.34.x.x` had not.

This bastion got `213.17.21.135` and SSH worked on the first attempt — **the
prediction holds.** The `main` run 2 failures were IP-range reachability from
this environment, not a port-22 block and not a provider defect.

---

### 5. Jump on to a workload node

```
$ eval "$(ssh-agent -s)"
$ ssh-add .ssh/id_ed25519
$ ssh -o StrictHostKeyChecking=accept-new -J ubuntu@"${BASTION_IP}" ubuntu@10.42.0.216 'hostname; uptime'

stackit-capi-test-control-plane-6tqnl
 11:48:29 up 3 min,  1 user,  load average: 0.20, 0.23, 0.09
```

**Result:** Works via `ssh-agent`. The provider-managed node-SSH security group
correctly permits bastion→node traffic.

An earlier attempt failed with `channel 0: open failed: connect failed: No
route to host` — the node VM was still booting at that point, not a
security-group problem; it succeeded once the machine reached `Provisioned`.

**Vergleich main:** `run-main1` reached the same result, also only via
`ssh-agent` (`-i` does not reliably apply to the `-J` hop).

---

### 6. kube-api through the tunnel and remote CNI install

```
$ LB_IP="$(kubectl get stackitcluster "${CLUSTER_NAME}" -o jsonpath='{.status.apiServerEndpoint.host}')"
$ echo "${LB_IP}"

213.17.23.103

$ ssh -f -N -L 6443:"${LB_IP}":6443 ubuntu@"${BASTION_IP}"
$ kubectl config --kubeconfig "${KUBECONF_WORKERCLUSTER}" set-cluster "${CLUSTER_NAME}" \
  --server="https://127.0.0.1:6443" --tls-server-name="${LB_IP}"

$ kubectl --kubeconfig "${KUBECONF_WORKERCLUSTER}" get nodes

NAME                                    STATUS     ROLES           AGE     VERSION
stackit-capi-test-control-plane-6tqnl   NotReady   control-plane   2m52s   v1.35.7

$ make install-workload-cni WORKLOAD_KUBECONFIG="${KUBECONF_WORKERCLUSTER}"

deployment "cilium-operator" successfully rolled out
Waiting for daemon set "cilium" rollout to finish: 1 of 4 updated pods are available...
Waiting for daemon set "cilium" rollout to finish: 3 of 4 updated pods are available...
daemon set "cilium" successfully rolled out
daemon set "cilium-envoy" successfully rolled out

$ kubectl --kubeconfig "${KUBECONF_WORKERCLUSTER}" get nodes

NAME                                    STATUS   ROLES           AGE     VERSION
stackit-capi-test-control-plane-6tqnl   Ready    control-plane   6m34s   v1.35.7
stackit-capi-test-md-0-gg2xc-574fn      Ready    <none>          2m42s   v1.35.7
stackit-capi-test-md-0-gg2xc-bgc4p      Ready    <none>          2m29s   v1.35.7
stackit-capi-test-md-0-gg2xc-ftflw      Ready    <none>          2m58s   v1.35.7

$ cilium status --kubeconfig "${KUBECONF_WORKERCLUSTER}"

    /¯¯\
 /¯¯\__/¯¯\    Cilium:             OK
 \__/¯¯\__/    Operator:           OK
 /¯¯\__/¯¯\    Envoy DaemonSet:    OK

DaemonSet              cilium                   Desired: 4, Ready: 4/4, Available: 4/4
DaemonSet              cilium-envoy             Desired: 4, Ready: 4/4, Available: 4/4
Deployment             cilium-operator          Desired: 1, Ready: 1/1, Available: 1/1
```

**Result:** Full cluster administration over the private path works — the CNI
was installed through the bastion tunnel, taking the cluster from unusable
(`NotReady`) to all 4 nodes `Ready` with Cilium `OK`. `--tls-server-name` is
required so certificate verification still targets the LB IP.

**Vergleich main:** same variant (4b) and same outcome as `run-main1`. Unlike
`run-main1`, no CCM/CoreDNS pods were stuck beforehand, because the CNI was
installed shortly after cluster creation rather than 95 minutes later.

**Note:** the API server was *also* reachable directly on `213.17.23.103`
without the tunnel. The tunnel was used anyway to match the reference test.

---

### 7. Access blocked from outside the allowed CIDR

Tested from a real second network (mobile carrier). Note this proves only that
an IP which was **never** allowed is refused — it cannot detect the stale-rule
defect, because the mobile IP was never in any rule. Demonstrating that one
needs the CIDR narrowed *and* the test run from the previously allowed network;
see [bastion-bug.md](bastion-bug.md#how-to-actually-prove-this-bug).

```
$ curl -s https://ifconfig.me

80.187.67.100

$ stackit security-group rule list --security-group-id "${SG}" -o json | ...

  109.250.24.213/32 {'max': 22, 'min': 22}

$ ssh -i .ssh/id_ed25519 -o ConnectTimeout=10 ubuntu@"${BASTION_IP}" 'hostname'

ssh: connect to host 213.17.21.135 port 22: Connection timed out
exit: 255

$ kubectl --kubeconfig "${KUBECONF_WORKERCLUSTER}" get nodes

The connection to the server 127.0.0.1:6443 was refused - did you specify the right host or port?
```

**Result:** From egress `80.187.67.100`, which is not in the allowed
`109.250.24.213/32`, SSH to the bastion times out. The security group refuses
non-allowlisted sources correctly. The `kubectl` refusal is a secondary effect
— the local tunnel died with the SSH connection, so there is nothing on
`127.0.0.1:6443`; it is not an independent test of the API server's exposure.

**Gotcha worth recording:** a first attempt at this step used a `BASTION_IP`
shell variable left over from the `main` run (`213.17.23.100`) and produced a
timeout against a host that no longer exists — a false positive. The IP must
be re-read from `status.bastion.publicIP` for each cluster.

**Vergleich main:** `run-main1` performed the same test from a mobile network
with the same result.

---

### 8. Clean up — the `kubectl delete -f` variant

Deliberately using the path that reproduces the open bug in
[deletion-bug.md](deletion-bug.md), instead of the safe `kubectl delete
cluster`.

```
$ stackit server list | grep -i "${CLUSTER_NAME}"

3f7f8b25-f8d1-4ed7-9da6-b2cc441206e1   stackit-capi-test-apiserver-nqlblarzonptcmaj-2382d   ACTIVE
3d278e9d-e0bf-4920-b08d-c3003bcdc39e   stackit-capi-test-apiserver-nqlblarzonptcmaj-b6701   ACTIVE
7f71d189-b807-468f-b1b1-456deed65c75   stackit-capi-test-md-0-gg2xc-ftflw                   ACTIVE
5c72b748-8487-4569-916e-35f43791a624   stackit-capi-test-md-0-gg2xc-574fn                   ACTIVE
eefd1d0b-e65b-4bb3-98f0-d018658cfe10   stackit-capi-test-control-plane-6tqnl                ACTIVE
66ad2772-0e91-4d99-b0c6-e5a10f07bb3a   stackit-capi-test-md-0-gg2xc-bgc4p                   ACTIVE
4cc84238-6041-4170-922c-b525152615f3   stackit-capi-test-bastion                            ACTIVE

$ kubectl delete -f cluster-bastion.yaml --wait=false

cluster.cluster.x-k8s.io "stackit-capi-test" deleted from default namespace
configmap "stackit-capi-test-bastion-cloud-init" deleted from default namespace
stackitcluster.infrastructure.cluster.x-k8s.io "stackit-capi-test" deleted from default namespace
kubeadmcontrolplane.controlplane.cluster.x-k8s.io "stackit-capi-test-control-plane" deleted from default namespace
stackitmachinetemplate.infrastructure.cluster.x-k8s.io "stackit-capi-test-control-plane" deleted from default namespace
machinedeployment.cluster.x-k8s.io "stackit-capi-test-md-0" deleted from default namespace
stackitmachinetemplate.infrastructure.cluster.x-k8s.io "stackit-capi-test-md-0" deleted from default namespace
kubeadmconfigtemplate.bootstrap.cluster.x-k8s.io "stackit-capi-test-md-0" deleted from default namespace
clusterresourceset.addons.cluster.x-k8s.io "stackit-capi-test-cloud-provider-stackit" deleted from default namespace
secret "stackit-capi-test-cloud-provider-stackit" deleted from default namespace

$ kubectl get cluster,stackitcluster,machine,stackitmachine   # every 20s

cluster.../stackit-capi-test  Deleting   # t+20s, t+40s — Machines already gone
No resources found                       # t+60s

$ stackit server list        | grep -ci stackit-capi-test
$ stackit volume list        | grep -ci stackit-capi-test
$ stackit load-balancer list | grep -ci stackit-capi-test
$ stackit security-group list| grep -ci stackit-capi-test
$ stackit public-ip list     | grep -ci stackit-capi-test

0
0
0
0
0
```

**Result:** The bug did **not** trigger this time. Everything was torn down in
60 seconds with zero orphaned VMs, volumes, load balancers, security groups or
public IPs. The base security group from `.envrc`
(`STACKITCAPITEST-nodes`) and the `stackit-capi-test-break-glass` key pair
remain, as intended.

**This is not evidence that the bug is fixed.** The responsible code path is
unchanged on this branch —
[../controller/stackitmachine_controller.go](../controller/stackitmachine_controller.go)
lines 91-94:

```go
if stackitCluster == nil {
    log.Info("StackitCluster not found, requeueing")
    return ctrl.Result{}, nil
}
```

This still returns **before** any delete handling and **without** a requeue, so
a `StackitMachine` whose `StackitCluster` has already gone can still never
clean itself up. There is also still no guard in the `StackitCluster`
`reconcileDelete` that waits for remaining machines — the fix specified in
[deletion-bug.md#fix-plan-not-yet-implemented](deletion-bug.md#fix-plan-not-yet-implemented)
is not implemented. Whether the bug bites is a race: here CAPI happened to
finish deleting the `StackitMachine`s before the `StackitCluster` finalizer
was released.

**Vergleich main:** `run-main1`/`run-main2` used the safe `kubectl delete
cluster` path and never exercised this. The original reproduction is in
[deletion-bug.md](deletion-bug.md).

## Conclusion

**Status:** ✅ works — and the first complete bastion verification since
`run-main1`

Refactor parity for this package: **confirmed**. Bastion provisioning, the
transient `BastionError`, the node-SSH security group, the tunnel path, remote
administration and CIDR enforcement all behave exactly as on `main`, and both
known defects (double security-group attach, hardcoded worker replicas)
reproduce unchanged.

**Resolved from the `main` runs:** the SSH failures in
[run-main2-4-bastion.md](run-main2-4-bastion.md) were **not** a provider
defect and **not** a general TCP/22 block. The falsifiable prediction in
[bastion-bug.md](bastion-bug.md#resolved-and-not-a-code-defect-the-ssh-failures) —
that a `213.17.x.x` bastion would be reachable — held: SSH worked first try on
`213.17.21.135`. The `192.214.x.x`/`188.34.x.x` ranges are unreachable from
this environment, independent of port.

**Deletion bug not reproduced, but not fixed either.** `kubectl delete -f`
completed cleanly this time; the code path that causes the bug is verifiably
unchanged, so this was luck of ordering, not a fix. `kubectl delete cluster`
should remain the documented default until
[deletion-bug.md#fix-plan-not-yet-implemented](deletion-bug.md#fix-plan-not-yet-implemented)
lands.

**Untested here:** whether changing `allowedCIDRs` revokes prior access — it
does not, per [bastion-bug.md](bastion-bug.md#2-changing-allowedcidrs-never-revokes-the-old-access),
and this run's CIDR test (from a never-allowed network) cannot detect it.
