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
// This is how the false-green gets tested without a cluster. The old gate polled
// .status.health.status immediately after patching targetRevision, so healthy OLD
// pods satisfied it and the job went green having deployed nothing. That scenario
// is scenario one below, and it must FAIL.

type deployScenario struct {
	revision  string
	syncState string
	health    string
	phase     string
	probeBody string
	probeFail bool
}

// stubEnv builds a PATH containing fake kubectl and curl that answer from the
// scenario, so the script's real logic runs against controlled inputs.
func stubEnv(t *testing.T, s deployScenario) []string {
	t.Helper()
	dir := t.TempDir()

	kubectl := fmt.Sprintf(`#!/usr/bin/env bash
# Answer -o jsonpath={...} queries from the scenario.
for arg in "$@"; do
  case "$arg" in
    *status.sync.revision*)          echo -n %q; exit 0 ;;
    *status.sync.status*)            echo -n %q; exit 0 ;;
    *status.health.status*)          echo -n %q; exit 0 ;;
    *status.operationState.phase*)   echo -n %q; exit 0 ;;
  esac
done
exit 0
`, s.revision, s.syncState, s.health, s.phase)

	curlScript := "#!/usr/bin/env bash\n"
	if s.probeFail {
		curlScript += "exit 22\n"
	} else {
		curlScript += fmt.Sprintf("cat <<'BODY'\n%s\nBODY\n", s.probeBody)
	}

	for name, body := range map[string]string{"kubectl": kubectl, "curl": curlScript} {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(body), 0o755); err != nil {
			t.Fatalf("write stub %s: %v", name, err)
		}
	}

	return append(os.Environ(),
		"PATH="+dir+string(os.PathListSeparator)+os.Getenv("PATH"),
		"MAX_TRIES=2", "SLEEP_TIME=0")
}

