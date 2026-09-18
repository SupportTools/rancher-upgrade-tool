// Package api is the HTTP surface: input normalization, the error contract, and
// the response envelope.
//
// The contract exists because the old handler had four ways to answer wrongly or
// silently, all of which looked identical to a legitimate "nothing to do":
// unparseable Rancher version returned 500, unparseable Kubernetes version returned
// an empty slice with no error, an unknown Rancher version hit an unchecked map read
// and produced an empty plan, and a "v"-prefixed version parsed but missed the map.
//
// Silently returning nothing is the worst failure available to a tool whose job is
// telling people what to do: someone on an unsupported version reads it as
// reassurance and stays there.
package api

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/hashicorp/go-version"
	"github.com/supporttools/rancher-upgrade-tool/internal/catalog"
	"github.com/supporttools/rancher-upgrade-tool/internal/planner"
)

const prerequisitesURL = "https://ranchermanager.docs.rancher.com/getting-started/installation-and-upgrade/install-upgrade-on-a-kubernetes-cluster/upgrades"

// claimNote is deliberately part of every successful response. Endpoint
// compatibility does not prove a safe upgrade: feature-chart upgrades,
// admission-webhook compatibility across a hop and node-upgrade completion all live
// between two valid states and are in no compatibility matrix.
const claimNote = "This is a version-compatibility itinerary, not an execution runbook. " +
	"It proves each state along the route is supported. It does not cover upgrade " +
	"prerequisites such as feature-chart upgrades, admission-webhook compatibility " +
	"across a hop, or node-upgrade completion."

const granularityNote = "One or more platforms on this route publish minor lines only, " +
	"not individual releases, so this route is planned minor to minor. RKE2 and k3s " +
	"publish real releases; AKS, EKS and GKE do not."

// ErrorKind classifies a 400 so a client can react without string matching.
type ErrorKind string

const (
	ErrInvalidVersion   ErrorKind = "invalid-version"
	ErrUnknownPlatform  ErrorKind = "unknown-platform"
	ErrUnknownRancher   ErrorKind = "unknown-rancher-version"
	ErrMissingParameter ErrorKind = "missing-parameter"
	// ErrConflictingParameter covers downstream_platform and downstream_platform_1
	// both being supplied with DIFFERENT values. A silent precedence rule would
	// pick one and plan for a fleet the caller did not describe.
	ErrConflictingParameter ErrorKind = "conflicting-parameter"
	// ErrTooManyClusters states the cap rather than truncating the fleet.
	ErrTooManyClusters ErrorKind = "too-many-clusters"
)

// APIError is the 400 body. It names the field and echoes the value so the user can
// see what the service actually received.
type APIError struct {
	Kind ErrorKind `json:"kind"`
	// Cluster names WHICH cluster was at fault. "v1.3o is not a valid version" is
	// unactionable when three clusters were submitted.
	Cluster      string `json:"cluster,omitempty"`
	ClusterLabel string `json:"cluster_label,omitempty"`
	Field        string `json:"field"`
	Value        string `json:"value,omitempty"`
	Detail       string `json:"detail"`
}

func (e *APIError) Error() string { return fmt.Sprintf("%s: %s", e.Kind, e.Detail) }

type ClusterState struct {
	Platform  string `json:"platform"`
	K8s       string `json:"k8s"`
	Lifecycle string `json:"lifecycle,omitempty"`
}

// FleetCluster is one downstream cluster as submitted, echoed back so a client can
// label a lane without holding its own copy of the request.
type FleetCluster struct {
	ID       string `json:"id"`
	Label    string `json:"label"`
	Platform string `json:"platform"`
	K8s      string `json:"k8s"`
}

type StartState struct {
	Rancher   string       `json:"rancher"`
	Lifecycle string       `json:"lifecycle,omitempty"`
	Local     ClusterState `json:"local"`

	// Clusters is the canonical fleet.
	Clusters []FleetCluster `json:"clusters"`

	// Downstream mirrors Clusters[0] and is populated ONLY when exactly one cluster
	// was submitted, so responses to the legacy single-pair request stay
	// byte-identical to what they were before the fleet work. Deprecated: remove
	// once static/app.js reads Clusters (feature 8, tasks F10-F12).
	Downstream *ClusterState `json:"downstream,omitempty"`
}

