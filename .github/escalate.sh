#!/usr/bin/env bash
#
# Open, bump or resolve the single staleness tracking issue.
#
# One issue, reused. A new issue per failed run trains people to close them in
# batches without reading, which is the same as not having them.
set -euo pipefail

MARKER="<!-- rancher-tips-staleness-tracker -->"
LABEL="${ESCALATE_LABEL:-data-staleness}"

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

if [ -n "$existing" ]; then
  gh issue comment "$existing" --body "$FULL"
  echo "bumped staleness issue #$existing"
else
  gh issue create --title "$TITLE" --body "$FULL" --label "$LABEL"
  echo "opened staleness issue"
fi