func runVerify(t *testing.T, s deployScenario, probeURL string) (string, error) {
	t.Helper()
	cmd := exec.Command("bash", "scripts/verify-deploy.sh", "prd", "v42")
	cmd.Env = stubEnv(t, s)
	if probeURL != "" {
		cmd.Env = append(cmd.Env, "PROBE_URL="+probeURL)
	}
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// THE REGRESSION. Old revision, but the old pods are healthy and ArgoCD reports
// Synced against the previous revision. The old gate passed here.
func TestDeployVerify_RejectsHealthyOldRevision(t *testing.T) {
	out, err := runVerify(t, deployScenario{
		revision: "v41", syncState: "Synced", health: "Healthy",
	}, "")
	if err == nil {
		t.Fatalf("gate PASSED while the running revision was v41, not v42.\n"+
			"This is the false-green the old gate produced.\n%s", out)
	}
	if !strings.Contains(out, "expected v42") {
		t.Errorf("failure message does not show the revision mismatch:\n%s", out)
	}
}

// Right revision but explicitly OutOfSync. Must still fail: that is a real
// disagreement between desired and live state, not the OCI diff limitation.
func TestDeployVerify_RejectsRightRevisionStillSyncing(t *testing.T) {
	out, err := runVerify(t, deployScenario{
		revision: "v42", syncState: "OutOfSync", health: "Healthy",
	}, "")
	if err == nil {
		t.Fatalf("gate passed while sync status was OutOfSync:\n%s", out)
	}
}

// OCI Helm sources make sync status PERMANENTLY Unknown, because ArgoCD cannot
// diff a chart it cannot re-fetch at the deployed digest. The chart now comes from
// Harbor over OCI, so requiring Synced would time out on every deploy.
//
// Accepting Unknown does not reopen the false-green: revision equality is the
// check that closes it, and it still applies.
func TestDeployVerify_AcceptsUnknownSyncForOCISources(t *testing.T) {
	out, err := runVerify(t, deployScenario{
		revision: "v42", syncState: "Unknown", health: "Healthy", phase: "Succeeded",
	}, "")
	if err != nil {
		t.Fatalf("gate rejected the normal OCI state (correct revision, Healthy, "+
			"Unknown sync). It would time out on every deploy: %v\n%s", err, out)
	}
	if !strings.Contains(out, "OCI") {
		t.Errorf("output does not explain why Unknown was accepted:\n%s", out)
	}
}

// Unknown sync is only acceptable BECAUSE the revision proves which chart is live.
// A stale revision must still fail even when sync is Unknown.
func TestDeployVerify_RejectsStaleRevisionEvenWhenSyncIsUnknown(t *testing.T) {
	out, err := runVerify(t, deployScenario{
		revision: "v41", syncState: "Unknown", health: "Healthy", phase: "Succeeded",
	}, "")
	if err == nil {
		t.Fatalf("gate passed on a STALE revision just because sync was Unknown. "+
			"That is the false-green with extra steps:\n%s", out)
	}
}

// If ArgoCD reports no revision at all, the load-bearing check is gone and only
// health remains, which is exactly the signal this gate exists to distrust.
func TestDeployVerify_RejectsMissingRevisionRatherThanTrustingHealth(t *testing.T) {
	out, err := runVerify(t, deployScenario{
		revision: "", syncState: "Unknown", health: "Healthy", phase: "Succeeded",
	}, "")
	if err == nil {
		t.Fatalf("gate passed with no sync.revision reported; nothing established "+
			"WHICH chart was live:\n%s", out)
	}
}

func TestDeployVerify_RejectsDegradedHealth(t *testing.T) {
	out, err := runVerify(t, deployScenario{
		revision: "v42", syncState: "Synced", health: "Degraded",
	}, "")
	if err == nil {
		t.Fatalf("gate passed while health was Degraded:\n%s", out)
	}
}

func TestDeployVerify_RejectsFailedSyncOperation(t *testing.T) {
	out, err := runVerify(t, deployScenario{
		revision: "v42", syncState: "Synced", health: "Healthy", phase: "Failed",
	}, "")
	if err == nil {
		t.Fatalf("gate passed while the last sync operation reported Failed:\n%s", out)
	}
	if !strings.Contains(out, "Failed") {
		t.Errorf("failure message does not name the operation phase:\n%s", out)
	}
}

// Revision live and healthy, but no probe configured. Passes, and says plainly that
// nothing confirmed the service actually answers.
func TestDeployVerify_PassesWithoutProbeButSaysSo(t *testing.T) {
	out, err := runVerify(t, deployScenario{
		revision: "v42", syncState: "Synced", health: "Healthy", phase: "Succeeded",
	}, "")
	if err != nil {
		t.Fatalf("gate failed on a correctly deployed revision: %v\n%s", err, out)
	}
	if !strings.Contains(out, "NO KNOWN-ANSWER PROBE") {
		t.Errorf("passed silently without noting the probe was skipped:\n%s", out)
	}
}

// The service is up and answering, but returns zero destinations. That is the
// failure this whole rebuild exists to remove: it reads to a user as
// "you are already current".
func TestDeployVerify_RejectsProbeReturningZeroDestinations(t *testing.T) {
	out, err := runVerify(t, deployScenario{
		revision: "v42", syncState: "Synced", health: "Healthy", phase: "Succeeded",
		probeBody: `{"destinations": [], "blockers": []}`,
	}, "http://example.invalid/api/plan-upgrade")
	if err == nil {
		t.Fatalf("gate passed while the API returned zero destinations for a query "+
			"that must always plan:\n%s", out)
	}
	if !strings.Contains(out, "ZERO destinations") {
		t.Errorf("failure message does not explain the problem:\n%s", out)
	}
}

func TestDeployVerify_RejectsUnreachableProbe(t *testing.T) {
	out, err := runVerify(t, deployScenario{
		revision: "v42", syncState: "Synced", health: "Healthy", phase: "Succeeded",
		probeFail: true,
	}, "http://example.invalid/api/plan-upgrade")
	if err == nil {
		t.Fatalf("gate passed while the probe request failed:\n%s", out)
	}
}

// The whole gate satisfied.
func TestDeployVerify_PassesWhenRevisionLiveAndAnswering(t *testing.T) {
	out, err := runVerify(t, deployScenario{
		revision: "v42", syncState: "Synced", health: "Healthy", phase: "Succeeded",
		probeBody: `{"destinations": [{"rancher":"2.15.1","steps":[]}], "blockers": []}`,
	}, "http://example.invalid/api/plan-upgrade")
	if err != nil {
		t.Fatalf("gate failed on a fully verified deploy: %v\n%s", err, out)
	}
	if !strings.Contains(out, "VERIFIED") {
		t.Errorf("success output does not confirm verification:\n%s", out)
	}
}

// THE ORDERING REGRESSION, proven behaviourally rather than structurally.
//
// The gate must not open its port-forward until the revision it is waiting for is
// actually live. When the workflow opened the tunnel first, the rollout deleted the
// pod underneath it and the probe failed on a deploy that had fully succeeded
// (mst, v216 -> v220). A stub kubectl records the order of operations, and the
// port-forward must not appear before the revision flips.
func TestDeployVerify_OpensPortForwardOnlyAfterRevisionFlips(t *testing.T) {
	dir := t.TempDir()
	order := filepath.Join(dir, "order.log")
	counter := filepath.Join(dir, "polls")

	// Reports the stale revision for the first two polls, then the expected one.
	// Logs every revision answer and any port-forward invocation.
	kubectl := fmt.Sprintf(`#!/usr/bin/env bash
for arg in "$@"; do
  if [ "$arg" = "port-forward" ]; then
    echo "port-forward" >> %[1]q
    exit 0
  fi
done
for arg in "$@"; do
  case "$arg" in
    *status.sync.revision*)
      n=0; [ -f %[2]q ] && n=$(cat %[2]q)
      n=$((n + 1)); echo "$n" > %[2]q
      if [ "$n" -le 2 ]; then rev="v216"; else rev="v220"; fi
      echo "revision:$rev" >> %[1]q
      echo -n "$rev"; exit 0 ;;
    *status.sync.status*)          echo -n "Unknown"; exit 0 ;;
    *status.health.status*)        echo -n "Healthy"; exit 0 ;;
    *status.operationState.phase*) echo -n "Succeeded"; exit 0 ;;
  esac
done
exit 0
`, order, counter)

	// healthz succeeds only once a port-forward has been recorded; the probe returns
	// a plannable itinerary. This makes a premature tunnel observably useless.
	curlStub := fmt.Sprintf(`#!/usr/bin/env bash
target="${!#}"
case "$target" in
  *healthz*)
    grep -q "port-forward" %[1]q 2>/dev/null || exit 7
    echo "ok"; exit 0 ;;
  *plan-upgrade*)
    echo "curl-probe" >> %[1]q
    echo '{"destinations":[{"rancher":"2.15.1"}],"blockers":[]}'; exit 0 ;;
esac
exit 0
`, order)

	for name, body := range map[string]string{"kubectl": kubectl, "curl": curlStub} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o755); err != nil {
			t.Fatalf("write stub %s: %v", name, err)
		}
	}

	cmd := exec.Command("bash", "scripts/verify-deploy.sh", "mst", "v220")
	cmd.Env = append(os.Environ(),
		"PATH="+dir+string(os.PathListSeparator)+os.Getenv("PATH"),
		"MAX_TRIES=10", "SLEEP_TIME=0",
		"PROBE_SERVICE=svc/website",
		"PROBE_NAMESPACE=rancherupgrade-mst",
		"PROBE_LOCAL_PORT=18080",
		"PROBE_REMOTE_PORT=3000",
		"PROBE_PATH=/api/plan-upgrade?rancher=2.9.6",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("gate failed on a deploy that succeeded: %v\n%s", err, out)
	}

	logged, readErr := os.ReadFile(order)
	if readErr != nil {
		t.Fatalf("read order log: %v", readErr)
	}
	events := strings.Split(strings.TrimSpace(string(logged)), "\n")

	pfIdx, probeIdx, lastStale := -1, -1, -1
	for i, e := range events {
		switch e {
		case "port-forward":
			if pfIdx == -1 {
				pfIdx = i
			}
		case "curl-probe":
			probeIdx = i
		case "revision:v216":
			lastStale = i
		}
	}
	if pfIdx == -1 {
		t.Fatalf("no port-forward was ever opened; the probe cannot have been reachable\n%s", out)
	}
	if pfIdx < lastStale {
		t.Errorf("port-forward opened at step %d, before the revision stopped reporting "+
			"stale (step %d). The rollout will delete the pod the tunnel is bound to.\nevents: %v",
			pfIdx, lastStale, events)
	}
	if probeIdx == -1 || probeIdx < pfIdx {
		t.Errorf("probe did not run after the port-forward (probe=%d, forward=%d)\nevents: %v",
			probeIdx, pfIdx, events)
	}
}