// ConstraintCluster is one cluster preventing the next Rancher hop.
type ConstraintCluster struct {
	ID       string `json:"id"`
	Label    string `json:"label"`
	K8s      string `json:"k8s"`
	Position string `json:"position"`
}

// BindingConstraint answers "what is holding me back right now", which is a
// different question from what is reachable. A fleet can reach a much newer Rancher
// by upgrading its laggards first; this says what it can run TODAY and why.
type BindingConstraint struct {
	HighestNow     string              `json:"highest_now,omitempty"`
	BlockedRancher string              `json:"blocked_rancher"`
	RequiresK8s    string              `json:"requires_k8s,omitempty"`
	Clusters       []ConstraintCluster `json:"clusters"`
}

// Window is a Rancher version's supported Kubernetes range for one platform, so a
// client can show which versions could hold the whole fleet without a second call.
// A mixed-platform fleet gets one entry per (rancher, platform) pair, because the
// window differs by platform.
type Window struct {
	Rancher   string `json:"rancher"`
	Lifecycle string `json:"lifecycle,omitempty"`
	Platform  string `json:"platform"`
	Min       string `json:"min"`
	Max       string `json:"max"`
}

type Step struct {
	Kind string `json:"kind"`
	// Cluster identifies WHICH cluster moved: "local", "c1", "c2"... absent on a
	// Rancher hop. Downstream steps keep the single downstream-k8s Kind, so a
	// consumer that only knows today's vocabulary still works.
	Cluster      string `json:"cluster,omitempty"`
	ClusterLabel string `json:"cluster_label,omitempty"`
	// Wave groups steps with no ordering requirement between them. A claim about
	// dependency, not safety.
	Wave      int      `json:"wave,omitempty"`
	Platform  string   `json:"platform,omitempty"`
	From      string   `json:"from"`
	To        string   `json:"to"`
	SourceURL string   `json:"source_url,omitempty"`
	AsOf      string   `json:"as_of,omitempty"`
	Caveats   []string `json:"caveats,omitempty"`
}

type Destination struct {
	Rancher         string `json:"rancher"`
	Lifecycle       string `json:"lifecycle,omitempty"`
	LifecycleAsOf   string `json:"lifecycle_as_of,omitempty"`
	Granularity     string `json:"granularity"`
	GranularityNote string `json:"granularity_note,omitempty"`
	Steps           []Step `json:"steps"`
}

type Blocker struct {
	Kind       string `json:"kind"`
	Constraint string `json:"constraint"`
	Detail     string `json:"detail"`
	SourceURL  string `json:"source_url,omitempty"`
}

// CatalogInfo tells the caller how fresh the advice is. It rides on every response
// rather than sitting on a separate endpoint nobody calls: a user acting on an
// upgrade plan should be able to see, right there, that the data behind it stopped
// being refreshed two months ago.
type CatalogInfo struct {
	GeneratedAt    string `json:"generated_at"`
	AgeDays        int    `json:"age_days"`
	Stale          bool   `json:"stale"`
	StaleAfterDays int    `json:"stale_after_days"`
	StaleNote      string `json:"stale_note,omitempty"`
}

type Response struct {
	Scope            string        `json:"scope"`
	Claim            string        `json:"claim"`
	ClaimNote        string        `json:"claim_note"`
	PrerequisitesURL string        `json:"prerequisites_url"`
	Start            StartState    `json:"start"`
	Destinations     []Destination `json:"destinations"`
	Blockers         []Blocker     `json:"blockers"`

	// BindingConstraint is set when the fleet cannot run the next Rancher version
	// as it stands. Nil when it is already on the newest its versions allow.
	BindingConstraint *BindingConstraint `json:"binding_constraint,omitempty"`

	// Windows covers only the platforms present in the request.
	Windows []Window `json:"windows,omitempty"`

	Catalog CatalogInfo `json:"catalog"`
}

// normalizeVersion accepts a version with or without a leading "v" and returns the
// canonical form. The UI taught both conventions at once while the lookup matched
// only one, so "v2.9.4" silently produced an empty plan.
func normalizeVersion(field, raw string) (string, *APIError) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", &APIError{Kind: ErrMissingParameter, Field: field,
			Detail: fmt.Sprintf("%s is required", field)}
	}
	canonical := catalog.Normalize(trimmed)
	if _, err := version.NewVersion(canonical); err != nil {
		return "", &APIError{Kind: ErrInvalidVersion, Field: field, Value: raw,
			Detail: fmt.Sprintf("%q is not a valid version", raw)}
	}
	return canonical, nil
}

