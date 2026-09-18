package main

import (
	"fmt"
	"strings"
	"testing"

	"github.com/supporttools/rancher-upgrade-tool/internal/catalog"
	"github.com/supporttools/rancher-upgrade-tool/internal/planner"
)

// Fleet regressions, asserted against the SHIPPED catalog.
//
// One Rancher manages many clusters and can only move when EVERY attached cluster
// sits inside the target version's window. Asking the single-cluster question once
// per cluster gives answers that are individually true and collectively wrong,
// because each one assumes the others do not exist. These prove the conjunction.
//
// Figures below were measured against data/catalog.json on 2026-09-17. If the
// catalog moves forward they will need re-deriving from the new windows — that is
// the point, not a maintenance burden.

func fleet(rancher string, localK8s string, clusterK8s ...string) planner.Node {
	n := planner.Node{
		Rancher:       rancher,
		LocalPlatform: catalog.RKE2,
		LocalK8s:      localK8s,
	}
	for i, k := range clusterK8s {
		n.Clusters = append(n.Clusters, planner.Cluster{
			ID:       fmt.Sprintf("c%d", i+1),
			Label:    fmt.Sprintf("Cluster %d", i+1),
			Platform: catalog.RKE2,
			K8s:      k,
		})
	}
	return n
}

func destinations(t *testing.T, res *planner.Result) []string {
	t.Helper()
	out := make([]string, 0, len(res.Routes))
	for _, r := range res.Routes {
		out = append(out, r.Destination)
	}
	return out
}

// CASE 1. The lagging clusters set the pace for the whole fleet.
//
// The user's original example: Cluster 1 v1.28, Cluster 2 v1.30, Cluster 3 v1.29,
// local v1.28, on Rancher 2.9.4.
//
// TWO DIFFERENT ANSWERS, and conflating them is the mistake this test pins down.
// The fleet CAN reach 2.15.1 -- by raising the lagging clusters first. But the
// highest version it can run TODAY, with no cluster upgrades at all, is 2.10.12,
// and the clusters holding it there are the actionable fact. Reporting only
// reachability would list seven destinations and never mention that two clusters
// and the local cluster set the pace.
func TestFleet_LaggingClustersSetThePace(t *testing.T) {
	c := shipped(t)
	start := fleet("2.9.4", "v1.28", "v1.28", "v1.30", "v1.29")
	res, err := planner.Reachable(c, start)
	if err != nil {
		t.Fatalf("Reachable: %v", err)
	}

	// Reachable eventually: everything, because the clusters can be raised.
	got := destinations(t, res)
	if len(got) == 0 || got[len(got)-1] != "2.15.1" {
		t.Errorf("reachable = %v, expected the walk to reach 2.15.1 by raising clusters first", got)
	}

	// Runnable today: only up to 2.10.12.
	if res.BindingConstraint == nil {
		t.Fatal("no binding constraint reported; the fleet cannot run the next Rancher version today")
	}
	bc := res.BindingConstraint
	if bc.HighestNow != "2.10.12" {
		t.Errorf("highest runnable today = %q, want 2.10.12", bc.HighestNow)
	}
	if bc.BlockedRancher != "2.11.17" {
		t.Errorf("blocked rancher = %q, want 2.11.17", bc.BlockedRancher)
	}

	behind := map[string]string{}
	for _, cl := range bc.Clusters {
		if cl.Position != planner.PositionBehind {
			t.Errorf("%s reported as %q; every out-of-range cluster here is behind", cl.ID, cl.Position)
		}
		behind[cl.ID] = cl.K8s
	}
	for id, k8s := range map[string]string{"c1": "v1.28", "c3": "v1.29", "local": "v1.28"} {
		if behind[id] != k8s {
			t.Errorf("expected %s on %s in the binding constraint, got %q", id, k8s, behind[id])
		}
	}
	// c2 is already at v1.30 and must NOT be named as holding anything back.
	if _, named := behind["c2"]; named {
		t.Error("Cluster 2 is already inside the window but was named as a constraint")
	}
}

