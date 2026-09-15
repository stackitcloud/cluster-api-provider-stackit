# Release packaging

Build the GitHub Release assets locally:

```sh
make release IMG=<registry>/cluster-api-provider-stackit:<tag>
```

This creates a flat `dist/release/` directory containing:

- `infrastructure-components.yaml`
- `metadata.yaml`
- `clusterclass.yaml`
- `cluster-template.yaml`
- `cluster-template-bastion.yaml`
- `cluster-template-development.yaml`
- `cluster-template-flatcar-workers.yaml`
- `cluster-template-topology.yaml`
- `cilium-values.yaml`
- `cloud-provider-stackit.yaml`
- `install.yaml`
- `checksums.txt`

`infrastructure-components.yaml` is the Cluster API provider manifest used by
`clusterctl`. `install.yaml` is the standalone installer for `kubectl apply`.

For local clusterctl repository development, use:

```sh
make clusterctl-release IMG=<registry>/cluster-api-provider-stackit:<tag>
```

---

# Release And Distribution Implementation Plan

This plan turns the current local-only release packaging into a production-ready
release flow for Cluster API Provider STACKIT (CAPSTK). The repo already has
`make clusterctl-release`, `make build-installer`, `metadata.yaml`, clusterctl
templates under `templates/`, and release docs that explicitly call this work
unfinished.

## Current State

- `Makefile` can generate local clusterctl assets under
  `dist/clusterctl/infrastructure-stackit/<version>/`.
- `metadata.yaml` maps release series `0.1` to the Cluster API `v1beta2`
  contract.
- `hack/clusterctl-local.yaml` points clusterctl at a local provider repository,
  but currently references `latest` while the Makefile writes a concrete
  version directory.
- CI has lint, unit/envtest, and kind e2e workflows only.
- `go.mod` currently uses Cluster API `v1.13.2` and Kubernetes libraries
  `v0.35.4`.
- Production docs say the release path is unfinished and needs versioned images,
  published clusterctl assets, upgrade guidance, support matrix, image signing,
  and SBOMs.

## Upstream Requirements And Guidance

- Cluster API providers must publish a provider repository with
  `metadata.yaml`, a components YAML, and optional cluster templates. For an
  infrastructure provider, the recommended components asset name is
  `infrastructure-components.yaml`.
- Clusterctl local repositories use
  `infrastructure-<provider>/<semver>/...`; each version folder must contain
  the components, metadata, and templates for that release.
- `metadata.yaml` documents release series to Cluster API contract mappings and
  is strictly validated by newer clusterctl versions.
- `clusterctl upgrade` upgrades provider components and CRDs/controllers, but
  it does not upgrade workload Cluster API objects or workload Kubernetes
  versions.
- Sigstore Cosign supports keyless container signing through OIDC in GitHub
  Actions. Sign images by digest, not by tag.
- GitHub artifact attestations and/or Cosign attestations can publish provenance
  and SBOM attestations.

Useful references:

- https://cluster-api.sigs.k8s.io/developer/providers/contracts/clusterctl
- https://cluster-api.sigs.k8s.io/clusterctl/commands/upgrade
- https://cluster-api.sigs.k8s.io/reference/versions
- https://docs.sigstore.dev/cosign/signing/signing_with_containers/
- https://github.com/actions/attest

## Implementation Steps

### 1. Choose The Distribution Contract

Use GitHub Releases as the clusterctl provider repository and GHCR as the
controller image registry.

Primary distribution:

- `clusterctl init --infrastructure stackit:<version>` using release assets.
- Controller image published to `ghcr.io/<owner>/cluster-api-provider-stackit`.

Secondary distribution:

- Attach `install.yaml` to the same release for users who do not use
  clusterctl.

Keep Helm out of scope unless maintainers explicitly choose it later.

Update:

- `docs/src/development/release-packaging.md`
- `docs/src/quick-start.md`
- `README.md`

### 2. Harden Clusterctl Packaging

Update `make clusterctl-release` so `CLUSTERCTL_RELEASE_VERSION` is derived from
the release tag in CI instead of defaulting permanently to `v0.1.0`.

Required generated assets:

- `metadata.yaml`
- `infrastructure-components.yaml`
- `cluster-template.yaml`
- `cluster-template-bastion.yaml`
- `cluster-template-development.yaml`
- `cluster-template-topology.yaml`
- `clusterclass.yaml`
- `addons/*.yaml`

Fix the local repository mismatch:

- Either generate a `latest` symlink/copy under `dist/clusterctl/infrastructure-stackit/latest`.
- Or update `hack/clusterctl-local.yaml` to point at the explicit
  `CLUSTERCTL_RELEASE_VERSION`.

Add `make verify-clusterctl-release`:

- Generate assets.
- Create or reuse an isolated kind cluster.
- Run `clusterctl init` against `hack/clusterctl-local.yaml`.
- Verify the provider Deployment, CRDs, and metadata are accepted.

### 3. Publish Versioned Images

Add `.github/workflows/release.yml`, triggered by `v*` tags.

Build and publish multi-architecture images:

- Required: `linux/amd64`, `linux/arm64`.
- Optional: `linux/s390x`, `linux/ppc64le` only after explicit test coverage.

Publish immutable tags:

- `ghcr.io/<owner>/cluster-api-provider-stackit:<tag>`
- Optional stable aliases: `<major>.<minor>` and `latest` for stable releases
  only.

