# Regression: cluster bootstrapping & networking

Date: 2026-08-05
Run: 1 of 2 — see [SUMMARY.md](SUMMARY.md#timeline)
Cluster: `stackit-workload`
Status: ✅ works

Verifies that a workload cluster bootstraps on current `main`: control-plane
and worker nodes join and reach `Ready`, and — once Cilium is installed as
CNI — workloads can be deployed, pods are scheduled, cross-node pod-to-pod
communication works, and in-cluster DNS resolves.

Bootstrapping and networking are covered together on purpose: running a
workload requires a CNI first (without one, pods stay
`Pending`/`ContainerCreating` because kubelet cannot set up the pod network
namespace), so testing them separately would either duplicate the deployment
check or create an ordering dependency between two documents.

**Out of scope:** CSI (storage) and Ingress / external load-balancing.

Prerequisites: see [SUMMARY.md](SUMMARY.md#shared-prerequisites). Cluster
generated from [../templates/cluster-template.yaml](../templates/cluster-template.yaml)
with `CONTROL_PLANE_MACHINE_COUNT=1`, `WORKER_MACHINE_COUNT=1`.

## Test steps

### 1. Generate and apply the workload cluster manifest

```
$ kubectl get clusters,stackitclusters

NAME                                        CLUSTERCLASS   AVAILABLE   PHASE
cluster.cluster.x-k8s.io/stackit-workload                  True        Provisioned

NAME                                                    READY   ENDPOINT
stackitcluster.infrastructure.cluster.x-k8s.io/stackit-workload   true    192.214.181.85
```

**Result:** Cluster was already provisioned prior to this run; `Cluster` and
`StackitCluster` both report ready/`Provisioned`.

---

### 2. Wait for control-plane and worker nodes to become Ready

```
$ kubectl --kubeconfig "${KUBECONF_WORKERCLUSTER}" get nodes -w

NAME                                   STATUS   ROLES           AGE    VERSION
stackit-workload-control-plane-qqb5n   Ready    control-plane   175m   v1.35.7
stackit-workload-md-0-t5k56-5k74b      Ready    <none>          171m   v1.35.7
```

**Result:** Both the control-plane node and the worker node are `Ready`.
Nodes may briefly report `Ready` with network-not-ready conditions until
step 3 installs a CNI.

---

### 3. Install Cilium

Install via `make install-workload-cni WORKLOAD_KUBECONFIG="${KUBECONF_WORKERCLUSTER}"`,
which uses [../templates/addons/cilium-values.yaml](../templates/addons/cilium-values.yaml)
(IPAM `cluster-pool`, pod CIDR `192.168.0.0/16`, `kubeProxyReplacement: false`).
See also [../docs/src/usage/cni.md](../docs/src/usage/cni.md) and
[../docs/src/usage/addons.md](../docs/src/usage/addons.md).

```
$ cilium status --wait --kubeconfig "${KUBECONF_WORKERCLUSTER}"

    /¯¯\
 /¯¯\__/¯¯\    Cilium:             OK
 \__/¯¯\__/    Operator:           OK
 /¯¯\__/¯¯\    Envoy DaemonSet:    OK
 \__/¯¯\__/    Hubble Relay:       disabled
    \__/       ClusterMesh:        disabled

DaemonSet              cilium                   Desired: 2, Ready: 2/2, Available: 2/2
DaemonSet              cilium-envoy             Desired: 2, Ready: 2/2, Available: 2/2
Deployment             cilium-operator          Desired: 1, Ready: 1/1, Available: 1/1
Cluster Pods:          3/3 managed by Cilium
Helm chart version:    1.19.4
```

**Result:** Cilium was already installed prior to this run; `cilium status
--wait` reports `OK` (Cilium, Operator, Envoy DaemonSet), 2/2 nodes covered
by the `cilium` DaemonSet, 3/3 cluster pods managed by Cilium.

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
$ kubectl scale machinedeployment stackit-workload-md-0 --replicas=2

machinedeployment.cluster.x-k8s.io/stackit-workload-md-0 scaled

$ kubectl --kubeconfig "${KUBECONF_WORKERCLUSTER}" get nodes -o wide

NAME                                   STATUS   ROLES           AGE     VERSION
stackit-workload-control-plane-qqb5n   Ready    control-plane   3h19m   v1.35.7
stackit-workload-md-0-t5k56-5k74b      Ready    <none>          3h16m   v1.35.7
stackit-workload-md-0-t5k56-8nlzf      Ready    <none>          4m3s    v1.35.7

$ NODE_A="$(kubectl --kubeconfig "${KUBECONF_WORKERCLUSTER}" get nodes -l '!node-role.kubernetes.io/control-plane' -o jsonpath='{.items[0].metadata.name}')"
$ NODE_B="$(kubectl --kubeconfig "${KUBECONF_WORKERCLUSTER}" get nodes -l '!node-role.kubernetes.io/control-plane' -o jsonpath='{.items[1].metadata.name}')"

$ kubectl --kubeconfig "${KUBECONF_WORKERCLUSTER}" run test-a --image=busybox --overrides="{\"spec\":{\"nodeName\":\"${NODE_A}\"}}" --command -- sleep 3600
$ kubectl --kubeconfig "${KUBECONF_WORKERCLUSTER}" run test-b --image=busybox --overrides="{\"spec\":{\"nodeName\":\"${NODE_B}\"}}" --command -- sleep 3600
$ kubectl --kubeconfig "${KUBECONF_WORKERCLUSTER}" wait --for=condition=Ready pod/test-a --timeout=120s
$ kubectl --kubeconfig "${KUBECONF_WORKERCLUSTER}" wait --for=condition=Ready pod/test-b --timeout=120s
$ kubectl --kubeconfig "${KUBECONF_WORKERCLUSTER}" get pods -o wide

NAME     READY   STATUS    RESTARTS   AGE   IP              NODE
test-a   1/1     Running   0          3s    192.168.1.165   stackit-workload-md-0-t5k56-5k74b
test-b   1/1     Running   0          3s    192.168.2.221   stackit-workload-md-0-t5k56-8nlzf

$ TEST_B_IP="$(kubectl --kubeconfig "${KUBECONF_WORKERCLUSTER}" get pod test-b -o jsonpath='{.status.podIP}')"
$ kubectl --kubeconfig "${KUBECONF_WORKERCLUSTER}" exec test-a -- ping -c3 "${TEST_B_IP}"

PING 192.168.2.221 (192.168.2.221): 56 data bytes
64 bytes from 192.168.2.221: seq=0 ttl=63 time=0.984 ms
64 bytes from 192.168.2.221: seq=1 ttl=63 time=0.797 ms
64 bytes from 192.168.2.221: seq=2 ttl=63 time=1.062 ms

--- 192.168.2.221 ping statistics ---
3 packets transmitted, 3 packets received, 0% packet loss
round-trip min/avg/max = 0.797/0.947/1.062 ms
```

**Result:** Worker `MachineDeployment` scaled 1→2; the new node
(`...8nlzf`) joined and reached `Ready` after ~4 minutes. The pinned pods
landed on the two distinct worker nodes as intended. Cross-node ping
succeeds (0% packet loss, ~0.8-1.1ms — noticeably higher than an earlier
same-node run at ~0.07-0.09ms, consistent with traffic actually crossing
nodes via Cilium's overlay).

Scale back down to one replica:

```
$ kubectl scale machinedeployment stackit-workload-md-0 --replicas=1

machinedeployment.cluster.x-k8s.io/stackit-workload-md-0 scaled

$ kubectl --kubeconfig "${KUBECONF_WORKERCLUSTER}" get nodes -w

NAME                                   STATUS                     ROLES           AGE     VERSION
stackit-workload-md-0-t5k56-5k74b      Ready,SchedulingDisabled   <none>          3h29m   v1.35.7
stackit-workload-md-0-t5k56-8nlzf      Ready                      <none>          18m     v1.35.7

$ kubectl get machines,stackitmachines

NAME                                                            NODE NAME                              PHASE
machine.cluster.x-k8s.io/stackit-workload-control-plane-qqb5n   stackit-workload-control-plane-qqb5n   Running
machine.cluster.x-k8s.io/stackit-workload-md-0-t5k56-8nlzf      stackit-workload-md-0-t5k56-8nlzf      Running
```

**Result:** Scale-down cordoned the *original* worker node (`...5k74b`), not
the newly added one — CAPI/MachineSet picks an arbitrary machine to delete
rather than preferring the most-recently-created one. Expected upstream
MachineSet behavior, not a provider defect. Afterwards only one worker
node/Machine/StackitMachine remains; cluster is back to its original size
with no leftover objects.

---

### 6. Verify in-cluster DNS

`test-a` was removed along with node `...5k74b` during the step 5
scale-down (it was pinned there via `nodeName`), so it is redeployed here
without a node pin:

```
$ kubectl --kubeconfig "${KUBECONF_WORKERCLUSTER}" expose pod test-b --port=80 --name=test-b-svc

service/test-b-svc exposed

$ kubectl --kubeconfig "${KUBECONF_WORKERCLUSTER}" run test-a --image=busybox --command -- sleep 3600
$ kubectl --kubeconfig "${KUBECONF_WORKERCLUSTER}" wait --for=condition=Ready pod/test-a --timeout=120s

$ kubectl --kubeconfig "${KUBECONF_WORKERCLUSTER}" exec test-a -- nslookup test-b-svc.default.svc.cluster.local

Server:		10.128.0.10
Address:	10.128.0.10:53

Name:	test-b-svc.default.svc.cluster.local
Address: 10.136.0.99
```

**Result:** DNS correctly resolves `test-b-svc.default.svc.cluster.local` to
its ClusterIP (`10.136.0.99`), served by cluster DNS (`10.128.0.10:53`).

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
service/kubernetes   ClusterIP   10.128.0.1   <none>        443/TCP   3h44m
```

**Result:** All test resources removed; only the built-in `kubernetes`
Service remains.

## Conclusion

**Status:** ✅ works

Everything in scope passes on current `main`: cluster provisioned, nodes
`Ready`, Cilium `OK`, deployments/pod scheduling confirmed, worker nodes
scale up and down cleanly with no leftover objects, cross-node pod-to-pod
communication works, and in-cluster DNS resolves.

**Observation, not a defect:** scaling a `MachineDeployment` down removes an
arbitrary Machine, not necessarily the most recently added one — expected
upstream MachineSet behavior, worth remembering when planning scale-down
tests (e.g. in [run1-2-ha-controlplane.md](run1-2-ha-controlplane.md)).
