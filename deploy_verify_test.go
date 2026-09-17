package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Drives scripts/verify-deploy.sh with a stub kubectl and curl.
//
// This is how the false greens get tested without a cluster. Three of them have
// shipped, and each one passed a gate while nothing had been deployed:
//
//  1. health alone -- old pods are Healthy too
//  2. .status.sync.revision echoes the REQUESTED revision, so the gate matched a
//     string it had itself asked for while image v216 kept serving traffic
//  3. spec.source.repoURL was never migrated, so the deploy kept succeeding
//     against the old chart museum after the repo said Harbor
//
// Each has a scenario below and each must FAIL.

type deployScenario struct {
	repoURL        string
	targetRevision string
	syncStatus     string
	health         string
	phase          string
	syncResult     string
	liveVersion    string // what /version reports; empty = endpoint absent
	probeBody      string
	probeFail      bool
}

// good is a fully correct deploy: Harbor source, matching chart version, ArgoCD
// content, the expected build actually answering, and a plannable itinerary.
func good() deployScenario {
	return deployScenario{
		repoURL:        "oci://harbor.support.tools/rancher-upgrade-tool/charts",
		targetRevision: "v0.221.0",
		syncStatus:     "Synced",
		health:         "Healthy",
		phase:          "Succeeded",
		syncResult:     "sha256:abc123",
		liveVersion:    "v221",
		probeBody:      `{"destinations":[{"rancher":"2.15.1"}],"blockers":[]}`,
	}
}

func stubEnv(t *testing.T, s deployScenario) []string {
	t.Helper()
	dir := t.TempDir()

	kubectl := fmt.Sprintf(`#!/usr/bin/env bash
for arg in "$@"; do
  case "$arg" in
    *spec.source.repoURL*)                       echo -n %q; exit 0 ;;
    *spec.source.targetRevision*)                echo -n %q; exit 0 ;;
    *status.sync.status*)                        echo -n %q; exit 0 ;;
    *status.health.status*)                      echo -n %q; exit 0 ;;
    *status.operationState.syncResult.revision*) echo -n %q; exit 0 ;;
    *status.operationState.phase*)               echo -n %q; exit 0 ;;
    *status.sync.revision*)                      echo -n %q; exit 0 ;;
  esac
done
exit 0
`, s.repoURL, s.targetRevision, s.syncStatus, s.health, s.syncResult, s.phase, s.targetRevision)

	// Distinguishes the /version read from the known-answer probe.
	versionBody := ""
	if s.liveVersion != "" {
		versionBody = fmt.Sprintf(`{"version":"%s","catalog_generated":"2026-09-17"}`, s.liveVersion)
	}
	probeCase := fmt.Sprintf("cat <<'BODY'\n%s\nBODY\n    exit 0", s.probeBody)
	if s.probeFail {
		probeCase = "exit 22"
	}
	versionCase := "exit 22"
	if versionBody != "" {
		versionCase = fmt.Sprintf("cat <<'BODY'\n%s\nBODY\n    exit 0", versionBody)
	}

	curlStub := fmt.Sprintf(`#!/usr/bin/env bash
target="${!#}"
case "$target" in
  */version)
    %s
    ;;
  *plan-upgrade*)
    %s
    ;;
esac
exit 0
`, versionCase, probeCase)

	for name, body := range map[string]string{"kubectl": kubectl, "curl": curlStub} {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(body), 0o755); err != nil {
			t.Fatalf("write stub %s: %v", name, err)
		}
	}

	return append(os.Environ(),
		"PATH="+dir+string(os.PathListSeparator)+os.Getenv("PATH"),
		"MAX_TRIES=2", "SLEEP_TIME=0",
		"PROBE_TRIES=2", "PROBE_SLEEP=0",
		"PROBE_HOST=https://mst.rancher.tips",
		"PROBE_PATH=/api/plan-upgrade?rancher=2.9.6",
		"EXPECTED_CHART_VERSION=v0.221.0",
	)
}

