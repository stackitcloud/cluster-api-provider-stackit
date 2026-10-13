# Overview

Cluster API Provider STACKIT (CAPSTK) creates STACKIT infrastructure for
Cluster API workload clusters.
The management cluster usually runs locally with kind, while workload-cluster
machines run as STACKIT Compute Engine servers.

Provider stack:

| Role | Provider |
| --- | --- |
| Core | `cluster-api` |
| Bootstrap | kubeadm / CABPK |
| Control plane | kubeadm / KCP |
| Infrastructure | `cluster-api-provider-stackit` / CAPSTK |

The provider does not implement its own bootstrap or control-plane logic. It
receives bootstrap data from Cluster API bootstrap Secrets and passes it to
STACKIT servers as cloud-init user data.

## MVP Flow

The target flow is:

```sh
kind create cluster --name capi-stackit
clusterctl init \
  --config hack/clusterctl-local.yaml \
  --bootstrap kubeadm \
  --control-plane kubeadm
make install
make run
kubectl apply -f rendered-cluster.yaml
```

The expected result is a Cluster API `Cluster`, `Machine` resources,
`StackitCluster`, and `StackitMachine` resources that become ready, with all
provider-created STACKIT resources deleted when the Cluster is deleted.

## Production Readiness Gaps

The provider is a solid MVP, but it is not production-ready yet. It can use
existing STACKIT networks, create and delete VMs, set provider IDs, and manage
an optional API server load balancer. The remaining gaps are mostly around
broader workload-cluster readiness coverage, release/distribution, and Cluster
API contract details.

- Workload-cluster readiness is covered by the NodeRef e2e path for 1
  control-plane / 1 worker clusters. The default cluster template installs
  `cloud-provider-stackit` through a Cluster API `ClusterResourceSet`, and the
  NodeRef e2e verifies `StackitMachine`, CAPI `Machine`, and workload `Node`
  provider ID alignment. The template does not install a CNI; users must install
  one separately after the workload API is reachable. The repository provides a
  `make install-workload-cni` helper for repeatable development installs.
- The release story is not finalized. Local `clusterctl` assets can be
  generated, but the final distribution path is still open. Production use
  needs versioned images, published `clusterctl` assets, upgrade guidance,
  support matrix, and preferably image signing / SBOMs.
- Existing cloud resources are currently required. The provider does not create
  STACKIT networks, security groups, SSH keys, or images. Production users need
  a clear prerequisite flow, for example Terraform modules or explicit
  documented setup steps, unless the provider grows first-class management for
  those resources.
- Failure domains are discovered dynamically from STACKIT IaaS availability
  zones, filtering out virtual metro zones (`-m`) so VMs are not placed into
  expensive replicated zones by default.
- Admission webhooks are implemented for mutating defaulting and validating
  immutability, safe updates, and spec constraints.
- Operational hardening is still needed. The manager deployment is still close
  to the Kubebuilder scaffold in places, and production use should define
  resource sizing, metrics TLS, HA/replica behavior, alerting, and runbooks.

## Full Cluster API Support

For full Cluster API support, the provider should complete the following areas:

- Continue hardening ClusterClass support. Topology create/ready/delete e2e now
  covers a 1 control-plane / 1 worker workload cluster, including template
  metadata propagation to generated STACKIT infrastructure objects. Highly
  available topology variants still need focused coverage.
- Keep the `cloud-provider-stackit` integration aligned with Kubernetes minor
  versions. Supported workload cluster minors are currently v1.33.x through
  v1.36.x, and the cloud-provider image minor must match the workload cluster
  Kubernetes minor.
- Harden scale, upgrade, remediation, and deletion behavior. Worker scale,
  worker upgrade, and single-control-plane upgrade coverage exists, but full
  support should include highly available control-plane replacement,
  MachineHealthCheck remediation, interrupted deletes, orphan cleanup, and
  cloud-side drift handling.

The relevant Cluster API contract areas are the InfraCluster and InfraMachine
contracts. `StackitCluster` must provide the control plane endpoint,
initialization state, failure domains, and conditions. `StackitMachine` must
provide the provider ID, initialization state, and should surface addresses and
failure-domain placement where available.
