// Rendering is DOM construction, never innerHTML.
//
// The old version interpolated server-echoed user input into a template string and
// assigned it to innerHTML. It did not fire, but only because the server rejected
// non-semver input before any step was built and the version library's grammar has
// no angle brackets. That is a safety property nobody chose and no test asserted,
// and the planner rewrite moved the parser it depended on. textContent does not
// care what the string contains.
//
// WHY THE OUTPUT LOOKS LIKE THIS. The first version of this page rendered every
// reachable destination expanded, each restating its whole journey from the start.
// For one real query (2.9.4, rke2, v1.28) that is 68 step rows on one screen, and
// because each row carried its own evidence it also printed the catalog date 68
// times and a "source" link 68 times resolving to 2 distinct URLs. The date is a
// property of the dataset, not of a step.
//
// So: one destination at a time (the picker), the sequence as three lanes, the
// exact versions as numbered rows beneath, and the invariants hoisted to a single
// footer per card.

const PLAN_URL = "/api/plan-upgrade";

const el = (tag, className, text) => {
  const node = document.createElement(tag);
  if (className) node.className = className;
  if (text !== undefined && text !== null) node.textContent = text;
  return node;
};

const badge = (kind, text) => el("span", `badge badge-${kind}`, text);

// The three axes a step can move, in lane order. Rancher last: it is the thing the
// clusters are being raised FOR, and putting it under them makes the dependency
// read downward.
const AXES = [
  { kind: "local-k8s", cls: "local", label: "Local cluster" },
  { kind: "downstream-k8s", cls: "down", label: "Downstream" },
  { kind: "rancher", cls: "ranch", label: "Rancher" },
];

function axisOf(kind) {
  return AXES.find((a) => a.kind === kind) || { kind, cls: "ranch", label: kind };
}

function stepLabel(kind) {
  return axisOf(kind).label;
}

// Lane cells are ~60px wide at best. "v1.32.13+rke2r1" does not fit at any readable
// size, so the lane shows the minor.patch and the row beneath carries the full
// string. Never use this where the exact version is the point.
function shortVersion(v) {
  return String(v).replace(/\+(rke2r\d+|k3s\d+)$/, "");
}

function sourceName(url) {
  if (url.indexOf("kontainer-driver-metadata") > -1) return "Rancher KDM";
  if (url.indexOf("suse.com") > -1) return "SUSE support matrix";
  return "source";
}

function externalLink(href, text) {
  const a = el("a", null, text);
  a.href = href;
  a.rel = "noopener noreferrer";
  a.target = "_blank";
  return a;
}

function axisTag(kind, className) {
  const a = axisOf(kind);
  const span = el("span", `${className} axis-${a.cls}`);
  span.appendChild(el("i", "axis-dot"));
  span.appendChild(el("span", null, a.label));
  return span;
}

function startValueFor(kind, start) {
  if (!start) return "";
  if (kind === "rancher") return start.rancher || "";
  if (kind === "local-k8s") return (start.local && start.local.k8s) || "";
  return (start.downstream && start.downstream.k8s) || "";
}

// ---------------------------------------------------------------------------
// The lane diagram. One column per step, one lane per axis. Position is sequence,
// so you can see that Rancher cannot move until the clusters have caught up.
// ---------------------------------------------------------------------------
function renderLanes(dest, start, onPick) {
  const wrap = el("div", "lanes-wrap");

  const cap = el("div", "lanes-cap");
  cap.appendChild(el("span", null, "Sequence"));
  cap.appendChild(el("span", null, "one column per step"));
  wrap.appendChild(cap);

  const scroller = el("div", "lanes-scroll");
  const lanes = el("div", "lanes");
  const steps = dest.steps;
  const marks = [];

  AXES.forEach((axis) => {
    const lane = el("div", `lane axis-${axis.cls}`);

    const id = el("div", "lane-id");
    const name = el("div", "lane-name");
    name.appendChild(el("i", "axis-dot"));
    name.appendChild(el("span", null, axis.label));
    id.appendChild(name);

    const mine = steps.filter((s) => s.kind === axis.kind);
    const from = startValueFor(axis.kind, start);
    id.appendChild(el("div", "lane-span", mine.length
      ? `${shortVersion(from)} → ${shortVersion(mine[mine.length - 1].to)}`
      : `${shortVersion(from)} · no change`));
    lane.appendChild(id);

    const track = el("div", "lane-track");

    // Column 0 is the starting state, dashed so it does not read as a step. Without
    // it the leftmost block is a step's DESTINATION and gets misread as "where I am".
    const startSlot = el("div", "slot slot-start");
    const startMark = el("div", "mark", shortVersion(from));
    startMark.title = `${axis.label} starts at ${from}`;
    startSlot.appendChild(startMark);
    track.appendChild(startSlot);

    steps.forEach((step, i) => {
      const slot = el("div", "slot");
      if (step.kind === axis.kind) {
        const mark = el("button", "mark mark-step", shortVersion(step.to));
        mark.type = "button";
        mark.dataset.step = String(i);
        const title = `Step ${i + 1}: ${step.from} → ${step.to}`;
        mark.title = title;
        mark.setAttribute("aria-label", title);
        mark.addEventListener("click", () => onPick(i));
        marks.push(mark);
        slot.appendChild(mark);
      }
      track.appendChild(slot);
    });

    lane.appendChild(track);
    lanes.appendChild(lane);
  });

  scroller.appendChild(lanes);

  const ticks = el("div", "lane-ticks");
  ticks.appendChild(el("div"));
  const tickRow = el("div", "tick-row");
  tickRow.appendChild(el("span", null, "start"));
  for (let i = 1; i <= steps.length; i += 1) {
    // Every other number once the route gets long, so the labels never collide.
    tickRow.appendChild(el("span", null, steps.length <= 12 || i % 2 === 1 ? String(i) : ""));
  }
  ticks.appendChild(tickRow);
  scroller.appendChild(ticks);

  wrap.appendChild(scroller);
  return { node: wrap, marks };
}

