// Rendering is DOM construction, never innerHTML.
//
// The old version interpolated server-echoed user input into a template string and
// assigned it to innerHTML. It did not fire, but only because the server rejected
// non-semver input before any step was built and the version library's grammar has
// no angle brackets. That is a safety property nobody chose and no test asserted,
// and the planner rewrite moved the parser it depended on. textContent does not
// care what the string contains.

const PLAN_URL = "/api/plan-upgrade";

const el = (tag, className, text) => {
  const node = document.createElement(tag);
  if (className) node.className = className;
  if (text !== undefined && text !== null) node.textContent = text;
  return node;
};

const badge = (kind, text) => el("span", `badge badge-${kind}`, text);

function stepLabel(kind) {
  switch (kind) {
    case "rancher": return "Rancher";
    case "local-k8s": return "Local cluster";
    case "downstream-k8s": return "Downstream";
    default: return kind;
  }
}

function renderStep(step) {
  const li = el("li");
  li.appendChild(el("span", "step-kind", stepLabel(step.kind)));

  const move = el("span", "step-move");
  move.appendChild(document.createTextNode(step.from));
  move.appendChild(document.createTextNode(" → "));
  move.appendChild(document.createTextNode(step.to));
  li.appendChild(move);

  if (step.platform && step.kind !== "rancher") {
    li.appendChild(document.createTextNode(` (${step.platform})`));
  }
  if (step.as_of) {
    li.appendChild(document.createTextNode(` · as of ${step.as_of}`));
  }
  if (step.source_url) {
    li.appendChild(document.createTextNode(" · "));
    const a = el("a", null, "source");
    a.href = step.source_url;
    a.rel = "noopener noreferrer";
    a.target = "_blank";
    li.appendChild(a);
  }
  (step.caveats || []).forEach((c) => li.appendChild(el("p", "caveat", c)));
  return li;
}

function renderDestination(dest) {
  const box = el("div", "dest");

  const head = el("div", "dest-head");
  head.appendChild(el("span", "dest-title", `Rancher ${dest.rancher}`));
  if (dest.lifecycle) head.appendChild(badge(dest.lifecycle, `${dest.lifecycle} support`));
  if (dest.granularity === "minor") head.appendChild(badge("minor", "minor-level"));
  head.appendChild(el("span", "step-kind", `${dest.steps.length} step${dest.steps.length === 1 ? "" : "s"}`));
  box.appendChild(head);

  const list = el("ul", "steps");
  dest.steps.forEach((s) => list.appendChild(renderStep(s)));
  box.appendChild(list);

  if (dest.granularity_note) box.appendChild(el("p", "note", dest.granularity_note));
  if (dest.lifecycle_as_of) {
    box.appendChild(el("p", "note", `Support status as of ${dest.lifecycle_as_of}.`));
  }
  return box;
}

function renderBlocker(b) {
  const box = el("div", b.kind === "end-of-life" ? "blocker blocker-eol" : "blocker");
  box.appendChild(el("div", "blocker-kind", b.kind.replace(/-/g, " ")));
  box.appendChild(el("p", "blocker-constraint", b.constraint));
  if (b.detail) box.appendChild(el("p", "blocker-detail", b.detail));
  if (b.source_url) {
    const a = el("a", null, "source");
    a.href = b.source_url;
    a.rel = "noopener noreferrer";
    a.target = "_blank";
    box.appendChild(a);
  }
  return box;
}

function render(out, payload) {
  out.replaceChildren();

  if (payload.error) {
    const box = el("div", "err");
    box.appendChild(el("p", "blocker-constraint", payload.error.detail));
    if (payload.error.field) {
      box.appendChild(el("p", "blocker-detail", `Field: ${payload.error.field}`));
    }
    out.appendChild(box);
    return;
  }

  (payload.blockers || []).forEach((b) => out.appendChild(renderBlocker(b)));

  const dests = payload.destinations || [];
  if (dests.length === 0 && (payload.blockers || []).length === 0) {
    // Should not happen: the API never returns a bare empty result. If it does,
    // say so rather than rendering nothing, which reads as "you are fine".
    out.appendChild(el("p", "blocker-detail",
      "No destinations and no explanation were returned. That is a bug, not a clean bill of health."));
  }
  dests.forEach((d) => out.appendChild(renderDestination(d)));

  if (payload.claim_note) out.appendChild(el("p", "claim", payload.claim_note));
  if (payload.scope) out.appendChild(el("p", "claim", payload.scope));
  if (payload.prerequisites_url) {
    const p = el("p", "claim");
    const a = el("a", null, "Rancher upgrade prerequisites");
    a.href = payload.prerequisites_url;
    a.rel = "noopener noreferrer";
    a.target = "_blank";
    p.appendChild(a);
    out.appendChild(p);
  }
}

document.getElementById("planButton").addEventListener("click", async () => {
  const out = document.getElementById("planOutput");
  const value = (id) => document.getElementById(id).value.trim();

  const fields = {
    rancher: value("currentRancher"),
    local_platform: value("localPlatform"),
    local_k8s: value("localK8s"),
    downstream_platform: value("downstreamPlatform"),
    downstream_k8s: value("downstreamK8s"),
  };

  const missing = Object.entries(fields).filter(([, v]) => !v).map(([k]) => k);
  if (missing.length) {
    out.replaceChildren();
    const box = el("div", "err");
    box.appendChild(el("p", "blocker-constraint",
      `Fill in every field. Missing: ${missing.join(", ")}`));
    out.appendChild(box);
    return;
  }

  out.replaceChildren(el("p", "note", "Planning…"));

  try {
    // URLSearchParams encodes. The old code interpolated raw input into path
    // segments, so a "/" in any field broke routing.
    const res = await fetch(`${PLAN_URL}?${new URLSearchParams(fields)}`);
    render(out, await res.json());
  } catch (err) {
    out.replaceChildren();
    const box = el("div", "err");
    box.appendChild(el("p", "blocker-constraint", "Could not reach the planner."));
    box.appendChild(el("p", "blocker-detail", String(err)));
    out.appendChild(box);
  }
});
