package main

import (
	"strings"
	"testing"

	"github.com/supporttools/rancher-upgrade-tool/internal/catalog"
)

const goodKDM = `
channels:
  - name: latest
    latest: v1.34.1+rke2r1
releases:
  - version: v1.30.14+rke2r2
    minChannelServerVersion: v2.9.0-alpha1
    maxChannelServerVersion: v2.11.99
  - version: v1.34.1+rke2r1
    minChannelServerVersion: v2.13.0-alpha1
    maxChannelServerVersion: v2.15.99
`

// The generator must refuse rather than emit a partial dataset. These are the
// shapes that would otherwise produce a catalog quietly missing a platform, which
// is the original defect one layer up: a pull-request diff a reviewer cannot see
// through, instead of a silent `continue` in the loader.

func TestKDM_TruncatedFileIsRefused(t *testing.T) {
	_, err := parseKDM([]byte("channels:\n  - name: latest\n"), "kdm-rke2")
	if err == nil {
		t.Fatal("accepted a KDM file with no releases")
	}
	if !strings.Contains(err.Error(), "no releases") {
		t.Errorf("error %q does not explain the problem", err)
	}
}

func TestKDM_ReleaseMissingItsRancherRangeIsRefused(t *testing.T) {
	bad := `
releases:
  - version: v1.34.1+rke2r1
    minChannelServerVersion: v2.13.0-alpha1
`
	_, err := parseKDM([]byte(bad), "kdm-rke2")
	if err == nil {
		t.Fatal("accepted a release with no maxChannelServerVersion; it cannot be attributed to any Rancher version")
	}
	if !strings.Contains(err.Error(), "v1.34.1+rke2r1") {
		t.Errorf("error %q does not name the offending release", err)
	}
}

// min <= rancher <= max is the whole attribution rule. Confirmed semantics:
// rancher/rancher#26011, rancher/dashboard#5409.
func TestKDM_AttributesReleasesByRancherRange(t *testing.T) {
	f, err := parseKDM([]byte(goodKDM), "kdm-rke2")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	for _, tc := range []struct {
		rancher string
		want    []string
	}{
		{"2.9.6", []string{"v1.30.14+rke2r2"}},
		{"2.11.3", []string{"v1.30.14+rke2r2"}},
		{"2.15.1", []string{"v1.34.1+rke2r1"}},
		{"2.12.13", nil}, // between the two ranges: neither release applies
	} {
		got, err := f.releasesFor(tc.rancher)
		if err != nil {
			t.Fatalf("releasesFor(%s): %v", tc.rancher, err)
		}
		if strings.Join(got, ",") != strings.Join(tc.want, ",") {
			t.Errorf("releasesFor(%s) = %v, want %v", tc.rancher, got, tc.want)
		}
	}
}

func TestMatrix_MissingTableIsRefused(t *testing.T) {
	_, err := parseMatrix([]byte("<html><body>nothing here</body></html>"), "matrix 2.15.1")
	if err == nil {
		t.Fatal("accepted a page with no platforms table")
	}
	if !strings.Contains(err.Error(), "Refusing to emit") {
		t.Errorf("error %q does not say it refused", err)
	}
}

func TestMatrix_HeadingPresentButRowsUnparseableIsRefused(t *testing.T) {
	html := `<h2>Supported Kubernetes Platforms for Rancher Manager</h2>
	<table><tr><td>something entirely different</td></tr></table>
	<h2>Rancher Manager Supported Architectures</h2>`
	_, err := parseMatrix([]byte(html), "matrix 2.15.1")
	if err == nil {
		t.Fatal("accepted a page whose table markup changed; that would emit a version with no hosted providers")
	}
}

