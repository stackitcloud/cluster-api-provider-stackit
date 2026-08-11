# Regression: cluster bootstrapping & networking

Date: 2026-08-07
Run: 2 of 2 — see [SUMMARY.md](SUMMARY.md#timeline)
Cluster: `stackit-capi-test`
Status: ✅ works

Verifies that a workload cluster bootstraps from scratch on current `main`:
control-plane and worker nodes join and reach `Ready`, and — once Cilium is
installed as CNI — workloads can be deployed, pods are scheduled, cross-node
pod-to-pod communication works, and in-cluster DNS resolves.

Bootstrapping and networking are covered together on purpose: running a
workload requires a CNI first (without one, pods stay
`Pending`/`ContainerCreating` because kubelet cannot set up the pod network
namespace), so testing them separately would either duplicate the deployment
check or create an ordering dependency between two documents.

**Out of scope:** CSI (storage) and Ingress / external load-balancing.

Prerequisites: see [SUMMARY.md](SUMMARY.md#shared-prerequisites). Cluster
generated from [../templates/cluster-template.yaml](../templates/cluster-template.yaml)
with `CONTROL_PLANE_MACHINE_COUNT=1`, `WORKER_MACHINE_COUNT=1`.

**Note on this run:** the first attempt at this cluster hit an outbound
network failure (later linked to STACKIT IP-range reachability from this
environment, see
[bastion-bug.md](bastion-bug.md#open-not-a-code-defect-the-ssh-failures)) —
the `Cluster`/`StackitCluster` were deleted cleanly and Steps 1–4 below are
from the second, successful attempt.

## Test steps

### 1. Generate and apply the workload cluster manifest

```
$ clusterctl generate cluster "${CLUSTER_NAME}" \
  --from templates/cluster-template.yaml \
  --target-namespace "${NAMESPACE}" > cluster.yaml

$ kubectl apply -f cluster.yaml

cluster.cluster.x-k8s.io/stackit-capi-test created
stackitcluster.infrastructure.cluster.x-k8s.io/stackit-capi-test created
kubeadmcontrolplane.controlplane.cluster.x-k8s.io/stackit-capi-test-control-plane created
stackitmachinetemplate.infrastructure.cluster.x-k8s.io/stackit-capi-test-control-plane created
machinedeployment.cluster.x-k8s.io/stackit-capi-test-md-0 created
stackitmachinetemplate.infrastructure.cluster.x-k8s.io/stackit-capi-test-md-0 created
kubeadmconfigtemplate.bootstrap.cluster.x-k8s.io/stackit-capi-test-md-0 created
clusterresourceset.addons.cluster.x-k8s.io/stackit-capi-test-cloud-provider-stackit unchanged
secret/stackit-capi-test-cloud-provider-stackit configured

$ kubectl get clusters,stackitclusters   # t+2m26s

NAME                                        CLUSTERCLASS   AVAILABLE   PHASE         AGE
cluster.cluster.x-k8s.io/stackit-capi-test                 False       Provisioned   2m26s

NAME                                                              READY   ENDPOINT
stackitcluster.infrastructure.cluster.x-k8s.io/stackit-capi-test   true    213.17.23.218
```

**Result:** `Cluster` reached `Provisioned` and `StackitCluster` reported
`Ready`/`true` with an assigned API-server endpoint about 2.5 minutes after
apply.

---

### 2. Wait for control-plane and worker nodes to become Ready

```
$ export KUBECONF_WORKERCLUSTER=/tmp/"${CLUSTER_NAME}".kubeconfig
$ clusterctl get kubeconfig "${CLUSTER_NAME}" -n "${NAMESPACE}" > "${KUBECONF_WORKERCLUSTER}"

$ kubectl --kubeconfig "${KUBECONF_WORKERCLUSTER}" get nodes -o wide   # t+1m30s (control-plane only)

NAME                                    STATUS   ROLES           AGE   VERSION
stackit-capi-test-control-plane-bkkfk   Ready    control-plane   105s  v1.35.7

$ kubectl --kubeconfig "${KUBECONF_WORKERCLUSTER}" get nodes -o wide   # t+2m40s (both nodes)

NAME                                    STATUS   ROLES           AGE     VERSION
stackit-capi-test-control-plane-bkkfk   Ready    control-plane   2m46s   v1.35.7
stackit-capi-test-md-0-8szbn-xcc5l      NotReady <none>           4s     v1.35.7
```

**Result:** Control-plane node reached `Ready` (kubelet-level readiness,
without CNI conditions counted) about 1.5 minutes after the VM finished
provisioning; the worker node appeared shortly after, initially `NotReady`
as expected before a CNI is installed.

---

### 3. Install Cilium

Install via `make install-workload-cni WORKLOAD_KUBECONFIG="${KUBECONF_WORKERCLUSTER}"`,
which uses [../templates/addons/cilium-values.yaml](../templates/addons/cilium-values.yaml)
(IPAM `cluster-pool`, pod CIDR `192.168.0.0/16`, `kubeProxyReplacement: false`).
See also [../docs/src/usage/cni.md](../docs/src/usage/cni.md) and
[../docs/src/usage/addons.md](../docs/src/usage/addons.md).

```
$ make install-workload-cni WORKLOAD_KUBECONFIG="${KUBECONF_WORKERCLUSTER}"

ℹ️  Using Cilium version 1.19.4
🔮 Auto-detected cluster name: stackit-capi-test
🔮 Auto-detected kube-proxy has been installed
Waiting for deployment "cilium-operator" rollout to finish: 0 of 1 updated replicas are available...
deployment "cilium-operator" successfully rolled out
Waiting for daemon set "cilium" rollout to finish: 0 of 2 updated pods are available...
Waiting for daemon set "cilium" rollout to finish: 1 of 2 updated pods are available...
daemon set "cilium" successfully rolled out
daemon set "cilium-envoy" successfully rolled out

$ cilium status --kubeconfig "${KUBECONF_WORKERCLUSTER}"   # ~60s after rollout finished

    /¯¯\
 /¯¯\__/¯¯\    Cilium:             OK
 \__/¯¯\__/    Operator:           OK
 /¯¯\__/¯¯\    Envoy DaemonSet:    OK
 \__/¯¯\__/    Hubble Relay:       disabled
    \__/       ClusterMesh:        disabled

DaemonSet              cilium                   Desired: 2, Ready: 2/2, Available: 2/2
DaemonSet              cilium-envoy             Desired: 2, Ready: 2/2, Available: 2/2
Deployment             cilium-operator          Desired: 1, Ready: 1/1, Available: 1/1

$ kubectl --kubeconfig "${KUBECONF_WORKERCLUSTER}" get nodes -o wide

NAME                                    STATUS   ROLES           AGE     VERSION
stackit-capi-test-control-plane-bkkfk   Ready    control-plane   8m54s   v1.35.7
stackit-capi-test-md-0-8szbn-xcc5l      Ready    <none>          4m46s   v1.35.7
```

**Result:** Cilium rolled out cleanly (`cilium-operator` 1/1, `cilium`/
`cilium-envoy` DaemonSets 2/2). Immediately after rollout, `cilium status`
briefly reported 4 errors (`unable to retrieve cilium status ... dial tcp:
lookup <node> on 1.1.1.1:53: no such host` for both nodes) — kubelet-exec
name lookups failing before CoreDNS/routing was fully up. Self-resolved
within about a minute; the check above shows `OK` with no errors. Both nodes
`Ready`.

**Note:** `cilium status` without `--kubeconfig`/`KUBECONFIG` targets the
current kubectl context (the kind **management** cluster), which correctly
has no Cilium installed — always point it at `"${KUBECONF_WORKERCLUSTER}"`.

---

### 4. Deploy a test workload and verify scheduling

Not executed separately — step 3's output already proves it: the
`cilium-operator` Deployment (1/1) and the `cilium`/`cilium-envoy`
DaemonSets (2/2 each) show that Deployments/DaemonSets are scheduled and
reach `Running` across both the control-plane and the worker node.

**Result:** Confirmed via step 3 — deployments and pod scheduling work.

---

### 5. Verify cross-node pod-to-pod communication

Scale up to two worker nodes so two genuinely different nodes are available,
then pin one test pod per node via `nodeName`:

```
$ kubectl scale machinedeployment stackit-capi-test-md-0 --replicas=2

machinedeployment.cluster.x-k8s.io/stackit-capi-test-md-0 scaled

$ kubectl --kubeconfig "${KUBECONF_WORKERCLUSTER}" get nodes -o wide   # ~2min40s later, new node Ready

NAME                                    STATUS   ROLES           AGE     VERSION
stackit-capi-test-control-plane-bkkfk   Ready    control-plane   11m     v1.35.7
stackit-capi-test-md-0-8szbn-q9p2b      Ready    <none>          61s     v1.35.7
stackit-capi-test-md-0-8szbn-xcc5l      Ready    <none>          8m27s   v1.35.7

$ NODE_A="$(kubectl --kubeconfig "${KUBECONF_WORKERCLUSTER}" get nodes -l '!node-role.kubernetes.io/control-plane' -o jsonpath='{.items[0].metadata.name}')"
$ NODE_B="$(kubectl --kubeconfig "${KUBECONF_WORKERCLUSTER}" get nodes -l '!node-role.kubernetes.io/control-plane' -o jsonpath='{.items[1].metadata.name}')"

$ kubectl --kubeconfig "${KUBECONF_WORKERCLUSTER}" run test-a --image=busybox --overrides="{\"spec\":{\"nodeName\":\"${NODE_A}\"}}" --command -- sleep 3600
$ kubectl --kubeconfig "${KUBECONF_WORKERCLUSTER}" run test-b --image=busybox --overrides="{\"spec\":{\"nodeName\":\"${NODE_B}\"}}" --command -- sleep 3600
$ kubectl --kubeconfig "${KUBECONF_WORKERCLUSTER}" wait --for=condition=Ready pod/test-a --timeout=120s
$ kubectl --kubeconfig "${KUBECONF_WORKERCLUSTER}" wait --for=condition=Ready pod/test-b --timeout=120s
$ kubectl --kubeconfig "${KUBECONF_WORKERCLUSTER}" get pods -o wide

NAME     READY   STATUS    RESTARTS   AGE   IP              NODE
test-a   1/1     Running   0          4s    192.168.2.113   stackit-capi-test-md-0-8szbn-q9p2b
test-b   1/1     Running   0          3s    192.168.0.130   stackit-capi-test-md-0-8szbn-xcc5l

$ TEST_B_IP="$(kubectl --kubeconfig "${KUBECONF_WORKERCLUSTER}" get pod test-b -o jsonpath='{.status.podIP}')"
$ kubectl --kubeconfig "${KUBECONF_WORKERCLUSTER}" exec test-a -- ping -c3 "${TEST_B_IP}"

PING 192.168.0.130 (192.168.0.130): 56 data bytes
64 bytes from 192.168.0.130: seq=0 ttl=63 time=2.123 ms
64 bytes from 192.168.0.130: seq=1 ttl=63 time=0.482 ms
64 bytes from 192.168.0.130: seq=2 ttl=63 time=0.460 ms

--- 192.168.0.130 ping statistics ---
3 packets transmitted, 3 packets received, 0% packet loss
round-trip min/avg/max = 0.460/1.021/2.123 ms
```

**Result:** Worker `MachineDeployment` scaled 1→2; the new node
(`...q9p2b`) joined and reached `Ready` in about 2 minutes. The pinned pods
landed on the two distinct worker nodes as intended. Cross-node ping
succeeds with 0% packet loss (0.46–2.12ms).

Scale back down to one replica:

```
$ kubectl scale machinedeployment stackit-capi-test-md-0 --replicas=1

machinedeployment.cluster.x-k8s.io/stackit-capi-test-md-0 scaled

$ kubectl --kubeconfig "${KUBECONF_WORKERCLUSTER}" get nodes   # ~15s later

NAME                                    STATUS                     ROLES           AGE     VERSION
stackit-capi-test-control-plane-bkkfk   Ready                      control-plane   13m     v1.35.7
stackit-capi-test-md-0-8szbn-q9p2b      Ready,SchedulingDisabled   <none>          2m16s   v1.35.7
stackit-capi-test-md-0-8szbn-xcc5l      Ready                      <none>          9m42s   v1.35.7

$ kubectl --kubeconfig "${KUBECONF_WORKERCLUSTER}" get nodes   # ~30s later still

NAME                                    STATUS   ROLES           AGE     VERSION
stackit-capi-test-control-plane-bkkfk   Ready    control-plane   14m     v1.35.7
stackit-capi-test-md-0-8szbn-xcc5l      Ready    <none>          9m58s   v1.35.7

$ kubectl get machines,stackitmachines -o wide

NAME                                                            NODE NAME                              PHASE
machine.cluster.x-k8s.io/stackit-capi-test-control-plane-bkkfk  stackit-capi-test-control-plane-bkkfk  Running
machine.cluster.x-k8s.io/stackit-capi-test-md-0-8szbn-xcc5l     stackit-capi-test-md-0-8szbn-xcc5l     Running
```

**Result:** This time the scale-down cordoned and removed the
**newly added** node (`...q9p2b`, 2m16s old), not the original
(`...xcc5l`, 9m42s old) — the opposite choice from a previous run of this
test. Confirms CAPI/MachineSet really does pick an arbitrary machine to
delete, not a fixed "oldest" or "newest" rule; not a provider defect.
Afterwards only one worker node/Machine/StackitMachine remains, cluster back
to its original size with no leftover objects.

---

### 6. Verify in-cluster DNS

`test-a` was removed along with node `...q9p2b` during the step 5
scale-down (it was pinned there via `nodeName`), so it is redeployed here
without a node pin. `test-b` (on the surviving node) is exposed as a Service:

```
$ kubectl --kubeconfig "${KUBECONF_WORKERCLUSTER}" expose pod test-b --port=80 --name=test-b-svc

service/test-b-svc exposed

$ kubectl --kubeconfig "${KUBECONF_WORKERCLUSTER}" run test-a --image=busybox --command -- sleep 3600
$ kubectl --kubeconfig "${KUBECONF_WORKERCLUSTER}" wait --for=condition=Ready pod/test-a --timeout=60s

$ kubectl --kubeconfig "${KUBECONF_WORKERCLUSTER}" exec test-a -- nslookup test-b-svc.default.svc.cluster.local

Server:		10.128.0.10
Address:	10.128.0.10:53

Name:	test-b-svc.default.svc.cluster.local
Address: 10.138.45.77
```

**Result:** DNS correctly resolves `test-b-svc.default.svc.cluster.local` to
its ClusterIP (`10.138.45.77`), served by cluster DNS (`10.128.0.10:53`).

---

### 7. Clean up test resources

```
$ kubectl --kubeconfig "${KUBECONF_WORKERCLUSTER}" delete pod test-a test-b --now

pod "test-a" deleted from default namespace
pod "test-b" deleted from default namespace

$ kubectl --kubeconfig "${KUBECONF_WORKERCLUSTER}" delete svc test-b-svc

service "test-b-svc" deleted from default namespace

$ kubectl --kubeconfig "${KUBECONF_WORKERCLUSTER}" get pods,svc

NAME                 TYPE        CLUSTER-IP   EXTERNAL-IP   PORT(S)   AGE
service/kubernetes   ClusterIP   10.128.0.1   <none>        443/TCP   14m
```

**Result:** All test resources removed; only the built-in `kubernetes`
Service remains.

## Conclusion

**Status:** ✅ works

Everything in scope passes on current `main`: cluster provisioned from
scratch, nodes `Ready`, Cilium `OK`, deployments/pod scheduling confirmed,
worker nodes scale up and down cleanly with no leftover objects, cross-node
pod-to-pod communication works (0% packet loss), and in-cluster DNS
resolves.

**Observation, not a defect:** scaling a `MachineDeployment` down removes an
arbitrary Machine — this run removed the newest node, a prior run removed
the original one. Expected upstream MachineSet behavior, worth remembering
when planning scale-down tests (e.g. in
[run2-2-ha-controlplane.md](run2-2-ha-controlplane.md)).

**Non-bug, self-resolving:** right after the Cilium rollout, `cilium status`
briefly reported 4 errors from kubelet-exec DNS lookups via `1.1.1.1:53`
failing for both node hostnames — cleared within about a minute once
cluster networking/DNS was fully up.
