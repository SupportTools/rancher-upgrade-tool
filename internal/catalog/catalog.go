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

import "errors"

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

// Load parses and validates a catalog, failing closed. A malformed entry is an
// error naming its path, never a silently dropped platform: the old loader did
// `continue` on an unparseable range (main.go:263-270), which is why EKS has been
// invisible for Rancher 2.5.10-2.5.16 since 2024 with nothing ever complaining.
func Load(data []byte) (*Catalog, error) { return nil, ErrNotImplemented }

// Validate checks structural and semantic invariants. Separated from Load so CI
// and the generator can run the identical check before anything is deployed.
func Validate(c *Catalog) error { return ErrNotImplemented }

// Find returns the entry for a Rancher version. The bool is not decoration: an
// unchecked map read (main.go:141-142) is what let an unknown version produce a
// zero value and an empty plan that read as "you are already current".
func (c *Catalog) Find(version string) (*RancherVersion, bool) { return nil, false }

// SupportFor returns the range for one platform under one Rancher version.
func (r *RancherVersion) SupportFor(p Platform) (*Support, bool) { return nil, false }
