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
)

// APIError is the 400 body. It names the field and echoes the value so the user can
// see what the service actually received.
type APIError struct {
	Kind   ErrorKind `json:"kind"`
	Field  string    `json:"field"`
	Value  string    `json:"value,omitempty"`
	Detail string    `json:"detail"`
}

func (e *APIError) Error() string { return fmt.Sprintf("%s: %s", e.Kind, e.Detail) }

type ClusterState struct {
	Platform  string `json:"platform"`
	K8s       string `json:"k8s"`
	Lifecycle string `json:"lifecycle,omitempty"`
}

type StartState struct {
	Rancher    string       `json:"rancher"`
	Lifecycle  string       `json:"lifecycle,omitempty"`
	Local      ClusterState `json:"local"`
	Downstream ClusterState `json:"downstream"`
}

type Step struct {
	Kind      string   `json:"kind"`
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
	Catalog          CatalogInfo   `json:"catalog"`
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
	var downPlat catalog.Platform
	var downK8s string
	if downPlat, apiErr = normalizePlatform("downstream_platform", q("downstream_platform")); apiErr != nil {
		return n, apiErr
	}
	if downK8s, apiErr = normalizeVersion("downstream_k8s", q("downstream_k8s")); apiErr != nil {
		return n, apiErr
	}
	n.Clusters = []planner.Cluster{{
		ID: "c1", Label: "Cluster 1", Platform: downPlat, K8s: downK8s,
	}}
	return n, nil
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
			Rancher:    n.Rancher,
			Local:      ClusterState{Platform: string(n.LocalPlatform), K8s: displayK8s(n.LocalK8s)},
			Downstream: ClusterState{Platform: string(n.Clusters[0].Platform), K8s: displayK8s(n.Clusters[0].K8s)},
		},
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

	// RKE1 is end of life and left the Rancher support matrix between 2.11.3 and
	// 2.13.9. An empty version list would read as "no upgrade available" rather
	// than "this product is over, migrate to RKE2".
	for role, p := range map[string]catalog.Platform{"local": n.LocalPlatform, "downstream": n.Clusters[0].Platform} {
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
