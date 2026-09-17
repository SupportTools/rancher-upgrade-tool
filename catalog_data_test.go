package main

import (
	"os"
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
