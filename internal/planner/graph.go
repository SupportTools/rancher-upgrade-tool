// Package planner turns the compatibility catalog into upgrade routes.
//
// The central correction over the old implementation: compatibility describes
// valid STATES, upgrade planning requires valid TRANSITIONS. The old code walked
// the union of two Rancher versions' supported ranges
// (append(r1.SupportedPlatforms, r2.SupportedPlatforms...), main.go:257), so it
// could route through a Kubernetes version that neither Rancher version supported
// on its own. It also appended the Rancher hop before deriving any Kubernetes step
// (main.go:136-138 vs :146-149), so it could order a Rancher upgrade the cluster's
// current Kubernetes could not support.
//
// Here an edge is emitted only when BOTH endpoints are individually valid, and
// moves exactly one component. The union bug becomes unrepresentable rather than
// filtered out afterwards, and correct ordering falls out of the structure:
//
//	start: Rancher 2.9.2, downstream v1.27; waypoint 2.10.x supports v1.28-v1.31
//	  old: emit "2.9.2 -> 2.10.1", then walk a union containing v1.27
//	  new: valid(2.10.1, *, v1.27) is false, so that edge does not exist. The only
//	       edge out is raising downstream v1.27 -> v1.28 under 2.9.2.
package planner

import (
	"errors"

	"github.com/supporttools/rancher-upgrade-tool/internal/catalog"
)

// ErrNotImplemented lets the regression suite fail readably rather than panic.
var ErrNotImplemented = errors.New("planner: not implemented")

// Node is a complete cluster state. Local and downstream platforms are separate
// identities because Rancher on k3s managing EKS is a common real install, and
// (per the T0 spike) Rancher Manager installs on hosted providers too.
type Node struct {
	Rancher string

	LocalPlatform catalog.Platform
	LocalK8s      string

	DownPlatform catalog.Platform
	DownK8s      string
}

// StepKind names which single component an edge moves.
type StepKind string

const (
	StepRancher StepKind = "rancher"
	StepLocal   StepKind = "local-k8s"
	StepDown    StepKind = "downstream-k8s"
)

// Step is one move, with the evidence that licensed it.
type Step struct {
	Kind     StepKind
	Platform catalog.Platform
	From     string
	To       string

	SourceURL string
	AsOf      string

	// Caveats carries conditions the matrix does not encode as versions, such as
	// GKE Autopilot being unable to host Rancher, or a hosted cluster needing an
	// ingress controller deployed before Rancher is installed.
	Caveats []string
}

// Route is an ordered path to one reachable destination.
type Route struct {
	Destination string
	Steps       []Step

	// Granularity is the weakest granularity along the route. A route touching a
	// hosted provider is minor-level, and the response says so rather than letting
	// it look as precise as an RKE2 route.
	Granularity catalog.Granularity
}

// BlockerKind distinguishes three outcomes the old code collapsed into one empty
// list. Telling them apart is the point: an empty result that reads as "you are
// already current" is the worst failure this tool can produce.
type BlockerKind string

const (
	// BlockerUnsupportedStart means valid(from) is false, so no edges exist by
	// construction. That is a statement about the start state, NOT proof that
	// recovery is impossible.
	BlockerUnsupportedStart BlockerKind = "unsupported-start"
	// BlockerMissingEvidence means the catalog cannot establish a supported route.
	BlockerMissingEvidence BlockerKind = "missing-evidence"
	// BlockerIncompatible means a real, evidenced incompatibility.
	BlockerIncompatible BlockerKind = "incompatible"
)

// Blocker explains why a walk stopped, naming the constraint responsible.
type Blocker struct {
	Kind       BlockerKind
	Constraint string
	Detail     string
	SourceURL  string
}

// Result is the whole answer: every reachable destination, plus why the rest are not.
type Result struct {
	Start    Node
	Routes   []Route
	Blockers []Blocker

	// Scope states what the answer covers. One local/downstream pair, never a
	// fleet: upgrading Rancher affects every cluster it manages, and this answer
	// speaks only for the pair supplied.
	Scope string
}

// Valid reports whether every component of a node is individually supported:
// Rancher installs on the local pair, and Rancher manages the downstream pair.
func Valid(c *catalog.Catalog, n Node) (bool, error) { return false, ErrNotImplemented }

// Edge is a candidate transition between two valid nodes.
type Edge struct {
	From Node
	To   Node
	Step Step
}

// Edges returns every legal transition out of a node. An edge moves exactly one
// component and is emitted only if valid(from) and valid(to).
func Edges(c *catalog.Catalog, n Node) ([]Edge, error) { return nil, ErrNotImplemented }

// Waypoints returns the Rancher versions worth stopping at: the latest patch of
// each minor, roughly 8 instead of 70.
//
// It replaces GetKeyVersions (main.go:350-372), which selected waypoints with
// strings.HasSuffix(v, ".9") plus three hardcoded version strings and therefore
// capped every answer the tool could give at Rancher 2.9.2.
func Waypoints(c *catalog.Catalog) ([]string, error) { return nil, ErrNotImplemented }

// NextRancherWaypoint returns the next Rancher waypoint after current.
//
// Adjacency is NUMERIC, not catalog order. If the numerically adjacent minor is
// absent from the catalog this returns a blocker rather than skipping to the next
// present entry whose endpoints happen to validate. Catalog ordering is not a
// safety property; the same rule the Kubernetes axes obey applies here.
//
// Crossing a minor also requires being on the latest patch of the current minor.
func NextRancherWaypoint(c *catalog.Catalog, current string) (string, *Blocker, error) {
	return "", nil, ErrNotImplemented
}

// Reachable walks every legal edge from start and returns each destination with
// its route, plus blockers for what could not be reached.
func Reachable(c *catalog.Catalog, start Node) (*Result, error) { return nil, ErrNotImplemented }
