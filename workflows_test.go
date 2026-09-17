package main

import (
	"os"
	"path/filepath"
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
