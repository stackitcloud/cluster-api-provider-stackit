# Accessing cluster instances

By default, workload clusters created by `cluster-api-provider-stackit` do not
expose SSH access. Cluster API bootstrap does not require SSH; CABPK provides
cloud-init user data through Kubernetes bootstrap Secrets.

For break-glass access, a `StackitCluster` can optionally ask the provider to
create one managed SSH bastion VM. The bastion is attached to the same STACKIT
network as the cluster nodes, gets a provider-managed public IP, and has a
provider-managed security group allowing TCP/22 only from configured CIDRs.

When bastion access is enabled, the provider also manages the node-side SSH
path: it creates a separate security group for cluster nodes, allows TCP/22
from the bastion security group, and attaches that node SSH security group to
control-plane and worker VMs.

## Prerequisites

- The workload cluster was generated with `clusterctl generate cluster` and
  applied to the management cluster.
- The configured STACKIT service account role includes the bastion permissions
  documented in [IAM Permissions Used](./iam-permissions.md).
- The bastion image is an Ubuntu image with SSH enabled for the expected user,
  normally `ubuntu`.
- The STACKIT SSH key named in `spec.bastion.sshKeyName` already exists for the
  service account used by the provider. STACKIT key pairs are not shared across
  service accounts, so importing a key with an inspection or admin service
  account does not make it usable by the controller service account.
- To SSH from the bastion into cluster nodes, the control-plane and worker
  `StackitMachineTemplate` resources must also set
  `spec.template.spec.sshKeyName`. The bastion does not inject SSH keys into
  existing node VMs.

## Configure SSH keys for the service account

The CAPSTK controller authenticates to STACKIT through a service account key. Because SSH key pairs are scoped to the authenticated identity in this case a service account, the SSH keys you want to use on the bastion or on cluster machines must be attached under that exact service account and not your personal user.

1. Generate an SSH key pair locally if you do not already have one:

   ```sh
   ssh-keygen -t ed25519 -f ~/.ssh/id_stackit -C "capstk-access"
   ```

2. Authenticate the STACKIT CLI using your service account credentials:

   ```sh
   stackit auth activate-service-account \
     --service-account-key-path /path/to/service-account.json
   ```

3. Import the public key as a key pair under that service account:

   ```sh
   export STACKIT_SSH_KEY_NAME="capstk-key"

   stackit key-pair create \
     --project-id "${STACKIT_PROJECT_ID}" \
     --region "${STACKIT_REGION}" \
     --name "${STACKIT_SSH_KEY_NAME}" \
     --public-key "@${HOME}/.ssh/id_stackit.pub"
   ```

4. Reference the key in your cluster definition:

   When using `templates/cluster-template-bastion.yaml`, pass the key name to the template variables:
   - `STACKIT_BASTION_SSH_KEY_NAME`: sets `spec.bastion.sshKeyName` on `StackitCluster`
   - `STACKIT_SSH_KEY_NAME`: sets `spec.template.spec.sshKeyName` on `StackitMachineTemplate` for control-plane and worker nodes

   You can use separate keys for the bastion and the nodes, or point both variables to the same key.

## Enable the bastion

Patch or edit the generated `StackitCluster`:

```yaml
apiVersion: infrastructure.cluster.x-k8s.io/v1alpha1
kind: StackitCluster
metadata:
  name: <cluster-name>
  namespace: <namespace>
spec:
  bastion:
    enabled: true
    imageID: <ubuntu-image-id>
    machineType: <machine-type>
    sshKeyName: <existing-stackit-ssh-key-name>
    allowedCIDRs:
      - <your-public-ip-or-network-cidr>
    rootVolume:
      sizeGiB: 50
      performanceClass: storage_premium_perf6
      deleteOnTermination: true
    cloudInitRef:
      kind: ConfigMap
      name: <bastion-cloud-init-configmap>
      key: userData
```

