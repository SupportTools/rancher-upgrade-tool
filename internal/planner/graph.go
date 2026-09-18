// Package planner turns the compatibility catalog into upgrade routes.
//
// The central correction over the old implementation: compatibility describes
// valid STATES, upgrade planning requires valid TRANSITIONS. The old code walked
// the union of two Rancher versions' supported ranges
// (append(r1.SupportedPlatforms, r2.SupportedPlatforms...), main.go:257), so it
// could route through a Kubernetes version that neither Rancher version supported
// on its own. It also appended the Rancher hop before deriving any Kubernetes step
// (main.go:136-138 vs :146-149), so it could order a Rancher upgrade the cluster's
// current Kubernetes could not support.
//
// Here an edge is emitted only when BOTH endpoints are individually valid, and
// moves exactly one component. The union bug becomes unrepresentable rather than
// filtered out afterwards, and correct ordering falls out of the structure:
//
//	start: Rancher 2.9.2, downstream v1.27; waypoint 2.10.x supports v1.28-v1.31
//	  old: emit "2.9.2 -> 2.10.1", then walk a union containing v1.27
//	  new: valid(2.10.1, *, v1.27) is false, so that edge does not exist. The only
//	       edge out is raising downstream v1.27 -> v1.28 under 2.9.2.
package planner

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/hashicorp/go-version"
	"github.com/supporttools/rancher-upgrade-tool/internal/catalog"
)

// ErrNotImplemented lets the regression suite fail readably rather than panic.
var ErrNotImplemented = errors.New("planner: not implemented")

// Cluster is one downstream cluster Rancher manages.
//
// ID is stable and machine-facing (c1, c2, ...); Label is a user-supplied display
// name and is never matched on. Keeping them apart matters because labels arrive
// from a public unauthenticated endpoint and must not reach a metric label.
type Cluster struct {
	ID       string
	Label    string
	Platform catalog.Platform
	K8s      string
}

// LocalID names the cluster Rancher itself runs on, in the same identifier space
// as the downstream clusters so a step can point at either.
const LocalID = "local"

// Node is a complete fleet state. Local and downstream platforms are separate
// identities because Rancher on k3s managing EKS is a common real install, and
// (per the T0 spike) Rancher Manager installs on hosted providers too.
//
// Clusters is the whole downstream fleet, not one cluster. Rancher can only move
// when EVERY attached cluster sits inside the target version's window, so asking
// the one-cluster question repeatedly gives answers that are individually true and
// collectively wrong: each one assumes the others do not exist.
type Node struct {
	Rancher string

	LocalPlatform catalog.Platform
	LocalK8s      string

	Clusters []Cluster
}

// NodeOf builds a single-downstream-cluster node, the shape the API's legacy
// parameter pair describes. It is the one-element case of the fleet, not a
// separate path: two planners diverge.
func NodeOf(rancher string, localPlat catalog.Platform, localK8s string,
	downPlat catalog.Platform, downK8s string) Node {
	return Node{
		Rancher: rancher, LocalPlatform: localPlat, LocalK8s: localK8s,
		Clusters: []Cluster{{ID: "c1", Label: "Cluster 1", Platform: downPlat, K8s: downK8s}},
	}
}

// axes returns local followed by every downstream cluster, in a fixed order.
//
// The order is fixed deliberately. Ranging over clusters in any runtime-dependent
// order would reintroduce the non-determinism that TestRegression_ReachableIsDeterministic
// exists to catch, one level up from the map iteration that caused it the first time.
func (n Node) axes() []Cluster {
	out := make([]Cluster, 0, len(n.Clusters)+1)
	out = append(out, Cluster{ID: LocalID, Label: "Local cluster",
		Platform: n.LocalPlatform, K8s: n.LocalK8s})
	out = append(out, n.Clusters...)
	return out
}

// withAxis returns a copy of n with one axis moved to k8s.
func (n Node) withAxis(id, k8s string) Node {
	out := n
	if id == LocalID {
		out.LocalK8s = k8s
		return out
	}
	cl := make([]Cluster, len(n.Clusters))
	copy(cl, n.Clusters)
	for i := range cl {
		if cl[i].ID == id {
			cl[i].K8s = k8s
		}
	}
	out.Clusters = cl
	return out
}

// StepKind names which single component an edge moves.
type StepKind string

const (
	StepRancher StepKind = "rancher"
	StepLocal   StepKind = "local-k8s"
	StepDown    StepKind = "downstream-k8s"
)

