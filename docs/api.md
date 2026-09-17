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
`missing-parameter`.

Unparseable input previously returned **500**, which told the user the server broke
when they had typed something wrong.

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
