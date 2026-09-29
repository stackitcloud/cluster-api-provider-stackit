# ClusterClass

ClusterClass is Cluster API's reusable cluster blueprint mechanism. Instead of
rendering every infrastructure, control plane, bootstrap, and worker resource
directly for each workload cluster, you define a `ClusterClass` once and create
workload clusters that reference it through `Cluster.spec.topology`.

For this provider, ClusterClass support is provided by:

- `templates/clusterclass.yaml`
- `templates/cluster-template-topology.yaml`

`templates/clusterclass.yaml` contains the reusable class and the STACKIT
infrastructure templates. `templates/cluster-template-topology.yaml` contains a
small topology `Cluster` that selects the class and passes values as topology
variables.

## Why use ClusterClass

Use ClusterClass when you want CAPI to own the generated cluster topology. CAPI
creates and reconciles the underlying `StackitCluster`, `KubeadmControlPlane`,
`MachineDeployment`, bootstrap templates, and `StackitMachine` infrastructure
objects from the class.

This gives you:

- one reusable cluster shape for multiple workload clusters
- smaller workload cluster manifests
- Kubernetes version, replica count, and infrastructure settings expressed as
  topology variables
- standard CAPI topology behavior for scaling and upgrades
- generated infrastructure resources with owner references, labels, and
  annotations managed by CAPI core

You do not need ClusterClass for a simple explicit cluster manifest. The classic
template in [Classic Cluster Template](cluster-template.md) is still valid.
ClusterClass is the preferred path when you want repeatable topology management
and fleet-style cluster creation.

## What the STACKIT ClusterClass configures

The current ClusterClass wires topology variables into STACKIT templates for:

- Kubernetes version
- control-plane replica count
- worker replica count
- machine type
- image ID
- region
- network ID
- credentials Secret name
- additional STACKIT cloud labels
- development fallback `preKubeadmCommands`

The ClusterClass uses these provider templates:

- `StackitClusterTemplate` for the generated `StackitCluster`
- one `StackitMachineTemplate` for control-plane machines
- one `StackitMachineTemplate` for worker machines

The templates also include `spec.template.metadata.labels` and
`spec.template.metadata.annotations`. CAPI topology copies this metadata onto
the generated `StackitCluster` and `StackitMachine` objects. This is useful for
policy, ownership, automation, and observability labels that should be present
on generated infrastructure objects.

## Prerequisites

The management cluster must run CAPI core and kubeadm-control-plane with
`ClusterTopology=true`. Otherwise the admission webhooks reject `ClusterClass`,
`KubeadmControlPlaneTemplate`, and `Cluster.spec.topology`.

For local clusterctl usage, `hack/clusterctl-local.yaml` sets
`CLUSTER_TOPOLOGY: "true"`.

The topology cluster template also includes a `cloud-provider-stackit`
`ClusterResourceSet` addon and a workload-cluster Secret for the cloud
controller manager. The management cluster must have the ClusterResourceSet
feature enabled as well. For local validation, `hack/clusterctl-local.yaml` sets
`CLUSTER_RESOURCE_SET: "true"`.

Before rendering, prepare the same STACKIT variables used by the classic
template:

```sh
export CLUSTER_NAME=stackit-workload
export NAMESPACE=default
export KUBERNETES_VERSION=v1.35.8
export KUBERNETES_APT_REPOSITORY_MINOR=v1.35
export CONTROL_PLANE_MACHINE_COUNT=1
export WORKER_MACHINE_COUNT=1

export STACKIT_PROJECT_ID=<project-uuid>
export STACKIT_REGION=eu01
export STACKIT_NETWORK_ID=<network-uuid>
export STACKIT_IMAGE_ID=<image-uuid>
export STACKIT_MACHINE_TYPE=c2i.2
export STACKIT_CREDENTIALS_SECRET_NAME=stackit-credentials

export STACKIT_SERVICE_ACCOUNT_JSON_FILE=./.stackit/cluster-api-provider-stackit-serviceaccount.json
export STACKIT_SERVICE_ACCOUNT_JSON_B64="$(base64 < "${STACKIT_SERVICE_ACCOUNT_JSON_FILE}" | tr -d '\n')"
export STACKIT_CLOUD_CONTROLLER_MANAGER_IMAGE=ghcr.io/stackitcloud/cloud-provider-stackit/cloud-controller-manager:v1.35.7
```