// ---------------------------------------------------------------------------
// One destination: header, lanes, numbered rows, and the evidence once at the end.
// ---------------------------------------------------------------------------
function renderDestination(dest, start) {
  const box = el("div", "dest");

  const head = el("div", "dest-head");
  head.appendChild(el("span", "dest-title", `Rancher ${dest.rancher}`));
  if (dest.lifecycle) head.appendChild(badge(dest.lifecycle, `${dest.lifecycle} support`));
  if (dest.granularity === "minor") head.appendChild(badge("minor", "minor-level"));
  head.appendChild(el("span", "dest-count",
    `${dest.steps.length} step${dest.steps.length === 1 ? "" : "s"}`));
  box.appendChild(head);

  if (dest.steps.length === 0) {
    box.appendChild(el("p", "note", "Nothing to do: you are already on this version."));
    return box;
  }

  const rows = el("div", "steps");
  const rowNodes = [];

  const lanes = renderLanes(dest, start, (i) => {
    rowNodes.forEach((r, k) => r.setAttribute("aria-current", String(k === i)));
    lanes.marks.forEach((m) => {
      m.dataset.active = String(Number(m.dataset.step) === i);
    });
    rowNodes[i].scrollIntoView({ block: "nearest", behavior: "smooth" });
  });
  box.appendChild(lanes.node);

  const rowsCap = el("div", "rows-cap");
  rowsCap.appendChild(el("span", null, "Steps in order"));
  rowsCap.appendChild(el("span", null, "exact versions"));
  box.appendChild(rowsCap);

  dest.steps.forEach((step, i) => {
    const row = el("div", `step-row axis-${axisOf(step.kind).cls}`);
    row.appendChild(el("span", "step-num", `${i + 1}.`));
    row.appendChild(axisTag(step.kind, "step-kind"));

    const move = el("span", "step-move");
    move.appendChild(el("span", "v-from", step.from));
    move.appendChild(el("span", "v-arrow", "→"));
    move.appendChild(el("span", "v-to", step.to));
    if (step.platform && step.kind !== "rancher") {
      move.appendChild(el("span", "v-plat", `(${step.platform})`));
    }
    row.appendChild(move);

    // Caveats are per-step and genuinely vary, so they stay on the row.
    (step.caveats || []).forEach((c) => row.appendChild(el("p", "caveat", c)));

    rows.appendChild(row);
    rowNodes.push(row);
  });
  box.appendChild(rows);

  // The invariants, once. These were previously repeated on every step: the same
  // date and the same two URLs, 68 times for a full page of destinations.
  const foot = el("div", "dest-foot");
  const asOf = dest.steps.map((s) => s.as_of).filter(Boolean);
  if (asOf.length) {
    const unique = Array.from(new Set(asOf));
    foot.appendChild(el("span", null, unique.length === 1
      ? `All ${dest.steps.length} steps verified as of ${unique[0]}.`
      : `Steps verified between ${unique.sort()[0]} and ${unique.sort()[unique.length - 1]}.`));
  }
  const urls = Array.from(new Set(dest.steps.map((s) => s.source_url).filter(Boolean)));
  if (urls.length) {
    const span = el("span");
    span.appendChild(document.createTextNode(urls.length === 1 ? "Source: " : "Sources: "));
    urls.forEach((u, i) => {
      if (i) span.appendChild(document.createTextNode(" · "));
      span.appendChild(externalLink(u, sourceName(u)));
    });
    foot.appendChild(span);
  }
  if (dest.granularity_note) foot.appendChild(el("span", null, dest.granularity_note));
  if (dest.lifecycle_as_of) {
    foot.appendChild(el("span", null, `Support status as of ${dest.lifecycle_as_of}.`));
  }
  if (foot.childNodes.length) box.appendChild(foot);

  return box;
}

