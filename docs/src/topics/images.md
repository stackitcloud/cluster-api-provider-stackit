# OS Images

## Ubuntu Nodes

CAPSTK includes `templates/cluster-template.yaml` as the default example for
Ubuntu-based clusters:

- control-plane machines use the Ubuntu image from `STACKIT_IMAGE_ID`
- worker machines use the same Ubuntu image from `STACKIT_IMAGE_ID`
- kubeadm bootstrap runs through cloud-init

Select one of the available Ubuntu images:

```sh
stackit image list \
    --project-id "${STACKIT_PROJECT_ID}" \
    --region "${STACKIT_REGION}" \
    --output-format json \
    | jq -r '.[] | select(.name | test("Ubuntu")) | "\(.id)\t\(.name)"'
```

The default template currently supports generic Ubuntu cloud images by
installing Kubernetes node dependencies at first boot:

- install `containerd`, `kubelet`, `kubeadm`, and `kubectl` with `apt-get`
- use the Kubernetes package repository selected by
  `KUBERNETES_APT_REPOSITORY_MINOR`
- configure containerd with `SystemdCgroup = true`
- enable `br_netfilter` and Kubernetes bridge/IP forwarding sysctls
- register kubelet with `cloud-provider=external`
- make kubeadm use the STACKIT hostname from cloud-init metadata
  (`{{ ds.meta_data.local_hostname }}`), so cloud-provider-stackit can map the
  Kubernetes node to the STACKIT server

Render the default template like any other clusterctl template:

```sh
export STACKIT_IMAGE_ID=<ubuntu-image-id>
export KUBERNETES_VERSION=v1.35.8
export KUBERNETES_APT_REPOSITORY_MINOR=v1.35

clusterctl generate cluster "${CLUSTER_NAME}" \
  --from templates/cluster-template.yaml \
  --kubernetes-version "${KUBERNETES_VERSION}" \
  --control-plane-machine-count "${CONTROL_PLANE_MACHINE_COUNT}" \
  --worker-machine-count "${WORKER_MACHINE_COUNT}" \
  | kubectl apply -f -
```

Notes:

- Ubuntu SSH access normally uses the `ubuntu` user.
- Install a workload CNI after the API server is reachable.
- The default template downloads Kubernetes packages during first boot. This is
  useful for development and e2e testing, but production clusters should use a
  kubeadm-ready image that already contains the expected container runtime and
  Kubernetes node packages for the chosen Kubernetes minor.
- Prefer non-ARM Ubuntu images until this provider publishes a supported image
  matrix.

## Flatcar Worker Nodes

See the [official Flatcar STACKIT docs](https://www.flatcar.org/docs/latest/installing/cloud/stackit/)
for generic Flatcar image usage on STACKIT.

CAPSTK includes `templates/cluster-template-flatcar-workers.yaml` as an example
for mixed-image clusters:

- control-plane machines use the regular Ubuntu image from `STACKIT_IMAGE_ID`
- worker machines use the Flatcar image from `STACKIT_WORKER_IMAGE_ID`

Example amd64 Flatcar image:

```sh
export STACKIT_WORKER_IMAGE_ID=419c31da-39e3-4ea3-9bd8-699b44e8394f # Flatcar 4459.2.4
```

Flatcar requires a different bootstrap path than Ubuntu:

- use `KubeadmConfigTemplate.spec.template.spec.format: ignition`
- enable Cluster API's `KubeadmBootstrapFormatIgnition` feature gate
- do not reuse Ubuntu `apt-get` based bootstrap commands
- make kubeadm use the STACKIT hostname from `/run/metadata/flatcar`
  (`COREOS_OPENSTACK_HOSTNAME`), otherwise the node may register as `localhost`
  and cloud-provider-stackit cannot map it to the STACKIT server

Render the example template like any other clusterctl template:

```sh
clusterctl generate cluster "${CLUSTER_NAME}" \
  --from templates/cluster-template-flatcar-workers.yaml \
  --kubernetes-version "${KUBERNETES_VERSION}" \
  --control-plane-machine-count "${CONTROL_PLANE_MACHINE_COUNT}" \
  --worker-machine-count "${WORKER_MACHINE_COUNT}" \
  | kubectl apply -f -
```

Notes:

- Flatcar SSH access uses the `core` user. When connecting through the CAPSTK
  bastion, use the bastion image's SSH user for the first hop, usually
  `ubuntu`, and `core` for the Flatcar node:

  ```sh
  ssh -i "${CLUSTER_SSH_KEY}" core@<flatcar-node-internal-ip> \
    -o "ProxyCommand ssh -W %h:%p -i ${CLUSTER_SSH_KEY} ubuntu@${BASTION_HOST}"
  ```

- Install a workload CNI after the API server is reachable.
- The example template downloads Kubernetes binaries during first boot.
  Production images should provide those binaries through an image build,
  package, or system extension instead.
