// Package catalog holds the Rancher/Kubernetes compatibility dataset and the
// fail-closed loader that guards it.
//
// Two facts from the T0 source spike shape this package:
//
//  1. SUSE publishes ONE range per (rancher, platform), in a table titled
//     "Supported Kubernetes Platforms for Rancher Manager", and that same range
//     governs BOTH the cluster Rancher is installed on and the clusters it
//     manages. Sampled across 2.7.5, 2.9.6, 2.11.3, 2.13.9 and 2.15.1 the install
//     range and the downstream range were identical every time. So Support is
//     stored once and applied to both node axes. Validate asserts the equality
//     holds, because five samples is not a proof.
//
//  2. Granularity is asymmetric and inherently so. KDM lists every real RKE2 and
//     k3s release, so those plan release-to-release. SUSE stopped publishing exact
//     hosted-provider releases after ~2.7.5, so AKS/EKS/GKE plan minor-to-minor.
//     No parser can recover what was never published; the API says so instead.
package catalog

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/hashicorp/go-version"
)

// ErrNotImplemented is returned by the stubs this package ships with. It exists
// so the regression suite fails with a readable reason rather than a nil panic.
var ErrNotImplemented = errors.New("catalog: not implemented")

// Granularity records how precisely a platform's versions are published upstream.
type Granularity string

const (
	// GranularityRelease means real releases are enumerable (RKE2, k3s via KDM).
	GranularityRelease Granularity = "release"
	// GranularityMinor means only minor lines are published (AKS, EKS, GKE).
	GranularityMinor Granularity = "minor"
)

// Platform is a Kubernetes distribution or hosted provider.
type Platform string

const (
	RKE1 Platform = "rke1"
	RKE2 Platform = "rke2"
	K3s  Platform = "k3s"
	AKS  Platform = "aks"
	EKS  Platform = "eks"
	GKE  Platform = "gke"
)

// KnownPlatforms is the closed set the tool models. A platform outside it (ACK,
// for instance, which appears in the 2.13.9 and 2.15.1 matrices) must be reported
// by the loader, never silently dropped.
var KnownPlatforms = []Platform{RKE1, RKE2, K3s, AKS, EKS, GKE}

// CanHostRancher reports whether Rancher Manager can be installed on this
// platform. Every platform in the matrix qualifies: Rancher installs on AKS, EKS
// and GKE as well as RKE2 and k3s. GKE Autopilot is the one exclusion and is not
// expressible as a platform here, so it is surfaced as a caveat on the step.
func (p Platform) CanHostRancher() bool { return true }

// Lifecycle is a Rancher version's support status, derived as of a stated date
// rather than hardcoded, so the answer stays honest as versions age.
type Lifecycle string

const (
	LifecycleFull    Lifecycle = "full"
	LifecycleLimited Lifecycle = "limited"
	LifecycleEnded   Lifecycle = "ended"
)

// Support is one platform's supported Kubernetes range under one Rancher version.
// The range governs both the local (install) and downstream (managed) axes.
type Support struct {
	Platform    Platform    `json:"platform"`
	MinVersion  string      `json:"min_version"`
	MaxVersion  string      `json:"max_version"`
	Granularity Granularity `json:"granularity"`

	// Releases holds real release strings when Granularity is release. Empty for
	// minor-granularity platforms. The planner must never synthesize entries here:
	// getMinorVersionsBetween in the old main.go invented major.minor.0 versions
	// that were never published, which is the bug this field exists to prevent.
	Releases []string `json:"releases,omitempty"`

	SourceURL string `json:"source_url"`
	AsOf      string `json:"as_of"`
}

// RancherVersion is one Rancher release and everything it supports.
type RancherVersion struct {
	Version       string    `json:"version"`
	Lifecycle     Lifecycle `json:"lifecycle"`
	LifecycleAsOf string    `json:"lifecycle_as_of"`
	Platforms     []Support `json:"platforms"`
}