// Step is one move, with the evidence that licensed it.
type Step struct {
	Kind     StepKind
	Platform catalog.Platform
	From     string
	To       string

	// Cluster identifies WHICH cluster moved: "local", "c1", "c2"... Empty on a
	// Rancher hop. Downstream steps keep the single downstream-k8s Kind rather than
	// gaining per-cluster kinds, so a consumer that only knows today's vocabulary
	// still works and simply cannot tell the clusters apart.
	Cluster      string
	ClusterLabel string

	// Wave groups steps with NO ORDERING REQUIREMENT between them. A Rancher hop
	// always occupies its own wave; the cluster raises between two hops share one.
	//
	// This is a claim about dependency, not safety. Concurrent cluster upgrades have
	// real control-plane and node-drain costs this tool does not model.
	Wave int

	SourceURL string
	AsOf      string

	// Caveats carries conditions the matrix does not encode as versions, such as
	// GKE Autopilot being unable to host Rancher, or a hosted cluster needing an
	// ingress controller deployed before Rancher is installed.
	Caveats []string
}

// Route is an ordered path to one reachable destination.
type Route struct {
	Destination string
	Steps       []Step

	// Granularity is the weakest granularity along the route. A route touching a
	// hosted provider is minor-level, and the response says so rather than letting
	// it look as precise as an RKE2 route.
	Granularity catalog.Granularity
}

// BlockerKind distinguishes three outcomes the old code collapsed into one empty
// list. Telling them apart is the point: an empty result that reads as "you are
// already current" is the worst failure this tool can produce.
type BlockerKind string

const (
	// BlockerUnsupportedStart means valid(from) is false, so no edges exist by
	// construction. That is a statement about the start state, NOT proof that
	// recovery is impossible.
	BlockerUnsupportedStart BlockerKind = "unsupported-start"
	// BlockerMissingEvidence means the catalog cannot establish a supported route.
	BlockerMissingEvidence BlockerKind = "missing-evidence"
	// BlockerIncompatible means a real, evidenced incompatibility.
	BlockerIncompatible BlockerKind = "incompatible"

	// BlockerClusterAhead means a cluster sits ABOVE a target Rancher version's
	// ceiling. Terminal for that destination: Kubernetes has no downgrade, so no
	// sequence of upgrades fixes it. Never phrase this as something to upgrade.
	BlockerClusterAhead BlockerKind = "cluster-ahead-of-window"

	// BlockerFleetSpread means the fleet spans more Kubernetes minors than the
	// widest window in the catalog, so NO Rancher version can manage all of it at
	// once, in any order. A property of the fleet, not of a route.
	BlockerFleetSpread BlockerKind = "fleet-spread-exceeds-window"
)

// ClusterPosition says which side of a window a cluster falls on.
type ClusterPosition string

const (
	// PositionBehind is below the floor: fixable by upgrading.
	PositionBehind ClusterPosition = "behind"
	// PositionAhead is above the ceiling: NOT fixable, since Kubernetes has no
	// downgrade. Telling someone to upgrade a cluster that is already too new is
	// worse than saying nothing.
	PositionAhead ClusterPosition = "ahead"
)

// ConstraintCluster is one cluster preventing a Rancher hop.
type ConstraintCluster struct {
	ID       string
	Label    string
	K8s      string
	Position ClusterPosition
}

// BindingConstraint answers "what is holding me back RIGHT NOW", which is a
// different question from what is reachable.
//
// The distinction matters and is easy to conflate. A fleet on v1.28/v1.30/v1.29 can
// reach Rancher 2.15.1 -- by raising the lagging clusters first, 28 steps away. But
// the highest version it can run TODAY, with no cluster upgrades at all, is 2.10.12,
// and the clusters keeping it there are the actionable fact. Reachability alone would
// report seven destinations and never mention that two clusters set the pace.
//
// With one lagging cluster it is THE constraint; with several they set the pace
// together and all are listed rather than one being picked arbitrarily.
type BindingConstraint struct {
	// HighestNow is the newest Rancher version the fleet can run without upgrading
	// any cluster.
	HighestNow string
	// BlockedRancher is the next version after HighestNow, and what the clusters
	// below are measured against.
	BlockedRancher string
	RequiresK8s    string
	Clusters       []ConstraintCluster
}

// Blocker explains why a walk stopped, naming the constraint responsible.
type Blocker struct {
	Kind       BlockerKind
	Constraint string
	Detail     string
	SourceURL  string
}