func normalizePlatform(field, raw string) (catalog.Platform, *APIError) {
	trimmed := strings.ToLower(strings.TrimSpace(raw))
	if trimmed == "" {
		return "", &APIError{Kind: ErrMissingParameter, Field: field,
			Detail: fmt.Sprintf("%s is required", field)}
	}
	p := catalog.Platform(trimmed)
	for _, known := range catalog.KnownPlatforms {
		if known == p {
			return p, nil
		}
	}
	names := make([]string, 0, len(catalog.KnownPlatforms))
	for _, k := range catalog.KnownPlatforms {
		names = append(names, string(k))
	}
	return "", &APIError{Kind: ErrUnknownPlatform, Field: field, Value: raw,
		Detail: fmt.Sprintf("%q is not a platform this tool models. Known: %s",
			raw, strings.Join(names, ", "))}
}

// displayK8s renders a Kubernetes version the way users and upstream write it, with
// the leading "v". Input is normalized without it so "v1.28" and "1.28" are the same
// value, but a response that shows "1.28 -> v1.29" reads as though the two came from
// different places.
func displayK8s(v string) string {
	if v == "" || strings.HasPrefix(v, "v") {
		return v
	}
	return "v" + v
}

// MaxClusters caps the fleet. Not a compute limit -- the planner is a linear walk,
// see docs/fleet-planning-algorithm.md -- but a bound on response size and on the
// metric label space, both of which are reachable by anyone on a public endpoint.
const MaxClusters = 8

// parseFleet reads the indexed downstream parameters.
//
// downstream_platform_N / downstream_k8s_N / downstream_label_N, indexes from 1 and
// contiguous. The unsuffixed form is slot 1, so the legacy single-pair request is a
// one-element fleet through this same code path rather than a second implementation.
// scripts/verify-deploy.sh probes that exact URL in all six environments.
func parseFleet(q func(string) string) ([]planner.Cluster, *APIError) {
	// read returns the value for a slot plus THE FIELD NAME IT CAME FROM, so a
	// refusal names the parameter the caller actually used. Reporting
	// "downstream_k8s_1 is required" to someone who submitted the unsuffixed form
	// sends them looking for a field they never wrote.
	read := func(i int, base string) (string, string, *APIError) {
		suffixed := fmt.Sprintf("%s_%d", base, i)
		val := strings.TrimSpace(q(suffixed))
		if i != 1 {
			return val, suffixed, nil
		}
		bare := strings.TrimSpace(q(base))
		switch {
		case val != "" && bare != "" && !strings.EqualFold(val, bare):
			return "", suffixed, &APIError{Kind: ErrConflictingParameter, Field: suffixed, Value: val,
				Detail: fmt.Sprintf("%s and %s were both supplied with different values (%q and %q). "+
					"They address the same cluster; supply one.", base, suffixed, bare, val)}
		case val != "":
			return val, suffixed, nil
		default:
			// Includes the empty case, so a missing slot 1 is reported against the
			// unsuffixed name that the legacy form and the deploy gate both use.
			return bare, base, nil
		}
	}

	var out []planner.Cluster
	for i := 1; ; i++ {
		platRaw, platField, apiErr := read(i, "downstream_platform")
		if apiErr != nil {
			return nil, apiErr
		}
		k8sRaw, k8sField, apiErr := read(i, "downstream_k8s")
		if apiErr != nil {
			return nil, apiErr
		}

		if platRaw == "" && k8sRaw == "" {
			if i == 1 {
				// Name the platform field, matching the pre-fleet behaviour.
				return nil, &APIError{Kind: ErrMissingParameter, Field: platField,
					Detail: fmt.Sprintf("%s is required", platField)}
			}
			break // indexes are contiguous, so the first empty slot ends the fleet
		}
		if len(out) >= MaxClusters {
			return nil, &APIError{Kind: ErrTooManyClusters, Field: platField,
				Detail: fmt.Sprintf("At most %d downstream clusters can be planned in one request.", MaxClusters)}
		}

		id := fmt.Sprintf("c%d", i)
		label := strings.TrimSpace(q(fmt.Sprintf("downstream_label_%d", i)))
		if i == 1 && label == "" {
			label = strings.TrimSpace(q("downstream_label"))
		}
		if label == "" {
			label = fmt.Sprintf("Cluster %d", i)
		}

		plat, apiErr := normalizePlatform(platField, platRaw)
		if apiErr != nil {
			decorate(apiErr, id, label, len(out) > 0 || i > 1)
			return nil, apiErr
		}
		k8s, apiErr := normalizeVersion(k8sField, k8sRaw)
		if apiErr != nil {
			decorate(apiErr, id, label, len(out) > 0 || i > 1)
			return nil, apiErr
		}
		out = append(out, planner.Cluster{ID: id, Label: label, Platform: plat, K8s: k8s})
	}

	if len(out) == 0 {
		return nil, &APIError{Kind: ErrMissingParameter, Field: "downstream_platform",
			Detail: "At least one downstream cluster is required (downstream_platform and downstream_k8s)."}
	}
	return out, nil
}