Use a narrow `allowedCIDRs` value such as `203.0.113.10/32` where possible.
`0.0.0.0/0` allows SSH from anywhere and should only be used deliberately.  
Set `rootVolume` when the chosen machine type's flavor disk is too small for
the chosen image.  
Set `cloudInitRef` when the bastion host needs additional
packages, users, files, or other cloud-init customization. The provider reads
the referenced ConfigMap or Secret and passes the value as-is to the bastion
VM. It is not applied to control-plane or worker nodes.

For node access, set the same or another existing SSH key on the machine
templates:

```yaml
apiVersion: infrastructure.cluster.x-k8s.io/v1alpha1
kind: StackitMachineTemplate
metadata:
  name: <machine-template-name>
spec:
  template:
    spec:
      sshKeyName: <existing-stackit-ssh-key-name>
```

Leaving `StackitMachineTemplate.spec.template.spec.sshKeyName` empty is valid,
but SSH to nodes through the bastion will not work.

## How node SSH access is wired

Enabling the bastion creates two provider-managed SSH security groups:

- The bastion SSH security group is attached to the bastion VM. It allows
  TCP/22 from `spec.bastion.allowedCIDRs`.
- The node SSH security group is attached to each provider-managed
  control-plane and worker VM. It allows TCP/22 from the bastion security
  group by using the bastion security group as the remote source.

This means users connect from their workstation to the bastion public IP, and
then from the bastion to node internal IPs. The provider manages the network
permissions for both hops, but it does not manage SSH users or private key
files. The SSH key named on the bastion and node specs must already exist in
STACKIT for the service account used by the provider.

The node SSH security group is shared by all nodes in the cluster. New nodes
created while bastion is enabled get the group attached during
`StackitMachine` reconciliation.

## Customize the bastion with cloud-init

`spec.bastion.cloudInitRef` references a complete cloud-init user-data document
for the bastion VM. Use a ConfigMap for non-sensitive configuration:

```yaml
apiVersion: v1
kind: ConfigMap
metadata:
  name: bastion-cloud-init
data:
  userData: |
    #cloud-config
    packages:
      - jq
---
spec:
  bastion:
    enabled: true
    imageID: <ubuntu-image-id>
    machineType: <machine-type>
    sshKeyName: <existing-stackit-ssh-key-name>
    allowedCIDRs:
      - <your-public-ip-or-network-cidr>
    cloudInitRef:
      kind: ConfigMap
      name: bastion-cloud-init
      key: userData
```

Use a Secret instead when the cloud-init document contains sensitive values:

```yaml
apiVersion: v1
kind: Secret
metadata:
  name: bastion-cloud-init
stringData:
  userData: |
    #cloud-config
    packages:
      - jq
---
spec:
  bastion:
    cloudInitRef:
      kind: Secret
      name: bastion-cloud-init
      key: userData
```

The referenced object must be in the same namespace as the `StackitCluster`.
The provider does not merge the referenced data with node bootstrap data.
Cloud-init user data is applied only when STACKIT creates a VM, so an existing
bastion cannot be reconfigured in place. If the referenced ConfigMap or Secret
content changes, the provider deletes and recreates the provider-managed
bastion VM with the new user data.

Recreating the bastion temporarily interrupts SSH access, assigns a new
provider-managed bastion server, and may assign a new public IP. The provider
also removes the old node SSH security group path and recreates it for the new
bastion so control-plane and worker nodes remain reachable through the current
bastion after reconciliation completes.

## Get the bastion IP

Use the management-cluster kubeconfig:

```sh
kubectl get stackitcluster <cluster-name> \
  --namespace <namespace> \
  -o jsonpath='{.status.bastion.publicIP}'
```

Or inspect the printcolumn:

```sh
kubectl get stackitclusters --namespace <namespace>
```

The `Bastion IP` column is empty until STACKIT has assigned and attached the
public IP.

## Get node internal IPs

Use the workload-cluster kubeconfig:

```sh
kubectl get nodes \
  -o custom-columns=NAME:.metadata.name,IP:'{.status.addresses[?(@.type=="InternalIP")].address}'
```