// Result is the whole answer: every reachable destination, plus why the rest are not.
type Result struct {
	Start    Node
	Routes   []Route
	Blockers []Blocker

	// BindingConstraint is set when at least one destination is reachable but the
	// NEXT Rancher version is not, and names what is in the way.
	BindingConstraint *BindingConstraint

	// Scope states what the answer covers. One local/downstream pair, never a
	// fleet: upgrading Rancher affects every cluster it manages, and this answer
	// speaks only for the pair supplied.
	Scope string
}

// Edge is a legal transition between two valid nodes.
type Edge struct {
	From Node
	To   Node
	Step Step
}

func minorOf(v string) (int, int, error) {
	parsed, err := version.NewVersion(catalog.Normalize(v))
	if err != nil {
		return 0, 0, fmt.Errorf("planner: %q is not a valid version: %w", v, err)
	}
	seg := parsed.Segments()
	if len(seg) < 2 {
		return 0, 0, fmt.Errorf("planner: %q has no minor segment", v)
	}
	return seg[0], seg[1], nil
}

// Valid reports whether every component of a node is individually supported:
// Rancher installs on the local pair, and Rancher manages the downstream pair.
//
// "Individually" is the whole point. The old code tested membership of a MERGED
// set built from two Rancher versions (main.go:257), which cannot establish that
// any single version supports the state.
func Valid(c *catalog.Catalog, n Node) (bool, error) {
	rv, ok := c.Find(n.Rancher)
	if !ok {
		return false, nil
	}
	// A conjunction over the whole fleet. Rancher can only move when EVERY attached
	// cluster is inside the window, which is exactly what the per-cluster question
	// cannot express.
	for _, ax := range n.axes() {
		sup, ok := rv.SupportFor(ax.Platform)
		if !ok {
			return false, nil
		}
		covered, err := sup.Covers(ax.K8s)
		if err != nil {
			return false, err
		}
		if !covered {
			return false, nil
		}
	}
	return true, nil
}

// why explains, for an invalid node, which constraint it violates. An
// undifferentiated empty result reads to a user as "you are already current",
// which is the most dangerous answer this tool can give.
func why(c *catalog.Catalog, n Node) Blocker {
	rv, ok := c.Find(n.Rancher)
	if !ok {
		return Blocker{Kind: BlockerMissingEvidence,
			Constraint: "rancher version not in catalog",
			Detail:     fmt.Sprintf("Rancher %s is not in the catalog, so nothing can be established about it.", n.Rancher)}
	}
	for _, ax := range n.axes() {
		role := ax.Label
		if role == "" {
			role = ax.ID
		}
		sup, ok := rv.SupportFor(ax.Platform)
		if !ok {
			return Blocker{Kind: BlockerMissingEvidence,
				Constraint: fmt.Sprintf("%s: platform %s not listed for Rancher %s", role, ax.Platform, n.Rancher),
				Detail:     fmt.Sprintf("The catalog lists no %s support under Rancher %s.", ax.Platform, n.Rancher)}
		}
		covered, err := sup.Covers(ax.K8s)
		if err != nil {
			return Blocker{Kind: BlockerMissingEvidence,
				Constraint: fmt.Sprintf("%s: version %q unreadable", role, ax.K8s),
				Detail:     err.Error()}
		}
		if !covered {
			return Blocker{Kind: BlockerUnsupportedStart,
				Constraint: fmt.Sprintf("Rancher %s supports %s %s-%s on %s",
					n.Rancher, ax.Platform, sup.MinVersion, sup.MaxVersion, role),
				Detail: fmt.Sprintf("%s is on %s, outside that range. This describes the "+
					"state you are in; it does not establish that no route out exists.", role, ax.K8s),
				SourceURL: sup.SourceURL}
		}
	}
	return Blocker{Kind: BlockerMissingEvidence, Constraint: "unknown", Detail: "no supported route could be established"}
}

