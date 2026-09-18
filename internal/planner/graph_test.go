package planner

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hashicorp/go-version"
	"github.com/supporttools/rancher-upgrade-tool/internal/catalog"
)

// The seven tests marked REGRESSION are mandatory. Each one proves a specific
// defect found in the pre-rewrite planner, or a behaviour that must survive the
// rewrite. They are not up for negotiation when the suite gets inconvenient.

func load(t *testing.T, name string) *catalog.Catalog {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	c, err := catalog.Load(data)
	if err != nil {
		t.Fatalf("load fixture %s: %v", name, err)
	}
	return c
}

func mustMinor(t *testing.T, v string) (int, int) {
	t.Helper()
	parsed, err := version.NewVersion(strings.TrimPrefix(v, "v"))
	if err != nil {
		t.Fatalf("unparseable version %q in emitted step: %v", v, err)
	}
	seg := parsed.Segments()
	if len(seg) < 2 {
		t.Fatalf("version %q has no minor segment", v)
	}
	return seg[0], seg[1]
}

// REGRESSION 3 — the union bug.
//
// getSortedK8sVersions did append(r1.SupportedPlatforms, r2.SupportedPlatforms...)
// (main.go:257) and walked the merged set, so a step could land on a version
// supported by neither Rancher version alone. Here 2.9.6 covers v1.27-v1.30 and
// 2.10.7 covers v1.28-v1.31; their union contains v1.27, which 2.10.7 does not
// support. Every endpoint of every emitted edge must validate on its own.
func TestRegression_EveryEdgeEndpointIndividuallyValid(t *testing.T) {
	c := load(t, "basic.json")
	start := NodeOf("2.9.6", catalog.RKE2, "v1.27", catalog.RKE2, "v1.27")

	res, err := Reachable(c, start)
	if err != nil {
		t.Fatalf("Reachable: %v", err)
	}
	for _, route := range res.Routes {
		cur := start
		for i, step := range route.Steps {
			ok, err := Valid(c, cur)
			if err != nil {
				t.Fatalf("Valid(from) step %d: %v", i, err)
			}
			if !ok {
				t.Errorf("route to %s step %d departs from an INVALID node %+v; "+
					"this is the union bug: a state supported by neither Rancher version alone",
					route.Destination, i, cur)
			}
			cur = applyStep(cur, step)
			ok, err = Valid(c, cur)
			if err != nil {
				t.Fatalf("Valid(to) step %d: %v", i, err)
			}
			if !ok {
				t.Errorf("route to %s step %d arrives at an INVALID node %+v",
					route.Destination, i, cur)
			}
		}
	}
}

// REGRESSION 4 — an edge moves exactly one component.
//
// If a single step changes both Rancher and Kubernetes, the "both endpoints valid"
// guarantee says nothing about the state in between, and the guarantee is the
// whole design.
func TestRegression_ExactlyOneComponentPerEdge(t *testing.T) {
	c := load(t, "basic.json")
	start := NodeOf("2.9.6", catalog.RKE2, "v1.27", catalog.RKE2, "v1.27")

	edges, err := Edges(c, start)
	if err != nil {
		t.Fatalf("Edges: %v", err)
	}
	if len(edges) == 0 {
		t.Fatal("no edges out of a valid start node")
	}
	for _, e := range edges {
		moved := 0
		if e.From.Rancher != e.To.Rancher {
			moved++
		}
		if e.From.LocalK8s != e.To.LocalK8s {
			moved++
		}
		if e.From.Clusters[0].K8s != e.To.Clusters[0].K8s {
			moved++
		}
		if moved != 1 {
			t.Errorf("edge moved %d components, want exactly 1: %+v -> %+v", moved, e.From, e.To)
		}
	}
}

// REGRESSION 5 — ordering: Kubernetes before the Rancher hop.
//
// PlanUpgrade appended the Rancher step (main.go:136-138) before deriving any
// Kubernetes step (main.go:146-149). From 2.9.6 at v1.27, hopping to 2.10.7 is
// impossible because 2.10.7's minimum is v1.28. The Kubernetes raise must come
// first, and no edge out of the start node may be a Rancher hop.
func TestRegression_K8sRaisedBeforeRancherHop(t *testing.T) {
	c := load(t, "basic.json")
	start := NodeOf("2.9.6", catalog.RKE2, "v1.27", catalog.RKE2, "v1.27")

	edges, err := Edges(c, start)
	if err != nil {
		t.Fatalf("Edges: %v", err)
	}
	for _, e := range edges {
		if e.Step.Kind == StepRancher {
			t.Errorf("emitted a Rancher hop %s -> %s while the cluster is on v1.27, "+
				"below that target's minimum; Kubernetes must be raised first",
				e.From.Rancher, e.To.Rancher)
		}
	}

	res, err := Reachable(c, start)
	if err != nil {
		t.Fatalf("Reachable: %v", err)
	}
	for _, route := range res.Routes {
		for i, step := range route.Steps {
			if step.Kind == StepRancher {
				if i == 0 {
					t.Errorf("route to %s leads with a Rancher hop from an unsupported "+
						"Kubernetes version", route.Destination)
				}
				break
			}
		}
	}
}

