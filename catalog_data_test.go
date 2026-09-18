package main

import (
	"os"
	"strings"
	"testing"

	"github.com/supporttools/rancher-upgrade-tool/internal/catalog"
)

// The shipped dataset must pass the same validation the service applies at
// startup. Fixtures prove the loader; this proves the data. Without it a bad
// catalog would only be discovered when a pod failed to start, which under
// fail-closed loading means discovering it in production.
func TestShippedCatalogIsValid(t *testing.T) {
	data, err := os.ReadFile("data/catalog.json")
	if err != nil {
		t.Fatalf("read data/catalog.json: %v", err)
	}
	c, err := catalog.Load(data)
	if err != nil {
		t.Fatalf("shipped catalog failed validation: %v", err)
	}
	if len(c.Rancher) == 0 {
		t.Fatal("shipped catalog contains no Rancher versions")
	}
	t.Logf("shipped catalog: %d Rancher versions, generated %s", len(c.Rancher), c.GeneratedAt)
}

// REGRESSION: EKS must resolve for Rancher 2.5.10 through 2.5.16.
//
// These seven entries carried max_version "v1.20.x", which does not parse, so
// getSortedK8sVersions hit `continue` (main.go:263-270) and dropped the whole EKS
// entry. Asking the live site about EKS on Rancher 2.5.12 returns nothing today,
// with total confidence, because of a typo from 2024.
func TestRegression_ShippedCatalogResolvesEKSForRancher25x(t *testing.T) {
	data, err := os.ReadFile("data/catalog.json")
	if err != nil {
		t.Fatalf("read data/catalog.json: %v", err)
	}
	c, err := catalog.Load(data)
	if err != nil {
		t.Fatalf("load: %v", err)
	}

	for _, v := range []string{"2.5.10", "2.5.11", "2.5.12", "2.5.13", "2.5.14", "2.5.15", "2.5.16"} {
		rv, ok := c.Find(v)
		if !ok {
			t.Errorf("Rancher %s missing from the catalog", v)
			continue
		}
		sup, ok := rv.SupportFor(catalog.EKS)
		if !ok {
			t.Errorf("Rancher %s: EKS not found; this is the defect that has been live since 2024", v)
			continue
		}
		if sup.MaxVersion == "v1.20.x" {
			t.Errorf("Rancher %s: EKS max_version is still the unparseable %q", v, sup.MaxVersion)
		}
		covered, err := sup.Covers("v1.20")
		if err != nil {
			t.Errorf("Rancher %s: EKS range unusable: %v", v, err)
			continue
		}
		if !covered {
			t.Errorf("Rancher %s: EKS range %s-%s does not cover v1.20",
				v, sup.MinVersion, sup.MaxVersion)
		}
	}
}

// Every Rancher version in the shipped catalog carries a lifecycle status and the
// date it was derived as of. A status with no date is not honest.
func TestShippedCatalogCarriesLifecycle(t *testing.T) {
	data, err := os.ReadFile("data/catalog.json")
	if err != nil {
		t.Fatalf("read data/catalog.json: %v", err)
	}
	c, err := catalog.Load(data)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	counts := map[catalog.Lifecycle]int{}
	for _, rv := range c.Rancher {
		counts[rv.Lifecycle]++
	}
	if counts[catalog.LifecycleEnded] == 0 {
		t.Error("no Rancher version is marked ended; the catalog spans 2.5.x, which is long over")
	}
	t.Logf("lifecycle spread: %v", counts)
}