// nextK8s returns the next Kubernetes version on a platform: exactly one minor up,
// never a skip. Upstream rule, https://kubernetes.io/releases/version-skew-policy/ :
// kube-apiserver must not skip minor versions when upgrading.
//
// For release-granularity platforms it returns the highest real release of the next
// minor. For minor-granularity platforms it returns the next minor line itself. It
// never synthesizes a release, which is what getMinorVersionsBetween did wrong by
// manufacturing major.minor.0 strings that may never have been published.
func nextK8s(sup *catalog.Support, current string) (string, error) {
	major, minor, err := minorOf(current)
	if err != nil {
		return "", err
	}
	target := minor + 1

	if sup.Granularity == catalog.GranularityMinor {
		return fmt.Sprintf("v%d.%d", major, target), nil
	}

	var best *version.Version
	var bestRaw string
	for _, r := range sup.Releases {
		rMajor, rMinor, err := minorOf(r)
		if err != nil {
			return "", err
		}
		if rMajor != major || rMinor != target {
			continue
		}
		parsed, err := version.NewVersion(catalog.Normalize(r))
		if err != nil {
			return "", err
		}
		if best == nil || parsed.GreaterThan(best) {
			best, bestRaw = parsed, r
		}
	}
	if best == nil {
		return "", nil // no release published for that minor
	}
	return bestRaw, nil
}

func caveatsFor(p catalog.Platform, role string) []string {
	if role != "local" {
		return nil
	}
	switch p {
	case catalog.AKS, catalog.EKS, catalog.GKE:
		c := []string{"Hosted clusters need an ingress controller deployed before Rancher is installed; RKE2 and k3s ship one."}
		if p == catalog.GKE {
			c = append(c, "GKE Autopilot cannot host Rancher: it restricts kube-system, which Rancher writes to during installation.")
		}
		return c
	}
	return nil
}

// Edges returns every legal transition out of a node. An edge moves exactly one
// component and is emitted only if valid(from) and valid(to).
func Edges(c *catalog.Catalog, n Node) ([]Edge, error) {
	ok, err := Valid(c, n)
	if err != nil || !ok {
		return nil, err
	}
	rv, _ := c.Find(n.Rancher)
	var edges []Edge

	// Kubernetes moves, one axis at a time: the local cluster and each downstream
	// cluster independently. Moving one cluster never moves another.
	for _, ax := range n.axes() {
		role := "downstream"
		kind := StepDown
		if ax.ID == LocalID {
			role, kind = "local", StepLocal
		}
		sup, ok := rv.SupportFor(ax.Platform)
		if !ok {
			continue
		}
		next, err := nextK8s(sup, ax.K8s)
		if err != nil {
			return nil, err
		}
		if next == "" {
			continue
		}
		to := n.withAxis(ax.ID, next)
		valid, err := Valid(c, to)
		if err != nil {
			return nil, err
		}
		if !valid {
			continue
		}
		edges = append(edges, Edge{From: n, To: to, Step: Step{
			Kind: kind, Platform: ax.Platform, From: ax.K8s, To: next,
			Cluster: ax.ID, ClusterLabel: ax.Label,
			SourceURL: sup.SourceURL, AsOf: sup.AsOf,
			Caveats: caveatsFor(ax.Platform, role),
		}})
	}

	// Rancher hop. Emitted only if the target supports BOTH clusters where they
	// currently stand, which is what forces Kubernetes to be raised first when the
	// target's minimum is above the cluster.
	next, blocker, err := NextRancherWaypoint(c, n.Rancher)
	if err != nil {
		return nil, err
	}
	if blocker == nil && next != "" {
		to := n
		to.Rancher = next
		valid, err := Valid(c, to)
		if err != nil {
			return nil, err
		}
		if valid {
			var src, asOf string
			if nrv, ok := c.Find(next); ok {
				if sup, ok := nrv.SupportFor(n.LocalPlatform); ok {
					src, asOf = sup.SourceURL, sup.AsOf
				}
			}
			edges = append(edges, Edge{From: n, To: to, Step: Step{
				Kind: StepRancher, From: n.Rancher, To: next,
				SourceURL: src, AsOf: asOf,
				Caveats: caveatsFor(n.LocalPlatform, "local"),
			}})
		}
	}
	return edges, nil
}

