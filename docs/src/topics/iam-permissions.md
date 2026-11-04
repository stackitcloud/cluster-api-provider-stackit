# IAM Permissions Used

`cluster-api-provider-stackit` authenticates to STACKIT with the service
account JSON stored in the `StackitCluster.spec.credentialsSecretRef` Secret.
That service account should use a custom project role with only the permissions
needed by the infrastructure provider.

Do not use broad project administrator roles for the controller. STACKIT
documents custom roles as the way to bundle an explicit permission set, and
role bindings as the way to assign that role to a user or service account.

## Required provider permissions

The current provider implementation uses these STACKIT API operations:

| Provider action | Code path | STACKIT permission |
| --- | --- | --- |
| Read the configured network | `GetNetwork` | `iaas.network.get` |
| Create managed network (if enabled) | `CreateNetwork` | `iaas.network.create` |
| Delete managed network (if enabled) | `DeleteNetwork` | `iaas.network.delete` |
| Create VM instances | `CreateServer` | `iaas.server.create` |
| Find existing tagged VM instances | `ListServers` | `iaas.server.list` |
| Read VM state | `GetServer` | `iaas.server.get` |
| Read VM NIC addresses for CAPI addresses and load balancer targets | `ListServerNICs` | `iaas.server.nic.list` |
| Delete VM instances | `DeleteServer` | `iaas.server.delete` |
| Create the optional bastion public IP | `CreatePublicIP` | `iaas.public-ip.create` |
| Find existing tagged bastion public IPs | `ListPublicIPs` | `iaas.public-ip.list` |
| Read the bastion public IP after attach | `GetPublicIP` | `iaas.public-ip.get` |
| Attach the bastion public IP to the bastion server | `AddPublicIpToServer` | `iaas.server.public-ip.add` |
| Detach the bastion public IP during cleanup | `RemovePublicIpFromServer` | `iaas.server.public-ip.remove` |
| Delete the optional bastion public IP | `DeletePublicIP` | `iaas.public-ip.delete` |
| Create the optional bastion security group | `CreateSecurityGroup` | `iaas.security-group.create` |
| Find existing tagged bastion security groups | `ListSecurityGroups` | `iaas.security-group.list` |
| Create bastion SSH ingress rules | `CreateSecurityGroupRule` | `iaas.security-group.rule.create` |
| List bastion SSH ingress rules | `ListSecurityGroupRules` | `iaas.security-group.rule.list` |
| Delete bastion SSH ingress rules during cleanup | `DeleteSecurityGroupRule` | `iaas.security-group.rule.delete` |
| Attach the bastion security group to the bastion server | `AddSecurityGroupToServer` | `iaas.server.security-group.add` |
| Detach the bastion security group during cleanup | `RemoveSecurityGroupFromServer` | `iaas.server.security-group.remove` |
| Delete the optional bastion security group | `DeleteSecurityGroup` | `iaas.security-group.delete` |
| Create the API server network load balancer | `CreateLoadBalancer` | `nlb.loadbalancer.create` |
| Find existing tagged load balancers | `ListLoadBalancers` | `nlb.loadbalancer.list` |
| Read the load balancer before target updates | `GetLoadBalancer` | `nlb.loadbalancer.get` |
| Delete the API server load balancer | `DeleteLoadBalancer` | `nlb.loadbalancer.delete` |
| Replace the API server target pool | `UpdateTargetPool` | `nlb.targetpool.replace` |

The least-privilege role for the current provider is therefore:

```text
iaas.network.create
iaas.network.delete
iaas.network.get
iaas.public-ip.create
iaas.public-ip.delete
iaas.public-ip.get
iaas.public-ip.list
iaas.server.create
iaas.server.delete
iaas.server.get
iaas.server.list
iaas.server.nic.list
iaas.server.public-ip.add
iaas.server.public-ip.remove
iaas.server.security-group.add
iaas.server.security-group.remove
iaas.security-group.create
iaas.security-group.delete
iaas.security-group.list
iaas.security-group.rule.create
iaas.security-group.rule.delete
iaas.security-group.rule.list
nlb.loadbalancer.create
nlb.loadbalancer.delete
nlb.loadbalancer.get
nlb.loadbalancer.list
nlb.targetpool.replace
```

