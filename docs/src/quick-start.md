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
export CAPSTK_VERSION=v0.1.0-alpha.2
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

### Enterprise proxies like Zscaler

If your network uses a TLS-intercepting proxy such as Zscaler, the Kind node
must trust the proxy's root certificate. The host's certificate store does not
automatically apply inside the Kind node. Create a local `kind-config.yaml`
that mounts the host CA bundle into the node:

```sh
cat > kind-config.yaml <<'EOF'
kind: Cluster
apiVersion: kind.x-k8s.io/v1alpha4
nodes:
  - role: control-plane
    extraMounts:
      - hostPath: /etc/ssl/certs/ca-certificates.crt
        containerPath: /etc/ssl/certs/ca-certificates.crt
        readOnly: true
EOF
kind create cluster --name capi-stackit --config kind-config.yaml
```

Without such a proxy, create the cluster without the configuration file:

```sh
kind create cluster --name capi-stackit
```

Select the management-cluster context and install the provider:

```sh
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

You can retrieve a valid image ID using the STACKIT CLI:

```sh
stackit image list --project-id "${STACKIT_PROJECT_ID}" --output-format json --label-selector linux,prod | jq -r '.[] | select(.name == "Ubuntu 24.04") | .id'
```

## Create a workload cluster

You can use the cluster template directly from the repository's `templates`
directory (`templates/cluster-template.yaml`) if you cloned the repository, or
download the template that matches the provider release:

```sh
curl --fail --location --remote-name \
  "https://github.com/stackitcloud/cluster-api-provider-stackit/releases/download/${CAPSTK_VERSION}/cluster-template.yaml"
```

Set the cluster values and render the template (use
`--from templates/cluster-template.yaml` if working from a repository clone):

```sh
export CLUSTER_NAME=stackit-workload
export NAMESPACE=default
export KUBERNETES_VERSION=v1.35.8
export KUBERNETES_APT_REPOSITORY_MINOR=v1.35
export CONTROL_PLANE_MACHINE_COUNT=1
export WORKER_MACHINE_COUNT=1
export STACKIT_CREDENTIALS_SECRET_NAME=stackit-credentials
export STACKIT_CLOUD_CONTROLLER_MANAGER_IMAGE=ghcr.io/stackitcloud/cloud-provider-stackit/cloud-controller-manager:v1.35.7

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

When the workload API is available, retrieve the workload kubeconfig to install
a CNI:

```sh
clusterctl get kubeconfig "${CLUSTER_NAME}" \
  --namespace "${NAMESPACE}" \
  > "${CLUSTER_NAME}".kubeconfig
```

Workload nodes remain in `NotReady` status until a CNI is installed. We provide
a preconfigured Cilium setup at `templates/addons/cilium-values.yaml` in the
templates addons directory, and it is also published for each release:

```sh
curl --fail --location --remote-name \
  "https://github.com/stackitcloud/cluster-api-provider-stackit/releases/download/${CAPSTK_VERSION}/cilium-values.yaml"
```

Install Cilium using the Cilium CLI or Helm with these values (or use
`templates/addons/cilium-values.yaml` directly from a local checkout):

```sh
cilium install \
  --kubeconfig "${CLUSTER_NAME}.kubeconfig" \
  --values cilium-values.yaml
```

If you are working from a local checkout, you can also use the development
target in the `Makefile` backed by `hack/install-workload-cni.sh`:

```sh
make install-workload-cni \
  WORKLOAD_KUBECONFIG="${CLUSTER_NAME}.kubeconfig"
```

See [Workload CNI](usage/cni.md) and [Workload Addons](usage/addons.md) for more
details on CNI options and verification.

## Cleanup

To fully delete the workload cluster and its cloud infrastructure:

```sh
kubectl delete -f cluster.yaml
```

Deletion should take at most 5 minutes. Verify that all resources are gone:

```sh
kubectl get cluster,machine,stackitcluster,stackitmachine --namespace "${NAMESPACE}"
```

### Troubleshooting stuck deletion

If resources persist after several minutes:

1. Check the status and conditions of persisting resources:

   ```sh
   kubectl describe cluster,machine,stackitcluster,stackitmachine --namespace "${NAMESPACE}"
   ```

2. Inspect the controller logs for errors:

   ```sh
   # Core Cluster API controller
   kubectl logs -n capi-system deployment/capi-controller-manager -c manager --tail=100

   # STACKIT provider controller
   kubectl logs -n cluster-api-provider-stackit-system deployment/cluster-api-provider-stackit-controller-manager --tail=100
   ```

3. If a `StackitMachine` cannot finish deletion and blocks its parent resources, you can remove its finalizer:

   ```sh
   kubectl patch stackitmachine <stackitmachine-name> -n "${NAMESPACE}" \
     --type=merge -p '{"metadata":{"finalizers":null}}'
   ```

   Removing the finalizer unblocks Kubernetes deletion for that resource and its parents. You may then have to manually delete the actual VM in the STACKIT portal or through the STACKIT API.

Use the [development guide](development/index.md) when you want to build and
run the provider from a local checkout.
