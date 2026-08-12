# Regression: cluster bootstrapping & networking

Date: 2026-08-11
Run: refactor 1 — see [SUMMARY.md](SUMMARY.md#timeline)
Branch: `refactor/code-cleanup-and-proper-abstraction` (via `debug/regression-test-code-cleanup-and-proper-abstraction`, commit `93eb06c`)
Cluster: `stackit-capi-test`
Status: ✅ works — behaviour identical to `main`

Repeats the bootstrapping package against the refactored provider and compares
every step against the `main` baseline. The refactor moved packages
(`internal/controller/` → `controller/`, `pkg/cloud/` → `cloud/`,
`pkg/scope/` → `scope/`, `pkg/util/` → `util/`) and split the controllers into
several files; the point of this run is to show that nothing changed
observably.

Reference: [run-main1-1-bootstrapping.md](run-main1-1-bootstrapping.md)
(and [run-main2-1-bootstrapping.md](run-main2-1-bootstrapping.md), which was
the first `main` run to evidence cluster creation from scratch).

**Out of scope:** CSI (storage) and Ingress / external load-balancing.

Prerequisites: see [SUMMARY.md](SUMMARY.md#shared-prerequisites), plus the
provider image rebuilt from this branch (see step 0). Cluster generated from
[../templates/cluster-template.yaml](../templates/cluster-template.yaml) with
`CONTROL_PLANE_MACHINE_COUNT=1`, `WORKER_MACHINE_COUNT=1`.

## Test steps

### 0. Build and roll out the refactored provider

Without this the run would silently re-test `main` — the deployed pod was 5
days old and built from the `main`-equivalent tree.

```
$ git rev-parse --abbrev-ref HEAD && git log --oneline -1

debug/regression-test-code-cleanup-and-proper-abstraction
93eb06c Merge remote-tracking branch 'origin/debug/regression-test-main' into debug/regression-test-refactor/code-cleanup-and-proper-abstraction

$ docker exec capi-stackit-control-plane crictl images | grep stackit

docker.io/library/cluster-api-provider-stackit   dev   47194f92bc843   32.7MB

$ GOTOOLCHAIN=auto make test

ok  github.com/stackitcloud/cluster-api-provider-stackit/cloud            coverage: 39.1% of statements
ok  github.com/stackitcloud/cluster-api-provider-stackit/controller       coverage: 70.2% of statements
ok  github.com/stackitcloud/cluster-api-provider-stackit/util             coverage: 31.0% of statements
ok  github.com/stackitcloud/cluster-api-provider-stackit/webhook/v1alpha1 coverage: 80.6% of statements

$ make docker-build IMG=cluster-api-provider-stackit:dev
$ kind load docker-image cluster-api-provider-stackit:dev --name capi-stackit
$ docker exec capi-stackit-control-plane crictl images | grep stackit

docker.io/library/cluster-api-provider-stackit   dev   6aedd49a80f6f   33.3MB

$ kubectl rollout restart deploy/cluster-api-provider-stackit-controller-manager -n cluster-api-provider-stackit-system
$ kubectl rollout status deploy/cluster-api-provider-stackit-controller-manager -n cluster-api-provider-stackit-system

deployment "cluster-api-provider-stackit-controller-manager" successfully rolled out

$ kubectl get pods -n cluster-api-provider-stackit-system

cluster-api-provider-stackit-controller-manager-84bf47f87fgrnt9   1/1   Running   0   40s
```

**Result:** Image digest changed `47194f92bc843` → `6aedd49a80f6f`, so the run
genuinely exercises the refactored code. All four unit-test packages pass. The
new pod acquired leader election and started both controllers with no errors.

**Gotcha:** `go env GOTOOLCHAIN` is `local` while `go.mod` now requires Go
1.26.0 (installed: 1.25.12), so plain `make test`/`make manifests` fail with
`go.mod requires go >= 1.26.0`. Prefixing `GOTOOLCHAIN=auto` makes Go fetch the
toolchain. The container build is unaffected — the `Dockerfile` uses
`golang:1.26`.

**Vergleich main:** new step, no counterpart — the `main` runs used an image
that was already current.

---

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

$ kubectl get clusters,stackitclusters   # t+2m34s

NAME                                        CLUSTERCLASS   AVAILABLE   PHASE         AGE
cluster.cluster.x-k8s.io/stackit-capi-test                 False       Provisioned   2m34s

NAME                                                              READY   ENDPOINT
stackitcluster.infrastructure.cluster.x-k8s.io/stackit-capi-test   true    213.17.21.81
```

**Result:** Identical object set created; `Provisioned` with a ready
`StackitCluster` and an API endpoint after 2m34s.

**Vergleich main:** `run-main2` reached the same state in 2m26s — same
behaviour, same order of magnitude. (`run-main1` reused an existing cluster and
has no creation timing.)

---

### 2. Wait for control-plane and worker nodes to become Ready

```
$ export KUBECONF_WORKERCLUSTER=/tmp/"${CLUSTER_NAME}".kubeconfig
$ clusterctl get kubeconfig "${CLUSTER_NAME}" -n "${NAMESPACE}" > "${KUBECONF_WORKERCLUSTER}"

$ kubectl --kubeconfig "${KUBECONF_WORKERCLUSTER}" get nodes -o wide

NAME                                    STATUS     ROLES           AGE     VERSION   OS-IMAGE             CONTAINER-RUNTIME
stackit-capi-test-control-plane-7xp8v   NotReady   control-plane   7m6s    v1.35.7   Ubuntu 24.04.4 LTS   containerd://2.2.1
stackit-capi-test-md-0-bx9jt-tnmhq      NotReady   <none>          2m30s   v1.35.7   Ubuntu 24.04.4 LTS   containerd://2.2.1
```

**Result:** Both nodes joined and report `NotReady` — expected, no CNI yet.

**Vergleich main:** same picture in both `main` runs; control-plane first, the
worker joining a few minutes later.

---

### 3. Install Cilium

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
Waiting for daemon set "cilium-envoy" rollout to finish: 0 of 2 updated pods are available...
Waiting for daemon set "cilium-envoy" rollout to finish: 1 of 2 updated pods are available...
daemon set "cilium-envoy" successfully rolled out

$ cilium status --kubeconfig "${KUBECONF_WORKERCLUSTER}"   # ~60s after rollout

    /¯¯\
 /¯¯\__/¯¯\    Cilium:             4 errors
 \__/¯¯\__/    Operator:           OK
 /¯¯\__/¯¯\    Envoy DaemonSet:    OK

DaemonSet              cilium                   Desired: 2, Ready: 2/2, Available: 2/2
DaemonSet              cilium-envoy             Desired: 2, Ready: 2/2, Available: 2/2
Deployment             cilium-operator          Desired: 1, Ready: 1/1, Available: 1/1

$ cilium status --kubeconfig "${KUBECONF_WORKERCLUSTER}"   # ~90s later

    /¯¯\
 /¯¯\__/¯¯\    Cilium:             OK
 \__/¯¯\__/    Operator:           OK
 /¯¯\__/¯¯\    Envoy DaemonSet:    OK

DaemonSet              cilium                   Desired: 2, Ready: 2/2, Available: 2/2
DaemonSet              cilium-envoy             Desired: 2, Ready: 2/2, Available: 2/2
Deployment             cilium-operator          Desired: 1, Ready: 1/1, Available: 1/1

$ kubectl --kubeconfig "${KUBECONF_WORKERCLUSTER}" get nodes -o wide

NAME                                    STATUS   ROLES           AGE     VERSION   INTERNAL-IP
stackit-capi-test-control-plane-7xp8v   Ready    control-plane   11m     v1.35.7   10.42.0.155
stackit-capi-test-md-0-bx9jt-tnmhq      Ready    <none>          7m20s   v1.35.7   10.42.0.103
```

**Result:** Cilium rolled out cleanly. The transient "4 errors" right after the
rollout (kubelet-exec name lookups via `1.1.1.1:53`) cleared on its own within
about 90 seconds, after which both nodes were `Ready` with internal IPs set.

**Vergleich main:** `run-main2` saw exactly the same transient errors clearing
within about a minute. Identical behaviour.

---

### 4. Deploy a test workload and verify scheduling

Not executed separately — step 3 already proves it: the `cilium-operator`
Deployment (1/1) and both DaemonSets (2/2) are scheduled and `Running` across
control-plane and worker.

**Result:** Confirmed via step 3.

**Vergleich main:** same reasoning used in both `main` runs.

---

### 5. Verify cross-node pod-to-pod communication

```
$ kubectl scale machinedeployment stackit-capi-test-md-0 --replicas=2

machinedeployment.cluster.x-k8s.io/stackit-capi-test-md-0 scaled

$ kubectl --kubeconfig "${KUBECONF_WORKERCLUSTER}" get nodes   # t+3m40s

NAME                                    STATUS   ROLES           AGE   VERSION
stackit-capi-test-control-plane-7xp8v   Ready    control-plane   15m   v1.35.7
stackit-capi-test-md-0-bx9jt-tnmhq      Ready    <none>          11m   v1.35.7
stackit-capi-test-md-0-bx9jt-whjms      Ready    <none>          60s   v1.35.7

$ NODE_A="$(kubectl --kubeconfig "${KUBECONF_WORKERCLUSTER}" get nodes -l '!node-role.kubernetes.io/control-plane' -o jsonpath='{.items[0].metadata.name}')"
$ NODE_B="$(kubectl --kubeconfig "${KUBECONF_WORKERCLUSTER}" get nodes -l '!node-role.kubernetes.io/control-plane' -o jsonpath='{.items[1].metadata.name}')"

$ kubectl --kubeconfig "${KUBECONF_WORKERCLUSTER}" run test-a --image=busybox --overrides="{\"spec\":{\"nodeName\":\"${NODE_A}\"}}" --command -- sleep 3600
$ kubectl --kubeconfig "${KUBECONF_WORKERCLUSTER}" run test-b --image=busybox --overrides="{\"spec\":{\"nodeName\":\"${NODE_B}\"}}" --command -- sleep 3600
$ kubectl --kubeconfig "${KUBECONF_WORKERCLUSTER}" get pods -o wide

NAME     READY   STATUS    RESTARTS   AGE   IP              NODE
test-a   1/1     Running   0          4s    192.168.0.252   stackit-capi-test-md-0-bx9jt-tnmhq
test-b   1/1     Running   0          4s    192.168.2.193   stackit-capi-test-md-0-bx9jt-whjms

$ TEST_B_IP="$(kubectl --kubeconfig "${KUBECONF_WORKERCLUSTER}" get pod test-b -o jsonpath='{.status.podIP}')"
$ kubectl --kubeconfig "${KUBECONF_WORKERCLUSTER}" exec test-a -- ping -c3 "${TEST_B_IP}"

PING 192.168.2.193 (192.168.2.193): 56 data bytes
64 bytes from 192.168.2.193: seq=0 ttl=63 time=0.674 ms
64 bytes from 192.168.2.193: seq=1 ttl=63 time=0.383 ms
64 bytes from 192.168.2.193: seq=2 ttl=63 time=0.379 ms

--- 192.168.2.193 ping statistics ---
3 packets transmitted, 3 packets received, 0% packet loss
round-trip min/avg/max = 0.379/0.478/0.674 ms
```

**Result:** Scale-up 1→2 took about 3m40s to a `Ready` node. Pods landed on the
two distinct workers; cross-node ping succeeds with 0% packet loss.

**Vergleich main:** `run-main2` measured ~2 minutes to `Ready` and 0.46–2.12ms
round-trip, also 0% loss. Same behaviour.

Scale back down:

```
$ kubectl scale machinedeployment stackit-capi-test-md-0 --replicas=1

machinedeployment.cluster.x-k8s.io/stackit-capi-test-md-0 scaled

$ kubectl --kubeconfig "${KUBECONF_WORKERCLUSTER}" get nodes   # ~10s later

NAME                                    STATUS                     ROLES           AGE     VERSION
stackit-capi-test-control-plane-7xp8v   Ready                      control-plane   17m     v1.35.7
stackit-capi-test-md-0-bx9jt-tnmhq      Ready,SchedulingDisabled   <none>          13m     v1.35.7
stackit-capi-test-md-0-bx9jt-whjms      Ready                      <none>          2m59s   v1.35.7

$ kubectl --kubeconfig "${KUBECONF_WORKERCLUSTER}" get nodes   # ~40s later

NAME                                    STATUS   ROLES           AGE     VERSION
stackit-capi-test-control-plane-7xp8v   Ready    control-plane   18m     v1.35.7
stackit-capi-test-md-0-bx9jt-whjms      Ready    <none>          3m39s   v1.35.7

$ kubectl get machines,stackitmachines

machine.cluster.x-k8s.io/stackit-capi-test-control-plane-7xp8v   stackit-capi-test-control-plane-7xp8v
machine.cluster.x-k8s.io/stackit-capi-test-md-0-bx9jt-whjms      stackit-capi-test-md-0-bx9jt-whjms

stackitmachine.infrastructure.cluster.x-k8s.io/stackit-capi-test-control-plane-7xp8v   21m
stackitmachine.infrastructure.cluster.x-k8s.io/stackit-capi-test-md-0-bx9jt-whjms      6m20s
```

**Result:** Scale-down removed the **original** worker (`...tnmhq`) and kept
the newly added one. Afterwards exactly one worker Machine/StackitMachine/Node
remains, no leftovers.

**Vergleich main:** `run-main1` also lost the original node, `run-main2` lost
the newest. Across three runs the choice is genuinely arbitrary — upstream
MachineSet behaviour, not a provider defect and not a refactor regression.

---

### 6. Verify in-cluster DNS

`test-a` was pinned to the removed node, so it is redeployed without a pin.

```
$ kubectl --kubeconfig "${KUBECONF_WORKERCLUSTER}" get pods

NAME     READY   STATUS    RESTARTS   AGE
test-b   1/1     Running   0          109s

$ kubectl --kubeconfig "${KUBECONF_WORKERCLUSTER}" expose pod test-b --port=80 --name=test-b-svc

service/test-b-svc exposed

$ kubectl --kubeconfig "${KUBECONF_WORKERCLUSTER}" run test-a --image=busybox --command -- sleep 3600
$ kubectl --kubeconfig "${KUBECONF_WORKERCLUSTER}" exec test-a -- nslookup test-b-svc.default.svc.cluster.local

Server:		10.128.0.10
Address:	10.128.0.10:53

Name:	test-b-svc.default.svc.cluster.local
Address: 10.129.126.55
```

**Result:** DNS resolves the Service name to its ClusterIP via cluster DNS.

**Vergleich main:** identical mechanism and result in both `main` runs (only
the ClusterIP differs, as expected).

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
service/kubernetes   ClusterIP   10.128.0.1   <none>        443/TCP   19m
```

**Result:** All test resources removed; only the built-in `kubernetes` Service
remains.

**Vergleich main:** identical.

## Conclusion

**Status:** ✅ works — no behavioural difference from `main`

Every step produced the same outcome as the `main` baseline: cluster
provisions in the same time frame, both nodes join, Cilium rolls out with the
same short-lived startup errors, cross-node traffic flows with 0% loss,
in-cluster DNS resolves, and scale-up/scale-down leave no orphaned objects.

**Refactor parity for this package: confirmed.** Nothing observable changed
despite `internal/controller/` → `controller/`, `pkg/cloud/` → `cloud/` and the
controller split.

**Environment note (not a provider issue):** `GOTOOLCHAIN=local` blocks local
Go tooling because `go.mod` now requires Go 1.26.0 while 1.25.12 is installed.
`GOTOOLCHAIN=auto` resolves it; the container build never was affected.
