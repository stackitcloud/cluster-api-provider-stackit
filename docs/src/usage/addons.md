# Workload Addons

This provider supports a tested addon flow for unmanaged STACKIT workload
clusters:

- `cloud-provider-stackit` (the Cloud Controller Manager or CCM) runs in the workload
  cluster to manage node lifecycles, provider IDs, and STACKIT Service Load Balancers.
- A CNI (such as Cilium or Calico) is installed after the workload API server is reachable.
- The default tested CNI path uses Cilium with preconfigured values in
  `templates/addons/cilium-values.yaml`.

This split is intentional:
1. The cloud provider must run early so Kubernetes Nodes get STACKIT provider IDs
   and the external cloud-provider taint (`node.cloudprovider.kubernetes.io/uninitialized`)
   is cleared.
2. The CNI is a cluster operator choice because routing, network policy, MTU,
   IPAM, and upgrade strategy are environment-specific.

## Cloud Controller Manager (`cloud-provider-stackit`)

The STACKIT Cloud Controller Manager runs inside the workload cluster in the `kube-system` namespace.
Its manifest is defined in `templates/addons/cloud-provider-stackit.yaml` and sets up:

- ServiceAccount, RBAC roles, and ClusterRoleBindings for `stackit-cloud-controller-manager`
- `stackit-cloud-config` ConfigMap containing `cloud.yaml` with the STACKIT project ID, region, and load balancer network ID
- `stackit-cloud-secret` Secret containing the service account JSON key (`sa_key.json`)
- Deployment running `stackit-cloud-controller-manager` with controllers:
  - `cloud-node-controller`: initializes Node objects and sets `spec.providerID` to `stackit://<server-id>`
  - `cloud-node-lifecycle-controller`: cleans up Node objects when servers are deleted in STACKIT
  - `service-lb-controller`: provisions and configures STACKIT Network Load Balancers for Kubernetes `Service` objects with `type: LoadBalancer`

### Configuration Variables

The manifest `templates/addons/cloud-provider-stackit.yaml` expects these environment variables:

| Variable | Description |
| --- | --- |
| `STACKIT_PROJECT_ID` | Target STACKIT project UUID |
| `STACKIT_REGION` | Target STACKIT region (e.g. `eu01`) |
| `STACKIT_NETWORK_ID` | STACKIT network UUID for Service load balancers |
| `STACKIT_SERVICE_ACCOUNT_JSON_B64` | Base64-encoded STACKIT service account JSON key |
| `STACKIT_CLOUD_CONTROLLER_MANAGER_IMAGE` | CCM image matching the workload Kubernetes minor version (e.g. `ghcr.io/stackitcloud/cloud-provider-stackit/cloud-controller-manager:v1.35.7`) |
| `CLUSTER_NAME` | Name of the workload cluster |

Set your credentials and CCM image:

```sh
export STACKIT_SERVICE_ACCOUNT_JSON_FILE=./.stackit/cluster-api-provider-stackit-serviceaccount.json
export STACKIT_SERVICE_ACCOUNT_JSON_B64="$(base64 < "${STACKIT_SERVICE_ACCOUNT_JSON_FILE}" | tr -d '\n')"
export STACKIT_CLOUD_CONTROLLER_MANAGER_IMAGE="ghcr.io/stackitcloud/cloud-provider-stackit/cloud-controller-manager:v1.35.7"
```

> **Important:** The `cloud-provider-stackit` image minor version must match the Kubernetes minor version of the workload cluster (e.g. v1.35.x CCM for v1.35.x Kubernetes). Use `hack/validate-stackit-versions.sh` before deployment to verify compatibility.

---

## Deploying CCM with BYO Networks

When using `templates/cluster-template.yaml` or `templates/cluster-template-topology.yaml`, you provide `STACKIT_NETWORK_ID` up front. The cluster template generates:

