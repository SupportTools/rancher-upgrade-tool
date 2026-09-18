package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// stripJSComments removes // line comments and /* */ blocks.
//
// Matching raw file text has produced a false positive in this repo three times now
// (TestBothWorkflowsRunTheSameGate, TestChartPublishesToHarborOverOCI,
// TestDeployGateDoesNotPortForwardAtAll), every time because a comment EXPLAINING the
// banned thing contained its name. static/app.js opens with a comment explaining why
// innerHTML is not used, so the naive check fails on the very file it is guarding.
func stripJSComments(src string) string {
	src = regexp.MustCompile(`(?s)/\*.*?\*/`).ReplaceAllString(src, "")
	src = regexp.MustCompile(`(?m)^\s*//.*$`).ReplaceAllString(src, "")
	return src
}

// THE REGRESSION. static/app.js used to interpolate server-echoed user input into a
// template string and assign it to innerHTML. It never fired, but only because the
// server rejected non-semver input before a step was built and the go-version grammar
// has no angle brackets — a safety property nobody chose, no test asserted, and the
// planner rewrite relocated.
//
// Rendering is DOM construction. textContent does not care what the string contains.
func TestFrontendNeverAssignsHTMLFromData(t *testing.T) {
	raw, err := os.ReadFile("static/app.js")
	if err != nil {
		t.Fatalf("read static/app.js: %v", err)
	}
	code := stripJSComments(string(raw))

	for _, banned := range []string{"innerHTML", "outerHTML", "insertAdjacentHTML", "document.write"} {
		if strings.Contains(code, banned) {
			t.Errorf("static/app.js uses %s. Server output includes versions echoed back "+
				"from user input; render with textContent and DOM construction instead.", banned)
		}
	}

	// The query must be encoded. Raw interpolation into the URL broke routing the
	// moment any field contained a "/".
	if !strings.Contains(code, "URLSearchParams") {
		t.Error("static/app.js does not build its query with URLSearchParams")
	}
}

// Every asset the page references must exist. A 404 on style.css renders the tool
// unstyled but still "working", which is exactly the kind of break nobody notices
// from a health check.
func TestFrontendAssetsResolve(t *testing.T) {
	raw, err := os.ReadFile("static/index.html")
	if err != nil {
		t.Fatalf("read static/index.html: %v", err)
	}
	refs := regexp.MustCompile(`(?:href|src)="([^"]+)"`).FindAllStringSubmatch(string(raw), -1)
	if len(refs) == 0 {
		t.Fatal("index.html references no assets at all")
	}

	local := 0
	for _, m := range refs {
		ref := m[1]
		if strings.HasPrefix(ref, "http://") || strings.HasPrefix(ref, "https://") ||
			strings.HasPrefix(ref, "data:") || strings.HasPrefix(ref, "#") {
			continue
		}
		local++
		path := filepath.Join("static", strings.TrimPrefix(ref, "/"))
		if _, err := os.Stat(path); err != nil {
			t.Errorf("index.html references %q but %s does not exist", ref, path)
		}
	}
	if local == 0 {
		t.Error("index.html references no local assets; expected at least app.js and style.css")
	}
}

// The destination picker renders one route at a time, so the elements it depends on
// have to keep existing together. This is a coupling check, not a style check: if the
// picker is removed the page silently goes back to rendering all destinations at once,
// which is the 68-row page this work replaced.
func TestFrontendRendersOneDestinationAtATime(t *testing.T) {
	raw, err := os.ReadFile("static/app.js")
	if err != nil {
		t.Fatalf("read static/app.js: %v", err)
	}
	code := stripJSComments(string(raw))

	for _, needed := range []string{"renderPicker", "aria-selected", "renderLanes"} {
		if !strings.Contains(code, needed) {
			t.Errorf("static/app.js no longer contains %q; the page may have reverted to "+
				"rendering every destination expanded", needed)
		}
	}

	css, err := os.ReadFile("static/style.css")
	if err != nil {
		t.Fatalf("read static/style.css: %v", err)
	}
	// The lane diagram is the only element allowed to scroll sideways. Without this
	// the page body scrolls horizontally on a phone.
	if !strings.Contains(string(css), "overflow-x: auto") {
		t.Error("style.css has no horizontal scroll container; the lane diagram is wider " +
			"than a phone screen and would scroll the page body instead")
	}
}

// The fleet UI. These are coupling checks: the three surfaces (input rows, support
// strip, lanes) each carry information the others cannot, so losing one silently
// degrades the answer rather than breaking the page.
func TestFrontendRendersTheFleetSurfaces(t *testing.T) {
	js, err := os.ReadFile("static/app.js")
	if err != nil {
		t.Fatalf("read static/app.js: %v", err)
	}
	code := stripJSComments(string(js))

	for sym, why := range map[string]string{
		"renderWindowStrip":       "the support strip is the only view that does not decompose into per-cluster answers",
		"renderBindingConstraint": "without it the page never says which cluster is holding the fleet back",
		"lanesFor":                "lanes must scale to one per cluster, not a fixed three",
		"clusterColour":           "a cluster's colour must be allocated once and reused across all three surfaces",
	} {
		if !strings.Contains(code, sym) {
			t.Errorf("static/app.js no longer defines %s: %s", sym, why)
		}
	}

	// Indexed parameters, per the contract in docs/api.md.
	if !strings.Contains(code, "downstream_platform_${n}") &&
		!strings.Contains(code, "`downstream_platform_${n}`") {
		t.Error("static/app.js does not send indexed downstream parameters")
	}

	html, err := os.ReadFile("static/index.html")
	if err != nil {
		t.Fatalf("read static/index.html: %v", err)
	}
	for _, id := range []string{"fleetRows", "addCluster"} {
		if !strings.Contains(string(html), id) {
			t.Errorf("index.html has no %q element; clusters cannot be added", id)
		}
	}
	// The single-cluster fields are gone, so nothing can quietly send one cluster
	// while the user has entered several.
	for _, gone := range []string{`id="downstreamPlatform"`, `id="downstreamK8s"`} {
		if strings.Contains(string(html), gone) {
			t.Errorf("index.html still carries %s; the fleet rows replaced it", gone)
		}
	}
}

// A cluster's colour must exist for every slot up to the API cap, or the eighth
// cluster renders unstyled, and out-of-window dots must differ in SHAPE as well as
// colour so the distinction survives greyscale and colour-blindness.
func TestFrontendFleetColoursCoverTheClusterCap(t *testing.T) {
	css, err := os.ReadFile("static/style.css")
	if err != nil {
		t.Fatalf("read static/style.css: %v", err)
	}
	body := string(css)

	// api.MaxClusters is 8; keep the palette at least that wide.
	for i := 1; i <= 8; i++ {
		token := fmt.Sprintf("--axis-c%d", i)
		if !strings.Contains(body, token) {
			t.Errorf("style.css defines no %s, so cluster %d would render unstyled", token, i)
		}
	}
	if !strings.Contains(body, `.strip-dot[data-out="true"]`) {
		t.Error("style.css does not distinguish out-of-window dots by shape; colour alone " +
			"fails in greyscale and for colour-blind readers")
	}
	if !strings.Contains(body, `.strip-row[data-fits="false"]`) {
		t.Error("style.css does not mark Rancher versions that cannot hold the whole fleet")
	}
}