// Catalog is the whole dataset.
type Catalog struct {
	GeneratedAt string           `json:"generated_at"`
	Rancher     []RancherVersion `json:"rancher"`
}

// Normalize strips a leading "v" so "v1.27" and "1.27" are the same version.
// The old code only did this in some paths, so a "v"-prefixed Rancher version
// parsed fine at main.go:123 and then missed the map lookup at main.go:141,
// silently producing an empty plan.
func Normalize(v string) string { return strings.TrimPrefix(strings.TrimSpace(v), "v") }

func parseVersion(v string) (*version.Version, error) { return version.NewVersion(Normalize(v)) }

// Load parses and validates a catalog, failing closed. A malformed entry is an
// error naming its path, never a silently dropped platform: the old loader did
// `continue` on an unparseable range (main.go:263-270), which is why EKS has been
// invisible for Rancher 2.5.10-2.5.16 since 2024 with nothing ever complaining.
//
// Decoding is STRICT. An unrecognised field is an error, not something to ignore:
// once a generator writes this file, a field the loader does not understand means
// the generator and the loader disagree about the schema, and that is precisely
// when to stop rather than proceed on a partial reading.
func Load(data []byte) (*Catalog, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()

	var c Catalog
	if err := dec.Decode(&c); err != nil {
		return nil, fmt.Errorf("catalog: parse failed: %w", err)
	}
	if err := Validate(&c); err != nil {
		// Deliberately return nil: a partial dataset must never escape alongside
		// an error, or a caller that logs and continues serves half a catalog.
		return nil, err
	}
	return &c, nil
}

// Validate checks structural and semantic invariants. Separated from Load so CI
// and the generator can run the identical check before anything is deployed.
func Validate(c *Catalog) error {
	if c == nil {
		return errors.New("catalog: nil catalog")
	}
	if strings.TrimSpace(c.GeneratedAt) == "" {
		return errors.New("catalog: generated_at is empty; a dataset with no date cannot report its own freshness")
	}
	if len(c.Rancher) == 0 {
		return errors.New("catalog: contains no Rancher versions")
	}

	seenVersion := map[string]bool{}
	for _, rv := range c.Rancher {
		where := fmt.Sprintf("catalog: rancher[%s]", rv.Version)

		if strings.TrimSpace(rv.Version) == "" {
			return errors.New("catalog: a rancher entry has an empty version")
		}
		if _, err := parseVersion(rv.Version); err != nil {
			return fmt.Errorf("%s: version %q is not a valid version: %w", where, rv.Version, err)
		}
		if seenVersion[Normalize(rv.Version)] {
			return fmt.Errorf("%s: duplicate rancher version", where)
		}
		seenVersion[Normalize(rv.Version)] = true

		switch rv.Lifecycle {
		case LifecycleFull, LifecycleLimited, LifecycleEnded:
		default:
			return fmt.Errorf("%s: lifecycle %q is not one of full, limited, ended", where, rv.Lifecycle)
		}
		if strings.TrimSpace(rv.LifecycleAsOf) == "" {
			return fmt.Errorf("%s: lifecycle_as_of is empty; a support status derived from no date is not honest", where)
		}
		if len(rv.Platforms) == 0 {
			return fmt.Errorf("%s: has no platforms", where)
		}

		seenPlatform := map[Platform]bool{}
		for _, p := range rv.Platforms {
			pw := fmt.Sprintf("%s.platforms[%s]", where, p.Platform)

			if !isKnownPlatform(p.Platform) {
				return fmt.Errorf("%s: unrecognised platform %q. The tool models %v. "+
					"An unmodelled distro row must be reported, never silently dropped",
					pw, p.Platform, KnownPlatforms)
			}
			if seenPlatform[p.Platform] {
				return fmt.Errorf("%s: duplicate platform entry", pw)
			}
			seenPlatform[p.Platform] = true

			minV, err := parseVersion(p.MinVersion)
			if err != nil {
				return fmt.Errorf("%s.min_version: %q is not a valid version: %w", pw, p.MinVersion, err)
			}
			maxV, err := parseVersion(p.MaxVersion)
			if err != nil {
				return fmt.Errorf("%s.max_version: %q is not a valid version: %w", pw, p.MaxVersion, err)
			}
			if minV.GreaterThan(maxV) {
				return fmt.Errorf("%s: min_version %q is greater than max_version %q", pw, p.MinVersion, p.MaxVersion)
			}

			switch p.Granularity {
			case GranularityRelease:
				if len(p.Releases) == 0 {
					return fmt.Errorf("%s: granularity is %q but no releases are listed; "+
						"the planner walks real releases and must never synthesize them",
						pw, GranularityRelease)
				}
				for i, r := range p.Releases {
					if _, err := parseVersion(r); err != nil {
						return fmt.Errorf("%s.releases[%d]: %q is not a valid version: %w", pw, i, r, err)
					}
				}
			case GranularityMinor:
				if len(p.Releases) != 0 {
					return fmt.Errorf("%s: granularity is %q but releases are listed. "+
						"SUSE stopped publishing exact hosted-provider releases, so these "+
						"would be invented", pw, GranularityMinor)
				}
			default:
				return fmt.Errorf("%s: granularity %q is not one of %q, %q",
					pw, p.Granularity, GranularityRelease, GranularityMinor)
			}

			if strings.TrimSpace(p.SourceURL) == "" {
				return fmt.Errorf("%s: source_url is empty; every fact must cite where it came from", pw)
			}
			if strings.TrimSpace(p.AsOf) == "" {
				return fmt.Errorf("%s: as_of is empty", pw)
			}
		}
	}
	return nil
}