1. A Cluster API `ClusterResourceSet` named `${CLUSTER_NAME}-cloud-provider-stackit`
2. A companion `Secret` containing the pre-rendered `cloud-provider-stackit.yaml`

As soon as the workload cluster API server is reachable, Cluster API automatically applies the CCM manifest into the workload cluster. No manual intervention is needed.

---

## Managed Networks and `cloud-provider-stackit`

When deploying a cluster with a managed network using `templates/cluster-template-managed-network.yaml`, `spec.network.id` is omitted. CAPSTK creates a dedicated `/24` network in STACKIT during cluster reconciliation.

Because the network ID does not exist when `clusterctl generate` is executed, the template creates the `ClusterResourceSet` but **does not include the Secret**. The STACKIT CCM requires this network ID in `stackit-cloud-config` to manage Service load balancers.

### Step 1: Retrieve the Provisioned Network ID

Wait for CAPSTK to create the network and populate `StackitCluster.status.network.id`:

```sh
export STACKIT_NETWORK_ID=$(kubectl get stackitcluster "${CLUSTER_NAME}" \
  --namespace "${NAMESPACE}" \
  -o jsonpath='{.status.network.id}')

echo "Managed Network ID: ${STACKIT_NETWORK_ID}"
```

### Step 2: Deploy CCM

You can provide the CCM manifest using either of two methods:

#### Method A: Via ClusterResourceSet Secret (Recommended)

Since `cluster-template-managed-network.yaml` already defined a `ClusterResourceSet` looking for a Secret named `${CLUSTER_NAME}-cloud-provider-stackit`, create that Secret in the management cluster. The ClusterResourceSet controller will automatically push it to the workload cluster:

```sh
# Render the addon manifest using envsubst and create the CRS Secret
kubectl create secret generic "${CLUSTER_NAME}-cloud-provider-stackit" \
  --namespace "${NAMESPACE}" \
  --type=addons.cluster.x-k8s.io/resource-set \
  --from-literal=cloud-provider-stackit.yaml="$(envsubst < templates/addons/cloud-provider-stackit.yaml)"
```

To verify the ClusterResourceSet applied the resources:

```sh
kubectl get clusterresourcesetbinding -n "${NAMESPACE}"
```

#### Method B: Direct Apply to the Workload Cluster

Alternatively, retrieve the workload cluster kubeconfig and apply the rendered addon directly:

```sh
clusterctl get kubeconfig "${CLUSTER_NAME}" \
  --namespace "${NAMESPACE}" \
  > "${CLUSTER_NAME}.kubeconfig"

envsubst < templates/addons/cloud-provider-stackit.yaml \
  | kubectl --kubeconfig "${CLUSTER_NAME}.kubeconfig" apply -f -
```

---

## CNI Installation

After CCM is deployed and the workload API server is reachable, retrieve the kubeconfig if you have not already:

```sh
clusterctl get kubeconfig "${CLUSTER_NAME}" \
  --namespace "${NAMESPACE}" \
  > "${CLUSTER_NAME}.kubeconfig"
```

Nodes remain in `NotReady` status until a CNI is installed.

### Installing Cilium (Default Tested Path)

Install Cilium using the checked-in values matching the cluster template's pod CIDR:

```sh
make install-workload-cni \
  WORKLOAD_KUBECONFIG="${CLUSTER_NAME}.kubeconfig"
```

Alternatively, install with the Cilium CLI or Helm using `templates/addons/cilium-values.yaml`:

```sh
cilium install \
  --kubeconfig "${CLUSTER_NAME}.kubeconfig" \
  --values templates/addons/cilium-values.yaml
```

### Installing Calico

To install Calico instead:

```sh
make install-workload-cni \
  WORKLOAD_KUBECONFIG="${CLUSTER_NAME}.kubeconfig" \
  STACKIT_WORKLOAD_CNI=calico
```

### Installing a Custom CNI

To apply an arbitrary CNI manifest:

