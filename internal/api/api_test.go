package api

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/supporttools/rancher-upgrade-tool/internal/catalog"
	"github.com/supporttools/rancher-upgrade-tool/internal/planner"
)

func shipped(t *testing.T) *catalog.Catalog {
	t.Helper()
	data, err := os.ReadFile("../../data/catalog.json")
	if err != nil {
		t.Fatalf("read catalog: %v", err)
	}
	c, err := catalog.Load(data)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	return c
}

func query(kv map[string]string) func(string) string {
	return func(k string) string { return kv[k] }
}

func valid() map[string]string {
	return map[string]string{
		"rancher": "2.9.4", "local_platform": "rke2", "local_k8s": "v1.28",
		"downstream_platform": "rke2", "downstream_k8s": "v1.28",
	}
}

// Each refusal case gets a distinct, typed response. Previously all four produced
// something indistinguishable from "nothing to do".
func TestParseRequest_RefusalsAreDistinct(t *testing.T) {
	c := shipped(t)
	for _, tc := range []struct {
		name   string
		mutate func(map[string]string)
		want   ErrorKind
		field  string
	}{
		{"unparseable rancher", func(m map[string]string) { m["rancher"] = "nonsense" }, ErrInvalidVersion, "rancher"},
		{"unparseable k8s", func(m map[string]string) { m["downstream_k8s"] = "v1.20.x" }, ErrInvalidVersion, "downstream_k8s"},
		{"unknown rancher", func(m map[string]string) { m["rancher"] = "9.9.9" }, ErrUnknownRancher, "rancher"},
		{"unknown platform", func(m map[string]string) { m["local_platform"] = "openshift" }, ErrUnknownPlatform, "local_platform"},
		{"missing parameter", func(m map[string]string) { delete(m, "downstream_k8s") }, ErrMissingParameter, "downstream_k8s"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			kv := valid()
			tc.mutate(kv)
			_, apiErr := ParseRequest(c, query(kv))
			if apiErr == nil {
				t.Fatalf("accepted bad input %v", kv)
			}
			if apiErr.Kind != tc.want {
				t.Errorf("kind = %q, want %q", apiErr.Kind, tc.want)
			}
			if apiErr.Field != tc.field {
				t.Errorf("field = %q, want %q", apiErr.Field, tc.field)
			}
			if apiErr.Detail == "" {
				t.Error("detail is empty; a refusal must say what was wrong")
			}
		})
	}
}

// REGRESSION: "v2.9.4" parsed at main.go:123 but missed the unprefixed map keys at
// main.go:141, silently producing an empty plan. Both forms must behave identically.
func TestRegression_VPrefixedVersionsBehaveIdentically(t *testing.T) {
	c := shipped(t)
	bare := valid()
	prefixed := valid()
	prefixed["rancher"] = "v2.9.4"

	a, errA := ParseRequest(c, query(bare))
	b, errB := ParseRequest(c, query(prefixed))
	if errA != nil || errB != nil {
		t.Fatalf("parse failed: %v / %v", errA, errB)
	}
	if a != b {
		t.Errorf("v-prefixed input produced a different node:\n  %+v\n  %+v", a, b)
	}
}

// Destinations and blockers are always arrays, never null. The old handler emitted
// {"upgrade_path": null} and left the frontend to interpret it.
func TestPlan_NeverEmitsNull(t *testing.T) {
	c := shipped(t)
	// A state with nothing reachable: newest catalogued Rancher line.
	n := planner.Node{Rancher: "2.11.3", LocalPlatform: catalog.RKE2, LocalK8s: "v1.32",
		DownPlatform: catalog.RKE2, DownK8s: "v1.32"}
	out, err := Plan(c, n)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	raw, err := json.Marshal(out)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var generic map[string]any
	if err := json.Unmarshal(raw, &generic); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	for _, key := range []string{"destinations", "blockers"} {
		if generic[key] == nil {
			t.Errorf("%q serialized as null; it must be an array", key)
		}
	}
	if out.ClaimNote == "" || out.Scope == "" {
		t.Error("response omits its claim note or scope")
	}
}

// An RKE1 query returns the end-of-life notice rather than an empty version list,
// which would read as "no upgrade available" rather than "this product is over".
func TestPlan_RKE1ReturnsEndOfLifeNotice(t *testing.T) {
	c := shipped(t)
	n := planner.Node{Rancher: "2.8.5", LocalPlatform: catalog.RKE1, LocalK8s: "v1.26",
		DownPlatform: catalog.RKE1, DownK8s: "v1.26"}
	out, err := Plan(c, n)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	found := false
	for _, b := range out.Blockers {
		if b.Kind == "end-of-life" {
			found = true
			if b.Detail == "" {
				t.Error("end-of-life blocker has no detail")
			}
		}
	}
	if !found {
		t.Error("RKE1 query returned no end-of-life notice")
	}
}

// Metric labels are bounded to catalog-known values. Raw user input as a Prometheus
// label on a public endpoint lets any visitor allocate unbounded series.
func TestLabelValues_BoundsTheLabelSpace(t *testing.T) {
	c := shipped(t)
	for _, tc := range []struct{ platform, rancher, k8s, wantP, wantR, wantK string }{
		{"rke2", "2.9.4", "v1.28", "rke2", "2.9", "v1.28"},
		{"RKE2", "v2.9.4", "1.28", "rke2", "2.9", "v1.28"},
		{"../../etc/passwd", "99999.1.1", "v9999.1", "other", "other", "other"},
		{"openshift", "2.9.4", "v1.28", "other", "2.9", "v1.28"},
	} {
		p, r, k := LabelValues(c, tc.platform, tc.rancher, tc.k8s)
		if p != tc.wantP || r != tc.wantR || k != tc.wantK {
			t.Errorf("LabelValues(%q,%q,%q) = (%q,%q,%q), want (%q,%q,%q)",
				tc.platform, tc.rancher, tc.k8s, p, r, k, tc.wantP, tc.wantR, tc.wantK)
		}
	}
}

// Kubernetes versions render consistently. Input normalization strips the leading
// "v" so "v1.28" and "1.28" are one value, but a response showing "1.28 -> v1.29"
// reads as though the two sides came from different places.
func TestPlan_KubernetesVersionsRenderConsistently(t *testing.T) {
	c := shipped(t)
	n := planner.Node{Rancher: "2.9.4", LocalPlatform: catalog.RKE2, LocalK8s: "1.28",
		DownPlatform: catalog.RKE2, DownK8s: "1.28"}
	out, err := Plan(c, n)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	for _, field := range []string{out.Start.Local.K8s, out.Start.Downstream.K8s} {
		if !strings.HasPrefix(field, "v") {
			t.Errorf("start state renders %q without the v prefix", field)
		}
	}
	for _, d := range out.Destinations {
		for _, s := range d.Steps {
			if s.Kind == "rancher" {
				if strings.HasPrefix(s.From, "v") || strings.HasPrefix(s.To, "v") {
					t.Errorf("rancher step should not carry a v prefix: %s -> %s", s.From, s.To)
				}
				continue
			}
			if !strings.HasPrefix(s.From, "v") || !strings.HasPrefix(s.To, "v") {
				t.Errorf("k8s step renders inconsistently: %s -> %s", s.From, s.To)
			}
		}
	}
}
