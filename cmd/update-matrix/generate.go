package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/hashicorp/go-version"
	"github.com/supporttools/rancher-upgrade-tool/internal/catalog"
	"github.com/supporttools/rancher-upgrade-tool/internal/planner"
)

// generate extends the existing catalog rather than replacing it.
//
// Extending is deliberate. Ranges vary between patch releases within a Rancher
// minor -- the 2.5 line alone has 13 distinct range signatures across 17 patches --
// and the existing 70 entries carry that per-patch detail. Upstream only publishes
// the LATEST patch per cycle today, so regenerating from scratch would flatten
// exactly the old versions people are actually stuck on, which is the population
// this tool exists to help.
func generate(snapDir, catPath, reportPath string, dry bool) error {
	// #nosec G304 -- operator-supplied path on a build-time CLI, not a request path.
	baseRaw, err := os.ReadFile(catPath)
	if err != nil {
		return fmt.Errorf("read existing catalog: %w", err)
	}
	base, err := catalog.Load(baseRaw)
	if err != nil {
		return fmt.Errorf("the EXISTING catalog does not validate, so no comparison is "+
			"trustworthy: %w", err)
	}

	// #nosec G304 -- operator-supplied path on a build-time CLI, not a request path.
	rke2Raw, err := os.ReadFile(filepath.Join(snapDir, "kdm-rke2.yaml"))
	if err != nil {
		return fmt.Errorf("kdm rke2 snapshot: %w (run with -fetch first)", err)
	}
	// #nosec G304 -- operator-supplied path on a build-time CLI, not a request path.
	k3sRaw, err := os.ReadFile(filepath.Join(snapDir, "kdm-k3s.yaml"))
	if err != nil {
		return fmt.Errorf("kdm k3s snapshot: %w (run with -fetch first)", err)
	}
	rke2KDM, err := parseKDM(rke2Raw, "kdm-rke2")
	if err != nil {
		return err
	}
	k3sKDM, err := parseKDM(k3sRaw, "kdm-k3s")
	if err != nil {
		return err
	}
	cycles, err := readEOL(snapDir)
	if err != nil {
		return err
	}

	now := time.Now().UTC()
	asOf := now.Format("2006-01-02")

	out := &catalog.Catalog{GeneratedAt: asOf}
	byVersion := map[string]*catalog.RancherVersion{}

	// Carry every existing entry forward, upgrading RKE2 and k3s to real releases
	// where KDM can attribute them to that Rancher version.
	upgraded := 0
	for i := range base.Rancher {
		rv := base.Rancher[i]
		for j := range rv.Platforms {
			p := &rv.Platforms[j]
			var src *kdmFile
			switch p.Platform {
			case catalog.RKE2:
				src = rke2KDM
			case catalog.K3s:
				src = k3sKDM
			default:
				continue
			}
			releases, err := src.releasesFor(rv.Version)
			if err != nil {
				return fmt.Errorf("rancher %s / %s: %w", rv.Version, p.Platform, err)
			}
			if len(releases) == 0 {
				continue // KDM does not reach this Rancher version; keep the range as-is
			}
			p.Granularity = catalog.GranularityRelease
			p.Releases = releases
			p.MinVersion = releases[0]
			p.MaxVersion = releases[len(releases)-1]
			p.SourceURL = kdmRKE2URL
			if p.Platform == catalog.K3s {
				p.SourceURL = kdmK3sURL
			}
			p.AsOf = asOf
			upgraded++
		}
		out.Rancher = append(out.Rancher, rv)
		byVersion[catalog.Normalize(rv.Version)] = &out.Rancher[len(out.Rancher)-1]
	}
	fmt.Printf("carried %d existing Rancher versions forward (%d platform entries upgraded to real releases)\n",
		len(out.Rancher), upgraded)

	// The catalog's oldest version is its floor. This command EXTENDS the catalog
	// forward; it does not backfill history the project never had. Rancher matrices
	// older than the floor use a different page layout, and treating that as a
	// generation failure would be the guard misfiring on something out of scope.
	//
	// The guard is deliberately not weakened to "skip anything that fails to parse".
	// That would recreate the silent-drop defect this project exists to remove. A
	// parse failure at or above the floor is still fatal.
	floor, err := catalogFloor(base)
	if err != nil {
		return err
	}
	fmt.Printf("catalog floor is Rancher %s; older cycles are out of scope\n", floor.Original())

	// Add the latest patch of every cycle upstream knows that we do not.
	added := 0
	for _, c := range cycles {
		if lv, err := version.NewVersion(catalog.Normalize(c.Latest)); err == nil && lv.LessThan(floor) {
			continue
		}
		latest := catalog.Normalize(c.Latest)
		if latest == "" {
			continue
		}
		if _, exists := byVersion[latest]; exists {
			// Refresh lifecycle from the published dates even for known versions.
			byVersion[latest].Lifecycle = lifecycleAsOf(c, now)
			byVersion[latest].LifecycleAsOf = asOf
			continue
		}
		entry, err := buildEntry(latest, c, snapDir, rke2KDM, k3sKDM, asOf, now)
		if err != nil {
			return err
		}
		if entry == nil {
			continue
		}
		out.Rancher = append(out.Rancher, *entry)
		byVersion[latest] = &out.Rancher[len(out.Rancher)-1]
		added++
		fmt.Printf("  added Rancher %s (%s support)\n", entry.Version, entry.Lifecycle)
	}
	fmt.Printf("added %d new Rancher versions\n", added)

	sort.Slice(out.Rancher, func(i, j int) bool {
		a, _ := version.NewVersion(catalog.Normalize(out.Rancher[i].Version))
		b, _ := version.NewVersion(catalog.Normalize(out.Rancher[j].Version))
		return a.LessThan(b)
	})

	if err := guardNoRegression(base, out); err != nil {
		return err
	}
	if err := catalog.Validate(out); err != nil {
		return fmt.Errorf("generated catalog does not validate: %w", err)
	}

	summary := reportJourneyDiff(base, out)

	if reportPath != "" {
		if err := writeReport(reportPath, base, out, summary); err != nil {
			return fmt.Errorf("write report: %w", err)
		}
		fmt.Printf("wrote review report to %s\n", reportPath)
	}

	encoded, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return err
	}
	encoded = append(encoded, '\n')
	if dry {
		fmt.Printf("\ndry run: would write %d bytes to %s\n", len(encoded), catPath)
		return nil
	}
	if err := os.WriteFile(catPath, encoded, 0o600); err != nil {
		return err
	}
	fmt.Printf("\nwrote %s (%d Rancher versions)\n", catPath, len(out.Rancher))
	return nil
}

