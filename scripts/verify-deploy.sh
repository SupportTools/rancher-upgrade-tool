#!/usr/bin/env bash
#
# Verify that a deploy actually happened.
#
# ------------------------------------------------------------------------------
# THREE FALSE GREENS THIS HAS NOW SURVIVED. Each one passed a gate while nothing
# had been deployed, and each was found only by making the gate stricter.
#
# 1. HEALTH ALONE. The original gate patched the ArgoCD Application's
#    targetRevision and immediately polled .status.health.status for "Healthy".
#    ArgoCD tracks sync and health separately, so in the seconds after the patch
#    the OLD pods are still running and still perfectly healthy. The poll passed
#    on its first attempt having deployed nothing.
#
# 2. THE REQUESTED REVISION IS NOT THE SYNCED REVISION. The replacement compared
#    .status.sync.revision to the expected version. That field reports what was
#    ASKED FOR, not what synced. Observed on mst: targetRevision was patched to
#    a chart version that had never been published, ArgoCD reported
#      sync.revision      : v221      <- echoes the request
#      health.status      : Healthy
#      operationState     : Succeeded
#      syncResult.revision: v216      <- what actually synced
#    and the pods were still running image v216. The gate matched the string it
#    had itself asked for and passed.
#
# 3. sync.revision IS A DIGEST FOR OCI SOURCES. For a working OCI Helm source
#    that field is "sha256:...", not a chart version, so a version-equality
#    check against it can never match and would fail every correct deploy. Any
#    gate built on that field is wrong in both directions at once.
#
# THE FIX: stop asking the control plane what it intended, and ask the running
# process what it IS. /version reports the build linked into the binary. Desired
# state cannot fake it, and reading it needs no cluster credentials.
# ------------------------------------------------------------------------------
#
# Checks, in order:
#   1. spec.source.repoURL     has the expected prefix     REQUIRED
#      (a source migration that never reached the cluster is invisible otherwise:
#       mst kept pulling from the old chart museum for two full pipeline runs
#       because Deploy only patched targetRevision on an existing Application)
#   2. spec.source.targetRevision == the chart version just published  REQUIRED
#      (catches Publish and Deploy disagreeing about the version, which is
#       exactly how false green 2 happened)
#   3. sync Synced + health Healthy + operationState Succeeded          REQUIRED
#   4. GET <host>/version == the app version just built                 REQUIRED
#      -- the load-bearing check. This is the only one that observes the
#         running code rather than the intent to run it.
#   5. a known API query returns a plannable itinerary                  REQUIRED
#
# Check 5 proves the deployed thing WORKS; check 4 proves it is the thing we
# built. Neither is optional, in any environment: every environment here is
# publicly routable (mst/dev/qas/tst/stg.rancher.tips and rancher.tips), which
# an earlier version of this script did not know. It tried to port-forward
# instead and failed twice -- first racing the rollout it was waiting for, then
# on RBAC the CI ServiceAccount does not have.
set -euo pipefail

ENVIRONMENT="${1:?usage: verify-deploy.sh <environment> <expected-app-version>}"
EXPECTED_APP_VERSION="${2:?usage: verify-deploy.sh <environment> <expected-app-version>}"

APP="${APP_PREFIX:-rancherupgrade}-${ENVIRONMENT}"
NAMESPACE="${ARGOCD_NAMESPACE:-argocd}"
MAX_TRIES="${MAX_TRIES:-60}"
SLEEP_TIME="${SLEEP_TIME:-10}"

# The chart version (v0.<run>.0) differs from the app version (v<run>), because
# OCI registries require SemVer2 and reject "v221". Publish exports both.
EXPECTED_CHART_VERSION="${EXPECTED_CHART_VERSION:-}"
EXPECTED_REPO_PREFIX="${EXPECTED_REPO_PREFIX:-oci://harbor.support.tools}"

# Public ingress for this environment. Required: there is no environment here
# that cannot be reached.
PROBE_HOST="${PROBE_HOST:?PROBE_HOST is required; every environment has a public ingress}"
PROBE_PATH="${PROBE_PATH:-}"
PROBE_TRIES="${PROBE_TRIES:-20}"
PROBE_SLEEP="${PROBE_SLEEP:-15}"

field() {
  kubectl -n "$NAMESPACE" get application "$APP" -o jsonpath="{$1}" 2>/dev/null || true
}

fail() {
  echo ""
  echo "DEPLOY VERIFICATION FAILED for ${APP}: $*"
  echo ""
  echo "  spec.repoURL          : $(field .spec.source.repoURL)"
  echo "  spec.targetRevision   : $(field .spec.source.targetRevision)   (expected ${EXPECTED_CHART_VERSION:-<unset>})"
  echo "  sync.status           : $(field .status.sync.status)"
  echo "  sync.revision         : $(field .status.sync.revision)   (requested, NOT proof of what synced)"
  echo "  syncResult.revision   : $(field .status.operationState.syncResult.revision)   (actually synced)"
  echo "  health.status         : $(field .status.health.status)"
  echo "  operationState        : $(field .status.operationState.phase)"
  echo "  live /version         : $(curl -fsS --max-time 10 "${PROBE_HOST}/version" 2>/dev/null || echo '<unreachable>')"
  echo "                          (expected version ${EXPECTED_APP_VERSION})"
  exit 1
}

echo "verifying ${APP} is running ${EXPECTED_APP_VERSION} from chart ${EXPECTED_CHART_VERSION:-<unset>}"

