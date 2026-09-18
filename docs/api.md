# API

## Versioning decision

The existing route is replaced in place rather than versioned. `static/app.js` is
the only known consumer, so a breaking change is close to free, and shipping
`/api/v2/` alongside a `/api/` that still returns advice capped at Rancher 2.9.2
would mean operating two answers, one of them known-wrong.

## `GET /api/plan-upgrade`

Query parameters, all required except `target`:

| Param | Example | Meaning |
|---|---|---|
| `local_platform` | `rke2` | distribution the Rancher server itself runs on |
| `local_k8s` | `v1.28` | that cluster's Kubernetes version |
| `downstream_platform` | `eks` | distribution of the managed cluster being planned |
| `downstream_k8s` | `v1.28` | that cluster's Kubernetes version |
| `rancher` | `2.9.4` | current Rancher version |

Versions are accepted with or without a leading `v`, on every parameter. The old
UI taught both conventions at once (`e.g., 2.6.8` for Rancher, `e.g., v1.23` for
Kubernetes) while the map lookup only matched one, so `v2.9.4` silently produced
an empty plan.

### More than one downstream cluster

One Rancher manages many clusters, and it can only move when **every** attached
cluster sits inside the target version's support window. Asking the single-cluster
question once per cluster gives answers that are individually true and collectively
wrong, because each one assumes the others do not exist.

Additional clusters are supplied with an **index suffix**:

| Param | Example | Meaning |
|---|---|---|
| `downstream_platform_N` | `eks` | distribution of cluster N |
| `downstream_k8s_N` | `v1.30` | cluster N's Kubernetes version |
| `downstream_label_N` | `prod-eu` | optional display name for cluster N |

Indexes start at 1 and must be contiguous. `downstream_platform` with no suffix is
the same slot as `downstream_platform_1`; supplying both with **different** values is
a `conflicting-parameter` error rather than a silent precedence rule.

```
/api/plan-upgrade?rancher=2.9.4
  &local_platform=rke2&local_k8s=v1.28
  &downstream_platform_1=rke2&downstream_k8s_1=v1.28&downstream_label_1=Cluster%201
  &downstream_platform_2=rke2&downstream_k8s_2=v1.30&downstream_label_2=Cluster%202
  &downstream_platform_3=rke2&downstream_k8s_3=v1.29&downstream_label_3=Cluster%203
```

**Why indexed rather than packed or repeated.** A packed
`downstream=rke2:v1.28:label` needs escaping rules invented for a delimiter that can
appear in a label. Repeating bare `downstream_platform` and `downstream_k8s` pairs
relies on positional matching of repeated keys, which silently misaligns the moment a
client drops or reorders one, and Fiber's `Query` returns only the first value anyway.
Indexed parameters cost verbosity and buy an unambiguous parse and an error that can
name `downstream_k8s_3` exactly.

**Labels are display-only.** They are echoed back in the response and never reach a
metric label, because they are free-form strings on a public unauthenticated endpoint.
Omitted labels default to `Cluster N`.

**Cap: 8 clusters.** Not a compute limit — the planner is a linear walk, see
`docs/fleet-planning-algorithm.md` — but a bound on response size and metric
cardinality. Exceeding it is a typed 400 stating the cap.

**The single-pair form is permanently supported.** `downstream_platform` and
`downstream_k8s` with no suffix describe a one-element fleet and produce exactly the
routes they produce today. This is not only politeness to old links:
`scripts/verify-deploy.sh` probes that exact URL in all six environments and fails the
deploy if it stops returning destinations. It is load-bearing infrastructure, not dead
API surface — do not remove it as cleanup.

Query parameters replace path parameters. Path segments carrying raw user input
had to be percent-encoded by every caller, and `static/app.js` did not do it.

### 200 — a plannable state

```json
{
  "scope": "This answer covers one local cluster and one downstream cluster...",
  "claim": "version-compatibility",
  "claim_note": "Endpoint compatibility does not prove a safe upgrade...",
  "prerequisites_url": "https://ranchermanager.docs.rancher.com/...",
  "start": {
    "rancher": "2.9.4",
    "local": {"platform": "rke2", "k8s": "v1.28", "lifecycle": "ended"},
    "clusters": [
      {"id": "c1", "label": "Cluster 1", "platform": "eks", "k8s": "v1.28"}
    ],
    "downstream": {"platform": "eks", "k8s": "v1.28"}
  },
  "destinations": [
    {
      "rancher": "2.10.7",
      "lifecycle": "ended",
      "lifecycle_as_of": "2026-09-17",
      "granularity": "minor",
      "granularity_note": "One or more platforms on this route publish minor lines only...",
      "steps": [
        {
          "kind": "downstream-k8s",
          "cluster": "c1",
          "cluster_label": "Cluster 1",
          "wave": 1,
          "platform": "eks",
          "from": "v1.28",
          "to": "v1.29",
          "source_url": "https://www.suse.com/...",
          "as_of": "2026-09-17",
          "caveats": []
        }
      ]
    }
  ],
  "blockers": []
}
```

