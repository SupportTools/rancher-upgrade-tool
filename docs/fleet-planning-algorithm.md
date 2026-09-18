# Fleet planning: why it is a linear walk, not a search

Findings from the F3 spike (TaskForge `rancher-upgrade-tool-ranchertips-28`), measured
against `data/catalog.json` as of 2026-09-17.

**Decision: plan the fleet with a step-at-a-time greedy walk, not breadth-first search.**
It is `O(hops x clusters)`, it agrees with exhaustive BFS on every valid start state we
can enumerate, and — the part that matters — it does not depend on the catalog having
any particular shape.

## The question

The single-cluster planner is a BFS over `(rancher, local, downstream)`. Extending the
node to `(rancher, local, cluster1..clusterN)` makes the state space multiplicative, so
the obvious worry was that BFS would stop being viable.

## What the state space actually does

Worst-case nodes explored from the widest valid start, real RKE2 catalog, 11 Rancher
waypoints:

| clusters | BFS nodes | greedy checks |
|---|---|---|
| 1 | 163 | 22 |
| 2 | 695 | 33 |
| 3 | 3,175 | 44 |
| 4 | **15,431** | 55 |
| 5 | 78,943 | 66 |
| 6 | 420,335 | 77 |

Growth is a clean ~5x per added cluster.

Two things to note:

1. **`maxNodes = 10000` in `internal/planner/graph.go` is exceeded at four clusters.**
   A BFS implementation would hit its own guard rail on a fleet size customers plausibly
   have, and the failure would look like "no route found" rather than "the planner gave
   up".

2. The earlier estimate in the task description (~80,000 nodes at three clusters) was
   **wrong, in the pessimistic direction**. Valid states are sparse: support windows are
   only 3-6 Kubernetes minors wide, so the overwhelming majority of the cartesian product
   is invalid and never enters the frontier. The real figure at three clusters is 3,175.

## Why a greedy walk is sufficient

**Step count is not an optimisation problem here.** Every move raises exactly one
component by exactly one minor, and nothing is ever downgraded. So for a given
destination the total is

```
sum over components of (final minor - start minor)  +  number of Rancher hops
```

which is fixed by the endpoints. Every valid route to a destination has the *same*
length. BFS's shortest-path property buys nothing; the only question is **feasibility**.

So the walk is: for each Rancher waypoint in order, bring every component up to that
waypoint's floor, then hop. Refuse if any component sits above the ceiling.

## The bug in the obvious version

The naive form — assign each lagging cluster straight to the next window's floor — is
**wrong**, and the real catalog hides it.

Raising a cluster from `v1.22` to `v1.25` passes through `v1.23` and `v1.24`. Those
intermediate states must be valid *under the Rancher version currently in effect*. If the
current window ends at `v1.22` and the next begins at `v1.25`, there is no way across,
and the naive version cheerfully reports the destination as reachable.

On a synthetic catalog with such a gap, naive greedy disagreed with BFS on **18 of 36**
valid start states. It has zero mismatches on today's data only because consecutive
windows happen to overlap.

**The version we are adopting raises one minor at a time and validates each intermediate
state against the Rancher version in effect at that moment.** That is what makes it
correct independently of the catalog's shape.

## Differential test results

Greedy vs exhaustive BFS, comparing the full set of reachable Rancher versions:

| catalog | valid start states | naive greedy | step-at-a-time greedy |
|---|---|---|---|
| real, 1 cluster | 163 starts | 0 mismatches | 0 mismatches |
| real, 2 clusters | 592 starts | 0 | 0 |
| real, 3 clusters | 931 starts | 0 | 0 |
| real, 4 clusters | 1,796 starts | 0 | 0 |
| synthetic: gap between windows | 36 | **18 mismatches** | 0 |
| synthetic: ceiling decreases | 766 | 0 | 0 |
| synthetic: floor decreases | 1,309 | 0 | 0 |

The 3-cluster figure is worth a caution: a first pass sampled 4,000 *random* fleets and
found only **8** valid start states, because a random triple of versions almost never
lands inside a 3-6 minor window. Random sampling looks like a strong test here and is
nearly vacuous. The table above enumerates valid starts exhaustively instead.

## Catalog properties, and why we still assert them

All six platforms (`rke2`, `k3s`, `rke1`, `aks`, `eks`, `gke`) currently satisfy:

- **P1** window floors are non-decreasing as the Rancher version increases
- **P2** window ceilings are non-decreasing
- **P3** consecutive windows overlap: `floor(R+1) <= ceiling(R)`

The adopted algorithm does **not** rely on these. They should still be asserted by the
catalog validator, because P3 failing is a real upstream event with an operational
meaning: a gap between consecutive windows means **no fleet can cross that Rancher hop at
all**, and the tool should say so loudly rather than discover it per-query. An assertion
turns a silent data change into a named condition.

This is the same discipline the catalog already applies to the install-range/downstream-range
equality: rely on the code being correct regardless, and assert the data property anyway so
a change in upstream reality is visible.

## Consequences for F4

- The node model still becomes `(rancher, local, clusters[])` and validity is still a
  conjunction over the cluster set. That part of F4 is unchanged.
- The search is replaced by a linear walk, so `maxNodes` is not a factor and no
  canonicalisation of the cluster multiset is needed. (Canonicalising by sorted multiset
  does cut BFS by ~3x at three clusters — 3,175 to 931 — but that is moot now.)
- A cluster cap is still worth setting, for response size and metric cardinality rather
  than for compute. **8** is suggested: comfortably beyond real fleets for a planning
  form, and at that size the answer is still linear.
- F4 keeps the requirement that a route is emitted only when both endpoints validate. The
  step-at-a-time walk satisfies this by construction, which is precisely why it survives
  catalogs the naive version does not.
