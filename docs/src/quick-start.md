# Quick start

This guide creates a Kind management cluster and installs the published STACKIT
infrastructure provider. You do not need to clone this repository or build an
image.

## Prerequisites

Install `kind`, `kubectl`, `clusterctl`, `curl`, and `base64`. You also need a
STACKIT project, a service-account JSON key, an existing network, image, and
machine type. See [IAM permissions](./topics/iam-permissions.md) for the
required service-account permissions.

Set the release version to install:

```sh
export CAPSTK_VERSION=v0.1.0-alpha.1
```

Create `clusterctl.yaml` for that release:

```sh
cat > clusterctl.yaml <<EOF
providers:
  - name: stackit
    url: https://github.com/stackitcloud/cluster-api-provider-stackit/releases/download/${CAPSTK_VERSION}/infrastructure-components.yaml
    type: InfrastructureProvider
EOF
```

## Create a management cluster

```sh
kind create cluster --name capi-stackit
kubectl config use-context kind-capi-stackit

clusterctl init \
  --config clusterctl.yaml \
  --infrastructure stackit:"${CAPSTK_VERSION}"
```

Wait for the provider controller:

```sh
kubectl rollout status \
  --namespace cluster-api-provider-stackit-system \
  deployment/cluster-api-provider-stackit-controller-manager
```

## Set credentials and cluster settings

Save the service-account JSON key anywhere on your machine, then set its path
and the IDs for the STACKIT resources the workload cluster will use:

```sh
export STACKIT_PROJECT_ID=<project-uuid>
export STACKIT_REGION=eu01
export STACKIT_NETWORK_ID=<network-uuid>
export STACKIT_IMAGE_ID=<image-uuid>
export STACKIT_MACHINE_TYPE=c2i.4
export STACKIT_SERVICE_ACCOUNT_JSON_FILE=/path/to/service-account.json
export STACKIT_SERVICE_ACCOUNT_JSON_B64="$(base64 < "${STACKIT_SERVICE_ACCOUNT_JSON_FILE}" | tr -d '\n')"

kubectl create secret generic stackit-credentials \
  --namespace default \
  --from-literal=project-id="${STACKIT_PROJECT_ID}" \
  --from-file=serviceaccount.json="${STACKIT_SERVICE_ACCOUNT_JSON_FILE}"
```

## Create a workload cluster

Download the template that matches the provider release:

```sh
curl --fail --location --remote-name \
  "https://github.com/stackitcloud/cluster-api-provider-stackit/releases/download/${CAPSTK_VERSION}/cluster-template.yaml"
```

Set the cluster values and render the template:

```sh
export CLUSTER_NAME=stackit-workload
export NAMESPACE=default
export KUBERNETES_VERSION=v1.35.3
export KUBERNETES_APT_REPOSITORY_MINOR=v1.35
export CONTROL_PLANE_MACHINE_COUNT=1
export WORKER_MACHINE_COUNT=1
export STACKIT_CREDENTIALS_SECRET_NAME=stackit-credentials
export STACKIT_CLOUD_CONTROLLER_MANAGER_IMAGE=ghcr.io/stackitcloud/cloud-provider-stackit/cloud-controller-manager:v1.35.3

clusterctl generate cluster "${CLUSTER_NAME}" \
  --from cluster-template.yaml \
  --target-namespace "${NAMESPACE}" \
  > cluster.yaml
kubectl apply -f cluster.yaml
```

Watch the provider create the cluster:

```sh
kubectl get cluster,machine,stackitcluster,stackitmachine --namespace "${NAMESPACE}"
```

When the workload API is available, install a CNI, then retrieve the workload
kubeconfig:

```sh
clusterctl get kubeconfig "${CLUSTER_NAME}" \
  --namespace "${NAMESPACE}" \
  > "${CLUSTER_NAME}".kubeconfig
```

Use the [development guide](development/index.md) when you want to build and
run the provider from a local checkout.
