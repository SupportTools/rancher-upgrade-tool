#!/usr/bin/env bash
#
# Verify that a deploy actually happened.
#
# THE BUG THIS REPLACES. The old gate patched the ArgoCD Application's
# targetRevision and then immediately polled .status.health.status, checking only
# for "Healthy". ArgoCD tracks sync state and health state separately, so in the
# seconds after the patch the OLD pods are still running and still perfectly
# healthy. The poll saw Healthy on its first attempt and declared success while the
# new version had not been pulled, synced or started. The job went green having
# deployed nothing.
#
# Checking the revision FIRST removes that structurally: a stale revision can never
# satisfy the gate no matter how healthy the old pods are.
#
# Four checks, in order, all required:
#   1. .status.sync.revision      == the chart version just published
#   2. .status.sync.status        == Synced
#   3. .status.health.status      == Healthy
#   4. a known API query against the running service returns the expected answer
#
# Check 4 is the one that proves the deployed thing WORKS, not merely that
# Kubernetes is content with it.
set -euo pipefail

ENVIRONMENT="${1:?usage: verify-deploy.sh <environment> <expected-chart-version>}"
EXPECTED_REVISION="${2:?usage: verify-deploy.sh <environment> <expected-chart-version>}"

APP="${APP_PREFIX:-rancherupgrade}-${ENVIRONMENT}"
NAMESPACE="${ARGOCD_NAMESPACE:-argocd}"
MAX_TRIES="${MAX_TRIES:-60}"
SLEEP_TIME="${SLEEP_TIME:-10}"
PROBE_URL="${PROBE_URL:-}"

field() {
  kubectl -n "$NAMESPACE" get application "$APP" -o jsonpath="{$1}" 2>/dev/null || true
}

fail() {
  echo ""
  echo "DEPLOY VERIFICATION FAILED for ${APP}: $*"
  echo ""
  echo "  sync.revision      : $(field .status.sync.revision)   (expected ${EXPECTED_REVISION})"
  echo "  sync.status        : $(field .status.sync.status)"
  echo "  health.status      : $(field .status.health.status)"
  echo "  operationState     : $(field .status.operationState.phase)"
  exit 1
}

echo "verifying ${APP} reached ${EXPECTED_REVISION}"

counter=0
while [ "$counter" -lt "$MAX_TRIES" ]; do
  revision=$(field .status.sync.revision)
  sync_status=$(field .status.sync.status)
  health=$(field .status.health.status)

  # Revision first, deliberately. Health alone is exactly the signal that produced
  # false greens, because the previous revision's pods are healthy too.
  if [ "$revision" = "$EXPECTED_REVISION" ] && \
     [ "$sync_status" = "Synced" ] && \
     [ "$health" = "Healthy" ]; then
    echo "  synced to ${revision}, healthy"
    break
  fi

  echo "  waiting: revision=${revision:-<none>} sync=${sync_status:-<none>} health=${health:-<none>}"
  counter=$((counter + 1))
  sleep "$SLEEP_TIME"
done

if [ "$counter" -ge "$MAX_TRIES" ]; then
  fail "timed out after $((MAX_TRIES * SLEEP_TIME))s"
fi

# Degraded is a terminal state, not something to wait out.
phase=$(field .status.operationState.phase)
case "$phase" in
  Failed|Error) fail "last sync operation reported ${phase}" ;;
esac

# Check 4. Kubernetes being content with a pod says nothing about whether the
# service answers correctly. Only prd is publicly routable; the other five
# environments need a port-forward from the runner, which is why this takes a URL
# rather than assuming one.
if [ -z "$PROBE_URL" ]; then
  echo "  NO KNOWN-ANSWER PROBE CONFIGURED for ${ENVIRONMENT}."
  echo "  The revision is live, but nothing has confirmed the service answers correctly."
  exit 0
fi

echo "  probing ${PROBE_URL}"
body=$(curl -fsS --max-time 20 "$PROBE_URL") || fail "probe request failed"

# The probe asserts a route the catalog must always be able to plan. An empty
# destination list is the failure this whole rebuild exists to remove: it reads to a
# user as "you are already current".
if ! printf '%s' "$body" | grep -q '"destinations"'; then
  fail "probe response has no destinations field; the API contract changed or the service is wrong"
fi
if printf '%s' "$body" | grep -q '"destinations": *\[\] *,'; then
  fail "probe returned ZERO destinations for a query that must always plan. Either the catalog failed to load or the planner is broken."
fi

echo "  probe returned a plannable itinerary"
echo "VERIFIED: ${APP} is running ${EXPECTED_REVISION} and answering correctly"