`STACKIT_CLOUD_CONTROLLER_MANAGER_IMAGE` must use a minor version matching
`KUBERNETES_VERSION`.

## Apply the ClusterClass

Apply the reusable class and its templates into the namespace that should own
the class:

```sh
kubectl apply -n "${NAMESPACE}" -f templates/clusterclass.yaml
```

If the class lives in a different namespace than the workload cluster, set:

```sh
export CLUSTER_CLASS_NAMESPACE=<clusterclass-namespace>
```

## Create a topology cluster

Render and apply a topology `Cluster`:

```sh
clusterctl generate cluster "${CLUSTER_NAME}" \
  --from templates/cluster-template-topology.yaml \
  --target-namespace "${NAMESPACE}" \
  --kubernetes-version "${KUBERNETES_VERSION}" \
  --control-plane-machine-count "${CONTROL_PLANE_MACHINE_COUNT}" \
  --worker-machine-count "${WORKER_MACHINE_COUNT}" \
  > "${CLUSTER_NAME}-topology.yaml"

kubectl apply -f "${CLUSTER_NAME}-topology.yaml"
```

CAPI topology then creates the generated infrastructure and control plane
objects. Inspect them with:

```sh
kubectl get cluster,machine,stackitcluster,stackitmachine -n "${NAMESPACE}"
```

Check propagated template metadata:

```sh
kubectl get stackitcluster -n "${NAMESPACE}" \
  -l "cluster.x-k8s.io/cluster-name=${CLUSTER_NAME}" \
  -o go-template='{{range .items}}{{index .metadata.labels "cluster-api-provider-stackit/template-metadata"}}{{"\n"}}{{end}}'

kubectl get stackitmachine -n "${NAMESPACE}" \
  -l "cluster.x-k8s.io/cluster-name=${CLUSTER_NAME}" \
  -o go-template='{{range .items}}{{.metadata.name}} {{index .metadata.labels "cluster-api-provider-stackit/template-metadata"}}{{"\n"}}{{end}}'
```

## Workload cluster addons

The topology cluster template installs the cloud controller manager through a
`ClusterResourceSet`. It does not install a CNI.

After the workload API is reachable, install a CNI that matches the configured
pod/service CIDRs and network policy expectations before expecting Nodes to
become Ready. For a reproducible development path, use the helper documented in
[Workload CNI](cni.md).

## Delete a topology cluster

Delete the topology `Cluster`; CAPI deletes the generated objects and this
provider deletes the STACKIT resources:

```sh
kubectl delete cluster "${CLUSTER_NAME}" -n "${NAMESPACE}"
```

After deletion, verify generated resources are gone:

```sh
kubectl get machine,stackitcluster,stackitmachine \
  -n "${NAMESPACE}" \
  -l "cluster.x-k8s.io/cluster-name=${CLUSTER_NAME}"
```

## E2E validation

Run the topology workload e2e path independently only when billable STACKIT
validation is intended:

```sh
env STACKIT_E2E_TOPOLOGY_WORKLOAD=true \
  KUBERNETES_VERSION=v1.35.8 \
  STACKIT_E2E_CNI=cilium \
  go test -timeout=90m -tags=e2e ./test/e2e -v -ginkgo.v \
  --ginkgo.focus='topology.*workload' --ginkgo.timeout=90m
```

Equivalent make target:

```sh
make test-e2e-workload-topology
```

The topology e2e creates a real STACKIT workload cluster, waits for Ready
Machines and Nodes, verifies providerID alignment, verifies template metadata
propagation, deletes the Cluster, and checks that tagged STACKIT servers and API
server load balancers are cleaned up.
