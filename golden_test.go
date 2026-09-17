package main

import (
	"encoding/json"
	"fmt"
	"os"
	"testing"

	"github.com/supporttools/rancher-upgrade-tool/internal/catalog"
	"github.com/supporttools/rancher-upgrade-tool/internal/planner"
)

// Golden expectations, hand-verified against cited upstream sources and asserted
// against the SHIPPED catalog.
//
// Unit tests prove the algorithm on fixtures built to exercise it. They say nothing
// about whether the data is right, and the data now arrives from two automated
// producers. A scraper that reads a redesigned table can emit values that are
// well-formed but false: every unit test stays green, the loader accepts them
// because they parse, and the pull-request diff looks like a normal refresh.
//
// These catch that, because they encode what a human read off the source rather
// than what the code last produced. A failure means upstream changed or the
// generator broke, and a snapshot test cannot tell those apart.
//
// A failure is NOT fixed by editing the expectation. Re-read the cited source.

type goldenRange struct {
	Rancher    string `json:"rancher"`
	Platform   string `json:"platform"`
	Min        string `json:"min"`
	Max        string `json:"max"`
	Source     string `json:"source"`
	VerifiedOn string `json:"verified_on"`
	Note       string `json:"note"`
}

type goldenState struct {
	Name      string `json:"name"`
	Valid     bool   `json:"valid"`
	Rancher   string `json:"rancher"`
	LocalPlat string `json:"local_platform"`
	LocalK8s  string `json:"local_k8s"`
	DownPlat  string `json:"downstream_platform"`
	DownK8s   string `json:"downstream_k8s"`
	Why       string `json:"why"`
}

type goldenReach struct {
	Name      string   `json:"name"`
	Rancher   string   `json:"rancher"`
	LocalPlat string   `json:"local_platform"`
	LocalK8s  string   `json:"local_k8s"`
	DownPlat  string   `json:"downstream_platform"`
	DownK8s   string   `json:"downstream_k8s"`
	MustReach []string `json:"must_reach"`
	Why       string   `json:"why"`
}

type goldenFile struct {
	SupportedRanges []goldenRange `json:"supported_ranges"`
	States          []goldenState `json:"states"`
	Reachability    []goldenReach `json:"reachability"`
}

func loadGolden(t *testing.T) (*goldenFile, *catalog.Catalog) {
	t.Helper()
	raw, err := os.ReadFile("testdata/golden-routes.json")
	if err != nil {
		t.Fatalf("read goldens: %v", err)
	}
	var g goldenFile
	if err := json.Unmarshal(raw, &g); err != nil {
		t.Fatalf("parse goldens: %v", err)
	}
	if len(g.SupportedRanges) == 0 || len(g.States) == 0 || len(g.Reachability) == 0 {
		t.Fatal("golden file is missing whole sections; it is the only check that " +
			"distinguishes an upstream change from a parser failure")
	}
	data, err := os.ReadFile("data/catalog.json")
	if err != nil {
		t.Fatalf("read catalog: %v", err)
	}
	c, err := catalog.Load(data)
	if err != nil {
		t.Fatalf("shipped catalog does not load: %v", err)
	}
	return &g, c
}

func fail(t *testing.T, source, verifiedOn, format string, args ...any) {
	t.Helper()
	t.Errorf("%s\n      This expectation was read off %s on %s.\n"+
		"      Do NOT edit it to make this pass. Re-read that page. If upstream "+
		"changed, update the entry and verified_on together and say so in the commit.",
		fmt.Sprintf(format, args...), source, verifiedOn)
}

func TestGolden_SupportedRangesMatchUpstream(t *testing.T) {
	g, c := loadGolden(t)
	for _, want := range g.SupportedRanges {
		rv, ok := c.Find(want.Rancher)
		if !ok {
			fail(t, want.Source, want.VerifiedOn, "Rancher %s is absent from the catalog", want.Rancher)
			continue
		}
		sup, ok := rv.SupportFor(catalog.Platform(want.Platform))
		if !ok {
			fail(t, want.Source, want.VerifiedOn, "Rancher %s has no %s entry", want.Rancher, want.Platform)
			continue
		}
		if sup.MinVersion != want.Min || sup.MaxVersion != want.Max {
			fail(t, want.Source, want.VerifiedOn,
				"Rancher %s / %s range is %s-%s, expected %s-%s",
				want.Rancher, want.Platform, sup.MinVersion, sup.MaxVersion, want.Min, want.Max)
		}
	}
}

func TestGolden_StatesValidateAsExpected(t *testing.T) {
	g, c := loadGolden(t)
	for _, want := range g.States {
		node := planner.Node{
			Rancher:       want.Rancher,
			LocalPlatform: catalog.Platform(want.LocalPlat), LocalK8s: want.LocalK8s,
			DownPlatform: catalog.Platform(want.DownPlat), DownK8s: want.DownK8s,
		}
		got, err := planner.Valid(c, node)
		if err != nil {
			t.Errorf("%s: Valid returned an error: %v", want.Name, err)
			continue
		}
		if got != want.Valid {
			t.Errorf("%s\n      Valid = %v, expected %v.\n      Why this matters: %s",
				want.Name, got, want.Valid, want.Why)
		}
	}
}

func TestGolden_DestinationsRemainReachable(t *testing.T) {
	g, c := loadGolden(t)
	for _, want := range g.Reachability {
		node := planner.Node{
			Rancher:       want.Rancher,
			LocalPlatform: catalog.Platform(want.LocalPlat), LocalK8s: want.LocalK8s,
			DownPlatform: catalog.Platform(want.DownPlat), DownK8s: want.DownK8s,
		}
		res, err := planner.Reachable(c, node)
		if err != nil {
			t.Errorf("%s: Reachable returned an error: %v", want.Name, err)
			continue
		}
		reached := map[string]bool{}
		for _, r := range res.Routes {
			reached[r.Destination] = true
		}
		for _, target := range want.MustReach {
			if !reached[target] {
				got := make([]string, 0, len(res.Routes))
				for _, r := range res.Routes {
					got = append(got, r.Destination)
				}
				t.Errorf("%s\n      Rancher %s can no longer reach %s. Reached: %v\n"+
					"      Why this matters: %s",
					want.Name, want.Rancher, target, got, want.Why)
			}
		}
	}
}
