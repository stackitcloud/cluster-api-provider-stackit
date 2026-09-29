# Developing Cluster API Provider STACKIT (CAPSTK) with Tilt

This guide describes a local development loop with [kind][kind], [Tilt][tilt],
upstream [Cluster API Tilt][cluster-api-tilt], and this provider. Tilt runs from
a local `cluster-api` checkout and loads `cluster-api-provider-stackit` through
this repository's `tilt-provider.yaml`.

Use this workflow when you want Tilt to rebuild and redeploy the STACKIT
controller while you edit Go code. Creating workload clusters still uses real
STACKIT cloud resources and may incur cost.

[kind]: https://kind.sigs.k8s.io/
[tilt]: https://tilt.dev/
[cluster-api-tilt]: https://cluster-api.sigs.k8s.io/developer/tilt.html

## Prerequisites

Install the development tools listed in the [development guide](index.md). For
this workflow you also need:

- `tilt`
- `kind`
- `kubectl`
- `clusterctl`
- Docker or another container runtime supported by Tilt
- a local checkout of `sigs.k8s.io/cluster-api`
- a STACKIT project, service-account JSON key, network, image, and machine type

The examples below assume both repositories are checked out next to each other:

```sh
cd /path/to/workspace
git clone https://github.com/kubernetes-sigs/cluster-api.git
```

If your `cluster-api` checkout is somewhere else, adjust paths in
`tilt-settings.yaml`.

## Create the management cluster

Run Cluster API's `kind-cluster` target from the upstream `cluster-api`
checkout. This creates a kind cluster with a local registry so Tilt can use
local images without pushing to a remote registry.

```sh
cd /path/to/cluster-api
make kind-cluster
kubectl config current-context
```

The current context should be a kind context, for example `kind-capi-test`.

## Configure Tilt

Create `tilt-settings.yaml` in the upstream `cluster-api` checkout:

```yaml
kind_cluster_name: capi-test
provider_repos:
  - ../cluster-api-provider-stackit
enable_providers:
  - kubeadm-bootstrap
  - kubeadm-control-plane
  - stackit
template_dirs:
  stackit:
    - ../cluster-api-provider-stackit/templates
kustomize_substitutions:
  CLUSTER_TOPOLOGY: "true"
  CLUSTER_RESOURCE_SET: "true"
  EXP_CLUSTER_RESOURCE_SET: "true"
  NAMESPACE: default
  KUBERNETES_VERSION: v1.35.8
  CONTROL_PLANE_MACHINE_COUNT: "1"
  WORKER_MACHINE_COUNT: "1"
  STACKIT_PROJECT_ID: "<project-uuid>"
  STACKIT_REGION: eu01
  STACKIT_NETWORK_ID: "<network-uuid>"
  STACKIT_IMAGE_ID: "<image-uuid>"
  STACKIT_MACHINE_TYPE: c2i.1
  STACKIT_AVAILABILITY_ZONE: eu01-1
  STACKIT_SECURITY_GROUP_ID: "<security-group-uuid>"
  STACKIT_SSH_KEY_NAME: ""
  STACKIT_CREDENTIALS_SECRET_NAME: stackit-credentials
  STACKIT_SERVICE_ACCOUNT_JSON_B64: "<base64-service-account-json>"
  STACKIT_CLOUD_CONTROLLER_MANAGER_IMAGE: "ghcr.io/stackitcloud/cloud-provider-stackit/cloud-controller-manager:v1.35.7"
```

Notes:

- `provider_repos` must point to this repository. Cluster API reads
  `tilt-provider.yaml` from that path.
- `template_dirs` makes the STACKIT cluster templates available as manual Tilt
  actions.
- Keep `STACKIT_CLOUD_CONTROLLER_MANAGER_IMAGE` on the same Kubernetes minor as
  `KUBERNETES_VERSION`.
- Set `preload_images: false` if Docker Desktop or multi-architecture image
  preloading fails.

Create the STACKIT credentials Secret in the management cluster namespace used by
the templates. The `tilt-settings.yaml` values are not exported into your shell,
so export at least these values before running the command manually:

