# HA, Availability Zones & Failure Domains

"Availability Zone" is a cloud provider-specific term, whereas "Failure Domain" is the broader, environment-agnostic concept used throughout Cluster API. A failure domain can represent a separation of physical switches, power feeds, rooms, racks, or distinct data centers. In the context of public cloud providers like STACKIT, failure domains and availability zones (AZs) are equivalent.

## Failure Domain Discovery

The provider dynamically discovers STACKIT availability zones from the STACKIT
IaaS API (`/v2/regions/{region}/availability-zones`) and publishes them as Cluster
API failure domains on `StackitCluster.status.failureDomains`.

For a region such as `eu01`, the provider exposes the real physical zones:

```text
eu01-1
eu01-2
eu01-3
```

Virtual metro zones (such as `eu01-m` with an `-m` suffix) are filtered out from
published failure domains so that machines are not placed into expensive
replicated metro zones by default.

## Control Plane Spreading

Control plane spreading across failure domains is handled directly by Cluster API
(`KubeadmControlPlane`). When scaling or rolling out control plane nodes, KCP inspects
existing control plane machines and assigns the least-populated failure domain to
`Machine.spec.failureDomain` (breaking ties deterministically).

For this automatic spreading to function correctly, the failure domain list in
`StackitCluster.status.failureDomains` must be accurately populated and marked with
`controlPlane: true`.

## Machine Placement and Defaulting

When provisioning a machine, the provider determines the target availability zone
using the following precedence:

1. **`Machine.spec.failureDomain`**: Honors the placement selected by Cluster API core
   (e.g., KCP spreading control plane nodes or a MachineDeployment configured with a
   failure domain).
2. **`StackitMachine.spec.availabilityZone`**: If explicitly configured on the
   `StackitMachine` (or via `StackitMachineTemplate`), this zone is used.
3. **Provider Defaulting**: When `failureDomain` is omitted, placement depends entirely on
   the infrastructure provider. If neither `Machine.spec.failureDomain` nor
   `StackitMachine.spec.availabilityZone` is specified (such as worker machines
   in a default `MachineDeployment`), the provider sorts the cluster's published
   failure domains alphabetically and selects the first zone (e.g., `eu01-1`). This
   matches standard Cluster API conventions (such as CAPG) and ensures VMs are never
   placed into the expensive STACKIT metro zone (`eu01-m`) by default.

The actual zone where the VM was placed is surfaced on `StackitMachine.status.availabilityZone`.

## Worker Node Spreading

Unlike the control plane, worker nodes do not automatically spread across failure domains
within a single `MachineDeployment`. Spreading worker nodes across AZs is an end-user
architecture decision made using the configuration fields on `MachineDeployment` or
`MachinePool` types.

The standard Cluster API pattern is to define multiple `MachineDeployment` resources (one
per availability zone), pinning each to a specific failure domain via
`spec.template.spec.failureDomain`.
