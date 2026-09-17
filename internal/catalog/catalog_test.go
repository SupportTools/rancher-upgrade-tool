package catalog

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func read(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	return data
}

// REGRESSION 1 — a malformed range is refused, not silently dropped.
//
// getSortedK8sVersions did `continue` on an unparseable min or max
// (main.go:263-270), discarding the entire platform entry with no log line and no
// error. That is why "v1.20.x" in the EKS rows for Rancher 2.5.10-2.5.16 has made
// EKS invisible for those versions since 2024 without anything ever complaining.
//
// It matters more now than it did then: once a scraper feeds this file, a
// mis-parsed value would vanish at load time rather than appear in the pull-request
// diff a human reviews. Fail closed, and name the offending path.
func TestRegression_MalformedRangeIsRefusedNotDropped(t *testing.T) {
	c, err := Load(read(t, "malformed-eks.json"))
	if err == nil {
		t.Fatal("Load accepted a catalog containing max_version \"v1.20.x\"; " +
			"a malformed range must fail closed, never be silently dropped")
	}
	if c != nil {
		t.Error("Load returned a catalog alongside an error; a partial dataset must never escape")
	}
	msg := err.Error()
	for _, want := range []string{"2.5.12", "eks", "v1.20.x"} {
		if !strings.Contains(strings.ToLower(msg), strings.ToLower(want)) {
			t.Errorf("error %q does not name %q; the message must locate the offending entry", msg, want)
		}
	}
}

// REGRESSION 2 — EKS resolves for the versions the typo hid.
//
// The live wrong answer today: ask rancher.tips about EKS on Rancher 2.5.12 and it
// reports nothing, with total confidence.
func TestRegression_EKSResolvesAfterTheDataFix(t *testing.T) {
	c, err := Load(read(t, "eks-fixed.json"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	rv, ok := c.Find("2.5.12")
	if !ok {
		t.Fatal("Rancher 2.5.12 not found")
	}
	sup, ok := rv.SupportFor(EKS)
	if !ok {
		t.Fatal("EKS not found for Rancher 2.5.12; this is the defect that has been live since 2024")
	}
	if sup.MinVersion != "v1.18" || sup.MaxVersion != "v1.20" {
		t.Errorf("EKS range = %s-%s, want v1.18-v1.20", sup.MinVersion, sup.MaxVersion)
	}
}

// An unrecognised distro row must be reported, never silently skipped. ACK appears
// in the 2.13.9 and 2.15.1 matrices; the tool does not model it, but a scraper that
// quietly discards rows it does not recognise is the same failure mode as the
// `continue` above, one layer up.
func TestUnknownPlatformIsReportedNotSilentlyDropped(t *testing.T) {
	_, err := Load(read(t, "unknown-platform.json"))
	if err == nil {
		t.Fatal("Load silently accepted an unmodelled platform (ack); it must be reported")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "ack") {
		t.Errorf("error %q does not name the unrecognised platform", err.Error())
	}
}

// The T0 spike found install range == downstream range for every Rancher version
// sampled (2.7.5, 2.9.6, 2.11.3, 2.13.9, 2.15.1), which is why Support stores the
// range once. Five samples is not a proof, so the validator asserts the equality
// and a future divergence must fail loudly rather than be assumed away.
func TestValidate_RejectsInstallDownstreamRangeDivergence(t *testing.T) {
	data := read(t, "range-divergence.json")
	c, err := Load(data)
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "install") {
			return // refused at load, which is also correct
		}
		t.Fatalf("Load: %v", err)
	}
	if err := Validate(c); err == nil {
		t.Fatal("Validate accepted an entry whose install range diverges from its " +
			"downstream range; the single-range model depends on that equality holding")
	}
}

// A valid catalog loads, and every entry carries the provenance the itinerary needs.
func TestLoad_ValidCatalogCarriesProvenance(t *testing.T) {
	c, err := Load(read(t, "basic.json"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(c.Rancher) == 0 {
		t.Fatal("catalog loaded with no Rancher versions")
	}
	for _, rv := range c.Rancher {
		if rv.Lifecycle == "" {
			t.Errorf("%s has no lifecycle status", rv.Version)
		}
		if rv.LifecycleAsOf == "" {
			t.Errorf("%s has no lifecycle as-of date; status derived from no date is not honest", rv.Version)
		}
		for _, p := range rv.Platforms {
			if p.SourceURL == "" {
				t.Errorf("%s/%s has no source URL; every fact must cite where it came from", rv.Version, p.Platform)
			}
			if p.AsOf == "" {
				t.Errorf("%s/%s has no as-of date", rv.Version, p.Platform)
			}
			switch p.Granularity {
			case GranularityRelease:
				if len(p.Releases) == 0 {
					t.Errorf("%s/%s is release-granularity but lists no releases", rv.Version, p.Platform)
				}
			case GranularityMinor:
				if len(p.Releases) != 0 {
					t.Errorf("%s/%s is minor-granularity but lists releases; the planner must "+
						"never synthesize hosted-provider patch versions", rv.Version, p.Platform)
				}
			default:
				t.Errorf("%s/%s has no granularity marker", rv.Version, p.Platform)
			}
		}
	}
}

func TestStubsAreNotImplemented(t *testing.T) {
	_, err := Load([]byte(`{}`))
	if !errors.Is(err, ErrNotImplemented) {
		t.Skip("catalog implemented; this guard has served its purpose")
	}
}