// catalogFloor returns the oldest Rancher version in the catalog.
func catalogFloor(c *catalog.Catalog) (*version.Version, error) {
	var floor *version.Version
	for _, rv := range c.Rancher {
		v, err := version.NewVersion(catalog.Normalize(rv.Version))
		if err != nil {
			return nil, fmt.Errorf("catalog contains unparseable version %q: %w", rv.Version, err)
		}
		if floor == nil || v.LessThan(floor) {
			floor = v
		}
	}
	if floor == nil {
		return nil, fmt.Errorf("catalog is empty; cannot establish a floor")
	}
	return floor, nil
}

func buildEntry(latest string, c eolCycle, snapDir string, rke2KDM, k3sKDM *kdmFile,
	asOf string, now time.Time) (*catalog.RancherVersion, error) {

	page := filepath.Join(snapDir, "matrix", "rancher-"+latest+".html")
	// #nosec G304 -- operator-supplied path on a build-time CLI, not a request path.
	html, err := os.ReadFile(page)
	if err != nil {
		// No matrix page: SUSE retires very old ones. Skip the version rather than
		// inventing hosted-provider data for it.
		fmt.Printf("  skip Rancher %s: no matrix snapshot (%v)\n", latest, err)
		return nil, nil
	}
	rows, err := parseMatrix(html, "matrix "+latest)
	if err != nil {
		return nil, err
	}

	parts := strings.Split(latest, ".")
	sourceURL := fmt.Sprintf(matrixBase, parts[0], parts[1], parts[2])

	entry := &catalog.RancherVersion{
		Version:       latest,
		Lifecycle:     lifecycleAsOf(c, now),
		LifecycleAsOf: asOf,
	}

	for _, row := range rows {
		p := catalog.Platform(row.Distro)
		known := false
		for _, k := range catalog.KnownPlatforms {
			if k == p {
				known = true
				break
			}
		}
		if !known {
			// Reported, never silently dropped. ACK appears in recent matrices and
			// the tool does not model it.
			fmt.Printf("  note: Rancher %s lists platform %q, which this tool does not model; not included\n",
				latest, row.Distro)
			continue
		}

		sup := catalog.Support{
			Platform:    p,
			MinVersion:  row.Min,
			MaxVersion:  row.Max,
			Granularity: catalog.GranularityMinor,
			SourceURL:   sourceURL,
			AsOf:        asOf,
		}

		var kdm *kdmFile
		switch p {
		case catalog.RKE2:
			kdm = rke2KDM
		case catalog.K3s:
			kdm = k3sKDM
		}
		if kdm != nil {
			releases, err := kdm.releasesFor(latest)
			if err != nil {
				return nil, fmt.Errorf("rancher %s / %s: %w", latest, p, err)
			}
			if len(releases) > 0 {
				sup.Granularity = catalog.GranularityRelease
				sup.Releases = releases
				sup.MinVersion = releases[0]
				sup.MaxVersion = releases[len(releases)-1]
				sup.SourceURL = kdmRKE2URL
				if p == catalog.K3s {
					sup.SourceURL = kdmK3sURL
				}
			}
		}
		entry.Platforms = append(entry.Platforms, sup)
	}

	if len(entry.Platforms) == 0 {
		return nil, fmt.Errorf("rancher %s: parsed the matrix but produced no modelled "+
			"platforms; refusing to emit an entry with nothing in it", latest)
	}
	return entry, nil
}

