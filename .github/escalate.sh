#!/usr/bin/env bash
#
# Open, bump or resolve the single staleness tracking issue.
#
# One issue, reused. A new issue per failed run trains people to close them in
# batches without reading, which is the same as not having them.
set -euo pipefail

MARKER="<!-- rancher-tips-staleness-tracker -->"
LABEL="${ESCALATE_LABEL:-data-staleness}"

# The label is created here rather than assumed to exist. Before this, the label
# had never been created on the repo, so `gh issue create --label` refused
# ("could not add label: 'data-staleness' not found") and every failed sync from
# 2026-09-21 on escalated to nobody. --force makes it idempotent: it updates an
# existing label instead of failing. A real failure (permissions) still stops the
# script, because an escalation that cannot be raised must not look like success.
ensure_label() {
  gh label create "$LABEL" --force \
    --color "B60205" \
    --description "Catalog data is stale: the weekly sync failed or its PR is unreviewed" \
    >/dev/null
}

existing=$(gh issue list --state open --label "$LABEL" --json number,title \
  --jq '.[0].number // empty' 2>/dev/null || true)

if [ "${1:-}" = "--resolve" ]; then
  if [ -n "$existing" ]; then
    gh issue comment "$existing" --body "Resolved: the catalog refresh is current again. $MARKER"
    gh issue close "$existing"
    echo "closed staleness issue #$existing"
  else
    echo "nothing to resolve"
  fi
  exit 0
fi

TITLE="${1:?usage: escalate.sh <title> <body> | --resolve}"
BODY="${2:?usage: escalate.sh <title> <body> | --resolve}"

FULL="${BODY}

Why this matters: the tool answers upgrade questions people act on against production clusters. Stale data does not look broken, it looks authoritative. This dataset previously drifted four Rancher releases behind without anything complaining, which is the failure this tracker exists to prevent repeating.

Run: ${GITHUB_SERVER_URL:-https://github.com}/${GITHUB_REPOSITORY:-}/actions/runs/${GITHUB_RUN_ID:-}

${MARKER}"

ensure_label

if [ -n "$existing" ]; then
  gh issue comment "$existing" --body "$FULL"
  echo "bumped staleness issue #$existing"
else
  gh issue create --title "$TITLE" --body "$FULL" --label "$LABEL"
  echo "opened staleness issue"
fi