func runVerify(t *testing.T, s deployScenario, extraEnv ...string) (string, error) {
	t.Helper()
	cmd := exec.Command("bash", "scripts/verify-deploy.sh", "mst", "v221")
	cmd.Env = append(stubEnv(t, s), extraEnv...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func mustFail(t *testing.T, out string, err error, wantSubstr string) {
	t.Helper()
	if err == nil {
		t.Fatalf("gate PASSED but should have failed\n%s", out)
	}
	if wantSubstr != "" && !strings.Contains(out, wantSubstr) {
		t.Errorf("failure message does not mention %q; a gate that fails for an\n"+
			"unexplained reason costs the next person the same investigation\n%s",
			wantSubstr, out)
	}
}

func TestDeployVerify_PassesOnAFullyCorrectDeploy(t *testing.T) {
	out, err := runVerify(t, good())
	if err != nil {
		t.Fatalf("gate failed on a correct deploy: %v\n%s", err, out)
	}
	for _, want := range []string{"live version: v221", "plannable itinerary", "VERIFIED"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q\n%s", want, out)
		}
	}
}

// FALSE GREEN 3. The repoURL migration never reached the live Application, so the
// deploy kept succeeding against the old chart museum. mst did this for two full
// pipeline runs while the repo said Harbor, and nothing reported it.
func TestDeployVerify_RejectsUnmigratedSource(t *testing.T) {
	s := good()
	s.repoURL = "https://charts.support.tools"
	out, err := runVerify(t, s)
	mustFail(t, out, err, "charts.support.tools")
}

// FALSE GREEN 2. Publish pushed v0.221.0; Deploy asked for v221. ArgoCD cannot
// sync a chart that was never published, keeps the old release running, and still
// reports the requested revision back.
func TestDeployVerify_RejectsTargetRevisionThatWasNeverPublished(t *testing.T) {
	s := good()
	s.targetRevision = "v221" // the app version, not the SemVer2 chart version
	out, err := runVerify(t, s)
	mustFail(t, out, err, "never pushed")
}

// FALSE GREEN 1, in its strongest form. ArgoCD is entirely content -- Synced,
// Healthy, Succeeded, correct targetRevision, correct source -- and the code
// answering requests is still the previous build.
func TestDeployVerify_RejectsOldCodeStillServingWhileArgoIsContent(t *testing.T) {
	s := good()
	s.liveVersion = "v216"
	out, err := runVerify(t, s)
	mustFail(t, out, err, "not the code this pipeline built")
}

// The running image predates /version, which itself proves the new build is not
// live. Must not be mistaken for an infrastructure hiccup and waited out.
func TestDeployVerify_RejectsMissingVersionEndpoint(t *testing.T) {
	s := good()
	s.liveVersion = ""
	out, err := runVerify(t, s)
	mustFail(t, out, err, "does not report a version")
}

// Unknown is NOT benign. A previous version of this script treated it as
// expected-and-permanent for OCI sources and passed on it. On this cluster every
// other OCI-sourced Application reports Synced, and mst reported Unknown
// precisely BECAUSE it was asked for a chart version that did not exist. Treating
// it as expected is what let that bug survive a full run.
func TestDeployVerify_RejectsUnknownSyncStatus(t *testing.T) {
	s := good()
	s.syncStatus = "Unknown"
	out, err := runVerify(t, s)
	mustFail(t, out, err, "timed out")
}

func TestDeployVerify_RejectsDegradedHealth(t *testing.T) {
	s := good()
	s.health = "Degraded"
	out, err := runVerify(t, s)
	mustFail(t, out, err, "timed out")
}

func TestDeployVerify_RejectsFailedSyncOperation(t *testing.T) {
	s := good()
	s.phase = "Failed"
	out, err := runVerify(t, s)
	mustFail(t, out, err, "Failed")
}

func TestDeployVerify_RejectsMissingRepoURL(t *testing.T) {
	s := good()
	s.repoURL = ""
	out, err := runVerify(t, s)
	mustFail(t, out, err, "no spec.source.repoURL")
}

// The right build, answering, but answering WRONG. An empty destination list is
// the failure the whole rebuild exists to remove: it reads to a user as "you are
// already current".
func TestDeployVerify_RejectsProbeReturningZeroDestinations(t *testing.T) {
	s := good()
	s.probeBody = `{"destinations": [],"blockers":[]}`
	out, err := runVerify(t, s)
	mustFail(t, out, err, "ZERO destinations")
}

func TestDeployVerify_RejectsProbeMissingTheDestinationsField(t *testing.T) {
	s := good()
	s.probeBody = `{"upgrade_path":null}`
	out, err := runVerify(t, s)
	mustFail(t, out, err, "no destinations field")
}

func TestDeployVerify_RejectsUnreachableProbe(t *testing.T) {
	s := good()
	s.probeFail = true
	out, err := runVerify(t, s)
	mustFail(t, out, err, "probe request failed")
}

// Version equality alone is not a verified deploy: it proves which build is
// running, not that it works. The script must refuse rather than pass quietly.
func TestDeployVerify_RefusesToPassOnVersionEqualityAlone(t *testing.T) {
	out, err := runVerify(t, good(), "PROBE_PATH=")
	mustFail(t, out, err, "PROBE_PATH is not set")
}

// PROBE_HOST is mandatory. Every environment here is publicly routable, so a
// missing host is a configuration error, not a reason to skip verification.
func TestDeployVerify_RequiresAProbeHost(t *testing.T) {
	cmd := exec.Command("bash", "scripts/verify-deploy.sh", "mst", "v221")
	env := stubEnv(t, good())
	filtered := make([]string, 0, len(env))
	for _, kv := range env {
		if !strings.HasPrefix(kv, "PROBE_HOST=") {
			filtered = append(filtered, kv)
		}
	}
	cmd.Env = filtered
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("gate passed with no PROBE_HOST\n%s", out)
	}
	if !strings.Contains(string(out), "PROBE_HOST") {
		t.Errorf("failure does not name PROBE_HOST\n%s", out)
	}
}