// decorate attaches cluster identity to a refusal. The label is prefixed to the
// detail only for a genuine multi-cluster request: prefixing "Cluster 1: " onto a
// single-cluster refusal would change messages the legacy form already returns.
func decorate(e *APIError, id, label string, fleet bool) {
	e.Cluster, e.ClusterLabel = id, label
	if fleet {
		e.Detail = label + ": " + e.Detail
	}
}

// ParseRequest validates every parameter against the catalog before planning, so a
// refusal names what was wrong instead of producing an empty plan.
func ParseRequest(c *catalog.Catalog, q func(string) string) (planner.Node, *APIError) {
	var n planner.Node

	rancher, apiErr := normalizeVersion("rancher", q("rancher"))
	if apiErr != nil {
		return n, apiErr
	}
	if _, ok := c.Find(rancher); !ok {
		return n, &APIError{Kind: ErrUnknownRancher, Field: "rancher", Value: rancher,
			Detail: fmt.Sprintf("Rancher %s is not in the compatibility catalog, so no route "+
				"can be established from it.", rancher)}
	}
	n.Rancher = rancher

	if n.LocalPlatform, apiErr = normalizePlatform("local_platform", q("local_platform")); apiErr != nil {
		return n, apiErr
	}
	if n.LocalK8s, apiErr = normalizeVersion("local_k8s", q("local_k8s")); apiErr != nil {
		return n, apiErr
	}
	clusters, apiErr := parseFleet(q)
	if apiErr != nil {
		return n, apiErr
	}
	n.Clusters = clusters
	return n, nil
}

// windowsFor returns the supported range per (rancher, platform) for the platforms
// actually present in the request, so a client can draw which Rancher versions
// could hold the whole fleet without asking again.
//
// Only the platforms in play: emitting all six would quadruple the payload with
// ranges the caller has no cluster on.
func windowsFor(c *catalog.Catalog, n planner.Node) []Window {
	present := map[catalog.Platform]bool{n.LocalPlatform: true}
	for _, cl := range n.Clusters {
		present[cl.Platform] = true
	}
	plats := make([]catalog.Platform, 0, len(present))
	for _, p := range catalog.KnownPlatforms {
		if present[p] {
			plats = append(plats, p)
		}
	}

	waypoints, err := planner.Waypoints(c)
	if err != nil {
		return nil
	}
	out := make([]Window, 0, len(waypoints)*len(plats))
	for _, w := range waypoints {
		rv, ok := c.Find(w)
		if !ok {
			continue
		}
		for _, p := range plats {
			sup, ok := rv.SupportFor(p)
			if !ok {
				continue
			}
			out = append(out, Window{
				Rancher: w, Lifecycle: string(rv.Lifecycle), Platform: string(p),
				Min: displayK8s(sup.MinVersion), Max: displayK8s(sup.MaxVersion),
			})
		}
	}
	return out
}