// REGRESSION 6 — no Kubernetes minor is ever skipped, on either axis.
//
// Upstream rule, https://kubernetes.io/releases/version-skew-policy/ :
// "Project policies for API deprecation and API change guidelines require
// kube-apiserver to not skip minor versions when upgrading, even in
// single-instance clusters."
//
// ace8f40 hardcoded this as an unsourced constant with no test. This is the test.
func TestRegression_NoKubernetesMinorSkipped(t *testing.T) {
	c := load(t, "basic.json")
	start := NodeOf("2.9.6", catalog.RKE2, "v1.27", catalog.RKE2, "v1.27")

	res, err := Reachable(c, start)
	if err != nil {
		t.Fatalf("Reachable: %v", err)
	}
	for _, route := range res.Routes {
		for _, step := range route.Steps {
			if step.Kind != StepLocal && step.Kind != StepDown {
				continue
			}
			fromMaj, fromMin := mustMinor(t, step.From)
			toMaj, toMin := mustMinor(t, step.To)
			if toMaj != fromMaj {
				t.Errorf("step crossed a major boundary %s -> %s without an explanation",
					step.From, step.To)
				continue
			}
			if toMin != fromMin && toMin != fromMin+1 {
				t.Errorf("step skipped a Kubernetes minor: %s -> %s (route to %s). "+
					"kube-apiserver must not skip minor versions",
					step.From, step.To, route.Destination)
			}
		}
	}
}

// REGRESSION 7 — no Rancher minor is skipped when one is absent from the catalog.
//
// Adjacency must be NUMERIC, not catalog order. In this fixture the 2.10 line is
// removed, so 2.11.3 is the next entry present and both endpoints validate at
// v1.30. A catalog-order walk would hop 2.9.6 -> 2.11.3 and silently skip 2.10.
// Missing data is a blocker, not a shortcut.
func TestRegression_NoRancherMinorSkippedWhenCatalogHasAGap(t *testing.T) {
	c := load(t, "missing-minor.json")

	next, blocker, err := NextRancherWaypoint(c, "2.9.6")
	if err != nil {
		t.Fatalf("NextRancherWaypoint: %v", err)
	}
	if blocker == nil {
		t.Fatalf("expected a blocker for the missing 2.10 line, got next=%q", next)
	}
	if blocker.Kind != BlockerMissingEvidence {
		t.Errorf("blocker kind = %q, want %q", blocker.Kind, BlockerMissingEvidence)
	}
	if next == "2.11.3" {
		t.Error("skipped the absent 2.10 minor straight to 2.11.3; adjacency must be " +
			"numeric, not catalog order")
	}

	start := NodeOf("2.9.6", catalog.RKE2, "v1.30", catalog.RKE2, "v1.30")
	res, err := Reachable(c, start)
	if err != nil {
		t.Fatalf("Reachable: %v", err)
	}
	for _, route := range res.Routes {
		for _, step := range route.Steps {
			if step.Kind == StepRancher && step.From == "2.9.6" && step.To == "2.11.3" {
				t.Error("route hopped 2.9.6 -> 2.11.3 across the absent 2.10 minor")
			}
		}
	}
}

