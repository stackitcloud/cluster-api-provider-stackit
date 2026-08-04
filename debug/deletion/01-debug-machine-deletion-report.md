# Debug: Machines are not deleted after `kubectl delete -f cluster.yaml`

Date: 2026-08-03
Cluster: `stackit-workload`

## Debug commands used

### 1. Overview of Machines

```
$ kubectl get machines,machinedeployments,clusters -A

NAME                                   CLUSTER            NODE NAME                              FAILURE DOMAIN   READY     AVAILABLE   UP-TO-DATE   PHASE      AGE     VERSION
stackit-workload-control-plane-64zwk   stackit-workload   stackit-workload-control-plane-64zwk                    Unknown   False       True         Running    2d17h   v1.35.3
stackit-workload-md-0-7bhgs-n4j2t      stackit-workload   stackit-workload-md-0-7bhgs-n4j2t                       False     False       True         Deleting   2d16h   v1.35.3
stackit-workload-md-0-7bhgs-q6mbg      stackit-workload   stackit-workload-md-0-7bhgs-q6mbg                       False     False       True         Deleting   2d16h   v1.35.3
stackit-workload-md-0-7bhgs-sjn22      stackit-workload   stackit-workload-md-0-7bhgs-sjn22                       False     False       True         Deleting   2d17h   v1.35.3
```

**Result:** All 3 worker Machines (`stackit-workload-md-0-*`) have been stuck in `PHASE=Deleting` for over 2 days (2d16h-2d17h). The control plane Machine is running normally.

---

### 2. Details of a stuck Machine