// Plan builds the response envelope. Destinations and Blockers are always non-nil,
// so the JSON carries [] rather than null: the old handler emitted
// {"upgrade_path": null} and left the frontend to interpret it.
func Plan(c *catalog.Catalog, n planner.Node) (*Response, error) {
	res, err := planner.Reachable(c, n)
	if err != nil {
		return nil, err
	}

	out := &Response{
		Scope:            res.Scope,
		Claim:            "version-compatibility",
		ClaimNote:        claimNote,
		PrerequisitesURL: prerequisitesURL,
		Destinations:     []Destination{},
		Blockers:         []Blocker{},
		Start: StartState{
			Rancher:  n.Rancher,
			Local:    ClusterState{Platform: string(n.LocalPlatform), K8s: displayK8s(n.LocalK8s)},
			Clusters: []FleetCluster{},
		},
	}
	for _, cl := range n.Clusters {
		out.Start.Clusters = append(out.Start.Clusters, FleetCluster{
			ID: cl.ID, Label: cl.Label,
			Platform: string(cl.Platform), K8s: displayK8s(cl.K8s),
		})
	}
	// The deprecated single-cluster mirror, so legacy responses are unchanged.
	if len(n.Clusters) == 1 {
		out.Start.Downstream = &ClusterState{
			Platform: string(n.Clusters[0].Platform),
			K8s:      displayK8s(n.Clusters[0].K8s),
		}
	}
	if rv, ok := c.Find(n.Rancher); ok {
		out.Start.Lifecycle = string(rv.Lifecycle)
	}
	out.Catalog = catalogInfo(c, time.Now().UTC())

	for _, r := range res.Routes {
		d := Destination{
			Rancher:     r.Destination,
			Granularity: string(r.Granularity),
			Steps:       []Step{},
		}
		if r.Granularity == catalog.GranularityMinor {
			d.GranularityNote = granularityNote
		}
		if rv, ok := c.Find(r.Destination); ok {
			d.Lifecycle = string(rv.Lifecycle)
			d.LifecycleAsOf = rv.LifecycleAsOf
		}
		for _, s := range r.Steps {
			from, to := s.From, s.To
			if s.Kind != planner.StepRancher {
				from, to = displayK8s(from), displayK8s(to)
			}
			d.Steps = append(d.Steps, Step{
				Kind: string(s.Kind), Platform: string(s.Platform),
				Cluster: s.Cluster, ClusterLabel: s.ClusterLabel, Wave: s.Wave,
				From: from, To: to,
				SourceURL: s.SourceURL, AsOf: s.AsOf, Caveats: s.Caveats,
			})
		}
		out.Destinations = append(out.Destinations, d)
	}

	for _, b := range res.Blockers {
		out.Blockers = append(out.Blockers, Blocker{
			Kind: string(b.Kind), Constraint: b.Constraint,
			Detail: b.Detail, SourceURL: b.SourceURL,
		})
	}

	if bc := res.BindingConstraint; bc != nil {
		mapped := &BindingConstraint{
			HighestNow:     bc.HighestNow,
			BlockedRancher: bc.BlockedRancher,
			RequiresK8s:    displayK8s(bc.RequiresK8s),
			Clusters:       []ConstraintCluster{},
		}
		for _, cl := range bc.Clusters {
			mapped.Clusters = append(mapped.Clusters, ConstraintCluster{
				ID: cl.ID, Label: cl.Label, K8s: displayK8s(cl.K8s),
				Position: string(cl.Position),
			})
		}
		out.BindingConstraint = mapped
	}

	out.Windows = windowsFor(c, n)

	// RKE1 is end of life and left the Rancher support matrix between 2.11.3 and
	// 2.13.9. An empty version list would read as "no upgrade available" rather
	// than "this product is over, migrate to RKE2".
	rke1Roles := []struct {
		role string
		plat catalog.Platform
	}{{"local", n.LocalPlatform}}
	for _, cl := range n.Clusters {
		rke1Roles = append(rke1Roles, struct {
			role string
			plat catalog.Platform
		}{cl.Label, cl.Platform})
	}
	for _, rr := range rke1Roles {
		role, p := rr.role, rr.plat
		if p == catalog.RKE1 {
			out.Blockers = append(out.Blockers, Blocker{
				Kind:       "end-of-life",
				Constraint: fmt.Sprintf("RKE1 is end of life (%s cluster)", role),
				Detail: "RKE1 reached end of life and no longer appears in the Rancher support " +
					"matrix. Historical paths still resolve, but no new versions will be added. " +
					"Plan a migration to RKE2.",
				SourceURL: "https://www.suse.com/support/kb/doc/?id=000021513",
			})
		}
	}
	return out, nil
}

