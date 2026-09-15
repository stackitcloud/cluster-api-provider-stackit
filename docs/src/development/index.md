# Developer Guide

This guide is for contributors working from a local checkout.

## Run against the current cluster

Install CRDs into the current cluster:

```sh
make install
```

Run the controller locally against the current kubeconfig context:

```sh
make run
```

Build and deploy the controller image:

```sh
export IMG=<registry>/cluster-api-provider-stackit:<tag>
make docker-build docker-push IMG="$IMG"
make deploy IMG="$IMG"
```

For a local kind management cluster, build and load the image instead of pushing it:

```sh
export IMG=cluster-api-provider-stackit:dev
make docker-build IMG="$IMG"
kind load docker-image "$IMG" --name capi-stackit
make deploy IMG="$IMG"
```

The local development cluster used during validation is `kind-capi-stackit`.

## Use locally built provider assets

Build a local clusterctl repository:

```sh
export IMG=cluster-api-provider-stackit:dev
make clusterctl-release IMG="${IMG}"
export STACKIT_CLUSTERCTL_REPOSITORY="$(pwd)/dist/clusterctl"
```

Create a Kind management cluster and install the local provider assets:

```sh
kind create cluster --name capi-stackit
kubectl config use-context kind-capi-stackit

clusterctl init \
  --config hack/clusterctl-local.yaml \
  --core cluster-api \
  --bootstrap kubeadm \
  --control-plane kubeadm \
  --infrastructure stackit:v0.1.0
```

Build and load the image, then deploy the controller:

```sh
make docker-build IMG="${IMG}"
kind load docker-image "${IMG}" --name capi-stackit
make deploy IMG="${IMG}"
```

`hack/clusterctl-local.yaml` enables `CLUSTER_TOPOLOGY` so ClusterClass and
topology clusters can pass the Cluster API admission webhooks.