// Waypoints returns the Rancher versions worth stopping at: the latest patch of
// each minor, roughly 8 instead of 70, ascending.
//
// It replaces GetKeyVersions (main.go:350-372), which selected waypoints with
// strings.HasSuffix(v, ".9") plus three hardcoded version strings and therefore
// capped every answer the tool could give at Rancher 2.9.2.
func Waypoints(c *catalog.Catalog) ([]string, error) {
	type line struct{ major, minor int }
	best := map[line]*version.Version{}
	raw := map[line]string{}

	for _, rv := range c.Rancher {
		major, minor, err := minorOf(rv.Version)
		if err != nil {
			return nil, err
		}
		parsed, err := version.NewVersion(catalog.Normalize(rv.Version))
		if err != nil {
			return nil, err
		}
		k := line{major, minor}
		if cur, ok := best[k]; !ok || parsed.GreaterThan(cur) {
			best[k], raw[k] = parsed, rv.Version
		}
	}

	lines := make([]line, 0, len(best))
	for k := range best {
		lines = append(lines, k)
	}
	sort.Slice(lines, func(i, j int) bool {
		if lines[i].major != lines[j].major {
			return lines[i].major < lines[j].major
		}
		return lines[i].minor < lines[j].minor
	})

	out := make([]string, 0, len(lines))
	for _, k := range lines {
		out = append(out, raw[k])
	}
	return out, nil
}

// NextRancherWaypoint returns the next Rancher waypoint after current.
//
// Adjacency is NUMERIC, not catalog order. If the numerically adjacent minor is
// absent from the catalog this returns a blocker rather than skipping to the next
// present entry whose endpoints happen to validate. Catalog ordering is not a
// safety property; the same rule the Kubernetes axes obey applies here.
//
// Crossing a minor also requires being on the latest patch of the current minor,
// so a cluster behind on patches is moved forward within its line first.
func NextRancherWaypoint(c *catalog.Catalog, current string) (string, *Blocker, error) {
	major, minor, err := minorOf(current)
	if err != nil {
		return "", nil, err
	}
	waypoints, err := Waypoints(c)
	if err != nil {
		return "", nil, err
	}

	// Still behind on patches within this minor: move there first.
	for _, w := range waypoints {
		wMajor, wMinor, err := minorOf(w)
		if err != nil {
			return "", nil, err
		}
		if wMajor != major || wMinor != minor {
			continue
		}
		cur, err := version.NewVersion(catalog.Normalize(current))
		if err != nil {
			return "", nil, err
		}
		latest, err := version.NewVersion(catalog.Normalize(w))
		if err != nil {
			return "", nil, err
		}
		if latest.GreaterThan(cur) {
			return w, nil, nil
		}
	}

	for _, w := range waypoints {
		wMajor, wMinor, err := minorOf(w)
		if err != nil {
			return "", nil, err
		}
		if wMajor == major && wMinor == minor+1 {
			return w, nil, nil
		}
	}

	// Is there anything beyond this minor at all? If so, the adjacent minor is
	// missing from the catalog and that is a data gap, not a licence to skip.
	for _, w := range waypoints {
		wMajor, wMinor, err := minorOf(w)
		if err != nil {
			return "", nil, err
		}
		if wMajor > major || (wMajor == major && wMinor > minor+1) {
			return "", &Blocker{
				Kind:       BlockerMissingEvidence,
				Constraint: fmt.Sprintf("Rancher %d.%d is absent from the catalog", major, minor+1),
				Detail: fmt.Sprintf("Later versions exist (%s), but reaching them would skip the %d.%d "+
					"line. Adjacency is numeric, not catalog order, so this is a data gap rather than "+
					"a shortcut.", w, major, minor+1),
			}, nil
		}
	}
	return "", nil, nil // already newest
}