func catalogInfo(c *catalog.Catalog, now time.Time) CatalogInfo {
	info := CatalogInfo{
		GeneratedAt:    c.GeneratedAt,
		StaleAfterDays: int(catalog.StaleAfter.Hours() / 24),
	}
	if age, ok := c.Age(now); ok {
		info.AgeDays = int(age.Hours() / 24)
	}
	if c.IsStale(now) {
		info.Stale = true
		info.StaleNote = fmt.Sprintf("This compatibility data was generated %s, %d days ago. "+
			"The automatic refresh has not succeeded since then, so newer Rancher or "+
			"Kubernetes releases may be missing from these routes.",
			c.GeneratedAt, info.AgeDays)
	}
	return info
}

// Handler serves GET /api/plan-upgrade.
func Handler(c *catalog.Catalog) fiber.Handler {
	return func(ctx *fiber.Ctx) error {
		n, apiErr := ParseRequest(c, func(k string) string { return ctx.Query(k) })
		if apiErr != nil {
			return ctx.Status(http.StatusBadRequest).JSON(fiber.Map{"error": apiErr})
		}
		out, err := Plan(c, n)
		if err != nil {
			return ctx.Status(http.StatusInternalServerError).
				JSON(fiber.Map{"error": &APIError{Kind: "internal", Detail: err.Error()}})
		}
		return ctx.JSON(out)
	}
}

// LabelValues returns metric label values bounded to what the catalog knows.
//
// versionsSubmitted.WithLabelValues was fed three raw user strings from the URL on a
// public unauthenticated endpoint. Prometheus holds a series per distinct label
// combination for the process lifetime, so anyone could walk incrementing versions
// and allocate unbounded memory. It was survivable only because nothing scraped the
// port; moving metrics to :9090 makes it live across six environments.
func LabelValues(c *catalog.Catalog, platform, rancher, k8s string) (string, string, string) {
	const other = "other"

	p := other
	candidate := catalog.Platform(strings.ToLower(strings.TrimSpace(platform)))
	for _, known := range catalog.KnownPlatforms {
		if known == candidate {
			p = string(known)
			break
		}
	}

	r := other
	if _, ok := c.Find(rancher); ok {
		if v, err := version.NewVersion(catalog.Normalize(rancher)); err == nil {
			seg := v.Segments()
			if len(seg) >= 2 {
				r = fmt.Sprintf("%d.%d", seg[0], seg[1])
			}
		}
	}

	k := other
	if v, err := version.NewVersion(catalog.Normalize(k8s)); err == nil {
		seg := v.Segments()
		if len(seg) >= 2 && seg[0] == 1 && seg[1] >= 16 && seg[1] <= 40 {
			k = fmt.Sprintf("v%d.%d", seg[0], seg[1])
		}
	}
	return p, r, k
}

// ClusterCountBucket buckets the fleet size for metrics.
//
// A fleet endpoint reopens the cardinality problem from a new direction: the cluster
// COUNT, each cluster's platform and every cluster LABEL are all attacker-controlled
// on a public unauthenticated endpoint. Labels especially are free-form strings.
//
// Rules, and they are deliberate: never label by cluster label or cluster identity,
// and bucket the count so the label space stays finite and known in advance.
func ClusterCountBucket(n int) string {
	switch {
	case n <= 0:
		return "0"
	case n == 1:
		return "1"
	case n == 2:
		return "2"
	case n == 3:
		return "3"
	case n <= 5:
		return "4-5"
	default:
		return "many"
	}
}

// FleetSize counts the downstream clusters described by a request without parsing
// or validating it, so the metric can be recorded before planning.
func FleetSize(q func(string) string) int {
	n := 0
	for i := 1; i <= MaxClusters+1; i++ {
		plat := strings.TrimSpace(q(fmt.Sprintf("downstream_platform_%d", i)))
		k8s := strings.TrimSpace(q(fmt.Sprintf("downstream_k8s_%d", i)))
		if i == 1 {
			if plat == "" {
				plat = strings.TrimSpace(q("downstream_platform"))
			}
			if k8s == "" {
				k8s = strings.TrimSpace(q("downstream_k8s"))
			}
		}
		if plat == "" && k8s == "" {
			break
		}
		n++
	}
	return n
}