func isKnownPlatform(p Platform) bool {
	for _, k := range KnownPlatforms {
		if k == p {
			return true
		}
	}
	return false
}

// Find returns the entry for a Rancher version. The bool is not decoration: an
// unchecked map read (main.go:141-142) is what let an unknown version produce a
// zero value and an empty plan that read as "you are already current".
func (c *Catalog) Find(v string) (*RancherVersion, bool) {
	if c == nil {
		return nil, false
	}
	want := Normalize(v)
	for i := range c.Rancher {
		if Normalize(c.Rancher[i].Version) == want {
			return &c.Rancher[i], true
		}
	}
	return nil, false
}

// SupportFor returns the range for one platform under one Rancher version. The
// range governs both the local (install) and downstream (managed) axes: the T0
// spike found them identical across every Rancher version sampled.
func (r *RancherVersion) SupportFor(p Platform) (*Support, bool) {
	if r == nil {
		return nil, false
	}
	for i := range r.Platforms {
		if r.Platforms[i].Platform == p {
			return &r.Platforms[i], true
		}
	}
	return nil, false
}

// Covers reports whether a Kubernetes version falls inside this platform's range.
// Comparison is minor-level: the matrix publishes ranges like v1.27-v1.30, and a
// patch release inside a covered minor is covered.
func (s *Support) Covers(k8s string) (bool, error) {
	v, err := parseVersion(k8s)
	if err != nil {
		return false, fmt.Errorf("catalog: %q is not a valid Kubernetes version: %w", k8s, err)
	}
	minV, err := parseVersion(s.MinVersion)
	if err != nil {
		return false, err
	}
	maxV, err := parseVersion(s.MaxVersion)
	if err != nil {
		return false, err
	}
	return minorOf(v) >= minorOf(minV) && minorOf(v) <= minorOf(maxV) &&
		majorOf(v) == majorOf(minV) && majorOf(v) == majorOf(maxV), nil
}

func majorOf(v *version.Version) int {
	seg := v.Segments()
	if len(seg) < 1 {
		return -1
	}
	return seg[0]
}

func minorOf(v *version.Version) int {
	seg := v.Segments()
	if len(seg) < 2 {
		return -1
	}
	return seg[1]
}
