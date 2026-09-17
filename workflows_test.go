package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// Merging to main deploys to production. These tests make the deploy-safety rules
// enforceable instead of comments someone deletes in a hurry.

type workflow struct {
	On   map[string]any `yaml:"on"`
	Jobs map[string]struct {
		If    string `yaml:"if"`
		Needs any    `yaml:"needs"`
	} `yaml:"jobs"`
}

func loadWorkflows(t *testing.T) map[string]workflow {
	t.Helper()
	paths, err := filepath.Glob(".github/workflows/*.yml")
	if err != nil || len(paths) == 0 {
		t.Fatalf("no workflows found: %v", err)
	}
	out := map[string]workflow{}
	for _, p := range paths {
		raw, err := os.ReadFile(p)
		if err != nil {
			t.Fatalf("read %s: %v", p, err)
		}
		var w workflow
		if err := yaml.Unmarshal(raw, &w); err != nil {
			t.Fatalf("parse %s: %v", p, err)
		}
		out[filepath.Base(p)] = w
	}
	return out
}

// REGRESSION GUARD. pipeline.yml chains Test -> Build -> Publish -> Deploy through
// needs: with no if: branch guard on any job. The ONLY thing keeping publication
// and deployment off pull requests is the absence of a pull_request trigger.
//
// Adding one would build an image, publish a chart to charts.support.tools, patch
// six ArgoCD applications and roll unmerged code to rancher.tips. PR validation
// lives in validate.yml, which holds no secrets and therefore also works for forks.
func TestPipelineNeverTriggersOnPullRequest(t *testing.T) {
	w, ok := loadWorkflows(t)["pipeline.yml"]
	if !ok {
		t.Fatal("pipeline.yml is missing")
	}
	if _, bad := w.On["pull_request"]; bad {
		t.Error("pipeline.yml has a pull_request trigger. Build, Publish and Deploy " +
			"have no branch guard, so opening a PR would deploy unmerged code to all " +
			"six environments including production. Put PR checks in validate.yml.")
	}
	if _, bad := w.On["pull_request_target"]; bad {
		t.Error("pipeline.yml has a pull_request_target trigger, which is worse: it runs " +
			"with repository secrets available to PR-controlled code.")
	}
}

// The nightly cron redeployed every environment including production each midnight
// with no code change, against an unpinned base image, and nobody watching. It is
// also incompatible with fail-closed catalog validation, which would turn one bad
// dataset into an unattended 00:00 outage.
func TestPipelineHasNoScheduledDeploy(t *testing.T) {
	w := loadWorkflows(t)["pipeline.yml"]
	if _, bad := w.On["schedule"]; bad {
		t.Error("pipeline.yml has a schedule trigger. This workflow deploys to " +
			"production; a scheduled run means production changes with no human " +
			"involved. A scheduled job that opens a PR is fine, one that deploys is not.")
	}
}

// Only pipeline.yml may reach the publish and deploy jobs. Any other workflow
// growing them would reintroduce a second uncontrolled path to production.
func TestOnlyThePipelineCanDeploy(t *testing.T) {
	for name, w := range loadWorkflows(t) {
		if name == "pipeline.yml" {
			continue
		}
		for job := range w.Jobs {
			switch job {
			case "Deploy", "Publish", "Build":
				t.Errorf("%s defines a %q job. Publication and deployment belong to "+
					"pipeline.yml alone, which is the workflow whose triggers are "+
					"audited by these tests.", name, job)
			}
		}
	}
}

// Validation must be reachable from a pull request, or nothing gates a merge.
func TestValidateRunsOnPullRequests(t *testing.T) {
	w, ok := loadWorkflows(t)["validate.yml"]
	if !ok {
		t.Fatal("validate.yml is missing; PRs would have no checks at all")
	}
	if _, good := w.On["pull_request"]; !good {
		t.Error("validate.yml does not trigger on pull_request, so nothing validates a PR")
	}
}

// The data refresh must open a PR, never push to main, because main deploys.
func TestDataSyncDoesNotTriggerOnPush(t *testing.T) {
	w, ok := loadWorkflows(t)["data-sync.yml"]
	if !ok {
		t.Skip("data-sync.yml not present")
	}
	if _, bad := w.On["push"]; bad {
		t.Error("data-sync.yml triggers on push. It regenerates upgrade advice and " +
			"must put a human between the generator and production.")
	}
}

