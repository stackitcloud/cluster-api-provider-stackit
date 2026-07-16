# Kubernetes Cluster API Provider STACKIT (CAPSTK)

<p align="center">
<img src="https://github.com/kubernetes/kubernetes/raw/master/logo/logo.png"  width="100x"><a href="https://stackit.com/"><img width="192x" src="https://raw.githubusercontent.com/stackitcloud/cluster-api-provider-stackit/refs/heads/main/docs/src/STACKIT_Logo_RGB_Regular_Navyblue-MZ.svg" alt="STACKIT - A Brand By Schwarz Digits"></a>
</p>

<p align="center">
    <a href="https://pkg.go.dev/github.com/stackitcloud/cluster-api-provider-stackit"><img src="https://pkg.go.dev/badge/github.com/stackitcloud/cluster-api-provider-stackit.svg" alt="Go Reference"></a>
</p>

------

Kubernetes-native declarative infrastructure for STACKIT.

## What is the Cluster API Provider STACKIT

The [Cluster API](https://cluster-api.sigs.k8s.io/introduction) brings declarative, Kubernetes-style APIs to cluster creation, configuration and management.

The API itself is shared across multiple cloud providers allowing for true STACKIT hybrid deployments of Kubernetes.

Cluster API Provider STACKIT is abbreviated as CAPSTK.

> ### ⚠️ WARNING ⚠️ 
> 
> **`cluster-api-provider-stackit` is not an official STACKIT project. It has been developed almost exclusively through AI-assisted tooling by a single person.**
> 
> The implementation is validated through end-to-end tests, but it has not yet received the same level of manual review, production hardening, or long-term operational validation as a mature provider.
> 
> Use at your own risk. Please review the code carefully before using it in production environments.

## Documentation

Please see our [book](https://stackitcloud.github.io/cluster-api-provider-stackit) for in-depth documentation.

Use `make serve-book` to serve the book locally from this repository.

## Launching a Kubernetes cluster on STACKIT

Check out the [Quick Start](./quick-start.md) for launching a cluster on STACKIT.

## Features

- [x] Native Kubernetes manifests and API
- [x] Doesn't use SSH for bootstrapping nodes.
- [x] Installs only the minimal components to bootstrap a control plane and workers.
- [x] Supports control planes and worker nodes on STACKIT VM instances.
- [x] Manages the bootstrapping of security groups and vm instances (networks are excluded for now).
- [x] [Optional Bastion hosts](./topics/accessing-vm-instances.md) for easier access of control plane or worker nodes
- [x] Tested Kubernetes Lifecycle (Scaling, Kubernetes Upgrades), see [E2E-Tests](./development/testing.md)
- [x] [ClusterClass Topology](https://cluster-api.sigs.k8s.io/tasks/experimental-features/cluster-class/)
- [x] Support varioius Linux Distributions (tested with [Ubuntu and Flatcar](./topics/images.md))
- [ ] Release distribution via OCI Images and Helm Charts
- [ ] Manage the bootstrapping of networks, security groups and vm instances.
  - [ ] Deploys Kubernetes control planes into private subnets with a separate bastion server.
- [ ] [SKE](https://stackit.com/de/produkte/runtime/stackit-kubernetes-engine) support

------

## Compatibility with Cluster API and Kubernetes Versions

This provider's versions are compatible with the following versions of Cluster API
and support all Kubernetes versions that is supported by its compatible Cluster API version:

|                          | Cluster API v1alpha4 (v0.4) | Cluster API v1beta1 (v1.x) |
| ------------------------ | :-------------------------: | :------------------------: |
| CAPSTK v1alpha1 `(main)` |              x              |             ✓              |

(See [Kubernetes support matrix](https://cluster-api.sigs.k8s.io/reference/versions.html) of Cluster API versions).


------

## Getting involved and contributing

Interested in contributing to cluster-api-provider-stackit? We welcome your ideas, contributions, and help. Feel free to reach out to the maintainers to learn how to get involved.

You do not need official write permissions to make an impact. We encourage active community members to take ownership, drive improvements, and help move the project forward.

We are also happy to welcome new maintainers over time. Get involved and show us what you can do!
