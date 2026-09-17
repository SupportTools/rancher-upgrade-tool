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

// Node is a complete cluster state. Local and downstream platforms are separate
// identities because Rancher on k3s managing EKS is a common real install, and
// (per the T0 spike) Rancher Manager installs on hosted providers too.
type Node struct {
	Rancher string

	LocalPlatform catalog.Platform
	LocalK8s      string

	DownPlatform catalog.Platform
	DownK8s      string
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
)

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

// maxNodes bounds the walk. The state space is rancher x localK8s x downstreamK8s;
// latest-patch-per-minor holds the Rancher axis to roughly 8, but the Kubernetes
// axes are bounded only by the data, so a crafted query must not be the expensive one.
const maxNodes = 10000

func (n Node) key() string {
	return strings.Join([]string{n.Rancher,
		string(n.LocalPlatform), catalog.Normalize(n.LocalK8s),
		string(n.DownPlatform), catalog.Normalize(n.DownK8s)}, "|")
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
	for _, pair := range []struct {
		platform catalog.Platform
		k8s      string
	}{{n.LocalPlatform, n.LocalK8s}, {n.DownPlatform, n.DownK8s}} {
		sup, ok := rv.SupportFor(pair.platform)
		if !ok {
			return false, nil
		}
		covered, err := sup.Covers(pair.k8s)
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
	for _, pair := range []struct {
		role     string
		platform catalog.Platform
		k8s      string
	}{{"local", n.LocalPlatform, n.LocalK8s}, {"downstream", n.DownPlatform, n.DownK8s}} {
		sup, ok := rv.SupportFor(pair.platform)
		if !ok {
			return Blocker{Kind: BlockerMissingEvidence,
				Constraint: fmt.Sprintf("%s platform %s not listed for Rancher %s", pair.role, pair.platform, n.Rancher),
				Detail:     fmt.Sprintf("The catalog lists no %s support under Rancher %s.", pair.platform, n.Rancher)}
		}
		covered, err := sup.Covers(pair.k8s)
		if err != nil {
			return Blocker{Kind: BlockerMissingEvidence,
				Constraint: fmt.Sprintf("%s version %q unreadable", pair.role, pair.k8s),
				Detail:     err.Error()}
		}
		if !covered {
			return Blocker{Kind: BlockerUnsupportedStart,
				Constraint: fmt.Sprintf("Rancher %s supports %s %s-%s on the %s cluster",
					n.Rancher, pair.platform, sup.MinVersion, sup.MaxVersion, pair.role),
				Detail: fmt.Sprintf("The %s cluster is on %s, outside that range. This describes the "+
					"state you are in; it does not establish that no route out exists.", pair.role, pair.k8s),
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

	// Kubernetes moves, one axis at a time.
	for _, axis := range []struct {
		kind     StepKind
		role     string
		platform catalog.Platform
		current  string
		apply    func(Node, string) Node
	}{
		{StepLocal, "local", n.LocalPlatform, n.LocalK8s, func(x Node, v string) Node { x.LocalK8s = v; return x }},
		{StepDown, "downstream", n.DownPlatform, n.DownK8s, func(x Node, v string) Node { x.DownK8s = v; return x }},
	} {
		sup, ok := rv.SupportFor(axis.platform)
		if !ok {
			continue
		}
		next, err := nextK8s(sup, axis.current)
		if err != nil {
			return nil, err
		}
		if next == "" {
			continue
		}
		to := axis.apply(n, next)
		valid, err := Valid(c, to)
		if err != nil {
			return nil, err
		}
		if !valid {
			continue
		}
		edges = append(edges, Edge{From: n, To: to, Step: Step{
			Kind: axis.kind, Platform: axis.platform, From: axis.current, To: next,
			SourceURL: sup.SourceURL, AsOf: sup.AsOf,
			Caveats: caveatsFor(axis.platform, axis.role),
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

// Reachable walks every legal edge from start and returns each destination with
// its route, plus blockers for what could not be reached.
func Reachable(c *catalog.Catalog, start Node) (*Result, error) {
	res := &Result{Start: start, Scope: "This answer covers one local cluster and one downstream " +
		"cluster, the pair supplied. Upgrading Rancher affects every cluster it manages, so other " +
		"attached clusters must be checked separately. It is a version-compatibility itinerary, not " +
		"an execution runbook: feature-chart upgrades, admission-webhook compatibility across a hop " +
		"and node-upgrade completion are not encoded in any compatibility matrix."}

	ok, err := Valid(c, start)
	if err != nil {
		return nil, err
	}
	if !ok {
		res.Blockers = append(res.Blockers, why(c, start))
		return res, nil
	}

	seen := map[string]visit{start.key(): {node: start}}
	queue := []Node{start}
	seenBlocker := map[string]bool{}

	for len(queue) > 0 && len(seen) < maxNodes {
		cur := queue[0]
		queue = queue[1:]

		if _, b, err := NextRancherWaypoint(c, cur.Rancher); err != nil {
			return nil, err
		} else if b != nil && !seenBlocker[b.Constraint] {
			seenBlocker[b.Constraint] = true
			res.Blockers = append(res.Blockers, *b)
		}

		edges, err := Edges(c, cur)
		if err != nil {
			return nil, err
		}
		for _, e := range edges {
			k := e.To.key()
			if _, dup := seen[k]; dup {
				continue
			}
			step := e.Step
			seen[k] = visit{node: e.To, parent: cur.key(), step: &step}
			queue = append(queue, e.To)
		}
	}

	// One route per reachable Rancher version, the shortest found.
	//
	// Iteration order is fixed deliberately. Ranging over `seen` directly made this
	// NON-DETERMINISTIC: Go randomises map iteration, so two routes of equal length
	// to the same destination were chosen by whichever the runtime happened to visit
	// first. The same query returned different step sequences between requests, and
	// the generator's journey diff compares route sets, so it would have reported
	// phantom changes on every run.
	//
	// Sorting the keys fixes the visit order, and the tie-break below fixes the
	// choice when two routes are the same length.
	keys := make([]string, 0, len(seen))
	for k := range seen {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	bestFor := map[string][]Step{}
	for _, k := range keys {
		v := seen[k]
		if v.node.Rancher == start.Rancher {
			continue
		}
		steps := path(seen, k)
		prev, ok := bestFor[v.node.Rancher]
		if !ok || betterRoute(steps, prev) {
			bestFor[v.node.Rancher] = steps
		}
	}

	dests := make([]string, 0, len(bestFor))
	for d := range bestFor {
		dests = append(dests, d)
	}
	sort.Slice(dests, func(i, j int) bool {
		a, _ := version.NewVersion(catalog.Normalize(dests[i]))
		b, _ := version.NewVersion(catalog.Normalize(dests[j]))
		return a.LessThan(b)
	})

	for _, d := range dests {
		res.Routes = append(res.Routes, Route{
			Destination: d,
			Steps:       bestFor[d],
			Granularity: granularityOf(c, start),
		})
	}
	return res, nil
}

// visit is one node in the breadth-first walk, with the edge that reached it.
type visit struct {
	node   Node
	parent string
	step   *Step
}

// betterRoute gives routes a total order so the choice never depends on map
// iteration. Shorter wins; equal lengths are broken by comparing steps in order,
// which is arbitrary but STABLE, and stable is the property that matters.
func betterRoute(candidate, incumbent []Step) bool {
	if len(candidate) != len(incumbent) {
		return len(candidate) < len(incumbent)
	}
	for i := range candidate {
		a, b := routeKey(candidate[i]), routeKey(incumbent[i])
		if a != b {
			return a < b
		}
	}
	return false
}

func routeKey(s Step) string {
	return string(s.Kind) + "|" + string(s.Platform) + "|" + s.From + "|" + s.To
}

// path walks parent pointers back to the start and returns the steps in order.
func path(seen map[string]visit, key string) []Step {
	var rev []Step
	for {
		v, ok := seen[key]
		if !ok || v.step == nil {
			break
		}
		rev = append(rev, *v.step)
		key = v.parent
	}
	steps := make([]Step, 0, len(rev))
	for i := len(rev) - 1; i >= 0; i-- {
		steps = append(steps, rev[i])
	}
	return steps
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
	for _, p := range []catalog.Platform{n.LocalPlatform, n.DownPlatform} {
		sup, ok := rv.SupportFor(p)
		if !ok || sup.Granularity == catalog.GranularityMinor {
			return catalog.GranularityMinor
		}
	}
	return catalog.GranularityRelease
}
