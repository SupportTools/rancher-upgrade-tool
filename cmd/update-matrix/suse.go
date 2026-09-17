package main

import (
	"fmt"
	"regexp"
	"strings"
)

// The SUSE support matrix publishes, near the top of each per-version page, a table
// titled "Supported Kubernetes Platforms for Rancher Manager":
//
//	Distro | Lowest Version certified | Highest Version certified on | Notes
//	RKE2   | v1.34                    | v1.36                        | ...
//	AKS    | v1.34                    | v1.36                        |
//
// The T0 source spike established two things about it. It is the ONLY published
// source for the hosted providers, and for every Rancher version sampled its range
// was identical to the downstream range, which is why one range serves both axes.
//
// It also established the limit: after roughly Rancher 2.7.5 this table stopped
// carrying exact hosted releases and gives minor lines only. No parser recovers what
// was never published, so hosted providers are minor-granularity and the API says so.

var (
	tagStripper = regexp.MustCompile(`<[^>]+>`)
	spaceRun    = regexp.MustCompile(`\s+`)
	// A row is a distro name followed by two version-shaped cells. Cells are
	// separated by a mix of pipes (from stripped tags) and whitespace, so the
	// separator class must accept both: the real markup renders as "|RKE2| |v1.34|".
	rowPattern = regexp.MustCompile(`(?i)[\s|](RKE2|RKE1|k3s|AKS|EKS|GKE|ACK)[\s|]+(v?\d[\w.\-+]*)[\s|]+(v?\d[\w.\-+]*)[\s|]`)
)

type matrixRow struct {
	Distro string
	Min    string
	Max    string
}

// parseMatrix extracts the Rancher Manager platforms table from a matrix page.
func parseMatrix(html []byte, label string) ([]matrixRow, error) {
	text := string(html)
	idx := indexFold(text, "Supported Kubernetes Platforms")
	if idx < 0 {
		return nil, fmt.Errorf("%s: the 'Supported Kubernetes Platforms for Rancher Manager' "+
			"table is absent. The page shape changed. Refusing to emit rather than guessing", label)
	}

	// Bound the window so a later table (Downstream Cluster Support) cannot bleed in.
	end := indexFold(text[idx:], "Rancher Manager Supported Architectures")
	if end < 0 {
		end = 4000
	}
	if idx+end > len(text) {
		end = len(text) - idx
	}
	window := text[idx : idx+end]

	flat := tagStripper.ReplaceAllString(window, "|")
	flat = spaceRun.ReplaceAllString(flat, " ")

	var rows []matrixRow
	seen := map[string]bool{}
	for _, m := range rowPattern.FindAllStringSubmatch(flat, -1) {
		distro := strings.ToLower(m[1])
		if seen[distro] {
			continue
		}
		seen[distro] = true
		rows = append(rows, matrixRow{Distro: distro, Min: m[2], Max: m[3]})
	}

	if len(rows) == 0 {
		return nil, fmt.Errorf("%s: found the platforms heading but parsed no rows. "+
			"The table markup changed. Refusing to emit a catalog missing every hosted "+
			"provider for this version", label)
	}
	return rows, nil
}

func indexFold(haystack, needle string) int {
	return strings.Index(strings.ToLower(haystack), strings.ToLower(needle))
}
