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

// One colour per lane, allocated once and reused by the input rows, the support
// strip and the lane diagram, so a colour means the same thing everywhere.
const CLUSTER_COLOURS = ["c1", "c2", "c3", "c4", "c5", "c6", "c7", "c8"];

function clusterColour(index) {
  return CLUSTER_COLOURS[index % CLUSTER_COLOURS.length];
}

// laneColour maps a step's cluster identity to its colour class. "local" and the
// Rancher axis are fixed; downstream clusters take their position in the fleet.
function laneColour(clusterId, fleet) {
  if (clusterId === "local") return "local";
  if (!clusterId) return "ranch";
  const i = fleet.findIndex((c) => c.id === clusterId);
  return i < 0 ? "ranch" : clusterColour(i);
}

function stepLabel(step) {
  if (step.kind === "rancher") return "Rancher";
  return step.cluster_label || (step.cluster === "local" ? "Local cluster" : step.cluster || step.kind);
}

// lanesFor builds the lane list: local, every downstream cluster, then Rancher.
// Rancher last because it is the thing the clusters are being raised FOR, so the
// dependency reads downward.
function lanesFor(payload) {
  const fleet = (payload.start && payload.start.clusters) || [];
  const lanes = [{
    id: "local", label: "Local cluster", colour: "local",
    start: (payload.start && payload.start.local && payload.start.local.k8s) || "",
  }];
  fleet.forEach((cl, i) => lanes.push({
    id: cl.id, label: cl.label || `Cluster ${i + 1}`, colour: clusterColour(i), start: cl.k8s,
  }));
  lanes.push({
    id: "", label: "Rancher", colour: "ranch",
    start: (payload.start && payload.start.rancher) || "",
  });
  return lanes;
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

function axisTag(step, fleet, className) {
  const span = el("span", `${className} axis-${laneColour(step.cluster, fleet)}`);
  span.appendChild(el("i", "axis-dot"));
  span.appendChild(el("span", null, stepLabel(step)));
  return span;
}

// ---------------------------------------------------------------------------
// The lane diagram. One column per step, one lane per axis. Position is sequence,
// so you can see that Rancher cannot move until the clusters have caught up.
// ---------------------------------------------------------------------------
function renderLanes(dest, payload, onPick) {
  const wrap = el("div", "lanes-wrap");
  const fleet = (payload.start && payload.start.clusters) || [];
  const lanes = lanesFor(payload);

  const cap = el("div", "lanes-cap");
  cap.appendChild(el("span", null, "Sequence"));
  cap.appendChild(el("span", null, "one column per step"));
  wrap.appendChild(cap);

  const scroller = el("div", "lanes-scroll");
  const laneBox = el("div", "lanes");
  const steps = dest.steps;
  const marks = [];

  lanes.forEach((lane) => {
    const row = el("div", `lane axis-${lane.colour}`);

    const id = el("div", "lane-id");
    const name = el("div", "lane-name");
    name.appendChild(el("i", "axis-dot"));
    name.appendChild(el("span", null, lane.label));
    id.appendChild(name);

    const mine = steps.filter((st) => (st.cluster || "") === lane.id);
    id.appendChild(el("div", "lane-span", mine.length
      ? `${shortVersion(lane.start)} → ${shortVersion(mine[mine.length - 1].to)}`
      : `${shortVersion(lane.start)} · no change`));
    row.appendChild(id);

    const track = el("div", "lane-track");

    // Column 0 is the starting state, dashed so it does not read as a step. Without
    // it the leftmost block is a step's DESTINATION and gets misread as "where I am".
    const startSlot = el("div", "slot slot-start");
    const startMark = el("div", "mark", shortVersion(lane.start));
    startMark.title = `${lane.label} starts at ${lane.start}`;
    startSlot.appendChild(startMark);
    track.appendChild(startSlot);

    steps.forEach((step, i) => {
      const slot = el("div", "slot");
      if ((step.cluster || "") === lane.id) {
        const mark = el("button", "mark mark-step", shortVersion(step.to));
        mark.type = "button";
        mark.dataset.step = String(i);
        const title = `Step ${i + 1}: ${lane.label} ${step.from} → ${step.to}`;
        mark.title = title;
        mark.setAttribute("aria-label", title);
        mark.addEventListener("click", () => onPick(i));
        marks.push(mark);
        slot.appendChild(mark);
      }
      track.appendChild(slot);
    });

    row.appendChild(track);
    laneBox.appendChild(row);
  });

  scroller.appendChild(laneBox);

  const ticks = el("div", "lane-ticks");
  ticks.appendChild(el("div"));
  const tickRow = el("div", "tick-row");
  tickRow.appendChild(el("span", null, "start"));
  for (let i = 1; i <= steps.length; i += 1) {
    tickRow.appendChild(el("span", null, steps.length <= 12 || i % 2 === 1 ? String(i) : ""));
  }
  ticks.appendChild(tickRow);
  scroller.appendChild(ticks);

  // Waves group steps with no ordering requirement between them. Say that as
  // dependency, never as a safety endorsement: concurrent cluster upgrades have
  // control-plane and drain costs this tool does not model.
  const clusterSteps = steps.filter((st) => st.kind !== "rancher" && st.cluster !== "local");
  const waves = new Set(clusterSteps.map((st) => st.wave));
  if (fleet.length > 1 && clusterSteps.length > waves.size) {
    scroller.appendChild(el("p", "lane-note",
      `${clusterSteps.length} cluster upgrades fall into ${waves.size} groups with no ordering ` +
      `requirement between the steps inside a group.`));
  }

  wrap.appendChild(scroller);
  return { node: wrap, marks };
}

// ---------------------------------------------------------------------------
// The support-window strip. One row per Rancher version, its supported Kubernetes
// range drawn as a bar, every cluster plotted as a dot.
//
// This is the view that does NOT decompose into per-cluster answers: a version
// either contains every dot or it does not. It is the reason the fleet case needs
// its own design rather than the single-cluster form repeated.
// ---------------------------------------------------------------------------
function minorNumber(v) {
  const m = /^v?(\d+)\.(\d+)/.exec(String(v || "").trim());
  return m ? parseInt(m[2], 10) : null;
}

function renderWindowStrip(payload) {
  const windows = payload.windows || [];
  if (!windows.length) return null;

  const lanes = lanesFor(payload).filter((l) => l.id !== "" );
  const points = lanes
    .map((l) => ({ label: l.label, colour: l.colour, minor: minorNumber(l.start) }))
    .filter((p) => p.minor !== null);
  if (!points.length) return null;

  // One row per Rancher version; a mixed-platform fleet contributes one bar per
  // platform, because the window differs by platform.
  const byRancher = new Map();
  windows.forEach((w) => {
    if (!byRancher.has(w.rancher)) byRancher.set(w.rancher, { lifecycle: w.lifecycle, bars: [] });
    byRancher.get(w.rancher).bars.push(w);
  });

  let lo = Infinity;
  let hi = -Infinity;
  windows.forEach((w) => {
    const a = minorNumber(w.min);
    const b = minorNumber(w.max);
    if (a !== null) lo = Math.min(lo, a);
    if (b !== null) hi = Math.max(hi, b);
  });
  points.forEach((p) => { lo = Math.min(lo, p.minor); hi = Math.max(hi, p.minor); });
  if (!isFinite(lo) || !isFinite(hi)) return null;
  const span = (hi - lo) + 1;
  const pct = (m) => (((m - lo) + 0.5) / span) * 100;

  const box = el("div", "strip-box");
  const head = el("div", "strip-head");
  head.appendChild(el("span", "picker-eyebrow", "Support windows"));
  head.appendChild(el("span", "picker-from",
    "Which Rancher versions can hold the whole fleet at once"));
  box.appendChild(head);

  const scroll = el("div", "strip-scroll");
  const strip = el("div", "strip");

  byRancher.forEach((info, rancher) => {
    const row = el("div", "strip-row");
    const who = el("div", "strip-who");
    who.appendChild(el("span", "strip-v", rancher));
    if (info.lifecycle) who.appendChild(badge(info.lifecycle, info.lifecycle));
    row.appendChild(who);

    const bar = el("div", "strip-bar");
    let fitsAll = true;
    info.bars.forEach((w) => {
      const a = minorNumber(w.min);
      const b = minorNumber(w.max);
      if (a === null || b === null) return;
      const win = el("div", "strip-win");
      const left = pct(a) - (50 / span);
      const right = pct(b) + (50 / span);
      win.style.left = `${Math.max(0, left)}%`;
      win.style.width = `${Math.min(100, right - Math.max(0, left))}%`;
      win.appendChild(el("span", "strip-wl", `${w.min} – ${w.max}`));
      win.title = `${rancher} supports ${w.platform} ${w.min} to ${w.max}`;
      bar.appendChild(win);

      points.forEach((p) => {
        if (p.minor < a || p.minor > b) fitsAll = false;
      });
    });

    points.forEach((p) => {
      const inAny = info.bars.some((w) => {
        const a = minorNumber(w.min);
        const b = minorNumber(w.max);
        return a !== null && b !== null && p.minor >= a && p.minor <= b;
      });
      const dot = el("span", `strip-dot axis-${p.colour}`);
      dot.style.left = `${pct(p.minor)}%`;
      // Shape, not only colour, so the distinction survives greyscale and
      // colour-blindness.
      dot.dataset.out = String(!inAny);
      dot.title = `${p.label} v1.${p.minor}${inAny ? "" : " — outside this window"}`;
      bar.appendChild(dot);
    });

    row.dataset.fits = String(fitsAll);
    row.appendChild(bar);
    strip.appendChild(row);
  });

  scroll.appendChild(strip);

  const axis = el("div", "strip-axis");
  axis.appendChild(el("div"));
  const ticks = el("div", "strip-ticks");
  for (let m = lo; m <= hi; m += 1) {
    const t = el("span", null, `v1.${m}`);
    t.style.left = `${pct(m)}%`;
    ticks.appendChild(t);
  }
  axis.appendChild(ticks);
  scroll.appendChild(axis);

  box.appendChild(scroll);
  return box;
}

// ---------------------------------------------------------------------------
// The binding constraint: what the fleet can run TODAY, and what is stopping it
// going further. A different question from what is reachable, and the actionable
// one — a fleet can reach a much newer Rancher by upgrading its laggards first.
// ---------------------------------------------------------------------------
function renderBindingConstraint(payload) {
  const bc = payload.binding_constraint;
  if (!bc || !bc.clusters || !bc.clusters.length) return null;

  const behind = bc.clusters.filter((c) => c.position === "behind");
  const ahead = bc.clusters.filter((c) => c.position === "ahead");

  const box = el("div", "binding");
  if (bc.highest_now) {
    box.appendChild(el("div", "binding-title",
      `Highest Rancher this fleet can run today: ${bc.highest_now}`));
  } else {
    box.appendChild(el("div", "binding-title", "This fleet cannot run the next Rancher version"));
  }

  if (behind.length) {
    const p = el("p", "binding-detail");
    p.appendChild(document.createTextNode(`To reach ${bc.blocked_rancher}, every cluster must be at `));
    p.appendChild(el("span", "v-to", bc.requires_k8s || "a newer version"));
    p.appendChild(document.createTextNode(" or newer. Still behind: "));
    behind.forEach((c, i) => {
      if (i) p.appendChild(document.createTextNode(", "));
      p.appendChild(el("span", "binding-cluster", `${c.label} (${c.k8s})`));
    });
    p.appendChild(document.createTextNode(behind.length === 1
      ? " — that one cluster is the constraint on the entire fleet."
      : " — these set the pace for the entire fleet."));
    box.appendChild(p);
  }

  if (ahead.length) {
    // Never phrased as something to upgrade: Kubernetes has no downgrade, so this
    // cannot be fixed by moving the cluster forward.
    const p = el("p", "binding-detail binding-ahead");
    p.appendChild(document.createTextNode("Ahead of the window and not downgradable: "));
    ahead.forEach((c, i) => {
      if (i) p.appendChild(document.createTextNode(", "));
      p.appendChild(el("span", "binding-cluster", `${c.label} (${c.k8s})`));
    });
    p.appendChild(document.createTextNode(
      `. Rancher ${bc.blocked_rancher} cannot manage this fleet while those clusters remain where they are.`));
    box.appendChild(p);
  }

  return box;
}

// ---------------------------------------------------------------------------
// One destination: header, lanes, numbered rows, and the evidence once at the end.
// ---------------------------------------------------------------------------
function renderDestination(dest, payload) {
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

  const fleet = (payload.start && payload.start.clusters) || [];
  const lanes = renderLanes(dest, payload, (i) => {
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
    const row = el("div", `step-row axis-${laneColour(step.cluster, fleet)}`);
    row.appendChild(el("span", "step-num", `${i + 1}.`));
    row.appendChild(axisTag(step, fleet, "step-kind"));

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
    body.replaceChildren(renderDestination(dest, payload));
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

  const binding = renderBindingConstraint(payload);
  if (binding) out.appendChild(binding);

  (payload.blockers || []).forEach((b) => out.appendChild(renderBlocker(b)));

  const strip = renderWindowStrip(payload);
  if (strip) out.appendChild(strip);

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

// ---------------------------------------------------------------------------
// Fleet input rows. Built here rather than in HTML so adding and removing a
// cluster is one code path, and so each cluster's colour is allocated once and
// reused by the strip and the lanes.
// ---------------------------------------------------------------------------
const PLATFORMS = [
  ["rke2", "RKE2"], ["k3s", "K3s"], ["rke1", "RKE1 (end of life)"],
  ["aks", "AKS"], ["eks", "EKS"], ["gke", "GKE"],
];

let fleetRows = [];
let nextRowSeq = 1;

function renderFleetRows() {
  const host = document.getElementById("fleetRows");
  host.replaceChildren();

  fleetRows.forEach((row, i) => {
    const line = el("div", "fleet-row");

    const swatch = el("span", `fleet-swatch axis-${clusterColour(i)}`);
    swatch.appendChild(el("i", "axis-dot"));
    line.appendChild(swatch);

    const name = el("input", "fleet-name");
    name.type = "text";
    name.id = `clusterLabel${row.seq}`;
    name.value = row.label;
    name.placeholder = `Cluster ${i + 1}`;
    name.setAttribute("aria-label", `Name for cluster ${i + 1}`);
    name.addEventListener("input", () => { row.label = name.value; });
    line.appendChild(name);

    const plat = el("select", "fleet-plat");
    plat.id = `clusterPlatform${row.seq}`;
    plat.setAttribute("aria-label", `Platform for cluster ${i + 1}`);
    PLATFORMS.forEach(([v, text]) => {
      const o = el("option", null, text);
      o.value = v;
      if (v === row.platform) o.selected = true;
      plat.appendChild(o);
    });
    plat.addEventListener("change", () => { row.platform = plat.value; });
    line.appendChild(plat);

    const k8s = el("input", "fleet-k8s");
    k8s.type = "text";
    k8s.id = `clusterK8s${row.seq}`;
    k8s.value = row.k8s;
    k8s.placeholder = "v1.28";
    k8s.setAttribute("aria-label", `Kubernetes version for cluster ${i + 1}`);
    k8s.addEventListener("input", () => { row.k8s = k8s.value; });
    line.appendChild(k8s);

    const del = el("button", "fleet-del", "×");
    del.type = "button";
    del.setAttribute("aria-label", `Remove ${row.label || `cluster ${i + 1}`}`);
    // The last cluster cannot be removed: a plan with no downstream cluster is not
    // a question this tool can answer.
    del.disabled = fleetRows.length <= 1;
    del.addEventListener("click", () => {
      fleetRows = fleetRows.filter((r) => r !== row);
      renderFleetRows();
    });
    line.appendChild(del);

    host.appendChild(line);
  });
}

function addFleetRow(platform, k8s) {
  fleetRows.push({
    seq: nextRowSeq,
    label: `Cluster ${fleetRows.length + 1}`,
    platform: platform || "rke2",
    k8s: k8s || "",
  });
  nextRowSeq += 1;
  renderFleetRows();
}

addFleetRow("rke2", "");
document.getElementById("addCluster").addEventListener("click", () => addFleetRow("rke2", ""));

document.getElementById("planButton").addEventListener("click", async () => {
  const out = document.getElementById("planOutput");
  const value = (id) => document.getElementById(id).value.trim();

  const fields = {
    rancher: value("currentRancher"),
    local_platform: value("localPlatform"),
    local_k8s: value("localK8s"),
  };

  const missing = Object.entries(fields).filter(([, v]) => !v).map(([k]) => k);

  // Indexed downstream parameters, one per cluster. Indexes start at 1 and must be
  // contiguous, which is why empty rows are a refusal rather than being skipped:
  // silently dropping row 2 would plan a fleet the user did not describe.
  fleetRows.forEach((row, i) => {
    const n = i + 1;
    if (!row.k8s.trim()) missing.push(`cluster ${n} Kubernetes version`);
    fields[`downstream_platform_${n}`] = row.platform;
    fields[`downstream_k8s_${n}`] = row.k8s.trim();
    if (row.label.trim()) fields[`downstream_label_${n}`] = row.label.trim();
  });

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