Render release manifests with the exact version tag or digest. Prefer digest
pinning if clusterctl/template UX remains acceptable.

### 4. Publish Release Assets

Release workflow sequence:

1. Checkout full history.
2. Setup Go from `go.mod`.
3. Run `make test`.
4. Build and push the multi-arch image.
5. Run:

   ```sh
   make clusterctl-release \
     IMG=ghcr.io/<owner>/cluster-api-provider-stackit:<tag> \
     CLUSTERCTL_RELEASE_VERSION=<tag>
   ```

6. Run:

   ```sh
   make build-installer IMG=ghcr.io/<owner>/cluster-api-provider-stackit:<tag>
   ```

7. Generate checksums for all release assets.
8. Upload assets to the GitHub Release.

Add a PR dry-run workflow that verifies image build and asset generation without
publishing anything.

### 5. Add Signing, SBOMs, And Provenance

Use GitHub Actions OIDC with keyless Sigstore/Cosign.

Workflow permissions needed:

```yaml
permissions:
  contents: write
  packages: write
  id-token: write
  attestations: write
  artifact-metadata: write
```

Image signing:

- Install Cosign.
- Sign the pushed image by digest.
- Publish verification instructions in docs.

SBOM:

- Generate SPDX JSON and CycloneDX JSON using Syft or Docker BuildKit SBOM
  support.
- Upload SBOM files as release assets.
- Attach SBOM attestations to the image.

Provenance:

- Publish build provenance through GitHub artifact attestations and/or Cosign
  attestations.

Docs must include examples:

```sh
cosign verify ghcr.io/<owner>/cluster-api-provider-stackit@sha256:<digest>
cosign verify-attestation ghcr.io/<owner>/cluster-api-provider-stackit@sha256:<digest>
sha256sum -c checksums.txt
```

### 6. Publish Provider Upgrade Guidance

Create `docs/src/usage/provider-upgrade.md`.

Cover management-cluster provider upgrades:

```sh
clusterctl upgrade plan
clusterctl upgrade apply --infrastructure stackit:<version>
```

State explicitly:

- Clusterctl upgrades provider components.
- Clusterctl does not upgrade workload `Cluster`, `MachineDeployment`,
  `Machine`, or Kubernetes versions.
- Workload Kubernetes upgrades are handled separately through CAPI object
  changes.

Move or rewrite `cluster-upgrade.md` into the docs tree as workload Kubernetes
upgrade guidance.

Initial support policy:

- Support adjacent provider minor upgrades only.
- Do not support skip-minor upgrades until validated by e2e tests.
- Pre-release versions require explicit version selection for all providers.

### 7. Create The Support Matrix

Fill `docs/src/topics/reference/versions.md`.

Matrix columns:

- CAPSTK version.
- Cluster API version.
- Cluster API contract.
- Management Kubernetes versions.
- Workload Kubernetes versions.
- Cloud provider STACKIT image version requirement.
- Tested CNI versions.
- Tested STACKIT regions.
- Tested architectures.
- Upgrade paths validated by e2e.

Seed current values:

- CAPSTK `v0.1.x`
- Cluster API `v1.13.x`
- Contract `v1beta2`
- Kubernetes libraries `v0.35.x`
- Workload cluster minors already documented as `v1.33.x` through `v1.36.x`
  until validated/updated.

Make the support matrix release-owned: every release must update or explicitly
confirm it.

### 8. Add Release Validation Gates

Create a release checklist and encode as much as possible in CI.

Required gates:

- `make lint`
- `make test`
- generated files clean after `make manifests generate`
- `make clusterctl-release`
- `make build-installer`
- local `clusterctl init` from generated assets
- release asset checksums generated
- image pushed by immutable tag
- image signed by digest
- SBOM generated and attached

Billable e2e gates for release candidates:

- `make test-e2e`
- `make test-e2e-workload-noderef`
- `make test-e2e-workload-scale`
- `make test-e2e-workload-upgrade-workers`
- `make test-e2e-workload-upgrade-control-plane`
- `make test-e2e-workload-topology`

Run billable e2e only in a dedicated STACKIT project with cleanup permissions
and cost controls.

### 9. Fix Contract And Documentation Mismatches Before First Production Release

Verify CRD contract metadata:

- Docs claim CRDs carry `cluster.x-k8s.io/v1beta2: v1alpha1`.
- Generated CRDs should be checked and fixed if that label is missing.
- `metadata.yaml` contract must match the actual CRD contract metadata.

Add CAPI RBAC aggregation if missing:

- Ship roles with `cluster.x-k8s.io/aggregate-to-manager: "true"` where
  required so CAPI core controllers can manage provider resources correctly.

Validate ClusterClass behavior:

- Existing topology e2e covers a small topology cluster.
- Add release validation for template metadata and HA topology variants before
  claiming broader production ClusterClass support.

## Acceptance Criteria

The release story is complete when an AI agent or maintainer can:

1. Tag a release.
2. CI builds and publishes a versioned multi-arch image.
3. CI signs the image by digest.
4. CI generates SBOMs and provenance.
5. CI publishes clusterctl assets and `install.yaml` to a GitHub Release.
6. A fresh kind management cluster can run `clusterctl init` from the published
   release assets.
7. Docs explain install, verify, upgrade, support matrix, and workload
   Kubernetes upgrade behavior.
8. Release validation covers unit/envtest, local clusterctl init, and the
   selected billable STACKIT e2e paths.
