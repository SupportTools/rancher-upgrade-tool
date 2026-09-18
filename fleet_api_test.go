package main

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"testing"

	"github.com/supporttools/rancher-upgrade-tool/internal/api"
	"github.com/supporttools/rancher-upgrade-tool/internal/catalog"
)

// F14: the merge gate. The riskiest part of the fleet work is not the new
// capability, it is silently changing the answer the tool already gives.
//
// The deploy gate depends on this: scripts/verify-deploy.sh probes the legacy
// single-pair URL in all six environments and fails the deploy if it stops
// returning destinations. A regression here breaks deploys, not just the feature.

// query turns a URL query string into the accessor ParseRequest expects.
func query(t *testing.T, qs string) func(string) string {
	t.Helper()
	v, err := url.ParseQuery(strings.TrimPrefix(qs, "?"))
	if err != nil {
		t.Fatalf("bad query %q: %v", qs, err)
	}
	return func(k string) string { return v.Get(k) }
}

// The exact URL scripts/verify-deploy.sh probes. If this stops returning
// destinations, every environment fails its deploy gate.
const deployGateQuery = "rancher=2.9.6&local_platform=rke2&local_k8s=v1.28" +
	"&downstream_platform=rke2&downstream_k8s=v1.28"

func TestLegacy_DeployGateProbeStillPlans(t *testing.T) {
	c := shipped(t)
	n, apiErr := api.ParseRequest(c, query(t, deployGateQuery))
	if apiErr != nil {
		t.Fatalf("the deploy gate's own URL was refused: %+v", apiErr)
	}
	resp, err := api.Plan(c, n)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if len(resp.Destinations) == 0 {
		t.Fatal("deploy gate probe returned zero destinations; every environment would fail")
	}
	// verify-deploy.sh greps the raw JSON for a "destinations" key and for an empty
	// array, so assert on the encoded form it actually sees.
	body, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(body), `"destinations"`) {
		t.Error("response has no destinations field; the deploy gate greps for it")
	}
	if strings.Contains(string(body), `"destinations":[]`) {
		t.Error("response carries an empty destinations array; the deploy gate treats that as failure")
	}
}

// A one-element fleet and the legacy pair must be the SAME request, not two paths
// that happen to agree. Two planners diverge.
func TestLegacy_SinglePairEqualsAOneElementFleet(t *testing.T) {
	c := shipped(t)

	legacy, apiErr := api.ParseRequest(c, query(t, deployGateQuery))
	if apiErr != nil {
		t.Fatalf("legacy parse: %+v", apiErr)
	}
	indexed, apiErr := api.ParseRequest(c, query(t,
		"rancher=2.9.6&local_platform=rke2&local_k8s=v1.28"+
			"&downstream_platform_1=rke2&downstream_k8s_1=v1.28"))
	if apiErr != nil {
		t.Fatalf("indexed parse: %+v", apiErr)
	}

	a, err := api.Plan(c, legacy)
	if err != nil {
		t.Fatalf("Plan(legacy): %v", err)
	}
	b, err := api.Plan(c, indexed)
	if err != nil {
		t.Fatalf("Plan(indexed): %v", err)
	}

	ja, _ := json.Marshal(a.Destinations)
	jb, _ := json.Marshal(b.Destinations)
	if string(ja) != string(jb) {
		t.Errorf("the legacy pair and a one-element fleet produced different routes:\n  %s\n  %s", ja, jb)
	}
}

// The deprecated single-cluster mirror keeps legacy responses byte-identical. It
// must appear for one cluster and vanish for a fleet, so a client cannot read it
// and silently ignore the other clusters.
func TestLegacy_DownstreamMirrorOnlyForASingleCluster(t *testing.T) {
	c := shipped(t)

	one, apiErr := api.ParseRequest(c, query(t, deployGateQuery))
	if apiErr != nil {
		t.Fatalf("parse: %+v", apiErr)
	}
	respOne, err := api.Plan(c, one)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if respOne.Start.Downstream == nil {
		t.Error("single-cluster response dropped start.downstream; legacy consumers read it")
	}
	if len(respOne.Start.Clusters) != 1 {
		t.Errorf("start.clusters = %d entries, want 1", len(respOne.Start.Clusters))
	}

	many, apiErr := api.ParseRequest(c, query(t,
		"rancher=2.9.6&local_platform=rke2&local_k8s=v1.28"+
			"&downstream_platform_1=rke2&downstream_k8s_1=v1.28"+
			"&downstream_platform_2=rke2&downstream_k8s_2=v1.29"))
	if apiErr != nil {
		t.Fatalf("parse fleet: %+v", apiErr)
	}
	respMany, err := api.Plan(c, many)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if respMany.Start.Downstream != nil {
		t.Error("multi-cluster response still carries start.downstream; a client reading only " +
			"that field would silently plan for one cluster out of several")
	}
	if len(respMany.Start.Clusters) != 2 {
		t.Errorf("start.clusters = %d entries, want 2", len(respMany.Start.Clusters))
	}
}

