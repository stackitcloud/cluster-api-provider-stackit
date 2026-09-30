# Cluster Template

`templates/cluster-template.yaml` is the default clusterctl template for a
non-topology workload cluster. It renders:

- `Cluster`
- `StackitCluster`
- `KubeadmControlPlane`
- control-plane `StackitMachineTemplate`
- `MachineDeployment`
- worker `StackitMachineTemplate`
- worker `KubeadmConfigTemplate`
- `MachineHealthCheck` for control-plane and worker Machines
- `ClusterResourceSet` and resource-set `Secret` for `cloud-provider-stackit`

Render a cluster:

```sh
clusterctl generate cluster "${CLUSTER_NAME}" \
  --from templates/cluster-template.yaml \
  --kubernetes-version "${KUBERNETES_VERSION}" \
  --control-plane-machine-count "${CONTROL_PLANE_MACHINE_COUNT}" \
  --worker-machine-count "${WORKER_MACHINE_COUNT}" \
  > "${CLUSTER_NAME}.yaml"
```

Apply the rendered manifest:

```sh
kubectl apply -f "${CLUSTER_NAME}.yaml"
```

The template configures kubeadm for an external cloud provider by setting
`cloud-provider=external` on kubelet and controller-manager.

The template also installs `cloud-provider-stackit` into the workload cluster
through Cluster API `ClusterResourceSet`. The management cluster must have the
ClusterResourceSet feature enabled before applying the generated cluster. For
local validation, `hack/clusterctl-local.yaml` sets `CLUSTER_RESOURCE_SET=true`.
The topology ClusterClass template follows the same pattern and also includes
the cloud-provider addon wiring.

Set `STACKIT_SERVICE_ACCOUNT_JSON_B64` to a single-line base64 encoding of the
STACKIT service account JSON. Set `STACKIT_CLOUD_CONTROLLER_MANAGER_IMAGE` to a
`cloud-provider-stackit` image whose minor version matches
`KUBERNETES_VERSION`; supported workload cluster minors are v1.33.x through
v1.36.x. Use `hack/validate-stackit-versions.sh` before rendering to catch
unsupported or mismatched versions.

The template installs the cloud controller manager, not a CNI. After the
workload API is reachable, install a CNI that matches the configured pod/service
CIDRs and network policy expectations before expecting Nodes to become Ready.
For a reproducible development path, use the helper documented in
[Workload CNI](cni.md).

The MachineHealthChecks remediate Machines whose Node does not register within
10 minutes or reports `Ready=False` or `Ready=Unknown` for 10 minutes. The
`MachineDeployment` limits worker remediation to 2 Machines at a time via
`spec.remediation.maxInFlight`.

Worker remediation only runs while at most 40% of the workers are unhealthy
(`triggerIf.unhealthyLessThanOrEqualTo`, rounded down). A MachineDeployment
with fewer than 3 workers is therefore never remediated; use
`WORKER_MACHINE_COUNT` ≥ 3 for automatic replacement. A network outage longer
than 10 minutes can still replace healthy workers once the first of them
recovers and the unhealthy share drops below the limit.

Control-plane and worker Machines use `nodeDrainTimeoutSeconds: 900`, so a
Node whose Pods cannot terminate does not block deletion forever. This applies
to every Machine deletion, including upgrades and scale-down: Pods still
protected by a PodDisruptionBudget after 15 minutes are not waited for.