Alternatively, inspect the management-cluster `StackitMachine` addresses:

```sh
kubectl get stackitmachines --namespace <namespace> \
  -o custom-columns=NAME:.metadata.name,IP:'{.status.addresses[?(@.type=="InternalIP")].address}'
```

## Connect through the bastion

Set local variables:

```sh
export CLUSTER_SSH_KEY="$HOME/.ssh/<private-key-file>"
export BASTION_HOST="$(kubectl get stackitcluster <cluster-name> \
  --namespace <namespace> \
  -o jsonpath='{.status.bastion.publicIP}')"
```

Connect to a node internal IP through the bastion:

```sh
ssh -i "${CLUSTER_SSH_KEY}" ubuntu@<node-internal-ip> \
  -o "ProxyCommand ssh -W %h:%p -i ${CLUSTER_SSH_KEY} ubuntu@${BASTION_HOST}"
```

Use the node image's SSH user for the final hop. For example, Ubuntu nodes
usually use `ubuntu`, while Flatcar nodes use `core`.

An equivalent SSH config entry:

```text
Host 10.*
  User ubuntu
  IdentityFile <cluster-ssh-key>
  ProxyCommand ssh -W %h:%p ubuntu@<bastion-public-ip>
```

## Cleanup

The provider deletes the managed bastion server, public IP, security group, and
security group rules when the `StackitCluster` is deleted. It also removes the
provider-managed node SSH security group from cluster nodes and deletes that
security group.

The same cleanup path runs when `spec.bastion.enabled` is changed from `true`
to `false`: the provider removes the node SSH security group from
provider-managed control-plane and worker VMs, deletes the node SSH security
group and its rules, and then deletes the bastion server, public IP, and
bastion security group.


## Additional Notes

### Using the STACKIT CLI instead of `kubectl`

It is also possible to use STACKIT CLI commands instead of `kubectl` to gather information about the cluster nodes.

For example, to use the STACKIT CLI to get the public IP address of the SSH bastion host, use this STACKIT CLI command:

```bash
set CLUSTER_NAME stackit-bastion-test

stackit server list -o json | jq -r --arg cluster "$CLUSTER_NAME" '
    .[]
    | select(.labels["cluster.x-k8s.io/cluster-name"] == $cluster)
    | select(.labels["cluster-api-provider-stackit/resource-role"] == "bastion")
    | .nics[]?.publicIp
'
188.34.94.28
```

You should substitute the correct cluster name for `<CLUSTER_NAME>` in the above command.

Similarly, to obtain the list of private IP addresses of the cluster nodes, use this STACKIT CLI command:

```bash
set CLUSTER_NAME stackit-bastion-test

stackit server list -o json | jq -r --arg cluster "$CLUSTER_NAME" '
  .[]
  | select(.labels["cluster.x-k8s.io/cluster-name"] == $cluster)
  | select(.labels["cluster-api-provider-stackit/resource-role"] != "bastion")
  | .nics[]?.ipv4
'
```

For names plus private IPs:

```bash
stackit server list -o json | jq -r --arg cluster "$CLUSTER_NAME" '
  .[]
  | select(.labels["cluster.x-k8s.io/cluster-name"] == $cluster)
  | select(.labels["cluster-api-provider-stackit/resource-role"] != "bastion")
  | [.name, (.nics[]?.ipv4 // empty)]
  | @tsv
'
```

Finally, to obtain STACKIT instance names mapped with their private IPs, you can use this STACKIT CLI command:

```bash
stackit server list -o json | jq -r --arg cluster "$CLUSTER_NAME" '
  .[]
  | select(.labels["cluster.x-k8s.io/cluster-name"] == $cluster)
  | select(.labels["cluster-api-provider-stackit/resource-role"] != "bastion")
  | [.name, (.nics[]?.ipv4 // empty)]
  | @tsv
'
```

Note that your STACKIT CLI must be configured with credentials that enable you to query the STACKIT Servers API.
