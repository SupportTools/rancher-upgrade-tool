#!/usr/bin/env python3
"""One-time migration: data/upgrade-paths.json -> data/catalog.json.

Superseded by cmd/update-matrix (T10) once that generates the catalog from KDM and
the SUSE matrix. Kept so the transformation is auditable rather than a mystery diff.

Three things it does beyond reshaping:

1. Fixes the seven EKS max_version values of "v1.20.x" on Rancher 2.5.10-2.5.16.
   "1.20.x" is not a version, so the old loader's `continue` (main.go:263-270)
   dropped the whole EKS entry, making EKS invisible for those seven Rancher
   versions on the live site since 2024 with nothing ever complaining.

2. Marks EVERY platform minor-granularity. The old file carries min/max ranges and
   nothing else, so that is genuinely all the data there is. T10 upgrades RKE2 and
   k3s to release granularity when KDM supplies real release lists. Claiming
   release granularity here would mean synthesizing releases, which is the exact
   defect getMinorVersionsBetween embodied.

3. Adds the provenance the itinerary needs: source URL and as-of date per entry,
   lifecycle status per Rancher version derived as of a stated date.
"""
import json
import re
import sys

AS_OF = "2026-09-17"
MATRIX = "https://www.suse.com/suse-rancher/support-matrix/all-supported-versions/rancher-v{}-{}-{}/"

PLATFORM = {"RKE1": "rke1", "RKE2": "rke2", "K3s": "k3s",
            "AKS": "aks", "EKS": "eks", "GKE": "gke"}

# Corrections applied to known-bad source values, each with its reason.
FIXES = {("eks", "max_version", "v1.20.x"): "v1.20"}


def lifecycle(ver):
    """Support status as of AS_OF. Sourced from endoflife.date/rancher and the
    SUSE support matrix: 2.15/2.14/2.13 full, 2.12/2.11 limited (2.11 ends
    2026-10-24), everything older ended."""
    major, minor = (int(x) for x in ver.split(".")[:2])
    if (major, minor) >= (2, 13):
        return "full"
    if (major, minor) in {(2, 11), (2, 12)}:
        return "limited"
    return "ended"


def main():
    src = json.load(open("data/upgrade-paths.json"))
    fixed = 0
    out = {"generated_at": AS_OF, "rancher": []}

    def sortkey(v):
        return [int(x) for x in re.findall(r"\d+", v)]

    for ver in sorted(src["rancher_manager"], key=sortkey):
        body = src["rancher_manager"][ver]
        parts = ver.split(".")
        entry = {
            "version": ver,
            "lifecycle": lifecycle(ver),
            "lifecycle_as_of": AS_OF,
            "platforms": [],
        }
        for p in body["supported_platforms"]:
            name = PLATFORM.get(p["platform"])
            if name is None:
                # Never silently drop: an unmodelled distro is a decision, not a no-op.
                sys.exit("unmapped platform {!r} on rancher {}".format(p["platform"], ver))
            mn, mx = p["min_version"], p["max_version"]
            for field, value in (("min_version", mn), ("max_version", mx)):
                key = (name, field, value)
                if key in FIXES:
                    if field == "min_version":
                        mn = FIXES[key]
                    else:
                        mx = FIXES[key]
                    fixed += 1
                    print("  fixed rancher {} {} {}: {!r} -> {!r}".format(
                        ver, name, field, value, FIXES[key]))
            entry["platforms"].append({
                "platform": name,
                "min_version": mn,
                "max_version": mx,
                # See docstring point 2: the source data is ranges only.
                "granularity": "minor",
                "source_url": MATRIX.format(parts[0], parts[1], parts[2]),
                "as_of": AS_OF,
            })
        out["rancher"].append(entry)

    with open("data/catalog.json", "w") as fh:
        json.dump(out, fh, indent=2)
        fh.write("\n")

    print("migrated {} rancher versions, applied {} corrections".format(len(out["rancher"]), fixed))
    if fixed != 7:
        sys.exit("expected 7 corrections (the EKS v1.20.x entries), applied {}".format(fixed))


if __name__ == "__main__":
    main()