// CASE 2. A cluster ABOVE the ceiling, reported as ahead and never as something
// to upgrade: Kubernetes has no downgrade.
//
// WHERE THIS CAN ACTUALLY OCCUR, which took a wrong test to work out. Window
// ceilings rise monotonically with the Rancher version, so a cluster inside the
// current window is never above a LATER ceiling. During a forward walk the "ahead"
// case is therefore unreachable on this catalog -- it can only arise in the START
// state, when the Rancher version someone is already running does not support a
// cluster that has been upgraded past it. That is exactly the situation a user
// gets into by upgrading a downstream cluster before Rancher.
func TestFleet_ClusterAheadOfCurrentRancherIsNamedNotRouted(t *testing.T) {
	c := shipped(t)
	// Rancher 2.8.15 tops out at v1.28 on rke2. A cluster already on v1.30 is ahead
	// of the Rancher managing it.
	start := fleet("2.8.15", "v1.28", "v1.28", "v1.30")
	res, err := planner.Reachable(c, start)
	if err != nil {
		t.Fatalf("Reachable: %v", err)
	}

	if len(res.Routes) != 0 {
		t.Errorf("planned %d routes from a state Rancher cannot support", len(res.Routes))
	}
	if len(res.Blockers) == 0 {
		t.Fatal("no blockers for an unsupported start state; an empty answer reads as 'you are fine'")
	}

	var named bool
	for _, b := range res.Blockers {
		if strings.Contains(b.Constraint, "v1.30") || strings.Contains(b.Detail, "v1.30") {
			named = true
		}
		if strings.Contains(strings.ToLower(b.Detail), "upgrade cluster 2") {
			t.Errorf("blocker tells the user to upgrade a cluster that is already too new: %q", b.Detail)
		}
	}
	if !named {
		t.Errorf("no blocker names the offending cluster's version; blockers = %+v", res.Blockers)
	}
}

// A cluster above the ceiling is reported with position "ahead", so a client can
// tell "raise this" from "this cannot be fixed".
func TestFleet_AheadPositionIsDistinguishedFromBehind(t *testing.T) {
	c := shipped(t)
	// Measured against Rancher 2.8.15, whose rke2 window is v1.25-v1.28: a cluster
	// on v1.30 is above the ceiling (ahead, unfixable) and one on v1.24 is below the
	// floor (behind, fixable by upgrading). ConstraintAgainst measures a hypothetical
	// target, so the node itself need not be valid under its own Rancher version.
	n := fleet("2.9.12", "v1.28", "v1.30", "v1.24")
	bc, err := planner.ConstraintAgainst(c, n, "2.8.15")
	if err != nil {
		t.Fatalf("ConstraintAgainst: %v", err)
	}
	if bc == nil {
		t.Fatal("expected clusters outside the 2.8.15 window")
	}
	pos := map[string]planner.ClusterPosition{}
	for _, cl := range bc.Clusters {
		pos[cl.ID] = cl.Position
	}
	if pos["c1"] != planner.PositionAhead {
		t.Errorf("c1 on v1.30 against a v1.28 ceiling = %q, want ahead", pos["c1"])
	}
	if pos["c2"] != planner.PositionBehind {
		t.Errorf("c2 on v1.24 against a v1.25 floor = %q, want behind", pos["c2"])
	}
}

