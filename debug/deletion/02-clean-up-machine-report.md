# Protocol: cleanup of the stuck `stackit-workload` cluster

> Reference: [01-debug-machine-deletion-report.md](01-debug-machine-deletion-report.md). Root cause: the `StackitCluster` was deleted prematurely, so `Machine`/`StackitMachine` objects are stuck in `Deleting` without the associated VMs ever being terminated at STACKIT.
>
> **Important:** Always clean up the cloud side (STACKIT) first, then remove the Kubernetes finalizers. Otherwise you lose the mapping between VM and Kubernetes object before you have checked whether the VM still exists.

---

## 1. Determine the instance IDs of the stuck workers

```
kubectl get stackitmachine stackit-workload-md-0-7bhgs-n4j2t -o jsonpath='{.status.instanceID}{"\n"}'
kubectl get stackitmachine stackit-workload-md-0-7bhgs-q6mbg  -o jsonpath='{.status.instanceID}{"\n"}'
kubectl get stackitmachine stackit-workload-md-0-7bhgs-sjn22  -o jsonpath='{.status.instanceID}{"\n"}'
```

Already known for `n4j2t`: `cec50749-6eef-4dc7-a64f-178ac8d08bcb`.

**Verification:** Each of the three StackitMachines shows a UUID in the output.

**Result:** Done. Instance IDs determined for all three workers.

---

## 2. Check whether the VMs still exist at STACKIT

In the STACKIT portal (Compute → Server) or via the CLI:

```
stackit compute instance list --project-id $STACKIT_PROJECT_ID
```

The project ID is referenced via the environment variable `STACKIT_PROJECT_ID` (see `.envrc`). If it is not set, it can be found in the `stackit-credentials` secret or in `spec.projectID` of the (no longer existing) `StackitCluster`, alternatively check the original `cluster.yaml` (field `StackitCluster.spec.projectID`).

**Verification:** The list contains (or does not contain) the 3 instance IDs from point 1.

**Result:** Done. The worker instances were found at STACKIT.

---

## 3. Delete the VMs at STACKIT (if still present)

```
stackit compute instance delete <instance-id> --project-id $STACKIT_PROJECT_ID
```
for each of the 3 instance IDs. Alternatively via the portal using "Delete server".

**Verification:** `stackit compute instance list` no longer shows the IDs.

**Result:** Done. All 3 worker VMs deleted at STACKIT.

---

## 4. Check the workers' boot volumes

Since `Root Volume: Delete On Termination: true` was set, the volumes should have been deleted automatically along with the VMs.

```
stackit storage volume list --project-id $STACKIT_PROJECT_ID
```

**Verification:** No orphaned volumes related to the 3 deleted worker instances remain. If any do, delete them individually.

**Result:** Done. No orphaned volumes remain.

---

## 5. Remove the finalizers on the `StackitMachine` objects

Only now, after points 2-4 have confirmed that the cloud resources are gone:

```
kubectl patch stackitmachine stackit-workload-md-0-7bhgs-n4j2t -p '{"metadata":{"finalizers":[]}}' --type=merge
kubectl patch stackitmachine stackit-workload-md-0-7bhgs-q6mbg  -p '{"metadata":{"finalizers":[]}}' --type=merge
kubectl patch stackitmachine stackit-workload-md-0-7bhgs-sjn22  -p '{"metadata":{"finalizers":[]}}' --type=merge
```

**Verification:** `kubectl get stackitmachine -A` no longer shows these three objects.

**Lesson learned:** Manually removing the finalizer on the `Machine` objects was **not** necessary. As soon as the `StackitMachine` is gone, the `Machine` controller notices that the infrastructure ref has disappeared and removes its own finalizer automatically. The `Machine` objects disappear on their own shortly afterward as a result.

**Result:** Done. All 3 worker `StackitMachine`s removed, the associated `Machine` objects disappeared automatically afterward (confirmed via `kubectl get machines -A`: only the control plane Machine remained).

---

## 6. Watch whether the cluster controller now deletes the control plane Machine

```
kubectl get machine,stackitmachine,cluster,kubeadmcontrolplane -A
```

Once the 3 workers are gone, the `Cluster` controller should trigger deletion of the control plane Machine (`stackit-workload-control-plane-64zwk`).

