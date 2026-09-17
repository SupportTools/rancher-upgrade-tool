# Reproducing the ArgoCD false-green (OQ3)

Status: **not yet run.** Needs a disposable environment. Everything else about this
premise is settled; this is the one piece taken on reasoning rather than observation,
and it should not stay that way silently.

## What is claimed

The old deploy gate patched an ArgoCD Application's `targetRevision` and then
immediately polled `.status.health.status`, passing on `Healthy`. ArgoCD tracks sync
state and health state separately, so in the window after the patch the **old** pods
are still running and still healthy. The claim is that the poll can therefore see
`Healthy` on its first attempt and report a successful deploy while the new revision
was never pulled.

## What is already established

- **The old gate read health only.** Verifiable from `pipeline.yml` history: it never
  read `.status.sync.status`, `.status.sync.revision` or `.status.operationState`.
- **A health-only gate passes on a stale-but-healthy revision.** Proven by
  `deploy_verify_test.go`, which drives the gate through exactly that state with a
  stub kubectl.

## What is NOT established

That ArgoCD actually reports `Healthy` alongside a stale `sync.revision` in that
window, on this cluster, with these applications. The mechanism is documented
behaviour and the code reading supports it, but nobody has watched it happen here.

## Procedure

Use `mst` or `dev`. **Never `prd`.**

1. Record the starting state:

       kubectl -n argocd get application rancherupgrade-dev \
         -o jsonpath='{.status.sync.revision}{"\t"}{.status.sync.status}{"\t"}{.status.health.status}'

2. Stop the sync from completing, so the old pods stay up while the target moves.
   Either suspend automated sync:

       kubectl -n argocd patch application rancherupgrade-dev \
         --type merge -p '{"spec":{"syncPolicy":{"automated":null}}}'

   or point `targetRevision` at a chart version that does not exist, which makes the
   sync fail while leaving the running pods untouched.

3. Change the target revision:

       kubectl -n argocd patch application rancherupgrade-dev \
         --type merge -p '{"spec":{"source":{"targetRevision":"v999999"}}}'

4. **Immediately** sample health the way the old gate did, with no delay:

       for i in $(seq 1 10); do
         kubectl -n argocd get application rancherupgrade-dev \
           -o jsonpath='{.status.sync.revision}{"\t"}{.status.sync.status}{"\t"}{.status.health.status}{"\n"}'
         sleep 1
       done

5. Run the new gate against the same state and confirm it refuses:

       MAX_TRIES=2 SLEEP_TIME=1 ./scripts/verify-deploy.sh dev v999999

## What each outcome means

- **`Healthy` appears while `sync.revision` is still the old value** — reproduced.
  The old gate would have passed. Record the sampled output on OQ3 and close it.
- **Health flips away from `Healthy` before the first sample** — not reproduced on
  this cluster. The fix is still correct defensively, because revision equality is a
  stronger check regardless, but the plan's framing of it as an active live bug
  should be softened and that correction recorded.

Either way the new gate must refuse in step 5, because the revision never matches.

## Restore

    kubectl -n argocd patch application rancherupgrade-dev \
      --type merge -p '{"spec":{"source":{"targetRevision":"<the value from step 1>"}}}'
    kubectl -n argocd patch application rancherupgrade-dev \
      --type merge -p '{"spec":{"syncPolicy":{"automated":{"prune":true,"selfHeal":true}}}}'

Confirm it returns to the step 1 state before walking away.