```
$ kubectl describe machine stackit-workload-md-0-7bhgs-n4j2t

Name:         stackit-workload-md-0-7bhgs-n4j2t
Namespace:    default
Labels:       cluster.x-k8s.io/cluster-name=stackit-workload
              cluster.x-k8s.io/deployment-name=stackit-workload-md-0
              cluster.x-k8s.io/set-name=stackit-workload-md-0-7bhgs
              machine-template-hash=978657239-7bhgs
Annotations:  <none>
API Version:  cluster.x-k8s.io/v1beta2
Kind:         Machine
Metadata:
  Creation Timestamp:             2026-07-31T14:16:26Z
  Deletion Grace Period Seconds:  0
  Deletion Timestamp:             2026-07-31T14:38:18Z
  Finalizers:
    machine.cluster.x-k8s.io
  Generation:  4
  Owner References:
    API Version:           cluster.x-k8s.io/v1beta2
    Block Owner Deletion:  true
    Controller:            true
    Kind:                  MachineSet
    Name:                  stackit-workload-md-0-7bhgs
    UID:                   c3e090ec-0254-481a-a130-259fdc5764af
  Resource Version:        14024
  UID:                     db48bab7-992a-403d-99f1-22f7f6a0a9ef
Spec:
  Bootstrap:
    Config Ref:
      API Group:       bootstrap.cluster.x-k8s.io
      Kind:            KubeadmConfig
      Name:            stackit-workload-md-0-7bhgs-n4j2t
    Data Secret Name:  stackit-workload-md-0-7bhgs-n4j2t
  Cluster Name:        stackit-workload
  Deletion:
    Node Deletion Timeout Seconds:  10
  Infrastructure Ref:
    API Group:  infrastructure.cluster.x-k8s.io
    Kind:       StackitMachine
    Name:       stackit-workload-md-0-7bhgs-n4j2t
  Provider ID:  stackit://cec50749-6eef-4dc7-a64f-178ac8d08bcb
  Version:      v1.35.3
Status:
  Addresses:
    Address:  10.0.3.189
    Type:     InternalIP
  Conditions:
    Last Transition Time:  2026-07-31T14:38:19Z
    Message:
    Observed Generation:   4
    Reason:                NotReady
    Status:                False
    Type:                  Available
    Last Transition Time:  2026-07-31T14:38:19Z
    Message:               * Deleting: Machine deletion in progress since more than 15m, stage: WaitingForInfrastructureDeletion
* NodeHealthy: Last successful probe at 2026-07-31T14:39:15Z
    Observed Generation:   4
    Reason:                NotReady
    Status:                False
    Type:                  Ready
    Last Transition Time:  2026-07-31T14:16:27Z
    Message:
    Observed Generation:   4
    Reason:                UpToDate
    Status:                True
    Type:                  UpToDate
    Last Transition Time:  2026-07-31T14:16:29Z
    Message:
    Observed Generation:   4
    Reason:                Ready
    Status:                True
    Type:                  BootstrapConfigReady
    Last Transition Time:  2026-07-31T14:18:56Z
    Message:
    Observed Generation:   4
    Reason:                Available
    Status:                True
    Type:                  InfrastructureReady
    Last Transition Time:  2026-07-31T14:44:24Z
    Message:               Last successful probe at 2026-07-31T14:39:15Z
    Observed Generation:   4
    Reason:                ConnectionDown
    Status:                Unknown
    Type:                  NodeHealthy
    Last Transition Time:  2026-07-31T14:44:24Z
    Message:               Last successful probe at 2026-07-31T14:39:15Z
    Observed Generation:   4
    Reason:                ConnectionDown
    Status:                Unknown
    Type:                  NodeReady
    Last Transition Time:  2026-07-31T14:16:27Z
    Message:
    Observed Generation:   4
    Reason:                NotUpdating
    Status:                False
    Type:                  Updating
    Last Transition Time:  2026-07-31T14:16:26Z
    Message:
    Observed Generation:   4
    Reason:                NotPaused
    Status:                False
    Type:                  Paused
    Last Transition Time:  2026-07-31T14:38:19Z
    Message:               Waiting for StackitMachine to be deleted
    Observed Generation:   4
    Reason:                WaitingForInfrastructureDeletion
    Status:                True
    Type:                  Deleting
  Deprecated:
    v1beta1:
      Conditions:
        Last Transition Time:  2026-07-31T14:16:30Z
        Status:                True
        Type:                  Ready
        Last Transition Time:  2026-07-31T14:16:31Z
        Status:                True
        Type:                  BootstrapReady
        Last Transition Time:  2026-07-31T14:18:56Z
        Reason:                Available
        Status:                True
        Type:                  InfrastructureReady
        Last Transition Time:  2026-07-31T14:38:20Z
        Reason:                Deleting
        Severity:              Info
        Status:                False
        Type:                  NodeHealthy
        Last Transition Time:  2026-07-31T14:38:19Z
        Status:                True
        Type:                  PreTerminateDeleteHookSucceeded
  Initialization:
    Bootstrap Data Secret Created:  true
    Infrastructure Provisioned:     true
  Last Updated:                     2026-07-31T14:38:19Z
  Node Info:
    Architecture:               amd64
    Boot ID:                    3f348ed6-585c-4f09-be3c-4588d9b36bd8
    Container Runtime Version:  containerd://2.2.1
    Kernel Version:             6.8.0-136-generic
    Kube Proxy Version:
    Kubelet Version:            v1.35.7
    Machine ID:                 cec507496eef4dc7a64f178ac8d08bcb
    Operating System:           linux
    Os Image:                   Ubuntu 24.04.4 LTS
    System UUID:                cec50749-6eef-4dc7-a64f-178ac8d08bcb
  Node Ref:
    Name:               stackit-workload-md-0-7bhgs-n4j2t
  Observed Generation:  4
  Phase:                Deleting
Events:                 <none>
```

**Result:**
- `DeletionTimestamp: 2026-07-31T14:38:18Z` (set for over 2 days)
- Finalizer `machine.cluster.x-k8s.io` still present
- Condition `Deleting`: `Reason: WaitingForInfrastructureDeletion`, Message: *"Waiting for StackitMachine to be deleted"*
- → The `Machine` is waiting for its infrastructure resource `StackitMachine`.

---

### 3. Details of the associated StackitMachine resource