`destinations` is every reachable Rancher version, each with its own ordered route,
ascending. There is no target parameter in the common case: the tool returns the
whole reachable set rather than marching to the newest release, because "upgrade to
the latest thing" is an opinion, not an answer.

`kind` is one of `rancher`, `local-k8s`, `downstream-k8s`. Exactly one component
moves per step.

`cluster` identifies **which** cluster a step touches: `local` for a `local-k8s` step,
`c1`/`c2`/... for a downstream one, and absent on a `rancher` step. Downstream steps
are not given per-cluster `kind` values, so a consumer that only understands
`downstream-k8s` keeps working; it simply cannot tell the clusters apart.
`cluster_label` is the display name and is never authoritative — match on `cluster`.

`wave` groups steps that have **no ordering requirement between them**. A Rancher hop
always starts a new wave, and every cluster raise between two hops shares one. This
exists because the planner moves one component per step, which serialises work that is
genuinely independent: rendering three cluster upgrades as steps 4, 5, 6 implies a
queue that does not exist and inflates how long the plan looks.

`wave` is a statement about **dependency, not safety**. Concurrent cluster upgrades
have real operational cost — control-plane load on the Rancher cluster, node drain
capacity — that this tool does not model. "These steps may be done in any order" is
the claim; "it is safe to run them simultaneously" is not.

`start.clusters` is the canonical fleet. `start.downstream` is retained as a mirror of
`clusters[0]`, and **only** when exactly one cluster was supplied, so that responses to
the single-pair form stay byte-identical to what they are today. It is deprecated and
will be removed once the frontend reads `clusters` (feature 8, tasks F10-F12).

`granularity` is `release` or `minor`, and is the weakest granularity on the route.
RKE2 and k3s plan release-to-release from KDM, which publishes real releases. AKS,
EKS and GKE plan minor-to-minor, because SUSE stopped publishing exact hosted
releases after roughly Rancher 2.7.5. The asymmetry is in the sources; the response
states it rather than letting one look as precise as the other.

### 200 — valid input, nothing plannable

Same envelope. `destinations` is `[]` and `blockers` is non-empty. **Never a bare
empty list.** An undifferentiated empty result reads as "you are already current",
which for someone sitting on an unsupported version is the most dangerous answer
this service can give.

```json
{
  "destinations": [],
  "blockers": [
    {
      "kind": "unsupported-start",
      "constraint": "Rancher 2.9.4 supports rke2 v1.27-v1.30 on the local cluster",
      "detail": "The local cluster is on v1.21, outside that range. This describes the state you are in; it does not establish that no route out exists.",
      "source_url": "https://www.suse.com/..."
    }
  ]
}
```

`kind` is one of:

- `unsupported-start` — the starting state is not supported, so no edges exist by
  construction. A statement about where you are, **not** proof that no route out
  exists.
- `missing-evidence` — the catalog cannot establish a route. Includes a Rancher
  minor being absent, which blocks rather than being hopped over.
- `incompatible` — a real, evidenced incompatibility.
- `cluster-ahead-of-window` — a cluster is **above** a target Rancher version's
  ceiling. Terminal for that destination: Kubernetes has no downgrade, so no sequence
  of upgrades fixes it. The destination is refused and named, not planned around.
- `fleet-spread-exceeds-window` — the fleet spans more Kubernetes minors than the
  widest support window in the catalog, so **no** Rancher version can manage all of it
  at once, in any order. Terminal, and a property of the fleet rather than of a route.

Both fleet kinds carry the arithmetic, because "no route found" reads as a limitation
of the tool rather than a fact about the cluster estate:

```json
{
  "kind": "fleet-spread-exceeds-window",
  "constraint": "This fleet spans v1.28 to v1.34, which is 7 Kubernetes minors",
  "detail": "The widest support window in the catalog is 6 minors (Rancher 2.9.12, rke2 v1.27-v1.31). No single Rancher version can manage every cluster at once. Close the gap by raising the clusters furthest behind before planning a Rancher upgrade.",
  "source_url": "https://www.suse.com/..."
}
```