// F16: the window-shape properties the fleet planner deliberately does NOT rely on.
//
//	P1  window floors are non-decreasing as the Rancher version rises
//	P2  window ceilings are non-decreasing
//	P3  consecutive windows OVERLAP: floor(R+1) <= ceiling(R)
//
// The planner stays correct when these fail -- it raises one minor at a time and
// validates each intermediate against the Rancher version in effect. They are
// asserted anyway because a P3 violation is a TRUE fact about upstream with an
// operational meaning: no fleet can cross that Rancher hop at all, whatever order
// the upgrades are done in. That deserves to be named once, here, rather than
// rediscovered one query at a time.
// knownWindowFindings are violations that are REAL, SOURCED upstream facts rather
// than data defects, verified by hand against the cited matrix pages. They are
// listed rather than suppressed so that a NEW violation still fails.
//
// Do not add to this list to make a test pass. Re-read the source first.
var knownWindowFindings = map[string]string{
	"P2|eks|2.6.14|2.7.0": "Rancher 2.7.0 genuinely NARROWED EKS support. 2.6.14 covered " +
		"v1.20 to v1.24.12-eks-ec5523e; 2.7.0 covers only v1.23 to v1.23.10-eks-15b7512. " +
		"Verified 2026-09-17 against the per-version SUSE matrix pages for v2-6-14 and " +
		"v2-7-0. The floor rose and the ceiling fell in the same step, which is a real " +
		"support decision, not a scrape error.",
}

func TestShippedCatalog_WindowsAreMonotonicAndOverlapping(t *testing.T) {
	c := shipped(t)
	for _, f := range catalog.CheckWindows(c) {
		key := strings.Join([]string{f.Property, string(f.Platform), f.From, f.To}, "|")
		if why, known := knownWindowFindings[key]; known {
			t.Logf("known finding %s: %s", key, why)
			continue
		}
		// A P3 gap is the serious one: no fleet can cross that Rancher hop at all.
		if f.Property == "P3" {
			t.Errorf("NEW uncrossable window gap: %s", f)
			continue
		}
		t.Errorf("NEW window-shape finding, not in the known list: %s\n"+
			"The planner tolerates this by design, but the data changed shape. Re-read the "+
			"cited source, then either fix the generator or add it to knownWindowFindings "+
			"with the evidence.", f)
	}
}

// The checker must actually catch a gap, or asserting it proves nothing.
func TestCheckWindows_CatchesAGapAndABackwardsCeiling(t *testing.T) {
	gapped := []byte(`{
	  "generated_at": "2026-09-17",
	  "rancher": [
	    {"version": "2.9.0", "lifecycle": "full", "lifecycle_as_of": "2026-09-17",
	     "platforms": [{"platform": "rke2", "min_version": "v1.20.0", "max_version": "v1.22.0",
	       "granularity": "minor", "releases": [],
	       "source_url": "https://example.invalid/a", "as_of": "2026-09-17"}]},
	    {"version": "2.10.0", "lifecycle": "full", "lifecycle_as_of": "2026-09-17",
	     "platforms": [{"platform": "rke2", "min_version": "v1.25.0", "max_version": "v1.27.0",
	       "granularity": "minor", "releases": [],
	       "source_url": "https://example.invalid/b", "as_of": "2026-09-17"}]},
	    {"version": "2.11.0", "lifecycle": "full", "lifecycle_as_of": "2026-09-17",
	     "platforms": [{"platform": "rke2", "min_version": "v1.26.0", "max_version": "v1.26.0",
	       "granularity": "minor", "releases": [],
	       "source_url": "https://example.invalid/c", "as_of": "2026-09-17"}]}
	  ]
	}`)
	c, err := catalog.Load(gapped)
	if err != nil {
		t.Fatalf("fixture should load (it is well-formed, just awkward): %v", err)
	}

	findings := catalog.CheckWindows(c)
	seen := map[string]bool{}
	for _, f := range findings {
		seen[f.Property] = true
	}
	// 2.9.0 ends at v1.22 and 2.10.0 starts at v1.25: a gap.
	if !seen["P3"] {
		t.Errorf("no P3 finding for non-overlapping windows; findings = %v", findings)
	}
	// 2.10.0 tops out at v1.27, 2.11.0 at v1.26: the ceiling went backwards.
	if !seen["P2"] {
		t.Errorf("no P2 finding for a backwards ceiling; findings = %v", findings)
	}
	for _, f := range findings {
		if f.Property == "P3" && !strings.Contains(f.Detail, "No fleet can cross this hop") {
			t.Errorf("P3 finding does not explain the operational consequence: %q", f.Detail)
		}
	}
}