```
$ kubectl describe stackitmachine stackit-workload-md-0-7bhgs-n4j2t

Name:         stackit-workload-md-0-7bhgs-n4j2t
Namespace:    default
Labels:       cluster.x-k8s.io/cluster-name=stackit-workload
              cluster.x-k8s.io/deployment-name=stackit-workload-md-0
              cluster.x-k8s.io/set-name=stackit-workload-md-0-7bhgs
              machine-template-hash=978657239-7bhgs
Annotations:  cluster.x-k8s.io/cloned-from-groupkind: StackitMachineTemplate.infrastructure.cluster.x-k8s.io
              cluster.x-k8s.io/cloned-from-name: stackit-workload-md-0
API Version:  infrastructure.cluster.x-k8s.io/v1alpha1
Kind:         StackitMachine
Metadata:
  Creation Timestamp:             2026-07-31T14:16:26Z
  Deletion Grace Period Seconds:  0
  Deletion Timestamp:             2026-07-31T14:38:19Z
  Finalizers:
    stackitmachine.infrastructure.cluster.x-k8s.io
  Generation:  3
  Owner References:
    API Version:           cluster.x-k8s.io/v1beta2
    Block Owner Deletion:  true
    Controller:            true
    Kind:                  Machine
    Name:                  stackit-workload-md-0-7bhgs-n4j2t
    UID:                   db48bab7-992a-403d-99f1-22f7f6a0a9ef
  Resource Version:        10769
  UID:                     4767c7e3-1126-4a9f-a3d2-dbd96cdc980d
Spec:
  Image ID:      f18283fa-bf02-4cab-bd44-3a813249ad9a
  Machine Type:  c2i.4
  Network:
    Id:         f3aaa41f-d7b7-4ecd-bbc2-38932bca6555
  Provider ID:  stackit://cec50749-6eef-4dc7-a64f-178ac8d08bcb
  Root Volume:
    Delete On Termination:  true
    Performance Class:      storage_premium_perf6
    Size Gi B:              50
Status:
  Addresses:
    Address:  10.0.3.189
    Type:     InternalIP
  Conditions:
    Last Transition Time:  2026-07-31T14:16:27Z
    Message:
    Observed Generation:   2
    Reason:                NotPaused
    Status:                False
    Type:                  Paused
    Last Transition Time:  2026-07-31T14:16:42Z
    Message:
    Observed Generation:   2
    Reason:                Available
    Status:                True
    Type:                  BootstrapReady
    Last Transition Time:  2026-07-31T14:18:56Z
    Message:
    Observed Generation:   2
    Reason:                Available
    Status:                True
    Type:                  Ready
    Last Transition Time:  2026-07-31T14:16:42Z
    Message:
    Observed Generation:   2
    Reason:                Available
    Status:                True
    Type:                  CredentialsReady
    Last Transition Time:  2026-07-31T14:18:56Z
    Message:
    Observed Generation:   2
    Reason:                Available
    Status:                True
    Type:                  InstanceReady
  Initialization:
    Provisioned:   true
  Instance ID:     cec50749-6eef-4dc7-a64f-178ac8d08bcb
  Instance State:  ACTIVE
  Provider ID:     stackit://cec50749-6eef-4dc7-a64f-178ac8d08bcb
  Ready:           true
Events:            <none>
```

**Result:**
- `DeletionTimestamp` set, finalizer `stackitmachine.infrastructure.cluster.x-k8s.io` still present
- `Generation: 3`, but all status conditions show `Observed Generation: 2`
- → The controller has **never processed** the deletion (no new reconcile pass visible in the status since the delete request).

---

### 4. Checking whether the controller pod is running

```
$ kubectl get pods -A | grep -iE "stackit|capi|cluster-api|controller"

capi-kubeadm-bootstrap-system         capi-kubeadm-bootstrap-controller-manager-79dc4c8d8b-2kls4        1/1     Running   0               2d17h
capi-kubeadm-control-plane-system     capi-kubeadm-control-plane-controller-manager-c7d4f75c6-swl5q     1/1     Running   0               2d17h
capi-system                           capi-controller-manager-7488f858b8-r9v55                          1/1     Running   0               2d17h
cluster-api-provider-stackit-system   cluster-api-provider-stackit-controller-manager-fd8dfb75b-6gxw4   1/1     Running   0               2d17h
kube-system                           etcd-capi-stackit-control-plane                                   1/1     Running   0               2d17h
kube-system                           kube-apiserver-capi-stackit-control-plane                         1/1     Running   0               2d17h
kube-system                           kube-controller-manager-capi-stackit-control-plane                1/1     Running   0               2d17h
kube-system                           kube-scheduler-capi-stackit-control-plane                         1/1     Running   0               2d17h
```

**Result:** All CAPI controller pods (including `cluster-api-provider-stackit-controller-manager-*`) are running normally (`1/1 Running`, no restarts). The controller itself has not crashed.