// The image must ship only the live catalog. data/upgrade-paths.json is the
// superseded dataset, kept in the repo because 63 of its 70 entries cannot be
// re-derived from upstream, but a second compatibility dataset sitting beside the
// live one in the container invites loading the wrong file.
func TestDockerfileShipsOnlyTheLiveCatalog(t *testing.T) {
	raw, err := os.ReadFile("Dockerfile")
	if err != nil {
		t.Fatalf("read Dockerfile: %v", err)
	}
	body := string(raw)

	if strings.Contains(body, "COPY --from=builder /app/data ./data") {
		t.Error("Dockerfile copies the whole data/ directory, which ships the " +
			"superseded upgrade-paths.json alongside the live catalog. Copy " +
			"data/catalog.json specifically.")
	}
	if !strings.Contains(body, "/app/data/catalog.json") {
		t.Error("Dockerfile does not copy data/catalog.json; the service reads it at " +
			"startup and fails closed without it")
	}
}

// The superseded dataset must stay in the repo. Most of it cannot be recovered.
func TestSupersededDatasetIsRetained(t *testing.T) {
	if _, err := os.Stat("data/upgrade-paths.json"); err != nil {
		t.Fatalf("data/upgrade-paths.json is gone: %v\n"+
			"63 of its 70 entries are NOT re-derivable from upstream: endoflife.date "+
			"publishes only the latest patch per cycle, and SUSE retires old "+
			"per-version matrix pages. Supported ranges vary between patches within a "+
			"minor (the 2.5 line has 13 signatures across 17 patches), and this file is "+
			"the only record of that. See data/README.md.", err)
	}
	if _, err := os.Stat("data/README.md"); err != nil {
		t.Error("data/README.md is missing; without it the retained file looks like " +
			"clutter and someone will delete it")
	}
}

// The deploy gate port-forwards to the service for the five environments that are
// not publicly routable. If the name or port drifts from what the chart renders,
// the gate fails on the FIRST environment in the matrix and blocks the entire
// deploy, turning a routine release into an incident.
//
// Caught exactly that before a merge: the gate referenced
// svc/rancher-upgrade-tool on port 80 while the chart renders `name: website` on
// port 3000.
func TestDeployGateTargetsTheServiceTheChartRenders(t *testing.T) {
	svc, err := os.ReadFile("charts/rancher-upgrade-tool/templates/service.yaml")
	if err != nil {
		t.Fatalf("read service template: %v", err)
	}
	var rendered struct {
		Metadata struct{ Name string } `yaml:"metadata"`
		Spec     struct {
			Ports []struct {
				Port int    `yaml:"port"`
				Name string `yaml:"name"`
			} `yaml:"ports"`
		} `yaml:"spec"`
	}
	if err := yaml.Unmarshal(svc, &rendered); err != nil {
		t.Fatalf("parse service template: %v", err)
	}
	if rendered.Metadata.Name == "" {
		t.Fatal("service template has no metadata.name")
	}

	var httpPort int
	for _, p := range rendered.Spec.Ports {
		if p.Name == "http" {
			httpPort = p.Port
		}
	}
	if httpPort == 0 {
		t.Fatal("service template declares no port named http")
	}

	pipeline, err := os.ReadFile(".github/workflows/pipeline.yml")
	if err != nil {
		t.Fatalf("read pipeline: %v", err)
	}
	body := string(pipeline)

	wantRef := "svc/" + rendered.Metadata.Name
	if !strings.Contains(body, wantRef) {
		t.Errorf("deploy gate does not port-forward to %q, which is what the chart "+
			"renders. A wrong service name fails the gate on the first environment and "+
			"blocks the whole matrix.", wantRef)
	}
	wantPort := fmt.Sprintf("18080:%d", httpPort)
	if !strings.Contains(body, wantPort) {
		t.Errorf("deploy gate does not forward to %q; the chart's http port is %d",
			wantPort, httpPort)
	}
}

