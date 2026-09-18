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
- `internal/planner` — the transition model. State is
  (rancher, local cluster, **every** downstream cluster); an edge moves exactly one
  component and is emitted only when BOTH endpoints are individually valid.
  Compatibility describes valid states; upgrade planning requires valid transitions,
  and conflating the two is what let the old code emit unsafe routes.

  **Validity is a conjunction over the whole fleet.** Rancher can only move when
  every attached cluster sits inside the target version's window. Asking the
  one-cluster question repeatedly gives answers that are individually true and
  collectively wrong, because each one assumes the others do not exist.

  **It is a linear walk, not a search** — `docs/fleet-planning-algorithm.md` has the
  measurements. Every move raises one component by one minor and nothing is
  downgraded, so every valid route to a destination has the same length: shortest-path
  buys nothing and only feasibility was ever in question. Search also does not scale
  (15,431 nodes at four clusters, past the old `maxNodes` of 10,000).

  **The trap, if you ever touch this:** do not raise a lagging cluster straight to the
  next window's floor. Raising v1.22 → v1.25 passes through v1.23 and v1.24, and each
  intermediate must be valid under the Rancher version *currently in effect*. That
  shortcut disagreed with exhaustive search on 18 of 36 valid starts on a
  gap-containing catalog. Every raise goes through `Edges`, one minor at a time.

  **Two different questions, both answered.** `destinations` is what is reachable
  after upgrades; `binding_constraint` is what the fleet can run *today* with no
  cluster upgrades, and which clusters hold it there. A fleet on v1.28/v1.30/v1.29 can
  reach 2.15.1 — 28 steps away — while only being able to run 2.10.12 right now.
  Reporting only the first would list seven destinations and never mention the
  pace-setters.
- `internal/api` — input normalization, the typed error contract, the response envelope.

Key components:

1. **Web Framework**: Uses Fiber v2 for high-performance HTTP handling
2. **Version Management**: Uses HashiCorp's go-version library for semantic version parsing and comparison
3. **Metrics**: Prometheus metrics on port 9090 (matching the chart; `METRICS_PORT` overrides locally)
4. **Frontend**: Static HTML/CSS/JS served from the `/static` directory

### Core Logic Flow

1. **Upgrade Path Calculation** (`GET /api/plan-upgrade`, query parameters):
   `rancher`, `local_platform`, `local_k8s`, and one or more downstream clusters as
   `downstream_platform_N` / `downstream_k8s_N` / `downstream_label_N` (indexes from 1,
   contiguous, capped at 8). Full contract in `docs/api.md`.

   **`downstream_platform` and `downstream_k8s` with no suffix are slot 1 and are
   permanently supported.** Not politeness to old links: `scripts/verify-deploy.sh`
   probes that exact URL in all six environments and fails the deploy if it stops
   returning destinations. Do not remove it as dead API surface.
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
   - **Both ends of a support window bind.** A cluster upgraded *ahead* of Rancher
     blocks a hop too, and unlike a lagging cluster it cannot be fixed by upgrading,
     because Kubernetes has no downgrade. Never phrase that case as something to
     upgrade. On this catalog ceilings rise monotonically, so the case arises only in
     the *starting* state — exactly what you get by upgrading a downstream cluster
     before Rancher.
   - **A fleet can be structurally unmanageable.** Windows are 3 to 6 Kubernetes
     minors wide, so a fleet spread wider than the widest window cannot be managed by
     *any* single Rancher version, in any order. This is a property of the domain, not
     of this implementation: v1.28 together with v1.34 is 7 minors and no catalog
     entry contains it. The planner returns a terminal blocker carrying the arithmetic,
     because "no route found" reads as a tool limitation rather than a fact about the
     cluster estate.
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

**Window-shape properties, checked but not enforced.** `catalog.CheckWindows` reports
three properties per platform across Rancher versions: floors non-decreasing (P1),
ceilings non-decreasing (P2), and consecutive windows overlapping (P3). The planner
deliberately does **not** rely on any of them — it validates every intermediate state —
so a violation is logged at startup and surfaced in the data-sync pull request rather
than being fatal. A P3 gap is a true fact about upstream with an operational meaning
(no fleet can cross that Rancher hop at all), and taking the pod down over it would
trade a correct answer for no answer.

Comparison is by **minor**, not full version. Clusters move along the minor axis, and
full-version comparison produces a false positive on real data: semver sorts
`v1.17.17-rancher2-4` *below* `v1.17.17`, because a suffix is a prerelease.

One known real violation is allowlisted in `catalog_data_test.go` with its evidence:
Rancher 2.7.0 genuinely narrowed EKS support relative to 2.6.14. A *new* violation
fails the test.

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

**Do not gate a deploy on any ArgoCD revision field.** Two properties make them
unusable for "did the right thing deploy", and both produced a false green here:

- `.status.sync.revision` reports the revision that was **requested**, not the one
  that synced. mst reported `sync.revision: v221`, `Healthy`, `Succeeded` while
  `syncResult.revision` was `v216` and the pods were running image `v216`.
- For a working OCI source that same field is a `sha256:` **digest**, not a chart
  version, so a version-equality check against it can never match a correct
  deploy either.

`scripts/verify-deploy.sh` therefore gates on, in order: the `repoURL` prefix (a
source migration that never reaches the live Application is otherwise invisible),
`targetRevision` equal to the chart version just published, `Synced` + `Healthy` +
`Succeeded`, then **`GET <host>/version` equal to the app version just built**, then
a known-answer API probe. The `/version` check is load-bearing: it observes the
running code rather than the intent to run it, and needs no cluster credentials.

`Synced` is required, **not** waived. An earlier version of this script treated
`Unknown` as expected-and-permanent for OCI sources; that is false on this cluster
(every other OCI-sourced Application reports `Synced`), and mst reported `Unknown`
precisely *because* it had been asked for a chart version that did not exist.
Waiving it is what let that bug survive a full pipeline run.

**Deploy applies the whole Application, never patches one field.** Patching only
`targetRevision` freezes the rest of the source, so the move to Harbor had no
effect on any live environment for two full runs while `argocd/*.yaml` said
otherwise.

**Every environment is publicly routable** -- `mst|dev|qas|tst|stg.rancher.tips`
and `rancher.tips` -- so verification never needs a port-forward. Port-forwarding
was tried and failed twice: once racing the rollout the gate was waiting for, once
on RBAC the CI ServiceAccount does not have.

### Routes

- `GET /` -- the single-page UI (static)
- `GET /api/plan-upgrade` -- the planner; query parameters above
- `GET /healthz` -- liveness/readiness, returns `OK`
- `GET /version` -- build identity and catalog freshness. `version` is linked in
  at build time from the Dockerfile's `VERSION` arg
  (`-ldflags "-X main.buildVersion=..."`). It exists so a deploy can be verified
  from outside the cluster; see the deployment notes above.
- `:9090/metrics` -- Prometheus

## Important Considerations

1. **Version Parsing**: Always use semantic versioning (e.g., "v2.9.4" or "2.9.4")
2. **Error Handling**: The application returns appropriate HTTP status codes and error messages
3. **Metrics**: Custom Prometheus metrics track request patterns and performance
4. **Health Checks**: Available at `/healthz` endpoint