### `binding_constraint`

Present when at least one destination is reachable but the **next** Rancher version
beyond the furthest reachable one is not. Names what is holding the fleet back:

```json
{
  "binding_constraint": {
    "blocked_rancher": "2.11.17",
    "requires_k8s": "v1.30",
    "clusters": [
      {"id": "c1", "label": "Cluster 1", "k8s": "v1.28", "position": "behind"},
      {"id": "c3", "label": "Cluster 3", "k8s": "v1.29", "position": "behind"},
      {"id": "local", "label": "Local cluster", "k8s": "v1.28", "position": "behind"}
    ]
  }
}
```

`position` is `behind` (below the floor — fixable by upgrading) or `ahead` (above the
ceiling — **not** fixable, since Kubernetes has no downgrade). The distinction is the
whole point: telling someone to upgrade a cluster that is already too new is worse
than saying nothing.

With one lagging cluster the wording names it as *the* constraint on the fleet. With
several, they set the pace together and are all listed; the API does not pick one
arbitrarily.

### `windows`

The supported Kubernetes range per candidate Rancher version, for the platforms
actually present in the request, so a client can show which versions could hold the
whole fleet without a second call:

```json
{
  "windows": [
    {"rancher": "2.10.12", "lifecycle": "ended",   "platform": "rke2", "min": "v1.28", "max": "v1.31"},
    {"rancher": "2.11.17", "lifecycle": "limited", "platform": "rke2", "min": "v1.30", "max": "v1.32"}
  ]
}
```

A mixed-platform fleet gets one entry per (rancher, platform) pair, because the window
differs by platform. A client drawing a single axis must therefore draw one bar per
platform present, not one bar per Rancher version.

### 400 — input the service cannot read

```json
{
  "error": {
    "kind": "invalid-version",
    "field": "rancher",
    "value": "nonsense",
    "detail": "\"nonsense\" is not a valid version"
  }
}
```

`kind` is one of `invalid-version`, `unknown-platform`, `unknown-rancher-version`,
`missing-parameter`, `conflicting-parameter`, `too-many-clusters`.

Unparseable input previously returned **500**, which told the user the server broke
when they had typed something wrong.

For a fleet request the error names the **cluster**, not just the field, because
`"v1.3o" is not a valid version` is unactionable when three clusters were submitted:

```json
{
  "error": {
    "kind": "invalid-version",
    "field": "downstream_k8s_3",
    "cluster": "c3",
    "cluster_label": "Cluster 3",
    "value": "v1.3o",
    "detail": "Cluster 3: \"v1.3o\" is not a valid version"
  }
}
```

`conflicting-parameter` covers supplying both `downstream_platform` and
`downstream_platform_1` with different values. `too-many-clusters` states the cap.

### Mixed platforms and mixed granularity

A fleet may mix platforms — RKE2 local managing EKS and RKE2 downstream is an ordinary
install. No new rule is needed for granularity: a destination's `granularity` is
already the **weakest** on the route, so one hosted-provider cluster makes the whole
route `minor` and `granularity_note` says why. Per-cluster precision is still readable
from each step's `platform`.

This is honest rather than convenient. SUSE stopped publishing exact hosted releases
after roughly Rancher 2.7.5, so an EKS cluster genuinely cannot be planned
release-to-release, and a route that mixes it with RKE2 cannot claim to be more precise
than its least precise leg.

### `scope`

The single-cluster wording — "this answer covers one local cluster and one downstream
cluster" — becomes a statement about the submitted fleet: the answer covers exactly the
clusters listed in `start.clusters`, and any cluster *not* submitted is unaccounted for.
That caveat gets more important with a fleet, not less: a Rancher upgrade affects every
attached cluster, and the tool cannot know about the ones nobody typed in.

### Platform-specific responses

An `rke1` platform returns 200 with an end-of-life notice in `blockers`, pointing at
RKE2 migration. RKE1 left the Rancher support matrix between 2.11.3 and 2.13.9.
Returning an empty version list instead would read as "no upgrade available" rather
than "this product is over".

## `GET /healthz`

Unchanged. Note it is the target of both probes while the catalog validator runs at
startup: fail-closed validation means a bad dataset stops the pod, which is intended.

## Metrics

`:9090/metrics`. Moved from `:9000`, which nothing ever scraped because the chart,
service and annotations all declared 9090.

Label values are validated against the catalog and bucketed to `other` otherwise.
They were previously raw user input on a public unauthenticated endpoint, so any
visitor could allocate unbounded Prometheus series.