// ---------------------------------------------------------------------------
// The picker. Nobody upgrades to seven versions at once; pick the target first.
// ---------------------------------------------------------------------------
function renderPicker(payload, host) {
  const dests = payload.destinations || [];
  const picker = el("div", "picker");

  const head = el("div", "picker-head");
  head.appendChild(el("span", "picker-eyebrow", "Reachable destinations"));
  const start = payload.start;
  if (start) {
    const bits = [`from Rancher ${start.rancher}`];
    if (start.local) bits.push(`local ${start.local.platform} ${start.local.k8s}`);
    if (start.downstream) bits.push(`downstream ${start.downstream.platform} ${start.downstream.k8s}`);
    head.appendChild(el("span", "picker-from", bits.join(" · ")));
  }
  picker.appendChild(head);

  const chips = el("div", "chips");
  chips.setAttribute("role", "tablist");
  chips.setAttribute("aria-label", "Destination");
  picker.appendChild(chips);

  const body = el("div", "picked");

  const select = (version) => {
    const dest = dests.find((d) => d.rancher === version);
    if (!dest) return;
    body.replaceChildren(renderDestination(dest, payload.start));
    Array.prototype.forEach.call(chips.children, (c) => {
      c.setAttribute("aria-selected", String(c.dataset.version === version));
    });
  };

  dests.forEach((d) => {
    const chip = el("button", "chip");
    chip.type = "button";
    chip.dataset.version = d.rancher;
    chip.setAttribute("role", "tab");
    chip.setAttribute("aria-selected", "false");
    chip.appendChild(el("span", "chip-v", d.rancher));
    if (d.lifecycle) chip.appendChild(badge(d.lifecycle, d.lifecycle));
    chip.appendChild(el("span", "chip-n",
      `${d.steps.length} step${d.steps.length === 1 ? "" : "s"}`));
    chip.addEventListener("click", () => select(d.rancher));
    chips.appendChild(chip);
  });

  host.appendChild(picker);
  host.appendChild(body);

  // Open on the newest FULLY supported destination rather than the furthest one.
  // The longest route is the worst default: it is the most work and, here, lands on
  // a version whose support window is narrower than the one before it.
  const preferred = dests.find((d) => d.lifecycle === "full") || dests[dests.length - 1];
  if (preferred) select(preferred.rancher);
}

function renderBlocker(b) {
  const box = el("div", b.kind === "end-of-life" ? "blocker blocker-eol" : "blocker");
  box.appendChild(el("div", "blocker-kind", b.kind.replace(/-/g, " ")));
  box.appendChild(el("p", "blocker-constraint", b.constraint));
  if (b.detail) box.appendChild(el("p", "blocker-detail", b.detail));
  if (b.source_url) box.appendChild(externalLink(b.source_url, "source"));
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

  // Staleness banner first, above everything. If the refresh stopped working, a
  // user acting on these routes should see that before they read them, not after.
  const cat = payload.catalog;
  if (cat && cat.stale) {
    const banner = el("div", "stale");
    banner.appendChild(el("div", "blocker-kind", "data may be out of date"));
    banner.appendChild(el("p", "blocker-detail", cat.stale_note));
    out.appendChild(banner);
  }

  (payload.blockers || []).forEach((b) => out.appendChild(renderBlocker(b)));

  const dests = payload.destinations || [];
  if (dests.length === 0 && (payload.blockers || []).length === 0) {
    // Should not happen: the API never returns a bare empty result. If it does,
    // say so rather than rendering nothing, which reads as "you are fine".
    out.appendChild(el("p", "blocker-detail",
      "No destinations and no explanation were returned. That is a bug, not a clean bill of health."));
  }
  if (dests.length) renderPicker(payload, out);

  if (cat && cat.generated_at) {
    out.appendChild(el("p", "claim",
      `Compatibility data generated ${cat.generated_at}${cat.age_days ? ` (${cat.age_days} days ago)` : ""}.`));
  }
  if (payload.claim_note) out.appendChild(el("p", "claim", payload.claim_note));
  if (payload.scope) out.appendChild(el("p", "claim", payload.scope));
  if (payload.prerequisites_url) {
    const p = el("p", "claim");
    p.appendChild(externalLink(payload.prerequisites_url, "Rancher upgrade prerequisites"));
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
