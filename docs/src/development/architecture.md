# Architecture

## Overview

The provider has five main areas:

- API types and CRDs
- Controllers
- Scopes (`ClusterScope` and `MachineScope`)
- Cloud client and resource services
- clusterctl templates and release assets

Key packages:

```text
api/v1alpha1/             Provider API types
controller/               Reconciliation logic
scope/                    Per-reconcile state, status patching, and service specs
cloud/                    Cloud client factory, provider ID helpers, and tag-based cleanup
cloud/services/           Resource services (availabilityzone, network, loadbalancer, publicip, securitygroup, server, shared)
cloud/fake/               In-memory fake HTTP transport and client for tests
util/                     Shared helpers
templates/                clusterctl templates
config/                   Kubebuilder manifests
```

Controllers construct a `ClusterScope` or `MachineScope` for each reconciliation pass and delegate cloud operations to the resource services under `cloud/services/`. Both the real `SDKClient` and the `fake.Client` implement `cloud.Client` (exposing the configured project ID, region, and STACKIT IaaS / Load Balancer SDK clients), keeping reconciliation testable with envtest and in-memory HTTP fakes.

## Cluster API Contract

The provider implements the Cluster API infrastructure contract for:

- `StackitCluster`
- `StackitClusterTemplate`
- `StackitMachine`
- `StackitMachineTemplate`

Important contract behavior:

- `StackitCluster.status.initialization.provisioned` is set when cluster
  infrastructure is ready.
- `StackitMachine.status.initialization.provisioned` is set when the VM is
  provisioned.
- `StackitMachine.spec.providerID` is set to the provider-compatible value.
- Conditions include `observedGeneration`.
- Paused clusters or paused resources must not trigger cloud API calls.
- Finalizers clean up provider-owned resources before Kubernetes object removal.

`StackitCluster.spec.controlPlaneEndpoint` is used for the Cluster API
infrastructure cluster contract when the provider manages the API server load
balancer endpoint.

## Controllers and Scopes

`ClusterScope` (`scope/cluster_scope.go`) and `MachineScope` (`scope/machine_scope.go`) hold the Kubernetes objects and cloud client for a single reconcile pass, patch status and conditions on close, and translate Kubernetes spec/status fields into service spec structs (`AvailabilityZoneSpec`, `NetworkSpec`, `LoadBalancerSpec`, `PublicIPSpec`, `SecurityGroupSpecs`, `ServerSpec`).

`StackitClusterReconciler` is responsible for:

- Reading credentials and building the cloud client
- Reconciling availability zones and publishing failure domains
- Reconciling (validating) the configured network
- Reconciling the optional API server load balancer and its control-plane target pool
- Reconciling the optional bastion host (server, security groups, and public IP)
- Updating readiness and contract status
- Cleaning up provider-managed bastion and load balancer resources on deletion

`StackitMachineReconciler` is responsible for:

- Validating the machine's availability zone against cluster failure domains
- Waiting for bootstrap data
- Reconciling the STACKIT server instance, provider ID, and addresses
- Reconciling node SSH security group attachment when the bastion is enabled
- Deleting the server on teardown

Reconciliation must be idempotent. Re-running the same reconcile loop should be
safe and should not create duplicate cloud resources.

## Cloud Client and Services

`cloud.Client` provides the project ID, region, and configured STACKIT IaaS (`*iaas.APIClient`) and Load Balancer (`*lb.APIClient`) SDK clients.

Cloud resource management is organized into service packages under `cloud/services/`:

- `availabilityzone`: Lists availability zones (`Reconcile`)
- `network`: Looks up and validates the cluster network (`Reconcile`)
- `loadbalancer`: Creates, adopts by tags, updates target pools, and deletes the API server load balancer (`Reconcile`, `Delete`)
- `publicip`: Creates, adopts by tags, associates/disassociates, and deletes public IPs (`Reconcile`, `Delete`)
- `securitygroup`: Creates, adopts by tags, reconciles rules and server attachments, and deletes security groups (`Reconcile`, `Delete`)
- `server`: Creates, adopts by tags, inspects state/addresses, and deletes servers (`Reconcile`, `Delete`)
- `shared`: Sentinel errors (`ErrNotFound`, `ErrUnauthorized`, `ErrInvalidInput`, `ErrConflict`, `ErrTransient`), SDK error classification, and tag lookup/deletion helpers

`cloud.CleanupByTags` uses the `publicip`, `loadbalancer`, `server`, and `securitygroup` services to delete all provider-managed resources matching a tag selector without requiring Kubernetes objects.

Controllers and scopes handle classified errors from `cloud/services/shared` differently:

- transient and conflict errors (`shared.IsRetryable`) trigger a requeue
- terminal validation or credential errors set clear conditions and avoid tight retry loops
- not-found errors during deletion are treated as successful cleanup

Tests use `cloud/fake.Client` (which backs the STACKIT SDK clients with an in-memory HTTP handler) for deterministic unit and envtest coverage. SDK integration tests are opt-in and require real STACKIT credentials.

## STACKIT ProviderID Compatibility

The providerID format is verified against the local `cloud-provider-stackit`
repository.

Relevant reference points:

- `pkg/ccm/instances.go`: `Instances.makeInstanceID` returns `stackit://<server-id>`.
- `pkg/ccm/instances.go`: `getInstance` resolves nodes with `GetServer(projectID, region, serverID)`, where project and region come from the cloud-controller-manager configuration.
- `pkg/ccm/instances_test.go`: the "new providerID" table entry expects `stackit://hello-server`.

`cluster-api-provider-stackit` therefore writes:

```text
StackitMachine.spec.providerID = stackit://<server-id>
StackitMachine.status.providerID = stackit://<server-id>
```

Cluster API then surfaces `StackitMachine.spec.providerID` to
`Machine.spec.providerID`, and `cloud-provider-stackit` uses the same value for
`Node.spec.providerID`. Project ID and region are intentionally not encoded in
the providerID.
