# Architecture

## Overview

The provider has four main areas:

- API types and CRDs
- Controllers
- Cloud abstraction layer
- clusterctl templates and release assets

Key packages:

```text
api/v1alpha1/             Provider API types
controller/               Reconciliation logic
cloud/                    Cloud client interface and SDK implementation
cloud/fake/               In-memory fake for tests
util/                     Shared helpers
templates/                clusterctl templates
config/                   Kubebuilder manifests
```

The controllers do not call the STACKIT SDK directly. They use the cloud client
interface, which keeps reconciliation testable and allows fake-client envtest
coverage.

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

## Controllers

`StackitClusterReconciler` is responsible for:

- Reading credentials
- Looking up the configured network
- Managing the optional API server load balancer
- Publishing failure domains
- Updating readiness and contract status
- Cleaning up provider-managed load balancers on deletion

`StackitMachineReconciler` is responsible for:

- Waiting for bootstrap data
- Creating STACKIT servers
- Setting provider IDs and addresses
- Registering control-plane machines as API server load balancer targets
- Deleting servers and load balancer targets on teardown

Reconciliation must be idempotent. Re-running the same reconcile loop should be
safe and should not create duplicate cloud resources.

## Cloud Client

The cloud client abstraction hides STACKIT SDK details from controllers.

It covers:

- Server create, get, list, and delete
- Network lookup
- Load balancer create, list, delete, and target-pool updates
- Provider ID helpers
- Error classification

Controllers should handle classified errors differently:

- transient errors should be retried
- terminal validation errors should set clear conditions
- not-found errors during deletion should be treated as successful cleanup where
  appropriate

Tests use an in-memory fake cloud client for deterministic reconciliation
coverage. SDK integration tests are opt-in and require real STACKIT credentials.

## STACKIT ProviderID Compatibility

The providerID format is verified against the local `cloud-provider-stackit`
repository.

Relevant reference points:

- `pkg/ccm/instances.go`: `Instances.makeInstanceID` returns `stackit://<server-id>`.
- `pkg/ccm/instances.go`: `instanceIDFromProviderID` parses `stackit://<server-id>` with no project or region component.
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