// Reachable plans the fleet forward and returns each reachable Rancher version
// with its route, plus blockers for what could not be reached.
//
// THIS IS A LINEAR WALK, NOT A SEARCH, and that is a measured decision rather than
// a shortcut. See docs/fleet-planning-algorithm.md. Two facts drive it:
//
//  1. Every move raises exactly one component by exactly one minor and nothing is
//     ever downgraded, so total steps = sum(final - start) + hops. That is fixed by
//     the endpoints, which means EVERY valid route to a destination has the same
//     length. Breadth-first search's shortest-path property buys nothing here; only
//     feasibility was ever in question.
//  2. Search does not scale. Worst-case nodes are 163 / 695 / 3,175 / 15,431 /
//     78,943 / 420,335 for one through six clusters. maxNodes is exceeded at FOUR,
//     and it would surface to a user as "no route found" rather than "the planner
//     gave up".
//
// THE TRAP THIS AVOIDS: do not assign a lagging cluster straight to the next
// window's floor. Raising v1.22 -> v1.25 passes through v1.23 and v1.24, and each
// intermediate must be valid under the Rancher version CURRENTLY in effect. On a
// catalog where consecutive windows do not overlap, that shortcut reported
// destinations as reachable that are not: it disagreed with exhaustive search on 18
// of 36 valid start states. It is correct on today's data only because consecutive
// windows happen to overlap, which is a property of the data and not of the code.
//
// So every raise here goes through Edges, one minor at a time. Edges emits a move
// only when both endpoints validate, so the intermediate check is structural rather
// than remembered.
func Reachable(c *catalog.Catalog, start Node) (*Result, error) {
	res := &Result{Start: start, Scope: scopeFor(start)}

	ok, err := Valid(c, start)
	if err != nil {
		return nil, err
	}
	if !ok {
		// A fleet spread wider than any window is a different, terminal fact from
		// "your starting state is unsupported", and is worth saying first.
		if b, err := fleetSpreadBlocker(c, start); err != nil {
			return nil, err
		} else if b != nil {
			res.Blockers = append(res.Blockers, *b)
		}
		res.Blockers = append(res.Blockers, why(c, start))
		return res, nil
	}

	// What can the fleet run with no cluster upgrades at all? Computed from the
	// start state, independently of the walk below, because "reachable eventually"
	// and "runnable today" are different answers and users need both.
	bc, err := bindingNow(c, start)
	if err != nil {
		return nil, err
	}
	res.BindingConstraint = bc

	cur := start
	var steps []Step
	wave := 1

	for {
		next, blocker, err := NextRancherWaypoint(c, cur.Rancher)
		if err != nil {
			return nil, err
		}
		if blocker != nil {
			res.Blockers = append(res.Blockers, *blocker)
			break
		}
		if next == "" {
			break // already on the newest waypoint
		}

		// Raise every axis that sits below the next window's floor, one minor at a
		// time, each move validated against the Rancher version in effect now.
		probe := cur
		var pending []Step
		stuck := false
		for {
			axis, need, err := axisBelowFloor(c, probe, next)
			if err != nil {
				return nil, err
			}
			if !need {
				break
			}
			edge, found, err := edgeForAxis(c, probe, axis)
			if err != nil {
				return nil, err
			}
			if !found {
				stuck = true
				break
			}
			st := edge.Step
			st.Wave = wave
			pending = append(pending, st)
			probe = edge.To
		}

		if stuck {
			res.Blockers = append(res.Blockers, Blocker{
				Kind:       BlockerMissingEvidence,
				Constraint: fmt.Sprintf("no published release bridges the gap to Rancher %s", next),
				Detail: fmt.Sprintf("Reaching Rancher %s needs a Kubernetes version that cannot be "+
					"arrived at one minor at a time from where the fleet stands, without leaving the "+
					"range Rancher %s supports along the way.", next, cur.Rancher),
			})
			break
		}

		to := probe
		to.Rancher = next
		okTo, err := Valid(c, to)
		if err != nil {
			return nil, err
		}
		if !okTo {
			// Something is above the ceiling. Kubernetes has no downgrade, so this
			// destination is refused rather than planned around.
			if b, err := aheadBlocker(c, probe, next); err != nil {
				return nil, err
			} else if b != nil {
				res.Blockers = append(res.Blockers, *b)
			}
			break
		}

		wave++
		hopStep := Step{Kind: StepRancher, From: cur.Rancher, To: next, Wave: wave}
		if nrv, ok := c.Find(next); ok {
			if sup, ok := nrv.SupportFor(cur.LocalPlatform); ok {
				hopStep.SourceURL, hopStep.AsOf = sup.SourceURL, sup.AsOf
			}
		}
		hopStep.Caveats = caveatsFor(cur.LocalPlatform, "local")
		wave++

		steps = append(steps, pending...)
		steps = append(steps, hopStep)
		cur = to

		route := make([]Step, len(steps))
		copy(route, steps)
		res.Routes = append(res.Routes, Route{
			Destination: next,
			Steps:       route,
			Granularity: granularityOf(c, cur),
		})
	}

	return res, nil
}

// scopeFor states what the answer covers. The caveat matters MORE with a fleet, not
// less: a Rancher upgrade affects every cluster it manages, and the tool cannot know
// about clusters nobody typed in.
func scopeFor(n Node) string {
	subject := "one local cluster and one downstream cluster, the pair supplied"
	if len(n.Clusters) != 1 {
		subject = fmt.Sprintf("one local cluster and the %d downstream clusters supplied", len(n.Clusters))
	}
	return "This answer covers " + subject + ". Any cluster not listed here is unaccounted for, " +
		"and upgrading Rancher affects every cluster it manages. It is a version-compatibility " +
		"itinerary, not an execution runbook: feature-chart upgrades, admission-webhook " +
		"compatibility across a hop and node-upgrade completion are not encoded in any " +
		"compatibility matrix."
}