# ---- Check 1. The source itself, before anything about revisions. -----------
repo_url=$(field .spec.source.repoURL)
case "$repo_url" in
  "${EXPECTED_REPO_PREFIX}"*) echo "  source: ${repo_url}" ;;
  "")
    fail "the Application reports no spec.source.repoURL"
    ;;
  *)
    fail "Application pulls from ${repo_url}, expected a source under \
${EXPECTED_REPO_PREFIX}. A repoURL change in argocd/ that is never applied to the \
live Application is silent: the deploy keeps succeeding against the OLD source."
    ;;
esac

# ---- Check 2. Desired state matches what was actually published. ------------
if [ -n "$EXPECTED_CHART_VERSION" ]; then
  target=$(field .spec.source.targetRevision)
  if [ "$target" != "$EXPECTED_CHART_VERSION" ]; then
    fail "targetRevision is ${target:-<none>} but the chart just published is \
${EXPECTED_CHART_VERSION}. ArgoCD is being asked for a chart version that was \
never pushed, so it cannot sync and will keep the previous release running while \
still reporting the requested revision back."
  fi
  echo "  targetRevision: ${target}"
fi

# ---- Check 3. Wait for ArgoCD to finish, on its own terms. ------------------
counter=0
while [ "$counter" -lt "$MAX_TRIES" ]; do
  sync_status=$(field .status.sync.status)
  health=$(field .status.health.status)
  phase=$(field .status.operationState.phase)

  # Terminal failures are not worth waiting out.
  case "$phase" in
    Failed|Error) fail "last sync operation reported ${phase}" ;;
  esac

  if [ "$sync_status" = "Synced" ] && [ "$health" = "Healthy" ]; then
    echo "  argocd: Synced and Healthy (phase ${phase:-<none>})"
    break
  fi

  echo "  waiting: sync=${sync_status:-<none>} health=${health:-<none>} phase=${phase:-<none>}"
  counter=$((counter + 1))
  sleep "$SLEEP_TIME"
done

if [ "$counter" -ge "$MAX_TRIES" ]; then
  # Unknown is NOT benign here. A previous version of this script treated it as
  # expected-and-permanent for OCI sources and passed on it; on this cluster the
  # other OCI-sourced Applications all report Synced, and mst reported Unknown
  # precisely BECAUSE it was asked for a chart version that did not exist.
  fail "timed out after $((MAX_TRIES * SLEEP_TIME))s waiting for Synced+Healthy"
fi

# ---- Check 4. The load-bearing check: what is actually running. -------------
echo "  reading ${PROBE_HOST}/version"
live_version=""
attempt=0
while [ "$attempt" -lt "$PROBE_TRIES" ]; do
  body=$(curl -fsS --max-time 15 "${PROBE_HOST}/version" 2>/dev/null || true)
  if [ -n "$body" ]; then
    live_version=$(printf '%s' "$body" \
      | sed -n 's/.*"version" *: *"\([^"]*\)".*/\1/p')
    if [ "$live_version" = "$EXPECTED_APP_VERSION" ]; then
      echo "  live version: ${live_version}"
      break
    fi
  fi
  attempt=$((attempt + 1))
  echo "  waiting for rollout: live=${live_version:-<none>} expected=${EXPECTED_APP_VERSION} (${attempt}/${PROBE_TRIES})"
  sleep "$PROBE_SLEEP"
done

if [ "$live_version" != "$EXPECTED_APP_VERSION" ]; then
  if [ -z "$live_version" ]; then
    fail "the service at ${PROBE_HOST} does not report a version. Either the \
rollout has not produced a pod serving /version, or the running image predates \
the /version endpoint -- which itself means the new build is NOT live."
  fi
  fail "${PROBE_HOST} reports version ${live_version}, expected \
${EXPECTED_APP_VERSION}. ArgoCD is content, but the code answering requests is \
not the code this pipeline built. This is the exact false green the gate exists \
to catch."
fi

# ---- Check 5. It is the right build; prove it also works. -------------------
if [ -z "$PROBE_PATH" ]; then
  fail "PROBE_PATH is not set, so nothing confirms the service answers correctly. \
Refusing to report a verified deploy on version equality alone."
fi

probe_url="${PROBE_HOST}${PROBE_PATH}"
echo "  probing ${probe_url}"
body=""
attempt=0
while [ "$attempt" -lt 5 ]; do
  if body=$(curl -fsS --max-time 20 "$probe_url" 2>/dev/null); then
    break
  fi
  attempt=$((attempt + 1))
  echo "  probe attempt ${attempt}/5 failed; retrying"
  sleep 3
done
[ -n "$body" ] || fail "probe request failed"

# The probe asserts a route the catalog must always be able to plan. An empty
# destination list is the failure this whole rebuild exists to remove: it reads to
# a user as "you are already current".
if ! printf '%s' "$body" | grep -q '"destinations"'; then
  fail "probe response has no destinations field; the API contract changed or the service is wrong"
fi
if printf '%s' "$body" | grep -qE '"destinations": *\[ *\]'; then
  fail "probe returned ZERO destinations for a query that must always plan. \
Either the catalog failed to load or the planner is broken."
fi

echo "  probe returned a plannable itinerary"
echo "VERIFIED: ${APP} is running ${EXPECTED_APP_VERSION} from ${repo_url} and answering correctly"