func TestMatrix_ParsesTheRealTableShape(t *testing.T) {
	html := `<h2>Supported Kubernetes Platforms for Rancher Manager</h2>
	<table><tr><th>Distro</th><th>Lowest Version certified</th><th>Highest Version certified on</th><th>Notes</th></tr>
	<tr><td>RKE2</td><td>v1.34</td><td>v1.36</td><td>See RKE2 Support Matrix</td></tr>
	<tr><td>k3s</td><td>v1.34</td><td>v1.36</td><td>See k3s Support Matrix</td></tr>
	<tr><td>AKS</td><td>v1.34</td><td>v1.36</td><td></td></tr>
	<tr><td>ACK</td><td>v1.34</td><td>v1.36</td><td></td></tr></table>
	<h2>Rancher Manager Supported Architectures</h2>`
	rows, err := parseMatrix([]byte(html), "matrix 2.15.1")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(rows) != 4 {
		t.Fatalf("parsed %d rows, want 4 (including the unmodelled ack, which must be seen and reported, not dropped by the parser)", len(rows))
	}
	if rows[0].Distro != "rke2" || rows[0].Min != "v1.34" || rows[0].Max != "v1.36" {
		t.Errorf("first row = %+v", rows[0])
	}
}

// The fail-rather-than-partial rule itself.
func TestGuard_RefusesWhenAPlatformDisappears(t *testing.T) {
	base := &catalog.Catalog{GeneratedAt: "2026-09-17", Rancher: []catalog.RancherVersion{{
		Version: "2.9.6", Lifecycle: catalog.LifecycleEnded, LifecycleAsOf: "2026-09-17",
		Platforms: []catalog.Support{
			{Platform: catalog.RKE2, MinVersion: "v1.27", MaxVersion: "v1.30", Granularity: catalog.GranularityMinor, SourceURL: "x", AsOf: "y"},
			{Platform: catalog.EKS, MinVersion: "v1.27", MaxVersion: "v1.30", Granularity: catalog.GranularityMinor, SourceURL: "x", AsOf: "y"},
		},
	}}}
	next := &catalog.Catalog{GeneratedAt: "2026-09-17", Rancher: []catalog.RancherVersion{{
		Version: "2.9.6", Lifecycle: catalog.LifecycleEnded, LifecycleAsOf: "2026-09-17",
		Platforms: []catalog.Support{
			{Platform: catalog.RKE2, MinVersion: "v1.27", MaxVersion: "v1.30", Granularity: catalog.GranularityMinor, SourceURL: "x", AsOf: "y"},
		},
	}}}

	if err := guardNoRegression(base, next); err == nil {
		t.Fatal("guard allowed EKS to vanish; a scraper half-breaking would silently lose a platform")
	} else if !strings.Contains(err.Error(), "eks") {
		t.Errorf("error %q does not name the lost platform", err)
	}

	if err := guardNoRegression(base, base); err != nil {
		t.Errorf("guard rejected an unchanged catalog: %v", err)
	}
}

func TestGuard_RefusesWhenAVersionDisappears(t *testing.T) {
	base := &catalog.Catalog{GeneratedAt: "x", Rancher: []catalog.RancherVersion{
		{Version: "2.9.6"}, {Version: "2.10.7"},
	}}
	next := &catalog.Catalog{GeneratedAt: "x", Rancher: []catalog.RancherVersion{{Version: "2.9.6"}}}
	err := guardNoRegression(base, next)
	if err == nil || !strings.Contains(err.Error(), "2.10.7") {
		t.Fatalf("guard did not catch a disappearing version: %v", err)
	}
}

// A waypoint moving within its minor line is not a loss, and reporting it as one on
// every refresh trains reviewers to ignore the warning.
func TestJourneyDiff_SeparatesWaypointMovesFromRealLosses(t *testing.T) {
	moves, lost := splitMoves(
		[]string{"2.8.12", "2.9.6", "2.7.18"},
		[]string{"2.8.15", "2.9.12", "2.15.1"},
	)
	if len(moves) != 2 {
		t.Errorf("moves = %v, want the two same-line pairs", moves)
	}
	if len(lost) != 1 || lost[0] != "2.7.18" {
		t.Errorf("reallyLost = %v, want [2.7.18]", lost)
	}
}