// F8: refusals name the cluster, because "v1.3o is not a valid version" is
// unactionable when three clusters were submitted.
func TestFleetAPI_RefusalNamesTheOffendingCluster(t *testing.T) {
	c := shipped(t)
	_, apiErr := api.ParseRequest(c, query(t,
		"rancher=2.9.6&local_platform=rke2&local_k8s=v1.28"+
			"&downstream_platform_1=rke2&downstream_k8s_1=v1.28"+
			"&downstream_platform_2=rke2&downstream_k8s_2=v1.29"+
			"&downstream_platform_3=rke2&downstream_k8s_3=nonsense&downstream_label_3=prod-eu"))
	if apiErr == nil {
		t.Fatal("an unparseable version in cluster 3 was accepted")
	}
	if apiErr.Cluster != "c3" {
		t.Errorf("error cluster = %q, want c3", apiErr.Cluster)
	}
	if apiErr.ClusterLabel != "prod-eu" {
		t.Errorf("error cluster label = %q, want prod-eu", apiErr.ClusterLabel)
	}
	if !strings.Contains(apiErr.Detail, "prod-eu") {
		t.Errorf("detail does not name the cluster: %q", apiErr.Detail)
	}
	if apiErr.Field != "downstream_k8s_3" {
		t.Errorf("error field = %q, want downstream_k8s_3", apiErr.Field)
	}
}

// Both spellings of slot 1 disagreeing is an error, not a precedence rule: a
// silent winner plans for a fleet the caller did not describe.
func TestFleetAPI_ConflictingSlotOneIsRefused(t *testing.T) {
	c := shipped(t)
	_, apiErr := api.ParseRequest(c, query(t,
		"rancher=2.9.6&local_platform=rke2&local_k8s=v1.28"+
			"&downstream_platform=rke2&downstream_k8s=v1.28"+
			"&downstream_platform_1=eks"))
	if apiErr == nil {
		t.Fatal("conflicting downstream_platform and downstream_platform_1 were accepted")
	}
	if apiErr.Kind != api.ErrConflictingParameter {
		t.Errorf("kind = %q, want %q", apiErr.Kind, api.ErrConflictingParameter)
	}
	// Agreeing values are fine.
	if _, err := api.ParseRequest(c, query(t,
		"rancher=2.9.6&local_platform=rke2&local_k8s=v1.28"+
			"&downstream_platform=rke2&downstream_k8s=v1.28"+
			"&downstream_platform_1=rke2&downstream_k8s_1=v1.28")); err != nil {
		t.Errorf("agreeing duplicate spellings were refused: %+v", err)
	}
}

func TestFleetAPI_ClusterCapIsStatedNotSilentlyTruncated(t *testing.T) {
	c := shipped(t)
	var b strings.Builder
	b.WriteString("rancher=2.9.6&local_platform=rke2&local_k8s=v1.28")
	for i := 1; i <= api.MaxClusters+1; i++ {
		fmt.Fprintf(&b, "&downstream_platform_%d=rke2&downstream_k8s_%d=v1.28", i, i)
	}
	_, apiErr := api.ParseRequest(c, query(t, b.String()))
	if apiErr == nil {
		t.Fatalf("%d clusters were accepted; the cap is %d", api.MaxClusters+1, api.MaxClusters)
	}
	if apiErr.Kind != api.ErrTooManyClusters {
		t.Errorf("kind = %q, want %q", apiErr.Kind, api.ErrTooManyClusters)
	}
	if !strings.Contains(apiErr.Detail, fmt.Sprintf("%d", api.MaxClusters)) {
		t.Errorf("refusal does not state the cap: %q", apiErr.Detail)
	}
}