// CASE 3. A fleet spread wider than the widest window cannot be managed by ANY
// single Rancher version, in any order. That is a fact about the estate, and the
// answer must carry its arithmetic rather than reading as "no route found".
func TestFleet_SpreadWiderThanAnyWindowIsTerminalAndExplained(t *testing.T) {
	c := shipped(t)
	// v1.28 and v1.34 is a 7-minor spread; the widest window in the catalog is 6.
	res, err := planner.Reachable(c, fleet("2.9.4", "v1.28", "v1.28", "v1.34"))
	if err != nil {
		t.Fatalf("Reachable: %v", err)
	}

	var spread *planner.Blocker
	for i := range res.Blockers {
		if res.Blockers[i].Kind == planner.BlockerFleetSpread {
			spread = &res.Blockers[i]
		}
	}
	if spread == nil {
		t.Fatalf("no fleet-spread blocker for a 7-minor fleet; blockers = %+v", res.Blockers)
	}
	for _, want := range []string{"v1.28", "v1.34", "7"} {
		if !strings.Contains(spread.Constraint, want) {
			t.Errorf("spread blocker constraint %q does not mention %q", spread.Constraint, want)
		}
	}
	if !strings.Contains(spread.Detail, "widest support window") {
		t.Errorf("spread blocker does not name the widest window: %q", spread.Detail)
	}
	if len(res.Routes) != 0 {
		t.Errorf("a fleet no Rancher version can manage returned %d routes", len(res.Routes))
	}
}

// CASE 4. No cluster's Kubernetes minor is ever skipped — asserted PER CLUSTER,
// not in aggregate. https://kubernetes.io/releases/version-skew-policy/
func TestFleet_NoClusterSkipsAKubernetesMinor(t *testing.T) {
	c := shipped(t)
	res, err := planner.Reachable(c, fleet("2.9.4", "v1.28", "v1.28", "v1.29", "v1.30"))
	if err != nil {
		t.Fatalf("Reachable: %v", err)
	}
	if len(res.Routes) == 0 {
		t.Fatal("no routes to check")
	}
	longest := res.Routes[len(res.Routes)-1]
	for _, s := range longest.Steps {
		if s.Kind == planner.StepRancher {
			continue
		}
		_, from := minor(t, s.From)
		_, to := minor(t, s.To)
		if to-from != 1 {
			t.Errorf("%s (%s) moved v1.%d -> v1.%d, skipping a minor", s.Cluster, s.ClusterLabel, from, to)
		}
	}
}

// CASE 5. Determinism with several clusters.
//
// The single-cluster version of this exists because ranging over a map produced
// four different route sets in twelve runs. With N clusters the order clusters are
// considered in is a second instance of the same hazard.
func TestFleet_ReachableIsDeterministic(t *testing.T) {
	c := shipped(t)
	start := fleet("2.9.4", "v1.28", "v1.28", "v1.30", "v1.29")

	var first string
	for i := 0; i < 20; i++ {
		res, err := planner.Reachable(c, start)
		if err != nil {
			t.Fatalf("Reachable: %v", err)
		}
		var b strings.Builder
		for _, r := range res.Routes {
			fmt.Fprintf(&b, "%s|", r.Destination)
			for _, s := range r.Steps {
				fmt.Fprintf(&b, "%s,%s,%s,%s,%d;", s.Kind, s.Cluster, s.From, s.To, s.Wave)
			}
		}
		if i == 0 {
			first = b.String()
			continue
		}
		if b.String() != first {
			t.Fatalf("run %d differs from run 0:\n  %s\n  %s", i, first, b.String())
		}
	}
}

// CASE 6. The fleet form of the union bug: no route may contain a state where any
// cluster sits outside the Rancher version in effect at that point.
func TestFleet_NoStepLandsOutsideTheRancherInEffect(t *testing.T) {
	c := shipped(t)
	start := fleet("2.9.4", "v1.28", "v1.28", "v1.29", "v1.30")
	res, err := planner.Reachable(c, start)
	if err != nil {
		t.Fatalf("Reachable: %v", err)
	}

	for _, route := range res.Routes {
		state := start
		for i, s := range route.Steps {
			switch s.Kind {
			case planner.StepRancher:
				state.Rancher = s.To
			default:
				state = applyAxis(state, s.Cluster, s.To)
			}
			ok, err := planner.Valid(c, state)
			if err != nil {
				t.Fatalf("Valid: %v", err)
			}
			if !ok {
				t.Fatalf("route to %s is invalid after step %d (%s %s -> %s): some cluster is "+
					"outside Rancher %s", route.Destination, i+1, s.Kind, s.From, s.To, state.Rancher)
			}
		}
	}
}

