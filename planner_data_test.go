package main

import (
	"os"
	"strings"
	"testing"

	"github.com/hashicorp/go-version"
	"github.com/supporttools/rancher-upgrade-tool/internal/catalog"
	"github.com/supporttools/rancher-upgrade-tool/internal/planner"
)

// End-to-end against the shipped 70-version dataset. The fixture tests prove the
// algorithm on data built to exercise it; these prove it on the data users hit.

func shipped(t *testing.T) *catalog.Catalog {
	t.Helper()
	data, err := os.ReadFile("data/catalog.json")
	if err != nil {
		t.Fatalf("read data/catalog.json: %v", err)
	}
	c, err := catalog.Load(data)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	return c
}

func minor(t *testing.T, v string) (int, int) {
	t.Helper()
	p, err := version.NewVersion(strings.TrimPrefix(v, "v"))
	if err != nil {
		t.Fatalf("unparseable version %q: %v", v, err)
	}
	s := p.Segments()
	return s[0], s[1]
}

// The old waypoint rule capped every answer at Rancher 2.9.2: a ".9" suffix match
// plus three hardcoded strings (main.go:353) selected six waypoints out of 70
// versions, and the 2.10.x and 2.11.x data was loaded, sorted, then discarded.
func TestShippedCatalog_WaypointsReachTheNewestData(t *testing.T) {
	c := shipped(t)
	wp, err := planner.Waypoints(c)
	if err != nil {
		t.Fatalf("Waypoints: %v", err)
	}
	if len(wp) == 0 {
		t.Fatal("no waypoints")
	}
	newest := wp[len(wp)-1]
	if newest == "2.9.2" || strings.HasPrefix(newest, "2.9.") {
		t.Errorf("newest waypoint is %s; the old .9-suffix rule capped the tool here", newest)
	}
	seen := map[[2]int]bool{}
	for _, w := range wp {
		maj, min := minor(t, w)
		if seen[[2]int{maj, min}] {
			t.Errorf("more than one waypoint for the %d.%d line", maj, min)
		}
		seen[[2]int{maj, min}] = true
	}
	t.Logf("waypoints (%d): %v", len(wp), wp)
}

// REGRESSION, end to end: EKS on Rancher 2.5.12 produced nothing on the live site
// because of the v1.20.x typo. It must now plan.
func TestShippedCatalog_EKSOnRancher2512Plans(t *testing.T) {
	c := shipped(t)
	start := planner.NodeOf("2.5.12", catalog.RKE1, "v1.19", catalog.EKS, "v1.19")
	valid, err := planner.Valid(c, start)
	if err != nil {
		t.Fatalf("Valid: %v", err)
	}
	if !valid {
		t.Fatal("Rancher 2.5.12 with EKS v1.19 is not even a valid state; the EKS entry is still broken")
	}
	res, err := planner.Reachable(c, start)
	if err != nil {
		t.Fatalf("Reachable: %v", err)
	}
	if len(res.Routes) == 0 {
		t.Fatal("no routes from Rancher 2.5.12 on EKS; this is the case that has been silently empty since 2024")
	}
	t.Logf("2.5.12/EKS reaches %d destinations, furthest %s",
		len(res.Routes), res.Routes[len(res.Routes)-1].Destination)
}

// No route over the real dataset may skip a Kubernetes minor, on either axis.
// This is the upstream rule applied to production data rather than a fixture:
// https://kubernetes.io/releases/version-skew-policy/
func TestShippedCatalog_NoRouteSkipsAKubernetesMinor(t *testing.T) {
	c := shipped(t)
	starts := []planner.Node{
		planner.NodeOf("2.5.12", catalog.RKE1, "v1.19", catalog.EKS, "v1.19"),
		planner.NodeOf("2.6.9", catalog.RKE2, "v1.22", catalog.RKE2, "v1.22"),
		planner.NodeOf("2.8.5", catalog.K3s, "v1.26", catalog.AKS, "v1.26"),
		planner.NodeOf("2.9.4", catalog.RKE2, "v1.28", catalog.RKE2, "v1.28"),
	}
	for _, start := range starts {
		res, err := planner.Reachable(c, start)
		if err != nil {
			t.Fatalf("Reachable from %s: %v", start.Rancher, err)
		}
		for _, route := range res.Routes {
			for _, s := range route.Steps {
				if s.Kind != planner.StepLocal && s.Kind != planner.StepDown {
					continue
				}
				fMaj, fMin := minor(t, s.From)
				tMaj, tMin := minor(t, s.To)
				if fMaj != tMaj || tMin != fMin+1 {
					t.Errorf("from Rancher %s, route to %s skips: %s -> %s",
						start.Rancher, route.Destination, s.From, s.To)
				}
			}
		}
	}
}

// Every step a user is shown carries the source it came from. Advice with no
// provenance is the thing this rebuild exists to stop shipping.
func TestShippedCatalog_EveryStepCitesItsSource(t *testing.T) {
	c := shipped(t)
	start := planner.NodeOf("2.6.9", catalog.RKE2, "v1.22", catalog.RKE2, "v1.22")
	res, err := planner.Reachable(c, start)
	if err != nil {
		t.Fatalf("Reachable: %v", err)
	}
	if len(res.Routes) == 0 {
		t.Fatal("no routes to check")
	}
	for _, route := range res.Routes {
		for i, s := range route.Steps {
			if s.SourceURL == "" {
				t.Errorf("route to %s step %d (%s %s->%s) has no source URL",
					route.Destination, i+1, s.Kind, s.From, s.To)
			}
			if s.AsOf == "" {
				t.Errorf("route to %s step %d has no as-of date", route.Destination, i+1)
			}
		}
	}
}

// An unsupported start reports a named constraint, never an empty result that
// reads as "you are already current".
func TestShippedCatalog_UnsupportedStartExplainsItself(t *testing.T) {
	c := shipped(t)
	start := planner.NodeOf("2.9.4", catalog.RKE2, "v1.21", catalog.RKE2, "v1.21")
	res, err := planner.Reachable(c, start)
	if err != nil {
		t.Fatalf("Reachable: %v", err)
	}
	if len(res.Blockers) == 0 {
		t.Fatal("no blockers for an unsupported start")
	}
	b := res.Blockers[0]
	if b.Kind != planner.BlockerUnsupportedStart {
		t.Errorf("blocker kind = %q, want %q", b.Kind, planner.BlockerUnsupportedStart)
	}
	if !strings.Contains(b.Constraint, "2.9.4") {
		t.Errorf("constraint %q does not name the Rancher version", b.Constraint)
	}
	t.Logf("blocker: %s -- %s", b.Constraint, b.Detail)
}