// Windows cover only the platforms in play. Emitting all six would quadruple the
// payload with ranges the caller has no cluster on.
func TestFleetAPI_WindowsCoverOnlyThePlatformsPresent(t *testing.T) {
	c := shipped(t)
	n, apiErr := api.ParseRequest(c, query(t,
		"rancher=2.9.6&local_platform=rke2&local_k8s=v1.28"+
			"&downstream_platform_1=rke2&downstream_k8s_1=v1.28"+
			"&downstream_platform_2=eks&downstream_k8s_2=v1.28"))
	if apiErr != nil {
		t.Fatalf("parse: %+v", apiErr)
	}
	resp, err := api.Plan(c, n)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if len(resp.Windows) == 0 {
		t.Fatal("no windows returned; the client cannot draw the support strip")
	}
	seen := map[string]bool{}
	for _, w := range resp.Windows {
		seen[w.Platform] = true
		if w.Min == "" || w.Max == "" {
			t.Errorf("window for %s %s has an empty bound", w.Rancher, w.Platform)
		}
	}
	for _, want := range []string{"rke2", "eks"} {
		if !seen[want] {
			t.Errorf("no window for %s, which the request uses", want)
		}
	}
	for _, unwanted := range []string{"aks", "gke", "k3s", "rke1"} {
		if seen[unwanted] {
			t.Errorf("windows include %s, which no cluster in the request uses", unwanted)
		}
	}
}

// F9: the fleet parameters must not open a new cardinality hole. Cluster count is
// bucketed and labels never reach a metric.
func TestFleetAPI_MetricLabelSpaceStaysBounded(t *testing.T) {
	buckets := map[string]bool{}
	for n := 0; n <= 50; n++ {
		buckets[api.ClusterCountBucket(n)] = true
	}
	if len(buckets) > 6 {
		t.Errorf("cluster-count bucketing produced %d distinct labels: %v", len(buckets), buckets)
	}

	// Junk platforms bucket to "other" rather than creating series.
	c := shipped(t)
	p, r, k := api.LabelValues(c, "'; DROP TABLE--", "99.99.99", "v9.99")
	for name, got := range map[string]string{"platform": p, "rancher": r, "k8s": k} {
		if got != "other" {
			t.Errorf("%s label = %q for junk input, want other", name, got)
		}
	}

	// A cluster LABEL must never appear in a metric label value.
	evil := "label-that-should-never-be-a-series"
	size := api.FleetSize(func(key string) string {
		switch key {
		case "downstream_platform_1":
			return "rke2"
		case "downstream_k8s_1":
			return "v1.28"
		case "downstream_label_1":
			return evil
		}
		return ""
	})
	if got := api.ClusterCountBucket(size); strings.Contains(got, evil) {
		t.Errorf("cluster label leaked into a metric label: %q", got)
	}
	if size != 1 {
		t.Errorf("FleetSize = %d, want 1", size)
	}
}

// FleetSize must not be fooled into an unbounded loop by a sparse index.
func TestFleetAPI_FleetSizeStopsAtTheFirstGap(t *testing.T) {
	size := api.FleetSize(func(key string) string {
		switch key {
		case "downstream_platform_1", "downstream_platform_2":
			return "rke2"
		case "downstream_k8s_1", "downstream_k8s_2":
			return "v1.28"
		// slot 3 missing, slot 4 present: indexes are contiguous, so counting stops
		case "downstream_platform_4":
			return "rke2"
		case "downstream_k8s_4":
			return "v1.28"
		}
		return ""
	})
	if size != 2 {
		t.Errorf("FleetSize = %d, want 2 (counting stops at the first empty slot)", size)
	}
}

// The RKE1 end-of-life notice must fire for ANY cluster on RKE1, not just the first.
func TestFleetAPI_RKE1NoticeCoversEveryCluster(t *testing.T) {
	c := shipped(t)
	n, apiErr := api.ParseRequest(c, query(t,
		"rancher=2.8.5&local_platform=rke2&local_k8s=v1.26"+
			"&downstream_platform_1=rke2&downstream_k8s_1=v1.26"+
			"&downstream_platform_2=rke1&downstream_k8s_2=v1.26&downstream_label_2=legacy-dc"))
	if apiErr != nil {
		t.Fatalf("parse: %+v", apiErr)
	}
	resp, err := api.Plan(c, n)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	var found bool
	for _, b := range resp.Blockers {
		if b.Kind == "end-of-life" && strings.Contains(b.Constraint, "legacy-dc") {
			found = true
		}
	}
	if !found {
		t.Errorf("no RKE1 end-of-life notice naming the second cluster; blockers = %+v", resp.Blockers)
	}
	_ = catalog.RKE1
}
