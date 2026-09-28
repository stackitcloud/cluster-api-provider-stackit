# Developer Guide

Use this guide to create a local development environment from a fresh checkout.
It creates a Kind management cluster, installs Cluster API, and runs the
STACKIT provider image you build from this repository. That management cluster
then creates workload clusters in STACKIT, so use a project intended for
development and expect the workload resources to incur cost.

## Before you start

Install:

- Go
- Docker
- `kind`
- `kubectl`
- `clusterctl`
- `base64`

You also need a STACKIT project, a service-account JSON key, an existing
network, an image, and a machine type. The service account needs the permissions
listed in [IAM permissions](../topics/iam-permissions.md).

Clone this repository and run the commands below from its root.

## Create the management cluster

The management cluster runs the Cluster API controllers and the provider. It is
not the Kubernetes cluster that will run your workload.

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

Point `kubectl` at the new management cluster:

```sh
kubectl config use-context kind-capi-stackit
```

## Install Cluster API

Install Cluster API core, bootstrap, and control-plane providers into the management
cluster:

```sh
clusterctl init \
  --config hack/clusterctl-local.yaml \
  --core cluster-api \
  --bootstrap kubeadm \
  --control-plane kubeadm
```

`hack/clusterctl-local.yaml` enables Cluster API features needed by the
repository's templates, including ClusterClass and ClusterResourceSet.

## Build and deploy the local provider

Build the controller image, load it into the Kind node, and deploy the
provider with that image:

```sh
export IMG=cluster-api-provider-stackit:dev
make docker-build IMG="${IMG}"
kind load docker-image "${IMG}" --name capi-stackit
make deploy IMG="${IMG}"
```

Wait until the locally built provider is ready:

```sh
kubectl rollout status \
  --namespace cluster-api-provider-stackit-system \
  deployment/cluster-api-provider-stackit-controller-manager
```

## Configure STACKIT and create a workload cluster

The provider is now running from your checkout. Continue with the quick start
from [set credentials and cluster settings](../quick-start.md#set-credentials-and-cluster-settings).
Create the Secret in the `default` namespace used there, then create the workload
cluster.

Because you are working from a repository checkout, use
`templates/cluster-template.yaml` directly instead of downloading a release
asset:

```sh
clusterctl generate cluster "${CLUSTER_NAME}" \
  --from templates/cluster-template.yaml \
  --target-namespace "${NAMESPACE}" \
  > cluster.yaml
kubectl apply -f cluster.yaml
```

When the workload API is available, retrieve the workload kubeconfig:

```sh
clusterctl get kubeconfig "${CLUSTER_NAME}" \
  --namespace "${NAMESPACE}" \
  > "${CLUSTER_NAME}".kubeconfig
```

Install a CNI using the repository helper (`hack/install-workload-cni.sh`) via
`make`:

```sh
make install-workload-cni \
  WORKLOAD_KUBECONFIG="${CLUSTER_NAME}.kubeconfig"
```

By default, this installs Cilium using `templates/addons/cilium-values.yaml`. You
can also install Calico by setting `STACKIT_WORKLOAD_CNI=calico`, or apply a
custom manifest by setting `CNI_MANIFEST=path/to/manifest.yaml`. See
[Workload CNI](../usage/cni.md) for more options.

## Run the controller on your host

For controller debugging, stop the deployed provider first, keep
`kind-capi-stackit` as the active kubeconfig context, and run:

```sh
make undeploy
make run
```

`make run` connects to the active management cluster. When you stop it, rebuild,
load, and deploy the image again with the commands in the previous section.

## Cleanup

To fully delete a workload cluster and its cloud infrastructure:

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