// guardNoRegression is the fail-rather-than-partial rule.
//
// A generator that silently loses a platform reproduces the defect this project
// began with -- getSortedK8sVersions dropping an entry it could not parse -- one
// layer up, where the pull-request diff a human reviews looks like a normal refresh.
func guardNoRegression(base, next *catalog.Catalog) error {
	index := map[string]map[catalog.Platform]bool{}
	for _, rv := range next.Rancher {
		set := map[catalog.Platform]bool{}
		for _, p := range rv.Platforms {
			set[p.Platform] = true
		}
		index[catalog.Normalize(rv.Version)] = set
	}

	var lost []string
	for _, rv := range base.Rancher {
		set, ok := index[catalog.Normalize(rv.Version)]
		if !ok {
			lost = append(lost, fmt.Sprintf("Rancher %s disappeared entirely", rv.Version))
			continue
		}
		for _, p := range rv.Platforms {
			if !set[p.Platform] {
				lost = append(lost, fmt.Sprintf("Rancher %s lost platform %s", rv.Version, p.Platform))
			}
		}
	}
	if len(lost) > 0 {
		sort.Strings(lost)
		return fmt.Errorf("refusing to emit: the new catalog loses data the old one had:\n  - %s",
			strings.Join(lost, "\n  - "))
	}
	return nil
}

// reportJourneyDiff states which routes broke and which became possible, so a
// reviewer assesses operational consequences instead of reading hundreds of changed
// JSON lines. A plausible-looking wrong number is indistinguishable from a correct
// new one in a raw diff.
func reportJourneyDiff(base, next *catalog.Catalog) string {
	probes := []planner.Node{
		{Rancher: "2.5.12", LocalPlatform: catalog.RKE1, LocalK8s: "1.19", DownPlatform: catalog.EKS, DownK8s: "1.19"},
		{Rancher: "2.6.9", LocalPlatform: catalog.RKE2, LocalK8s: "1.22", DownPlatform: catalog.RKE2, DownK8s: "1.22"},
		{Rancher: "2.8.5", LocalPlatform: catalog.K3s, LocalK8s: "1.26", DownPlatform: catalog.AKS, DownK8s: "1.26"},
		{Rancher: "2.9.6", LocalPlatform: catalog.RKE2, LocalK8s: "1.28", DownPlatform: catalog.RKE2, DownK8s: "1.28"},
		{Rancher: "2.11.3", LocalPlatform: catalog.RKE2, LocalK8s: "1.30", DownPlatform: catalog.EKS, DownK8s: "1.30"},
	}

	var buf strings.Builder
	emit := func(format string, args ...any) {
		line := fmt.Sprintf(format, args...)
		fmt.Print(line)
		buf.WriteString(line)
	}
	emit("\nJOURNEY DIFF (what changed for real queries, not raw JSON lines)\n")
	for _, probe := range probes {
		before := destinations(base, probe)
		after := destinations(next, probe)

		gained := difference(after, before)
		lost := difference(before, after)

		// Distinguish a waypoint MOVING within its minor line from a destination
		// genuinely disappearing. Waypoints are latest-patch-per-minor, so every
		// refresh that picks up a new patch retires the old waypoint for that line.
		// Reporting those as losses would cry wolf on every single run, and a
		// reviewer who learns to ignore the warning is worse off than one who never
		// had it.
		moved, reallyLost := splitMoves(lost, gained)

		label := fmt.Sprintf("  Rancher %s, %s local / %s downstream at %s",
			probe.Rancher, probe.LocalPlatform, probe.DownPlatform, probe.DownK8s)

		if len(gained) == 0 && len(lost) == 0 {
			emit("%s: unchanged (%d destinations)\n", label, len(after))
			continue
		}
		emit("%s: %d -> %d destinations\n", label, len(before), len(after))
		if len(moved) > 0 {
			emit("      waypoint moved: %s\n", strings.Join(moved, ", "))
		}
		if newly := difference(gained, movedTargets(moved)); len(newly) > 0 {
			emit("      NEWLY REACHABLE: %s\n", strings.Join(newly, ", "))
		}
		if len(reallyLost) > 0 {
			emit("      NO LONGER REACHABLE: %s   <-- REVIEW THIS\n", strings.Join(reallyLost, ", "))
		}
	}
	return buf.String()
}