// Validate must predict the merge. If pipeline.yml verifies anything that
// validate.yml does not, a green PR stops meaning a green merge.
//
// That exact drift shipped: `make ci` omitted gosec while pipeline.yml ran it as a
// separate step, so PR #6 went green and the merge failed on 9 gosec findings with
// Build, Publish and Deploy skipped.
//
// Checks the parsed `run:` shell of every step, NOT the raw file: the first version
// of this test matched a tool name inside a COMMENT and reported a violation that
// did not exist.
func TestBothWorkflowsRunTheSameGate(t *testing.T) {
	type stepped struct {
		Jobs map[string]struct {
			Steps []struct {
				Name string `yaml:"name"`
				Run  string `yaml:"run"`
			} `yaml:"steps"`
		} `yaml:"jobs"`
	}

	runScripts := func(name string) []string {
		raw, err := os.ReadFile(filepath.Join(".github/workflows", name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		var w stepped
		if err := yaml.Unmarshal(raw, &w); err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		var out []string
		for _, job := range w.Jobs {
			for _, st := range job.Steps {
				if strings.TrimSpace(st.Run) != "" {
					out = append(out, st.Run)
				}
			}
		}
		return out
	}

	// Every workflow that verifies the code must go through `make ci`.
	verifiers := []string{"pipeline.yml", "validate.yml", "data-sync.yml"}
	for _, wf := range verifiers {
		scripts := runScripts(wf)
		joined := strings.Join(scripts, "\n")

		if !strings.Contains(joined, "make ci") {
			t.Errorf("%s does not run `make ci`; the gates cannot be equivalent", wf)
		}

		// No verification tool invoked directly. It belongs in `make ci`, where
		// developers and every other workflow get it too.
		for _, tool := range []string{"gosec ./...", "staticcheck ./...", "go vet ./...", "go test ./..."} {
			if strings.Contains(joined, tool) {
				t.Errorf("%s invokes %q directly in a run: block. Put it in `make ci`, "+
					"otherwise a green PR stops predicting a green merge.", wf, tool)
			}
		}
	}

	// `make ci` must actually carry the full gate.
	mk, err := os.ReadFile("makefile")
	if err != nil {
		t.Fatalf("read makefile: %v", err)
	}
	var ciLine string
	for _, l := range strings.Split(string(mk), "\n") {
		if strings.HasPrefix(l, "ci:") {
			ciLine = l
		}
	}
	if ciLine == "" {
		t.Fatal("makefile has no `ci:` target")
	}
	for _, prereq := range []string{"fmt-check", "vet", "test", "scan"} {
		if !strings.Contains(ciLine, prereq) {
			t.Errorf("`ci` target is missing %q: %q", prereq, ciLine)
		}
	}
}

// The chart moved from the git-backed museum at charts.support.tools to OCI on
// harbor.support.tools. These guard the move, because half-migrating is worse than
// either end state: the old path needed BOT_TOKEN, which had expired and blocked
// deploys entirely.
func TestChartPublishesToHarborOverOCI(t *testing.T) {
	// Parse the workflow and inspect only `run:` shell and `uses:` actions.
	// Matching the raw file catches explanatory COMMENTS: the first version of this
	// test failed on the comment that says to use docker/login-action INSTEAD of
	// `helm registry login`. Same mistake as TestBothWorkflowsRunTheSameGate made.
	raw, err := os.ReadFile(".github/workflows/pipeline.yml")
	if err != nil {
		t.Fatalf("read pipeline: %v", err)
	}
	var wf struct {
		Jobs map[string]struct {
			Steps []struct {
				Run  string         `yaml:"run"`
				Uses string         `yaml:"uses"`
				With map[string]any `yaml:"with"`
			} `yaml:"steps"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(raw, &wf); err != nil {
		t.Fatalf("parse pipeline: %v", err)
	}

	var shell, actions []string
	withValues := map[string]string{}
	for _, job := range wf.Jobs {
		for _, st := range job.Steps {
			if strings.TrimSpace(st.Run) != "" {
				shell = append(shell, st.Run)
			}
			if st.Uses != "" {
				actions = append(actions, st.Uses)
			}
			for k, v := range st.With {
				withValues[k] = fmt.Sprint(v)
			}
		}
	}
	allShell := strings.Join(shell, "\n")
	allActions := strings.Join(actions, "\n")
	allWith := fmt.Sprint(withValues)

	// The credential that blocked every deploy must be gone, not merely unused.
	if strings.Contains(allWith, "BOT_TOKEN") || strings.Contains(allShell, "BOT_TOKEN") {
		t.Error("pipeline.yml still uses BOT_TOKEN. The git-backed chart push is replaced " +
			"by an OCI push; that secret had expired and blocked every deploy.")
	}
	if strings.Contains(allWith, "supporttools/helm-chart") {
		t.Error("pipeline.yml still checks out the chart museum repository")
	}
	if !strings.Contains(allShell, "oci://harbor.support.tools/rancher-upgrade-tool/charts") {
		t.Error("pipeline.yml does not push the chart to the Harbor OCI path")
	}

	// `helm registry login` fails against Harbor even with correct credentials:
	// Harbor's token service satisfies Docker's auth handshake but rejects Helm's
	// basic-auth probe against /v2/. helm push reads ~/.docker/config.json instead.
	if strings.Contains(allShell, "helm registry login") {
		t.Error("a run: block uses `helm registry login`, which fails against Harbor. " +
			"Use docker/login-action; helm push reads the docker config.")
	}
	if !strings.Contains(allActions, "docker/login-action") {
		t.Error("pipeline.yml does not use docker/login-action to authenticate to Harbor")
	}
}

// OCI registries require a valid SemVer2 chart version. The previous scheme
// produced "v219", which has no minor or patch and helm push rejects.
func TestChartVersionIsValidSemver(t *testing.T) {
	pipeline, err := os.ReadFile(".github/workflows/pipeline.yml")
	if err != nil {
		t.Fatalf("read pipeline: %v", err)
	}
	body := string(pipeline)

	// Substitute a run number the way Actions would, then check the shape.
	re := regexp.MustCompile(`CHART_VERSION="([^"]+)"`)
	m := re.FindStringSubmatch(body)
	if m == nil {
		t.Fatal("could not find CHART_VERSION in pipeline.yml")
	}
	concrete := strings.ReplaceAll(m[1], "${{ github.run_number }}", "219")

	semver := regexp.MustCompile(`^v?\d+\.\d+\.\d+$`)
	if !semver.MatchString(concrete) {
		t.Errorf("CHART_VERSION resolves to %q, which is not SemVer2. OCI registries "+
			"reject it: helm push needs major.minor.patch (a leading v is fine).", concrete)
	}
}

// Every environment must pull the chart from Harbor. A single app left pointing at
// the dead museum would fail to sync while the others succeeded.
func TestAllArgoAppsPullFromHarbor(t *testing.T) {
	apps, err := filepath.Glob("argocd/*.yaml")
	if err != nil || len(apps) == 0 {
		t.Fatalf("no argocd manifests found: %v", err)
	}
	found := 0
	for _, p := range apps {
		raw, err := os.ReadFile(p)
		if err != nil {
			t.Fatalf("read %s: %v", p, err)
		}
		var app struct {
			Kind string `yaml:"kind"`
			Spec struct {
				Source struct {
					RepoURL string `yaml:"repoURL"`
					Chart   string `yaml:"chart"`
				} `yaml:"source"`
			} `yaml:"spec"`
		}
		if err := yaml.Unmarshal(raw, &app); err != nil {
			t.Fatalf("parse %s: %v", p, err)
		}
		if app.Kind != "Application" {
			continue
		}
		found++
		want := "oci://harbor.support.tools/rancher-upgrade-tool/charts"
		if app.Spec.Source.RepoURL != want {
			t.Errorf("%s repoURL = %q, want %q", filepath.Base(p), app.Spec.Source.RepoURL, want)
		}
		if app.Spec.Source.Chart != "rancher-upgrade-tool" {
			t.Errorf("%s chart = %q", filepath.Base(p), app.Spec.Source.Chart)
		}
	}
	if found != 6 {
		t.Errorf("checked %d Applications, expected 6 (mst, dev, qas, tst, stg, prd)", found)
	}
}
