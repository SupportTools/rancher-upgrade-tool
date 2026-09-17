// Command update-matrix regenerates data/catalog.json from upstream.
//
// It exists because the compatibility matrix was hand-maintained and drifted four
// Rancher releases behind: the data stopped at 2.11.3 while supported Rancher was
// 2.15. Hand-maintaining predicts its own failure.
//
// Three sources, each the most machine-readable one available:
//
//   - endoflife.date/api/rancher.json  Rancher cycles, latest patch, support and EOL
//     dates. Gives the waypoints and the lifecycle status directly.
//   - KDM channels files                RKE2 and k3s, with REAL release lists, keyed
//     by the Rancher range that supports each release.
//   - SUSE support matrix pages         AKS, EKS and GKE. The only published source,
//     and minor-granularity only for recent versions.
//
// Two rules govern the whole thing:
//
//  1. FAIL RATHER THAN EMIT PARTIAL. If a platform present in the previous catalog
//     disappears, or an upstream shape changes, write nothing. A generator that
//     quietly drops a platform reproduces the exact defect this project began with,
//     one layer up, where a human reviewing a diff cannot see it either.
//
//  2. NEVER SYNTHESIZE. Hosted providers get the minor lines upstream publishes and
//     no invented patch versions. That was getMinorVersionsBetween's defect.
//
// Output goes to a pull request for human review. It never commits to main.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/supporttools/rancher-upgrade-tool/internal/catalog"
)

const (
	kdmBranch  = "release-v2.15"
	kdmRKE2URL = "https://raw.githubusercontent.com/rancher/kontainer-driver-metadata/" + kdmBranch + "/channels-rke2.yaml"
	kdmK3sURL  = "https://raw.githubusercontent.com/rancher/kontainer-driver-metadata/" + kdmBranch + "/channels.yaml"
	eolURL     = "https://endoflife.date/api/rancher.json"
	matrixBase = "https://www.suse.com/suse-rancher/support-matrix/all-supported-versions/rancher-v%s-%s-%s/"
)

func normalize(v string) string { return strings.TrimPrefix(strings.TrimSpace(v), "v") }

type eolCycle struct {
	Cycle   string `json:"cycle"`
	Latest  string `json:"latest"`
	Support any    `json:"support"`
	EOL     any    `json:"eol"`
}

func main() {
	fetch := flag.Bool("fetch", false, "download upstream snapshots before generating")
	snapDir := flag.String("snapshots", "snapshots", "directory holding pinned upstream snapshots")
	catPath := flag.String("catalog", "data/catalog.json", "catalog to extend and rewrite")
	dry := flag.Bool("dry-run", false, "report what would change without writing")
	flag.Parse()

	if *fetch {
		if err := doFetch(*snapDir); err != nil {
			fmt.Fprintf(os.Stderr, "fetch failed: %v\n", err)
			os.Exit(1)
		}
	}
	if err := generate(*snapDir, *catPath, *dry); err != nil {
		fmt.Fprintf(os.Stderr, "\nGENERATION FAILED, nothing written: %v\n", err)
		os.Exit(1)
	}
}

func get(url, dest string) error {
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "rancher-upgrade-tool update-matrix (+https://rancher.tips)")
	client := &http.Client{Timeout: 90 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s: HTTP %d", url, resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if len(body) == 0 {
		return fmt.Errorf("%s: empty body", url)
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	return os.WriteFile(dest, body, 0o644)
}

func doFetch(snapDir string) error {
	fmt.Println("fetching upstream snapshots...")
	for _, job := range []struct{ url, dest string }{
		{kdmRKE2URL, filepath.Join(snapDir, "kdm-rke2.yaml")},
		{kdmK3sURL, filepath.Join(snapDir, "kdm-k3s.yaml")},
		{eolURL, filepath.Join(snapDir, "endoflife-rancher.json")},
	} {
		if err := get(job.url, job.dest); err != nil {
			return err
		}
		fmt.Printf("  %s\n", job.dest)
	}

	cycles, err := readEOL(snapDir)
	if err != nil {
		return err
	}
	for _, c := range cycles {
		parts := strings.Split(normalize(c.Latest), ".")
		if len(parts) < 3 {
			continue
		}
		url := fmt.Sprintf(matrixBase, parts[0], parts[1], parts[2])
		dest := filepath.Join(snapDir, "matrix", "rancher-"+normalize(c.Latest)+".html")
		if err := get(url, dest); err != nil {
			// A matrix page that 404s is normal for very old cycles: SUSE retires
			// them. Record and continue; generate() decides whether the absence
			// matters for a version it actually needs.
			fmt.Printf("  SKIP %s (%v)\n", dest, err)
			continue
		}
		fmt.Printf("  %s\n", dest)
	}
	return nil
}

func readEOL(snapDir string) ([]eolCycle, error) {
	raw, err := os.ReadFile(filepath.Join(snapDir, "endoflife-rancher.json"))
	if err != nil {
		return nil, fmt.Errorf("endoflife snapshot: %w (run with -fetch first)", err)
	}
	var cycles []eolCycle
	if err := json.Unmarshal(raw, &cycles); err != nil {
		return nil, fmt.Errorf("endoflife snapshot: %w", err)
	}
	if len(cycles) == 0 {
		return nil, fmt.Errorf("endoflife snapshot contains no cycles; refusing to proceed")
	}
	return cycles, nil
}

// lifecycleAsOf derives support status from the published dates rather than
// hardcoding it, so the answer stays honest as versions age.
func lifecycleAsOf(c eolCycle, now time.Time) catalog.Lifecycle {
	parse := func(v any) (time.Time, bool) {
		s, ok := v.(string)
		if !ok {
			return time.Time{}, false
		}
		t, err := time.Parse("2006-01-02", s)
		return t, err == nil
	}
	if support, ok := parse(c.Support); ok && now.Before(support) {
		return catalog.LifecycleFull
	}
	if eol, ok := parse(c.EOL); ok && now.Before(eol) {
		return catalog.LifecycleLimited
	}
	return catalog.LifecycleEnded
}