// position reports where a Kubernetes version sits relative to a Rancher version's
// window for a platform.
func position(c *catalog.Catalog, rancher string, plat catalog.Platform, k8s string) (ClusterPosition, bool, error) {
	rv, ok := c.Find(rancher)
	if !ok {
		return "", false, nil
	}
	sup, ok := rv.SupportFor(plat)
	if !ok {
		return "", false, nil
	}
	covered, err := sup.Covers(k8s)
	if err != nil {
		return "", false, err
	}
	if covered {
		return "", false, nil
	}
	_, have, err := minorOf(k8s)
	if err != nil {
		return "", false, err
	}
	_, floor, err := minorOf(sup.MinVersion)
	if err != nil {
		return "", false, err
	}
	if have < floor {
		return PositionBehind, true, nil
	}
	return PositionAhead, true, nil
}

// axisBelowFloor finds the first axis sitting below target's floor, in fixed order.
func axisBelowFloor(c *catalog.Catalog, n Node, target string) (string, bool, error) {
	for _, ax := range n.axes() {
		pos, out, err := position(c, target, ax.Platform, ax.K8s)
		if err != nil {
			return "", false, err
		}
		if out && pos == PositionBehind {
			return ax.ID, true, nil
		}
	}
	return "", false, nil
}

// edgeForAxis returns the legal one-minor raise for a single axis, if one exists.
func edgeForAxis(c *catalog.Catalog, n Node, axisID string) (Edge, bool, error) {
	edges, err := Edges(c, n)
	if err != nil {
		return Edge{}, false, err
	}
	for _, e := range edges {
		if e.Step.Kind != StepRancher && e.Step.Cluster == axisID {
			return e, true, nil
		}
	}
	return Edge{}, false, nil
}

// bindingNow finds the newest Rancher version the fleet can run AS IT STANDS, then
// names what keeps it from the next one. Returns nil when the fleet is already on
// the newest waypoint its versions allow.
func bindingNow(c *catalog.Catalog, n Node) (*BindingConstraint, error) {
	waypoints, err := Waypoints(c)
	if err != nil {
		return nil, err
	}
	cur, err := version.NewVersion(catalog.Normalize(n.Rancher))
	if err != nil {
		return nil, err
	}

	highest, highestIdx := "", -1
	for i, w := range waypoints {
		wv, err := version.NewVersion(catalog.Normalize(w))
		if err != nil {
			return nil, err
		}
		if wv.LessThan(cur) {
			continue // never suggest going backwards
		}
		probe := n
		probe.Rancher = w
		ok, err := Valid(c, probe)
		if err != nil {
			return nil, err
		}
		if ok {
			highest, highestIdx = w, i
		}
	}
	if highestIdx < 0 || highestIdx+1 >= len(waypoints) {
		return nil, nil // nothing runnable, or already at the top
	}

	next := waypoints[highestIdx+1]
	bc, err := bindingConstraint(c, n, next)
	if err != nil || bc == nil {
		return nil, err
	}
	bc.HighestNow = highest
	return bc, nil
}

// ConstraintAgainst names every cluster outside a specific Rancher version's window
// and which side it falls on. Exported so a caller (and the regression suite) can
// ask the question about a version other than the next one.
func ConstraintAgainst(c *catalog.Catalog, n Node, target string) (*BindingConstraint, error) {
	return bindingConstraint(c, n, target)
}

// bindingConstraint names every cluster outside target's window, and which side.
func bindingConstraint(c *catalog.Catalog, n Node, target string) (*BindingConstraint, error) {
	bc := &BindingConstraint{BlockedRancher: target}
	if rv, ok := c.Find(target); ok {
		if sup, ok := rv.SupportFor(n.LocalPlatform); ok {
			bc.RequiresK8s = sup.MinVersion
		}
	}
	for _, ax := range n.axes() {
		pos, out, err := position(c, target, ax.Platform, ax.K8s)
		if err != nil {
			return nil, err
		}
		if !out {
			continue
		}
		bc.Clusters = append(bc.Clusters, ConstraintCluster{
			ID: ax.ID, Label: ax.Label, K8s: ax.K8s, Position: pos,
		})
	}
	if len(bc.Clusters) == 0 {
		return nil, nil
	}
	return bc, nil
}

