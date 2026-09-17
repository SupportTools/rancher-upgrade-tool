package main

import (
	"fmt"
	"sort"

	"github.com/hashicorp/go-version"
	"gopkg.in/yaml.v3"
)

// KDM is Rancher's Kontainer Driver Metadata: the machine-readable compatibility
// data Rancher's own UI uses to decide which Kubernetes versions you may pick.
//
// Each release entry carries the Rancher version RANGE that supports it:
//
//   - version: v1.30.14+rke2r2
//     minChannelServerVersion: v2.9.0-alpha1
//     maxChannelServerVersion: v2.11.99
//
// Confirmed semantics: rancher/rancher#26011 and rancher/dashboard#5409.
//
// This is the source the tool spent two years hand-retyping out of an HTML table.
// It covers RKE2 and k3s only; the hosted providers have no equivalent feed.
type kdmRelease struct {
	Version                 string `yaml:"version"`
	MinChannelServerVersion string `yaml:"minChannelServerVersion"`
	MaxChannelServerVersion string `yaml:"maxChannelServerVersion"`
}

type kdmFile struct {
	Releases []kdmRelease `yaml:"releases"`
}

// parseKDM reads a channels file. It refuses a file with no releases, and refuses
// any release missing its Rancher range: an entry we cannot place is not an entry
// we may quietly skip, which is the failure mode this whole rewrite exists to end.
func parseKDM(data []byte, label string) (*kdmFile, error) {
	var f kdmFile
	if err := yaml.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("%s: parse failed: %w", label, err)
	}
	if len(f.Releases) == 0 {
		return nil, fmt.Errorf("%s: no releases found. The upstream shape changed, "+
			"or the snapshot is truncated. Refusing to emit rather than silently "+
			"producing a catalog with no %s data", label, label)
	}
	for i, r := range f.Releases {
		if r.Version == "" {
			return nil, fmt.Errorf("%s: releases[%d] has no version", label, i)
		}
		if r.MinChannelServerVersion == "" || r.MaxChannelServerVersion == "" {
			return nil, fmt.Errorf("%s: release %s is missing its Rancher range "+
				"(min=%q max=%q); without it the release cannot be attributed to any "+
				"Rancher version", label, r.Version, r.MinChannelServerVersion, r.MaxChannelServerVersion)
		}
	}
	return &f, nil
}

// releasesFor returns every Kubernetes release this Rancher version supports,
// ascending, by testing min <= rancher <= max on each entry.
func (f *kdmFile) releasesFor(rancherVersion string) ([]string, error) {
	rv, err := version.NewVersion(normalize(rancherVersion))
	if err != nil {
		return nil, fmt.Errorf("rancher version %q: %w", rancherVersion, err)
	}

	type parsed struct {
		raw string
		v   *version.Version
	}
	var out []parsed

	for _, r := range f.Releases {
		minV, err := version.NewVersion(normalize(r.MinChannelServerVersion))
		if err != nil {
			return nil, fmt.Errorf("release %s: minChannelServerVersion %q: %w",
				r.Version, r.MinChannelServerVersion, err)
		}
		maxV, err := version.NewVersion(normalize(r.MaxChannelServerVersion))
		if err != nil {
			return nil, fmt.Errorf("release %s: maxChannelServerVersion %q: %w",
				r.Version, r.MaxChannelServerVersion, err)
		}
		if rv.LessThan(minV) || rv.GreaterThan(maxV) {
			continue
		}
		kv, err := version.NewVersion(normalize(r.Version))
		if err != nil {
			return nil, fmt.Errorf("release version %q: %w", r.Version, err)
		}
		out = append(out, parsed{raw: r.Version, v: kv})
	}

	sort.Slice(out, func(i, j int) bool { return out[i].v.LessThan(out[j].v) })
	names := make([]string, 0, len(out))
	for _, p := range out {
		names = append(names, p.raw)
	}
	return names, nil
}
