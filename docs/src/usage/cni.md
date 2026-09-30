# Workload CNI

The default cluster template provisions STACKIT infrastructure and installs
`cloud-provider-stackit`, but it intentionally does not install a CNI. This
keeps CNI choice with the cluster operator, where network policy, routing, MTU,
IPAM, and upgrade strategy belong.

Nodes will not become fully Ready until a CNI is installed. The templates ship
MachineHealthChecks that replace Machines whose Node stays `Ready=False` for 10
minutes, so install the CNI within that window after the Nodes join. For local
validation and simple development clusters, this repository provides a
repeatable helper for Cilium or Calico.

For the complete tested workload addon flow, including the embedded
`cloud-provider-stackit` `ClusterResourceSet` and verification commands, see
[Workload Addons](addons.md).

First, retrieve the workload-cluster kubeconfig:

```sh
clusterctl get kubeconfig "${CLUSTER_NAME}" \
  --namespace "${NAMESPACE}" \
  > "${CLUSTER_NAME}.kubeconfig"
```

Install Cilium using the checked-in values:

```sh
make install-workload-cni \
  WORKLOAD_KUBECONFIG="${CLUSTER_NAME}.kubeconfig"
```

The Cilium values live in `templates/addons/cilium-values.yaml` and match the
default pod CIDR from `templates/cluster-template.yaml`.

To install Calico instead:

```sh
make install-workload-cni \
  WORKLOAD_KUBECONFIG="${CLUSTER_NAME}.kubeconfig" \
  STACKIT_WORKLOAD_CNI=calico
```

To apply a fully custom CNI manifest:

```sh
make install-workload-cni \
  WORKLOAD_KUBECONFIG="${CLUSTER_NAME}.kubeconfig" \
  CNI_MANIFEST=./my-cni.yaml
```

Custom manifests are applied as-is. The helper cannot infer which resources
prove that an arbitrary CNI is healthy, so custom CNI users must wait for their
own rollout resources.

For production clusters, prefer managing the CNI with Helm, GitOps, or a
Cluster API addon provider. `ClusterResourceSet` is useful for simple static
resources, but it is not a full addon lifecycle manager for upgrades, rollback,
or drift reconciliation.