---

### 5. Searching controller logs for errors/deletion activity

```
$ kubectl logs -n cluster-api-provider-stackit-system cluster-api-provider-stackit-controller-manager-fd8dfb75b-6gxw4 --tail=200 | grep -iE "error|delete|n4j2t|panic|retry"

2026-07-31T14:16:26Z	INFO	stackitmachine-resource	Defaulting for StackitMachine	{"name": "stackit-workload-md-0-7bhgs-n4j2t"}
2026-07-31T14:16:26Z	INFO	stackitmachine-resource	Validation for StackitMachine upon creation	{"name": "stackit-workload-md-0-7bhgs-n4j2t"}
2026-07-31T14:16:26Z	INFO	StackitMachine has no owning Machine yet, requeueing	{"controller": "stackitmachine", ...}
2026-07-31T14:16:26Z	INFO	stackitmachine-resource	Defaulting for StackitMachine	{"name": "stackit-workload-md-0-7bhgs-n4j2t"}
2026-07-31T14:16:26Z	INFO	stackitmachine-resource	Validation for StackitMachine upon update	{"name": "stackit-workload-md-0-7bhgs-n4j2t"}
2026-07-31T14:16:26Z	INFO	StackitMachine has no owning Machine yet, requeueing	{"controller": "stackitmachine", ...}
2026-07-31T14:16:26Z	INFO	StackitMachine has no owning Machine yet, requeueing	{"controller": "stackitmachine", ...}
2026-07-31T14:16:26Z	INFO	StackitMachine has no owning Machine yet, requeueing	{"controller": "stackitmachine", ...}
2026-07-31T14:16:27Z	INFO	stackitmachine-resource	Defaulting for StackitMachine	{"name": "stackit-workload-md-0-7bhgs-n4j2t"}
2026-07-31T14:16:27Z	INFO	stackitmachine-resource	Validation for StackitMachine upon update	{"name": "stackit-workload-md-0-7bhgs-n4j2t"}
2026-07-31T14:16:27Z	INFO	StackitMachine has no owning Machine yet, requeueing	{"controller": "stackitmachine", ...}
2026-07-31T14:16:27Z	INFO	stackitmachine-resource	Defaulting for StackitMachine	{"name": "stackit-workload-md-0-7bhgs-n4j2t"}
2026-07-31T14:16:27Z	INFO	stackitmachine-resource	Validation for StackitMachine upon update	{"name": "stackit-workload-md-0-7bhgs-n4j2t"}
2026-07-31T14:16:27Z	INFO	stackitmachine-resource	Defaulting for StackitMachine	{"name": "stackit-workload-md-0-7bhgs-n4j2t"}
2026-07-31T14:16:27Z	INFO	stackitmachine-resource	Validation for StackitMachine upon update	{"name": "stackit-workload-md-0-7bhgs-n4j2t"}
2026-07-31T14:16:27Z	INFO	metadata.finalizers: "stackitmachine.infrastructure.cluster.x-k8s.io": prefer a domain-qualified finalizer name including a path (/) to avoid accidental conflicts with other finalizer writers	{"controller": "stackitmachine", ...}
2026-07-31T14:16:28Z	INFO	stackitmachine-resource	Defaulting for StackitMachine	{"name": "stackit-workload-md-0-7bhgs-n4j2t"}
2026-07-31T14:16:28Z	INFO	stackitmachine-resource	Validation for StackitMachine upon update	{"name": "stackit-workload-md-0-7bhgs-n4j2t"}
2026-07-31T14:16:53Z	INFO	stackitmachine-resource	Defaulting for StackitMachine	{"name": "stackit-workload-md-0-7bhgs-n4j2t"}
2026-07-31T14:16:53Z	INFO	stackitmachine-resource	Validation for StackitMachine upon update	{"name": "stackit-workload-md-0-7bhgs-n4j2t"}
2026-07-31T14:16:54Z	INFO	stackitmachine-resource	Defaulting for StackitMachine	{"name": "stackit-workload-md-0-7bhgs-n4j2t"}
2026-07-31T14:16:54Z	INFO	stackitmachine-resource	Validation for StackitMachine upon update	{"name": "stackit-workload-md-0-7bhgs-n4j2t"}
2026-07-31T14:17:02Z	INFO	stackitmachine-resource	Defaulting for StackitMachine	{"name": "stackit-workload-md-0-7bhgs-n4j2t"}
2026-07-31T14:17:02Z	INFO	stackitmachine-resource	Validation for StackitMachine upon update	{"name": "stackit-workload-md-0-7bhgs-n4j2t"}
2026-07-31T14:18:56Z	DEBUG	StackitMachine ready	{"controller": "stackitmachine", ..., "providerID": "stackit://cec50749-6eef-4dc7-a64f-178ac8d08bcb"}
2026-07-31T14:18:57Z	INFO	stackitmachine-resource	Defaulting for StackitMachine	{"name": "stackit-workload-md-0-7bhgs-n4j2t"}
2026-07-31T14:18:57Z	INFO	stackitmachine-resource	Validation for StackitMachine upon update	{"name": "stackit-workload-md-0-7bhgs-n4j2t"}
2026-07-31T14:18:59Z	DEBUG	StackitMachine ready	{"controller": "stackitmachine", ..., "providerID": "stackit://cec50749-6eef-4dc7-a64f-178ac8d08bcb"}
2026-07-31T14:19:01Z	DEBUG	StackitMachine ready	{"controller": "stackitmachine", ..., "providerID": "stackit://cec50749-6eef-4dc7-a64f-178ac8d08bcb"}
2026-07-31T14:19:29Z	DEBUG	StackitMachine ready	{"controller": "stackitmachine", ..., "providerID": "stackit://cec50749-6eef-4dc7-a64f-178ac8d08bcb"}
2026-07-31T14:19:32Z	DEBUG	StackitMachine ready	{"controller": "stackitmachine", ..., "providerID": "stackit://cec50749-6eef-4dc7-a64f-178ac8d08bcb"}
2026-07-31T14:19:38Z	DEBUG	StackitMachine ready	{"controller": "stackitmachine", ..., "providerID": "stackit://cec50749-6eef-4dc7-a64f-178ac8d08bcb"}
2026-07-31T14:19:51Z	DEBUG	StackitMachine ready	{"controller": "stackitmachine", ..., "providerID": "stackit://cec50749-6eef-4dc7-a64f-178ac8d08bcb"}
2026-07-31T14:38:23Z	INFO	StackitCluster not found, requeueing	{"controller": "stackitmachine", "StackitMachine": {"name":"stackit-workload-md-0-7bhgs-n4j2t","namespace":"default"}, ...}
2026-07-31T14:44:24Z	INFO	StackitCluster not found, requeueing	{"controller": "stackitmachine", ...}
2026-07-31T14:54:55Z	INFO	StackitCluster not found, requeueing	{"controller": "stackitmachine", ...}
2026-08-01T13:42:49Z	INFO	StackitCluster not found, requeueing	{"controller": "stackitmachine", ...}
2026-08-01T13:52:38Z	INFO	StackitCluster not found, requeueing	{"controller": "stackitmachine", ...}
2026-08-01T14:28:30Z	INFO	StackitCluster not found, requeueing	{"controller": "stackitmachine", ...}
2026-08-01T23:53:29Z	INFO	StackitCluster not found, requeueing	{"controller": "stackitmachine", ...}
2026-08-02T00:08:14Z	INFO	StackitCluster not found, requeueing	{"controller": "stackitmachine", ...}
2026-08-02T01:02:01Z	INFO	StackitCluster not found, requeueing	{"controller": "stackitmachine", ...}
2026-08-02T10:04:09Z	INFO	StackitCluster not found, requeueing	{"controller": "stackitmachine", ...}
2026-08-02T10:23:49Z	INFO	StackitCluster not found, requeueing	{"controller": "stackitmachine", ...}
2026-08-02T11:35:31Z	INFO	StackitCluster not found, requeueing	{"controller": "stackitmachine", ...}
2026-08-02T20:14:49Z	INFO	StackitCluster not found, requeueing	{"controller": "stackitmachine", ...}
2026-08-02T23:38:03Z	INFO	StackitCluster not found, requeueing	{"controller": "stackitmachine", ...}
2026-08-03T07:05:04Z	INFO	StackitCluster not found, requeueing	{"controller": "stackitmachine", ...}
```