// aheadBlocker reports clusters above target's ceiling. Separated from the behind
// case because it is TERMINAL: Kubernetes has no downgrade, so there is no sequence
// of upgrades that fixes it, and phrasing it as something to upgrade is worse than
// saying nothing.
func aheadBlocker(c *catalog.Catalog, n Node, target string) (*Blocker, error) {
	var ahead []string
	var src string
	for _, ax := range n.axes() {
		pos, out, err := position(c, target, ax.Platform, ax.K8s)
		if err != nil {
			return nil, err
		}
		if out && pos == PositionAhead {
			label := ax.Label
			if label == "" {
				label = ax.ID
			}
			ahead = append(ahead, fmt.Sprintf("%s (%s)", label, ax.K8s))
			if rv, ok := c.Find(target); ok {
				if sup, ok := rv.SupportFor(ax.Platform); ok {
					src = sup.SourceURL
				}
			}
		}
	}
	if len(ahead) == 0 {
		return nil, nil
	}
	ceiling := ""
	if rv, ok := c.Find(target); ok {
		if sup, ok := rv.SupportFor(n.LocalPlatform); ok {
			ceiling = sup.MaxVersion
		}
	}
	return &Blocker{
		Kind:       BlockerClusterAhead,
		Constraint: fmt.Sprintf("Rancher %s supports Kubernetes up to %s; %s above it", target, ceiling, strings.Join(ahead, ", ")),
		Detail: "Kubernetes cannot be downgraded, so this is not something to fix by upgrading. " +
			"Rancher " + target + " and anything newer cannot manage this fleet while those clusters " +
			"remain where they are.",
		SourceURL: src,
	}, nil
}

// fleetSpreadBlocker reports a fleet spanning more Kubernetes minors than the widest
// window in the catalog. No Rancher version can manage it at once, in any order, so
// this is a fact about the estate rather than a route that could not be found.
func fleetSpreadBlocker(c *catalog.Catalog, n Node) (*Blocker, error) {
	lo, hi := 1<<30, -1
	for _, ax := range n.axes() {
		_, m, err := minorOf(ax.K8s)
		if err != nil {
			return nil, err
		}
		if m < lo {
			lo = m
		}
		if m > hi {
			hi = m
		}
	}
	if hi < 0 {
		return nil, nil
	}
	spread := hi - lo + 1

	widest, widestWhere, src := 0, "", ""
	for _, rv := range c.Rancher {
		for _, ax := range n.axes() {
			sup, ok := rv.SupportFor(ax.Platform)
			if !ok {
				continue
			}
			_, floor, err := minorOf(sup.MinVersion)
			if err != nil {
				return nil, err
			}
			_, ceil, err := minorOf(sup.MaxVersion)
			if err != nil {
				return nil, err
			}
			if w := ceil - floor + 1; w > widest {
				widest = w
				widestWhere = fmt.Sprintf("Rancher %s, %s %s-%s", rv.Version, ax.Platform, sup.MinVersion, sup.MaxVersion)
				src = sup.SourceURL
			}
		}
	}
	if widest == 0 || spread <= widest {
		return nil, nil
	}
	return &Blocker{
		Kind:       BlockerFleetSpread,
		Constraint: fmt.Sprintf("This fleet spans v1.%d to v1.%d, which is %d Kubernetes minors", lo, hi, spread),
		Detail: fmt.Sprintf("The widest support window in the catalog is %d minors (%s). No single "+
			"Rancher version can manage every cluster at once, whatever order the upgrades are done "+
			"in. Close the gap by raising the clusters furthest behind before planning a Rancher "+
			"upgrade.", widest, widestWhere),
		SourceURL: src,
	}, nil
}

// granularityOf reports the weakest granularity in play. A route touching a hosted
// provider is minor-level and must say so, rather than looking as precise as a
// route planned from real KDM releases.
//
// The asymmetry is inherent to the sources, not a gap in handling: KDM lists every
// real RKE2 and k3s release, while SUSE stopped publishing exact hosted-provider
// releases after ~2.7.5. No parser recovers what was never published.
func granularityOf(c *catalog.Catalog, n Node) catalog.Granularity {
	rv, ok := c.Find(n.Rancher)
	if !ok {
		return catalog.GranularityMinor
	}
	for _, ax := range n.axes() {
		sup, ok := rv.SupportFor(ax.Platform)
		if !ok || sup.Granularity == catalog.GranularityMinor {
			return catalog.GranularityMinor
		}
	}
	return catalog.GranularityRelease
}