// writeReport produces the pull-request body. A reviewer should be able to judge
// operational consequences from it without reading a 270KB JSON diff, where a
// plausible-looking wrong number is indistinguishable from a correct new one.
func writeReport(path string, base, next *catalog.Catalog, journey string) error {
	var b strings.Builder
	b.WriteString("## Catalog refresh\n\n")
	fmt.Fprintf(&b, "Generated %s by `cmd/update-matrix`.\n\n", next.GeneratedAt)

	fmt.Fprintf(&b, "| | before | after |\n|---|---|---|\n")
	fmt.Fprintf(&b, "| Rancher versions | %d | %d |\n", len(base.Rancher), len(next.Rancher))
	fmt.Fprintf(&b, "| newest | %s | %s |\n", newest(base), newest(next))

	beforeG, afterG := granularityCounts(base), granularityCounts(next)
	fmt.Fprintf(&b, "| release-granularity entries | %d | %d |\n", beforeG, afterG)

	added := addedVersions(base, next)
	if len(added) > 0 {
		fmt.Fprintf(&b, "\n**Rancher versions added:** %s\n", strings.Join(added, ", "))
	}

	b.WriteString("\n### What changed for real queries\n\n```\n")
	b.WriteString(strings.TrimPrefix(journey, "\n"))
	b.WriteString("```\n")

	b.WriteString("\n### Reviewer checklist\n\n")
	b.WriteString("- [ ] Any line marked `REVIEW THIS` is a destination that genuinely stopped being reachable, not a waypoint moving within its minor line.\n")
	b.WriteString("- [ ] Golden routes passed in CI. They encode facts read off upstream by a human; if one failed, re-read the cited source rather than editing the expectation.\n")
	b.WriteString("- [ ] Any newly added Rancher version's ranges match its support-matrix page.\n")
	b.WriteString("\nThis PR is generated. It never commits to main on its own.\n")

	return os.WriteFile(path, []byte(b.String()), 0o600)
}

func newest(c *catalog.Catalog) string {
	if len(c.Rancher) == 0 {
		return "none"
	}
	best := c.Rancher[0].Version
	bv, _ := version.NewVersion(catalog.Normalize(best))
	for _, rv := range c.Rancher {
		v, err := version.NewVersion(catalog.Normalize(rv.Version))
		if err == nil && (bv == nil || v.GreaterThan(bv)) {
			best, bv = rv.Version, v
		}
	}
	return best
}

func granularityCounts(c *catalog.Catalog) int {
	n := 0
	for _, rv := range c.Rancher {
		for _, p := range rv.Platforms {
			if p.Granularity == catalog.GranularityRelease {
				n++
			}
		}
	}
	return n
}

func addedVersions(base, next *catalog.Catalog) []string {
	had := map[string]bool{}
	for _, rv := range base.Rancher {
		had[catalog.Normalize(rv.Version)] = true
	}
	var out []string
	for _, rv := range next.Rancher {
		if !had[catalog.Normalize(rv.Version)] {
			out = append(out, rv.Version)
		}
	}
	return out
}

func minorLine(v string) string {
	parts := strings.Split(catalog.Normalize(v), ".")
	if len(parts) < 2 {
		return v
	}
	return parts[0] + "." + parts[1]
}

// splitMoves pairs each lost destination with a gained one on the same minor line.
// A pair is a waypoint move; anything unpaired is a real loss.
func splitMoves(lost, gained []string) (moves []string, reallyLost []string) {
	byLine := map[string]string{}
	for _, g := range gained {
		byLine[minorLine(g)] = g
	}
	for _, l := range lost {
		if g, ok := byLine[minorLine(l)]; ok {
			moves = append(moves, fmt.Sprintf("%s -> %s", l, g))
			continue
		}
		reallyLost = append(reallyLost, l)
	}
	return moves, reallyLost
}

func movedTargets(moves []string) []string {
	out := make([]string, 0, len(moves))
	for _, m := range moves {
		if i := strings.Index(m, " -> "); i >= 0 {
			out = append(out, m[i+4:])
		}
	}
	return out
}

func destinations(c *catalog.Catalog, n planner.Node) []string {
	res, err := planner.Reachable(c, n)
	if err != nil || res == nil {
		return nil
	}
	out := make([]string, 0, len(res.Routes))
	for _, r := range res.Routes {
		out = append(out, r.Destination)
	}
	return out
}

func difference(a, b []string) []string {
	inB := map[string]bool{}
	for _, v := range b {
		inB[v] = true
	}
	var out []string
	for _, v := range a {
		if !inB[v] {
			out = append(out, v)
		}
	}
	return out
}
