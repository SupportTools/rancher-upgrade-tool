# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

This is the **Rancher Upgrade Tool** - a Go-based web service that helps users plan safe upgrade paths for Rancher and Kubernetes across multiple platforms (RKE1, RKE2, K3s, AKS, EKS, GKE).

## Build and Development Commands

### Core Development Commands
```bash
# Build Docker image
make build

# Run linting and static analysis
make lint

# Run security scanning
make security

# Run tests
make test

# Format code
make fmt

# Run all checks, build, and push
make all

# Build and push multi-platform images
make buildx
```

### Running Locally
```bash
# Install dependencies
go mod tidy

# Run the application
go run main.go

# The service starts on port 3000 (app + API) and 9090 (metrics).
# METRICS_PORT overrides the metrics port for local work.
```

## Architecture Overview

The application is a Go service. `main.go` is wiring only; the substance lives in
three internal packages:

- `internal/catalog` — the dataset, its types, and the fail-closed loader. A malformed
  entry stops the service rather than being dropped.
- `internal/planner` — the transition graph. State is
  (rancher, localPlatform+localK8s, downstreamPlatform+downstreamK8s); an edge moves
  exactly one component and is emitted only when BOTH endpoints are individually
  valid. Compatibility describes valid states; upgrade planning requires valid
  transitions, and conflating the two is what let the old code emit unsafe routes.
- `internal/api` — input normalization, the typed error contract, the response envelope.

Key components:

1. **Web Framework**: Uses Fiber v2 for high-performance HTTP handling
2. **Version Management**: Uses HashiCorp's go-version library for semantic version parsing and comparison
3. **Metrics**: Integrated Prometheus metrics on port 9000
4. **Frontend**: Static HTML/CSS/JS served from the `/static` directory

### Core Logic Flow

1. **Upgrade Path Calculation** (`GET /api/plan-upgrade`, query parameters):
   `rancher`, `local_platform`, `local_k8s`, `downstream_platform`, `downstream_k8s`.
   Versions are accepted with or without a leading `v` on every parameter.
   - Validates every parameter against the catalog before planning, returning a typed
     400 naming the field rather than an empty plan.
   - Loads and validates `data/catalog.json` once at startup, failing closed.
   - Walks the transition graph and returns EVERY reachable Rancher version with its
     own ordered route, each step carrying its source URL and as-of date.
   - Returns blockers, never a bare empty list. An undifferentiated empty result reads
     to a user as "you are already current", which is the most dangerous answer this
     service can give.
   - Full schema: `docs/api.md`.

2. **Platform-Specific Rules**:
   - Kubernetes minor versions must never be skipped, on any platform or axis.
     Example: v1.24 → v1.25 → v1.26, never v1.24 → v1.26.

     Source: https://kubernetes.io/releases/version-skew-policy/ — "Project policies
     for API deprecation and API change guidelines require `kube-apiserver` to not
     skip minor versions when upgrading, even in single-instance clusters."

     Enforced in `internal/planner` and asserted by
     `TestRegression_NoKubernetesMinorSkipped`, plus an end-to-end check over the
     real dataset. It was previously a hardcoded constant with no citation and no test.
   - The same rule applies to the Rancher axis, and adjacency there is NUMERIC, not
     catalog order: a Rancher minor absent from the catalog produces a blocker rather
     than a hop across it, even when both endpoints validate.
   - Not enforced, and captured only as notes because the tool does not collect
     per-node input: kubelet and kube-proxy may be up to three minor versions older
     than kube-apiserver (two below 1.25); control-plane components may be one minor
     older; kubectl is supported within one minor either way.

### Key Data Structure

`data/catalog.json` is the compatibility dataset. It is generated, validated on
load, and the service refuses to start if it is malformed.

```json
{
  "generated_at": "2026-09-17",
  "rancher": [
    {
      "version": "2.11.3",
      "lifecycle": "limited",
      "lifecycle_as_of": "2026-09-17",
      "platforms": [
        {
          "platform": "rke2",
          "min_version": "v1.30",
          "max_version": "v1.32",
          "granularity": "minor",
          "releases": [],
          "source_url": "https://www.suse.com/suse-rancher/support-matrix/...",
          "as_of": "2026-09-17"
        }
      ]
    }
  ]
}
```

One range per (rancher, platform), governing BOTH the cluster Rancher is installed
on and the clusters it manages. The T0 source spike found those identical for every
Rancher version sampled, and the validator asserts it so a future divergence fails
loudly rather than being assumed away.

`granularity` is `release` or `minor`. RKE2 and k3s can be `release`, because KDM
publishes real release lists. AKS, EKS and GKE are `minor`, because SUSE stopped
publishing exact hosted releases after roughly Rancher 2.7.5. A `minor` entry must
carry no releases; synthesizing them is the defect `getMinorVersionsBetween` embodied.

`data/upgrade-paths.json` is the superseded original, kept for reference.
`scripts/migrate-upgrade-paths.py` produced the catalog from it.

## Deployment

The application is deployed using:
- **Docker**: Multi-stage build on a pinned Alpine base. Image
  `docker.io/supporttools/rancher-upgrade-tool:v<run_number>`.
- **Helm**: Chart source in `/charts/rancher-upgrade-tool/`, published as an **OCI
  artifact to Harbor**: `oci://harbor.support.tools/rancher-upgrade-tool/charts`.
  Moved off the git-backed museum at charts.support.tools, which needed a `BOT_TOKEN`
  that expired and blocked deploys entirely.
- **ArgoCD**: `/argocd/` holds one Application per environment (mst, dev, qas, tst,
  stg, prd), all pulling the chart from Harbor.

Two things about the chart worth knowing before you touch the pipeline:

1. **Chart versions must be SemVer2.** OCI registries reject anything else. The old
   scheme produced `v219`, which has no minor or patch; it is now
   `v0.<run_number>.0`. A leading `v` is fine, Helm strips it.
2. **Authenticate with `docker/login-action`, never `helm registry login`.** Harbor's
   token service satisfies Docker's auth handshake but rejects Helm's basic-auth
   probe against `/v2/`, so `helm registry login` fails on correct credentials.
   `helm push` reads `~/.docker/config.json` transparently.

**ArgoCD sync status will read `Unknown` forever**, and that is expected: ArgoCD
cannot compute a diff for an OCI Helm source. `scripts/verify-deploy.sh` therefore
gates on **sync revision equality plus health plus a live known-answer probe**, not
on `Synced`. Revision equality is the check that matters; it is what stops a healthy
*old* revision from reporting a successful deploy.

## Important Considerations

1. **Version Parsing**: Always use semantic versioning (e.g., "v2.9.4" or "2.9.4")
2. **Error Handling**: The application returns appropriate HTTP status codes and error messages
3. **Metrics**: Custom Prometheus metrics track request patterns and performance
4. **Health Checks**: Available at `/healthz` endpoint