**Result:** Starting at `2026-07-31T14:38:23Z` (shortly after the deletion timestamps were set), the log repeats nothing but `StackitCluster not found, requeueing` for over 2 days, without ever logging a deletion attempt for the cloud instance. On every reconcile, the `StackitMachine` controller tries to load the associated `StackitCluster` resource but cannot find it. It only logs this and returns `nil` (no error, no backoff retry). The deletion of the actual cloud instance is **never executed**.

---

### 6. Checking whether the StackitCluster is really missing

```
$ kubectl get stackitcluster -A

No resources found
```

```
$ kubectl get cluster -A

NAMESPACE   NAME               CLUSTERCLASS   AVAILABLE   CP DESIRED   CP AVAILABLE   CP UP-TO-DATE   W DESIRED   W AVAILABLE   W UP-TO-DATE   PHASE      AGE     VERSION
default     stackit-workload                  False       1            0              1               3           0             3              Deleting   2d17h
```

```
$ kubectl get secret -A | grep -i stackit

cluster-api-provider-stackit-system   webhook-server-cert                               kubernetes.io/tls         3      2d17h
default                               stackit-credentials                               Opaque                    2      2d17h
default                               stackit-workload-ca                               cluster.x-k8s.io/secret   2      2d17h
default                               stackit-workload-control-plane-64zwk              cluster.x-k8s.io/secret   2      2d17h
default                               stackit-workload-etcd                             cluster.x-k8s.io/secret   2      2d17h
default                               stackit-workload-kubeconfig                       cluster.x-k8s.io/secret   1      2d17h
default                               stackit-workload-md-0-7bhgs-n4j2t                 cluster.x-k8s.io/secret   2      2d17h
default                               stackit-workload-md-0-7bhgs-q6mbg                 cluster.x-k8s.io/secret   2      2d17h
default                               stackit-workload-md-0-7bhgs-sjn22                 cluster.x-k8s.io/secret   2      2d17h
default                               stackit-workload-proxy                            cluster.x-k8s.io/secret   2      2d17h
default                               stackit-workload-sa                               cluster.x-k8s.io/secret   2      2d17h
```

