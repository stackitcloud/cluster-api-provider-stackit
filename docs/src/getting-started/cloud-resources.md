# STACKIT Cloud Resources

The provider currently consumes existing STACKIT resources. It creates and
deletes workload-cluster servers and, when enabled, an API server load balancer,
but it does not create networks, security groups, SSH keys, or images.

## Network

`StackitCluster.spec.network.id` is the STACKIT network ID used by all cluster
machines and the provider-managed API server load balancer.

Choose or create a network in the same region as the workload cluster. The
network must provide:

- IP addresses for all control-plane and worker machines.
- Routing between workload-cluster nodes.
- Egress for cloud-init, package repositories when using development images,
  container image pulls, DNS, NTP, and STACKIT APIs.
- Traffic required by the selected CNI.

Network creation is intentionally outside the provider for now because networks
usually encode platform-level policy such as address planning, routing,
peering, VPN, DNS, and shared-service access.

Create a network with the STACKIT CLI:

```sh
export STACKIT_REGION=eu01
export STACKIT_NETWORK_NAME="${CLUSTER_NAME}-network"

stackit network create \
  --project-id "${STACKIT_PROJECT_ID}" \
  --region "${STACKIT_REGION}" \
  --name "${STACKIT_NETWORK_NAME}" \
  --ipv4-prefix "10.42.0.0/24" \
  --labels cluster-api-provider-stackit=true
```

Get the network ID for `STACKIT_NETWORK_ID`:

```sh
stackit network list \
  --project-id "${STACKIT_PROJECT_ID}" \
  --region "${STACKIT_REGION}" \
  --output-format json
```

Delete a test network when it is no longer used:

```sh
stackit network delete "${STACKIT_NETWORK_ID}" \
  --project-id "${STACKIT_PROJECT_ID}" \
  --region "${STACKIT_REGION}"
```

## Security Groups

`StackitMachine.spec.securityGroups` is an optional list of STACKIT security
group IDs attached to each created server. If omitted, STACKIT project defaults
apply.

For production clusters, prefer explicit security groups over implicit project
defaults. At minimum, the selected security groups must allow:

- The Kubernetes API server on TCP `6443` from the API server load balancer and
  from trusted administration networks.
- Node-to-node traffic required by kubeadm, kubelet, the control plane, and the
  selected CNI.
- Egress for bootstrap, package repositories if needed, image registries, DNS,
  NTP, and STACKIT APIs.

Do not open SSH ingress by default. If SSH is needed for break-glass debugging,
restrict it to trusted CIDRs.

Create a security group:

```sh
export STACKIT_SECURITY_GROUP_NAME="${CLUSTER_NAME}-nodes"

stackit security-group create \
  --project-id "${STACKIT_PROJECT_ID}" \
  --region "${STACKIT_REGION}" \
  --name "${STACKIT_SECURITY_GROUP_NAME}" \
  --stateful \
  --description "Cluster API STACKIT nodes" \
  --labels cluster-api-provider-stackit=true
```

Get the security group ID:

```sh
stackit security-group list \
  --project-id "${STACKIT_PROJECT_ID}" \
  --region "${STACKIT_REGION}" \
  --output-format json
```

Add a Kubernetes API ingress rule, replacing `STACKIT_SECURITY_GROUP_ID` and
`ADMIN_CIDR`:

```sh
stackit security-group rule create \
  --project-id "${STACKIT_PROJECT_ID}" \
  --region "${STACKIT_REGION}" \
  --security-group-id "${STACKIT_SECURITY_GROUP_ID}" \
  --direction ingress \
  --protocol-name tcp \
  --port-range-min 6443 \
  --port-range-max 6443 \
  --ip-range "${ADMIN_CIDR}" \
  --description "Kubernetes API"
```

Delete a test security group when it is no longer attached to servers:

```sh
stackit security-group delete "${STACKIT_SECURITY_GROUP_ID}" \
  --project-id "${STACKIT_PROJECT_ID}" \
  --region "${STACKIT_REGION}"
```

## SSH Keys

`StackitMachine.spec.sshKeyName` is optional. It references an existing STACKIT
SSH key pair by name.

SSH is not used for Cluster API bootstrap. Kubeadm bootstrap data is generated
by CABPK, stored in a Kubernetes Secret, and sent to STACKIT as cloud-init user
data when each server is created. Leave `sshKeyName` and
`STACKIT_SSH_KEY_NAME` empty when SSH access is not required.

Because the CAPSTK controller accesses STACKIT through a service account, the SSH key pairs must be attached to that service account. Key pairs are scoped per identity and cannot be shared across service accounts or users.

Authenticate the STACKIT CLI using your service account credentials:

```sh
stackit auth activate-service-account \
  --service-account-key-path "${STACKIT_SERVICE_ACCOUNT_JSON_FILE}"
```

Import an existing SSH public key:

```sh
export STACKIT_SSH_KEY_NAME="${CLUSTER_NAME}-break-glass"

stackit key-pair create \
  --project-id "${STACKIT_PROJECT_ID}" \
  --region "${STACKIT_REGION}" \
  --name "${STACKIT_SSH_KEY_NAME}" \
  --public-key "@${HOME}/.ssh/id_ed25519.pub" \
  --labels cluster-api-provider-stackit=true
```

Only create and pass a key pair when operators need SSH for debugging. Opening
SSH access also requires an explicit security group rule; the provider does not
need SSH to create or join Nodes.

Delete a test key pair:

```sh
stackit key-pair delete "${STACKIT_SSH_KEY_NAME}" \
  --project-id "${STACKIT_PROJECT_ID}" \
  --region "${STACKIT_REGION}"
```

## Node Images

`StackitMachine.spec.imageID` is the STACKIT image ID used for the node root
disk. The image must be compatible with cloud-init and kubeadm bootstrap.

For production clusters, use kubeadm-ready images that already contain the
expected container runtime and Kubernetes node packages for the chosen
Kubernetes minor. Runtime package installation is useful for development, but
it adds dependency on external package repositories during cluster creation and
slows down Machine readiness.

The current real e2e path can bootstrap generic Ubuntu images by installing
`containerd`, `kubelet`, `kubeadm`, and `kubectl` in kubeadm
`preKubeadmCommands`. Treat this as a development fallback, not the desired
production path.

List candidate images:

```sh
stackit image list \
  --project-id "${STACKIT_PROJECT_ID}" \
  --region "${STACKIT_REGION}" \
  --limit 20 \
  --output-format json
```

Use the selected image ID as `STACKIT_IMAGE_ID`. Prefer non-ARM Ubuntu images
or purpose-built kubeadm-ready images until this provider publishes a supported
image matrix.

## Verification

The command forms above were verified against STACKIT with temporary resources:

- `stackit network create`, `network describe`, and `network delete`
- `stackit security-group create`, `security-group rule create`, and
  `security-group delete`
- `stackit key-pair create` and `key-pair delete`
- `stackit image list`