func applyAxis(n planner.Node, id, k8s string) planner.Node {
	if id == planner.LocalID {
		n.LocalK8s = k8s
		return n
	}
	cl := make([]planner.Cluster, len(n.Clusters))
	copy(cl, n.Clusters)
	for i := range cl {
		if cl[i].ID == id {
			cl[i].K8s = k8s
		}
	}
	n.Clusters = cl
	return n
}

// CASE 7. Monotonicity: adding a cluster can never INCREASE the set of reachable
// destinations. More constraints cannot unlock more targets.
//
// This is the cheapest global check on the whole model — it holds for every fleet,
// so it runs over generated input rather than fixtures, and a violation means the
// conjunction is wrong somewhere without needing to know the right answer.
//
// Generated, not random: a random triple of versions almost never lands inside a
// 3-6 minor window, so random sampling here looks like a strong test and is nearly
// vacuous. The F3 spike found only 8 valid states in 4,000 random draws.
func TestFleet_AddingAClusterNeverUnlocksMoreDestinations(t *testing.T) {
	c := shipped(t)
	versions := []string{"v1.27", "v1.28", "v1.29", "v1.30", "v1.31", "v1.32"}
	ranchers := []string{"2.9.4", "2.10.5", "2.11.3"}

	checked := 0
	for _, r := range ranchers {
		for _, base := range versions {
			for _, extra := range versions {
				small := fleet(r, base, base)
				big := fleet(r, base, base, extra)

				okSmall, err := planner.Valid(c, small)
				if err != nil {
					t.Fatalf("Valid: %v", err)
				}
				okBig, err := planner.Valid(c, big)
				if err != nil {
					t.Fatalf("Valid: %v", err)
				}
				if !okSmall || !okBig {
					continue // only compare states both fleets can legally start from
				}
				checked++

				resSmall, err := planner.Reachable(c, small)
				if err != nil {
					t.Fatalf("Reachable: %v", err)
				}
				resBig, err := planner.Reachable(c, big)
				if err != nil {
					t.Fatalf("Reachable: %v", err)
				}

				have := map[string]bool{}
				for _, d := range destinations(t, resSmall) {
					have[d] = true
				}
				for _, d := range destinations(t, resBig) {
					if !have[d] {
						t.Errorf("adding a cluster on %s UNLOCKED %s (rancher %s, base %s). "+
							"More constraints cannot reach more targets; the conjunction is wrong.",
							extra, d, r, base)
					}
				}
			}
		}
	}
	if checked == 0 {
		t.Fatal("no valid fleet pairs were compared; the generator produced nothing testable")
	}
	t.Logf("compared %d valid fleet pairs", checked)
}

// Waves group steps with no ordering requirement between them. A Rancher hop always
// occupies its own wave; the cluster raises between two hops share one.
func TestFleet_WavesGroupIndependentWork(t *testing.T) {
	c := shipped(t)
	res, err := planner.Reachable(c, fleet("2.9.4", "v1.28", "v1.28", "v1.29"))
	if err != nil {
		t.Fatalf("Reachable: %v", err)
	}
	if len(res.Routes) == 0 {
		t.Fatal("no routes")
	}
	for _, r := range res.Routes {
		waveKinds := map[int]map[planner.StepKind]bool{}
		for _, s := range r.Steps {
			if s.Wave == 0 {
				t.Errorf("step %s %s->%s carries no wave", s.Kind, s.From, s.To)
			}
			if waveKinds[s.Wave] == nil {
				waveKinds[s.Wave] = map[planner.StepKind]bool{}
			}
			waveKinds[s.Wave][s.Kind] = true
		}
		// A Rancher hop never shares a wave with cluster work: it depends on it.
		for w, kinds := range waveKinds {
			if kinds[planner.StepRancher] && len(kinds) > 1 {
				t.Errorf("wave %d mixes a Rancher hop with cluster raises; the hop depends on them", w)
			}
		}
	}
}