**Result:**
- `stackitcluster`: **No resources found**. It is completely gone.
- `cluster` (`stackit-workload`): still exists, `PHASE=Deleting`.
- Secrets (`stackit-credentials`, kubeconfig, etc.) still exist. The credentials themselves were not lost, only the `StackitCluster` CR through which the controller normally resolves them.

---

### 7. Code analysis: why does the missing StackitCluster block deletion?

```
internal/controller/stackitmachine_controller.go:92-99
```
```go
stackitCluster, err := r.getStackitCluster(ctx, cluster)
if err != nil {
    return ctrl.Result{}, err
}
if stackitCluster == nil {
    log.Info("StackitCluster not found, requeueing")
    return ctrl.Result{}, nil
}
```
Without a live `StackitCluster` resource, no `MachineScope`/`cloud.Client` (credentials) can be built. The reconcile aborts early, **before** it ever attempts to terminate the VM at STACKIT or remove the finalizer.

---

### 8. Code analysis: why is the StackitCluster already gone while 3 Machines still exist?

```
internal/controller/stackitcluster_controller.go:376-410 (reconcileDelete)
```
```go
func (r *StackitClusterReconciler) reconcileDelete(ctx context.Context, s *scope.ClusterScope) error {
    ...
    controllerutil.RemoveFinalizer(sc, infrav1.ClusterFinalizer)
    return nil
}
```
`reconcileDelete` of the `StackitCluster` does **not** check whether `StackitMachine`/`Machine` objects still exist for the cluster. It only cleans up its own cloud infrastructure (load balancer, bastion) and immediately removes its finalizer, regardless of the state of the worker Machines.

**Note:** The bug exists identically on the branch `refactor/code-cleanup-and-proper-abstraction` (upstream `github.com/stackitcloud/cluster-api-provider-stackit`, commit `ba2a21f`). There, `internal/controller/` was split into several files under `controller/`, but the affected logic was carried over unchanged.