**Verification:** The control plane `Machine` gets a `DeletionTimestamp`.

**Result:** Confirmed. `deletionTimestamp: 2026-08-03T08:11:32Z` was set on `stackit-workload-control-plane-64zwk`, the `Cluster` controller triggered the control plane deletion automatically as expected.

---

## 7. Handle the same bug for the control plane Machine

Confirmed to have occurred: the `Cluster` controller triggered deletion of `stackit-workload-control-plane-64zwk` at `2026-08-03T08:11:32Z`, and the associated `StackitMachine` has since been stuck in the `StackitCluster not found, requeueing` loop as well.

### 7.1 Determine the instance ID

```
kubectl get stackitmachine stackit-workload-control-plane-64zwk -o jsonpath='{.status.instanceID}{"\n"}'
```

Already known: `76803c9b-a9a6-45e0-ad65-70d60c841b1f` (provider ID: `stackit://76803c9b-a9a6-45e0-ad65-70d60c841b1f`, last known `instanceState`: `ACTIVE`).

**Result:** Done. Instance ID of the control plane VM confirmed.

### 7.2 Check whether the VM still exists at STACKIT

```
stackit compute instance describe 76803c9b-a9a6-45e0-ad65-70d60c841b1f --project-id $STACKIT_PROJECT_ID
```

or search the portal (Compute → Server) for this instance ID.

**Verification:** The instance is listed or it is not.

**Result:** Done. The control plane VM was found at STACKIT.

### 7.3 Delete the VM (if still present)

```
stackit compute instance delete 76803c9b-a9a6-45e0-ad65-70d60c841b1f --project-id $STACKIT_PROJECT_ID
```

**Verification:** `stackit compute instance describe 76803c9b-a9a6-45e0-ad65-70d60c841b1f --project-id $STACKIT_PROJECT_ID` returns a not-found error.

**Result:** Done. Control plane VM deleted at STACKIT.

### 7.4 Check the boot volume

```
stackit storage volume list --project-id $STACKIT_PROJECT_ID
```

**Verification:** No volume related to instance `76803c9b-a9a6-45e0-ad65-70d60c841b1f` remains (should have been deleted automatically via `deleteOnTermination: true`).

**Result:** Done. No orphaned volume remains.

### 7.5 Remove the finalizer on the `StackitMachine`

```
kubectl patch stackitmachine stackit-workload-control-plane-64zwk -p '{"metadata":{"finalizers":[]}}' --type=merge
```

**Verification:** `kubectl get stackitmachine -A` no longer shows this object. Manually removing the finalizer on the `Machine` is also **not** necessary here (see the lesson learned in point 5). The `Machine` disappears automatically as soon as its `StackitMachine` is gone.

**Result:** Done. `StackitMachine` and `Machine` of the control plane have both disappeared (confirmed: `kubectl get stackitmachine,machines -A` returns `No resources found`).

---

## 8. Check remaining CAPI objects

```
kubectl get kubeadmcontrolplane,machineset,machinedeployment,kubeadmconfig -A
```

These objects should normally clean themselves up once no Machines remain (they typically have owner references instead of their own cloud finalizers). If any are still stuck, check finalizers analogously and remove them if needed, but only once it has been confirmed that no associated cloud resource remains open.

**Verification:** All objects are gone.

**Result:** `No resources found`. Confirmed: `KubeadmControlPlane`, `MachineSet`, `MachineDeployment`, and `KubeadmConfig` cleaned themselves up as expected, without needing finalizers removed manually.

---

## 9. Check the Cluster object itself

```
kubectl get cluster -A
```

**Verification:** `stackit-workload` is no longer listed. The cleanup is complete.

**Result:** Empty output. The `Cluster stackit-workload` has completely disappeared. The cleanup is now complete.

---

## Don't forget the bug fix

This manual procedure is only a workaround for the already-damaged state. To prevent this from happening again on the next cluster deletion, `StackitClusterReconciler.reconcileDelete` (see [01-debug-machine-deletion-report.md](01-debug-machine-deletion-report.md), section 8) should be adjusted so it only removes its own finalizer once no `StackitMachine`s remain for the cluster.