```sh
make install-workload-cni \
  WORKLOAD_KUBECONFIG="${CLUSTER_NAME}.kubeconfig" \
  CNI_MANIFEST=./my-cni.yaml
```

---

## Verification

Verify that the cloud provider Deployment rolled out:

```sh
clusterctl get kubeconfig "${CLUSTER_NAME}" \
  --namespace "${NAMESPACE}" \
  > "${CLUSTER_NAME}.kubeconfig"
```

Install the default tested Cilium CNI:

```sh
make install-workload-cni \
  WORKLOAD_KUBECONFIG="${CLUSTER_NAME}.kubeconfig"
```

The helper waits for the Cilium operator, Cilium DaemonSet, and Cilium Envoy
DaemonSet when present.

To install Calico instead:

```sh
make install-workload-cni \
  WORKLOAD_KUBECONFIG="${CLUSTER_NAME}.kubeconfig" \
  STACKIT_WORKLOAD_CNI=calico
```

To apply a custom CNI manifest:

```sh
make install-workload-cni \
  WORKLOAD_KUBECONFIG="${CLUSTER_NAME}.kubeconfig" \
  CNI_MANIFEST=./my-cni.yaml
```

Custom manifests are applied as-is. The helper cannot know which workload
resources prove that an arbitrary CNI is healthy, so custom CNI users must wait
for their own rollout resources.

## Verification

Verify that the cloud provider Deployment rolled out:

```sh
kubectl --kubeconfig "${CLUSTER_NAME}.kubeconfig" \
  -n kube-system rollout status deployment/stackit-cloud-controller-manager \
  --timeout=5m
```

Verify Cilium rollout for the default path:

```sh
kubectl --kubeconfig "${CLUSTER_NAME}.kubeconfig" \
  -n kube-system rollout status deployment/cilium-operator --timeout=10m
kubectl --kubeconfig "${CLUSTER_NAME}.kubeconfig" \
  -n kube-system rollout status daemonset/cilium --timeout=10m
kubectl --kubeconfig "${CLUSTER_NAME}.kubeconfig" \
  -n kube-system rollout status daemonset/cilium-envoy --timeout=10m
```

Verify Nodes are Ready and have STACKIT provider IDs:

```sh
kubectl --kubeconfig "${CLUSTER_NAME}.kubeconfig" get nodes
kubectl --kubeconfig "${CLUSTER_NAME}.kubeconfig" \
  get nodes \
  -o custom-columns=NAME:.metadata.name,PROVIDER_ID:.spec.providerID
```

Verify the external cloud-provider taint is gone:

```sh
kubectl --kubeconfig "${CLUSTER_NAME}.kubeconfig" \
  get nodes -o json \
  | jq -e '[.items[].spec.taints[]? | select(.key == "node.cloudprovider.kubernetes.io/uninitialized")] | length == 0'
```

Verify CAPI Machines and workload Nodes agree on STACKIT provider IDs:

```sh
kubectl -n "${NAMESPACE}" \
  get machines,stackitmachines \
  -l "cluster.x-k8s.io/cluster-name=${CLUSTER_NAME}" \
  -o custom-columns=KIND:.kind,NAME:.metadata.name,PROVIDER_ID:.spec.providerID,NODE:.status.nodeRef.name
kubectl --kubeconfig "${CLUSTER_NAME}.kubeconfig" \
  get nodes \
  -o custom-columns=NAME:.metadata.name,PROVIDER_ID:.spec.providerID
```

The e2e workload tests validate this flow by waiting for the embedded
`cloud-provider-stackit` rollout, installing CNI, waiting for Ready Nodes,
checking the external cloud-provider taint is removed, and verifying CAPI
Machine, `StackitMachine`, and Node provider IDs align.

## Production Addon Management

`ClusterResourceSet` is useful for static bootstrap addons such as this tested
cloud-provider installation, but it is not a full addon lifecycle manager. For
production CNI management, prefer Helm, GitOps, or a Cluster API addon provider
that handles upgrades, rollback, and drift reconciliation.