`reconcileDelete` in `controller/stackitcluster_infrastructure.go:188-235`:
```go
func (r *StackitClusterReconciler) reconcileDelete(ctx context.Context, s *scope.ClusterScope) error {
	sc := s.StackitCluster
	if sc.Status.APIServerLoadBalancerID != "" || hasBastionStatus(sc.Status.Bastion) || sc.Spec.APIServerLoadBalancer.Enabled {
		cloudClient, err := util.BuildCloudClient(ctx, r.Client, r.CloudClientFactory, sc)
		if err != nil {
			util.SetConditions(
				&sc.Status.Conditions,
				sc.Generation,
				metav1.ConditionFalse,
				"CredentialsInvalid",
				err.Error(),
				infrav1.ClusterCredentialsReadyCondition,
			)
			return err
		}
		loadBalancerID, err := loadbalancerservice.ResolveID(ctx, cloudClient, sc)
		if err != nil {
			return err
		}
		if loadBalancerID != "" {
			if err := cloudClient.DeleteAPIServerLoadBalancer(ctx, loadBalancerID); err != nil && !cloud.IsNotFound(err) {
				return err
			}
			sc.Status.APIServerLoadBalancerID = ""
			if r.Recorder != nil {
				r.Recorder.Eventf(sc, corev1.EventTypeNormal, "LoadBalancerDeleted", "Deleted API server load balancer %s", loadBalancerID)
			}
		}
		if hasBastionStatus(sc.Status.Bastion) {
			if err := cloudClient.DeleteNodeSSHAccess(ctx, bastionservice.NodeSSHAccessTags(sc)); err != nil && !cloud.IsNotFound(err) {
				return err
			}
			if err := cloudClient.DeleteBastion(ctx, bastionservice.Input(sc, nil), cloud.Bastion{
				ServerID:        sc.Status.Bastion.ServerID,
				PublicIPID:      sc.Status.Bastion.PublicIPID,
				PublicIP:        sc.Status.Bastion.PublicIP,
				SecurityGroupID: sc.Status.Bastion.SecurityGroupID,
			}); err != nil && !cloud.IsNotFound(err) {
				return err
			}
			s.ClearBastionStatus()
			if r.Recorder != nil {
				r.Recorder.Eventf(sc, corev1.EventTypeNormal, "BastionDeleted", "Deleted bastion")
			}
		}
	}
	controllerutil.RemoveFinalizer(sc, infrav1.ClusterFinalizer)
	return nil
}
```
Same here: no check for remaining `Machine`s, the finalizer is removed unconditionally.

Counterpart in `controller/stackitmachine_controller.go:86-92`:
```go
stackitCluster, err := r.getStackitCluster(ctx, cluster)
if err != nil {
	return ctrl.Result{}, err
}
if stackitCluster == nil {
	log.Info("StackitCluster not found, requeueing")
	return ctrl.Result{}, nil
}
```
Identical to the `main` version. If this refactor branch is ever merged, the fix from [03-plan-fix-deletion.md](03-plan-fix-deletion.md) will therefore also need to be applied there (in `controller/stackitcluster_infrastructure.go`).

---

## Conclusion

**Root cause:** `kubectl delete -f cluster.yaml` released all objects in the file (including `Cluster`, `StackitCluster`, `Machine`s) for deletion at the same time, instead of following CAPI's normal deletion order (which the cluster controller would otherwise orchestrate: delete Machines first, then the infrastructure cluster resource). Because `StackitCluster.reconcileDelete` does not wait for pending `StackitMachine`s, the `StackitCluster` was removed almost instantly, before the three worker VMs could be terminated at STACKIT. The `StackitMachine` controller, however, absolutely needs the `StackitCluster` to build credentials/project context for the cloud API call. Since it is missing, the controller hangs in an infinite requeue loop (`StackitCluster not found, requeueing`) without ever actually deleting the VMs. The finalizers stay in place, and the `Machine`/`StackitMachine` objects remain stuck in `Deleting` forever.

**Two separate problems:**
1. **Cleaning up the current cluster:** The 3 VMs presumably still exist at STACKIT but are "orphaned". There is no way left to delete them through the normal reconcile loop, since the credentials source (`StackitCluster`) is gone.
2. **Bug in the provider code:** Missing safeguard in `StackitClusterReconciler.reconcileDelete` that should prevent its own finalizer from being removed while `StackitMachine`s still exist for the cluster (a standard pattern for CAPI infrastructure providers).

**Next step:** see [02-clean-up-machine-report.md](02-clean-up-machine-report.md) for the concrete cleanup.