This list covers `StackitCluster` and `StackitMachine` reconciliation,
including the optional provider-managed bastion host. It does not include
permissions for manually creating networks, SSH keys, images, or other
prerequisite resources. It also does not include permissions for the in-cluster
`cloud-provider-stackit` add-on if you configure that add-on to manage
Kubernetes `Service` load balancers beyond the provider-managed API server load
balancer.

## Create a strict role and service account with OpenTofu

Use OpenTofu and the STACKIT provider to create the custom role, service
account, role assignment, and service-account key as one managed setup.

The bootstrap identity used by OpenTofu needs these setup permissions:

- `iam.role.add` to create the custom role
- `iam.role.get` and `iam.role.list` to read role state
- `iam.member.add` to assign the role to the service account
- `iam.member.get` to read role-assignment state
- `iam.service-account.create` to create the service account
- `iam.service-account.get` and `iam.service-account.list` to read service-account state
- `iam.service-account-key.create` to create the service-account key

If the same OpenTofu configuration should also destroy the setup later, the
bootstrap identity also needs the corresponding remove/delete permissions:
`iam.role.remove`, `iam.service-account.delete`, and
`iam.service-account-key.delete`.

Create a STACKIT role:

```hcl
{{#include ../../../hack/tf/iam-setup/capi-stackit-role.tf}}
```

Create a STACKIT service account, assign the role and create a service account key:

```hcl
{{#include ../../../hack/tf/iam-setup/capi-stackit-sa.tf}}
```

You will find a working example in `hack/tf/iam-setup`.

To apply it:

```sh
tofu init

tofu apply \
  -var "project_id=${STACKIT_PROJECT_ID}" \
  -var "bootstrap_service_account_key_path=${BOOTSTRAP_SERVICE_ACCOUNT_KEY_PATH}"
```

Write the generated key to a local file:

```sh
export STACKIT_SERVICE_ACCOUNT_JSON_FILE=./.stackit/cluster-api-provider-stackit-serviceaccount.json

mkdir -p "$(dirname "${STACKIT_SERVICE_ACCOUNT_JSON_FILE}")"
tofu output -raw service_account_key_json > "${STACKIT_SERVICE_ACCOUNT_JSON_FILE}"
```

Next, create the Kubernetes Secret used by `StackitCluster`:

```sh
kubectl create secret generic stackit-credentials \
  --namespace default \
  --from-literal=project-id="${STACKIT_PROJECT_ID}" \
  --from-file=serviceaccount.json="${STACKIT_SERVICE_ACCOUNT_JSON_FILE}"
```

## For Developers

Verify the strict role with the billable e2e tests, not only by reading the
permission list. The create/delete scenario exercises VM creation, VM lookup,
VM deletion, load balancer creation, target-pool updates, and load balancer
cleanup.

Run at least:

```sh
export STACKIT_E2E_CREATE_CLUSTER=true
export STACKIT_E2E_NODE_REF=true
export STACKIT_CREDENTIALS_SECRET_NAME=stackit-credentials
export STACKIT_CREDENTIALS_SECRET_NAMESPACE=default

make test-e2e-workload-noderef
```

To verify the optional bastion path, first import the SSH key pair with the
same service account stored in `stackit-credentials`; key pairs imported with a
different service account are not visible to the provider. Then set
`STACKIT_SSH_KEY_NAME` and `STACKIT_BASTION_SSH_KEY_NAME` and run:

```sh
export STACKIT_E2E_CREATE_CLUSTER=true
export STACKIT_E2E_BASTION=true
export STACKIT_SSH_KEY_NAME=<provider-service-account-keypair-name>
export STACKIT_BASTION_SSH_KEY_NAME="${STACKIT_SSH_KEY_NAME}"

make test-e2e-workload-bastion
```

For release validation, also run the scale, worker-upgrade, control-plane
upgrade, and topology e2e targets with the same strict service account:

```sh
make test-e2e-workload-scale
make test-e2e-workload-upgrade-workers
make test-e2e-workload-upgrade-control-plane
make test-e2e-workload-topology
```

> [!CAUTION]  
> Some broader SDK integration tests call helper APIs that are not used by the
> provider at runtime. For example, `TestSDKClientListNetworksIntegration` calls
> `ListNetworks` and therefore needs `iaas.network.list`; the provider
> reconciler only calls `GetNetwork` with the configured network ID, so
> `iaas.network.get` is sufficient for runtime.
