# Cluster Templates

CAPSTK provides clusterctl templates under `templates/` for various workload cluster configurations:

- `templates/cluster-template.yaml`: Default template using a pre-existing (Bring-Your-Own / BYO) STACKIT network.
- `templates/cluster-template-managed-network.yaml`: Managed network template where CAPSTK automatically creates and manages a dedicated `/24` STACKIT network.
- `templates/cluster-template-bastion.yaml`: Enables an optional provider-managed SSH bastion host.
- `templates/cluster-template-flatcar-workers.yaml`: Mixed OS cluster with Flatcar Linux worker nodes.
- `templates/cluster-template-topology.yaml`: ClusterClass topology-based cluster.

## Resource Manifest Breakdown

The classic templates (`cluster-template.yaml` and `cluster-template-managed-network.yaml`) render:

- `Cluster`
- `StackitCluster`
- `KubeadmControlPlane`
- control-plane `StackitMachineTemplate`
- `MachineDeployment`
- worker `StackitMachineTemplate`
- worker `KubeadmConfigTemplate`
- `MachineHealthCheck` for control-plane and worker Machines
- `ClusterResourceSet` for `cloud-provider-stackit`

## Deploying with a Bring-Your-Own (BYO) Network

In the BYO network flow, `STACKIT_NETWORK_ID` is known upfront. The template embeds both the `ClusterResourceSet` and the resource-set `Secret` with the pre-rendered `cloud-provider-stackit.yaml`.

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

## Deploying with a Managed Network

When using `templates/cluster-template-managed-network.yaml`, `STACKIT_NETWORK_ID` is omitted. CAPSTK automatically provisions a dedicated `/24` network during `StackitCluster` reconciliation and publishes its ID to `StackitCluster.status.network.id`.

Render and apply the cluster:

```sh
clusterctl generate cluster "${CLUSTER_NAME}" \
  --from templates/cluster-template-managed-network.yaml \
  --kubernetes-version "${KUBERNETES_VERSION}" \
  --control-plane-machine-count "${CONTROL_PLANE_MACHINE_COUNT}" \
  --worker-machine-count "${WORKER_MACHINE_COUNT}" \
  | kubectl apply -f -
```

Because the network ID is generated at runtime, the template creates the `ClusterResourceSet` but omits the Secret. Once CAPSTK provisions the network, you retrieve the network ID and supply the `cloud-provider-stackit` Secret or apply the addon directly. See [Workload Addons](addons.md#managed-networks-and-cloud-provider-stackit) for the full procedure.

## External Cloud Provider Wiring

The templates configure kubeadm for an external cloud provider by setting
`cloud-provider=external` on kubelet and controller-manager.

The management cluster must have the ClusterResourceSet feature enabled before applying templates that use it. For local validation, `hack/clusterctl-local.yaml` sets `CLUSTER_RESOURCE_SET=true`.

Set `STACKIT_SERVICE_ACCOUNT_JSON_B64` to a single-line base64 encoding of the
STACKIT service account JSON. Set `STACKIT_CLOUD_CONTROLLER_MANAGER_IMAGE` to a
`cloud-provider-stackit` image whose minor version matches
`KUBERNETES_VERSION`; supported workload cluster minors are v1.33.x through
v1.36.x. Use `hack/validate-stackit-versions.sh` before rendering to catch
unsupported or mismatched versions.

The templates install or prepare the cloud controller manager, not a CNI. After the
workload API is reachable, install a CNI that matches the configured pod/service
CIDRs and network policy expectations before expecting Nodes to become Ready.
See [Workload CNI](cni.md) and [Workload Addons](addons.md).

## Machine Health Checks and Remediation

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