// Waypoints are the latest patch of each minor, not every patch and not a string
// suffix match. GetKeyVersions used strings.HasSuffix(v, ".9") plus three
// hardcoded strings (main.go:353), which is why nothing past 2.9.2 was reachable.
func TestWaypoints_LatestPatchPerMinor(t *testing.T) {
	c := load(t, "basic.json")
	got, err := Waypoints(c)
	if err != nil {
		t.Fatalf("Waypoints: %v", err)
	}
	want := []string{"2.9.6", "2.10.7", "2.11.3"}
	if len(got) != len(want) {
		t.Fatalf("Waypoints() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("Waypoints()[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

// An unsupported start has no outgoing edges by construction. Saying so is correct;
// concluding that recovery is impossible is not. The three negative outcomes must
// stay distinguishable, because an undifferentiated empty result reads to a user as
// "you are already current".
func TestUnsupportedStart_IsNotAnEmptyResult(t *testing.T) {
	c := load(t, "basic.json")
	start := NodeOf("2.9.6", catalog.RKE2, "v1.21", catalog.RKE2, "v1.21")

	res, err := Reachable(c, start)
	if err != nil {
		t.Fatalf("Reachable: %v", err)
	}
	if len(res.Blockers) == 0 {
		t.Fatal("unsupported start produced no blockers; an empty result is indistinguishable " +
			"from 'nothing to do'")
	}
	if res.Blockers[0].Kind != BlockerUnsupportedStart {
		t.Errorf("blocker kind = %q, want %q", res.Blockers[0].Kind, BlockerUnsupportedStart)
	}
	if res.Blockers[0].Constraint == "" {
		t.Error("blocker does not name the constraint that stopped the walk")
	}
}

// Rancher Manager installs on hosted providers too. The T0 spike corrected an
// earlier assumption that localPlatform was restricted to RKE2 and k3s: every
// sampled SUSE matrix lists AKS, EKS and GKE under "Supported Kubernetes Platforms
// for Rancher Manager".
func TestLocalPlatform_HostedProvidersCanHostRancher(t *testing.T) {
	c := load(t, "basic.json")
	start := NodeOf("2.9.6", catalog.EKS, "v1.28", catalog.RKE2, "v1.28")

	ok, err := Valid(c, start)
	if err != nil {
		t.Fatalf("Valid: %v", err)
	}
	if !ok {
		t.Error("Rancher on EKS managing RKE2 was rejected; hosted providers are valid " +
			"install targets per the Rancher Manager platforms table")
	}
}

// Granularity is asymmetric because the sources are. A route touching a hosted
// provider is minor-level and must say so, rather than looking as precise as an
// RKE2 route planned from real KDM releases.
func TestRoute_HostedProviderIsMinorGranularity(t *testing.T) {
	c := load(t, "basic.json")
	start := NodeOf("2.9.6", catalog.RKE2, "v1.28", catalog.EKS, "v1.28")

	res, err := Reachable(c, start)
	if err != nil {
		t.Fatalf("Reachable: %v", err)
	}
	if len(res.Routes) == 0 {
		t.Fatal("no routes for an EKS downstream cluster")
	}
	for _, route := range res.Routes {
		if route.Granularity != catalog.GranularityMinor {
			t.Errorf("route to %s has granularity %q, want %q: it touches a hosted provider",
				route.Destination, route.Granularity, catalog.GranularityMinor)
		}
	}
}

// The answer covers one local/downstream pair. Upgrading Rancher affects every
// cluster it manages, so the result must not read as fleet-wide clearance.
func TestResult_StatesItsScope(t *testing.T) {
	c := load(t, "basic.json")
	start := NodeOf("2.9.6", catalog.RKE2, "v1.28", catalog.RKE2, "v1.28")

	res, err := Reachable(c, start)
	if err != nil {
		t.Fatalf("Reachable: %v", err)
	}
	if strings.TrimSpace(res.Scope) == "" {
		t.Error("Result.Scope is empty; a single-pair answer must say so")
	}
}

// Guard against the stubs being mistaken for an implementation.
func TestStubsAreNotImplemented(t *testing.T) {
	_, err := Reachable(nil, Node{})
	if !errors.Is(err, ErrNotImplemented) {
		t.Skip("planner implemented; this guard has served its purpose")
	}
}

// applyStep advances a node by one step, mirroring what the planner must do.
func applyStep(n Node, s Step) Node {
	switch s.Kind {
	case StepRancher:
		n.Rancher = s.To
	case StepLocal:
		n.LocalK8s = s.To
	case StepDown:
		n.Clusters[0].K8s = s.To
	}
	return n
}

// REGRESSION: route selection must be deterministic.
//
// Reachable used to build its result by ranging over a map. Go randomises map
// iteration, so two routes of equal length to the same destination were chosen by
// whichever the runtime visited first: the same query returned different step
// sequences between requests, and the generator's journey diff compares route sets,
// so it reported phantom changes on every run.
//
// The property tests did not catch this, because they assert things that hold for
// any valid route. Only comparing whole results across runs exposes it.
func TestRegression_ReachableIsDeterministic(t *testing.T) {
	c := load(t, "basic.json")
	start := NodeOf("2.9.6", catalog.RKE2, "v1.27", catalog.RKE2, "v1.27")

	signature := func() string {
		res, err := Reachable(c, start)
		if err != nil {
			t.Fatalf("Reachable: %v", err)
		}
		var b strings.Builder
		for _, r := range res.Routes {
			b.WriteString(r.Destination + ":")
			for _, s := range r.Steps {
				b.WriteString(string(s.Kind) + s.From + ">" + s.To + ",")
			}
			b.WriteString("|")
		}
		return b.String()
	}

	first := signature()
	for i := 0; i < 25; i++ {
		if got := signature(); got != first {
			t.Fatalf("run %d produced a different result.\n  first: %s\n  got:   %s",
				i+2, first, got)
		}
	}
}