```sh
export STACKIT_PROJECT_ID=<project-uuid>
export STACKIT_SERVICE_ACCOUNT_JSON_FILE=/path/to/serviceaccount.json

kubectl create secret generic stackit-credentials \
  --namespace default \
  --from-literal=project-id="${STACKIT_PROJECT_ID}" \
  --from-file=serviceaccount.json="${STACKIT_SERVICE_ACCOUNT_JSON_FILE}"
```

## Run Tilt

Start Tilt from the upstream `cluster-api` checkout:

```sh
cd /path/to/cluster-api
tilt up
```

Tilt should deploy:

- core Cluster API
- kubeadm bootstrap provider
- kubeadm control plane provider
- STACKIT infrastructure provider

Verify the provider pod:

```sh
kubectl get pods -n cluster-api-provider-stackit-system
kubectl logs -n cluster-api-provider-stackit-system \
  deployment/cluster-api-provider-stackit-controller-manager \
  -c manager
```

## Create a workload cluster

Workload cluster creation is a manual Tilt action from the STACKIT templates. In
the Tilt UI, run the `default` template action for
`templates/cluster-template.yaml`, or render and apply it from the command line:

```sh
cd /path/to/cluster-api-provider-stackit

export CLUSTER_NAME=stackit-dev
export NAMESPACE=default
export KUBERNETES_VERSION=v1.35.8
export CONTROL_PLANE_MACHINE_COUNT=1
export WORKER_MACHINE_COUNT=1
export STACKIT_PROJECT_ID=<project-uuid>
export STACKIT_REGION=eu01
export STACKIT_NETWORK_ID=<network-uuid>
export STACKIT_IMAGE_ID=<image-uuid>
export STACKIT_MACHINE_TYPE=c2i.1
export STACKIT_AVAILABILITY_ZONE=eu01-1
export STACKIT_SECURITY_GROUP_ID=<security-group-uuid>
export STACKIT_SSH_KEY_NAME=""
export STACKIT_CREDENTIALS_SECRET_NAME=stackit-credentials
export STACKIT_SERVICE_ACCOUNT_JSON_B64=<base64-service-account-json>
export STACKIT_CLOUD_CONTROLLER_MANAGER_IMAGE=ghcr.io/stackitcloud/cloud-provider-stackit/cloud-controller-manager:v1.35.7

clusterctl generate cluster "${CLUSTER_NAME:-stackit-dev}" \
  --from templates/cluster-template.yaml \
  --target-namespace default \
  | kubectl apply -f -
```

Watch Cluster API and STACKIT resources:

```sh
kubectl get cluster,machine,stackitcluster,stackitmachine -n default
```

When the control plane is ready, install a workload CNI:

```sh
clusterctl get kubeconfig "${CLUSTER_NAME:-stackit-dev}" -n default > /tmp/stackit-dev.kubeconfig
make install-workload-cni WORKLOAD_KUBECONFIG=/tmp/stackit-dev.kubeconfig
```

## Debug the STACKIT controller

Add a `debug` section for the `stackit` provider in `tilt-settings.yaml`:

```yaml
debug:
  stackit:
    continue: true
    port: 30000
```

Restart Tilt and attach a Go debugger to `127.0.0.1:30000`.

## Clean up

Delete workload clusters before deleting the management cluster:

```sh
kubectl delete cluster "${CLUSTER_NAME:-stackit-dev}" -n default
kind delete cluster --name capi-test
```

Use the STACKIT cleanup helper only for resources labelled by the e2e tests:

```sh
make cleanup-stackit STACKIT_E2E_TEST_ID=<test-id>
```

## Troubleshooting

- `Failed to load provider. No tilt-provider.{yaml|json} file found`: verify
  `provider_repos` points to this repository, not to `cluster-api`.
- `default_registry is required`: run `make kind-cluster` first so the kind
  cluster has the local-registry ConfigMap, or configure a writable
  `default_registry`.
- `context ... is not allowed`: switch to the kind management-cluster context or
  add the current context to `allowed_contexts`.
- `StackitCluster` cannot read credentials: ensure the
  `stackit-credentials` Secret exists in the same namespace as the Cluster and
  contains `project-id` plus `serviceaccount.json`.
- Workload Nodes stay unready: verify the selected image matches the Kubernetes
  version, `STACKIT_CLOUD_CONTROLLER_MANAGER_IMAGE` matches the Kubernetes
  minor, and a CNI was installed into the workload cluster.
