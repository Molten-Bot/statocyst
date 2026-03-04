const UI = StatocystUI;

function selectedOrg() {
  return UI.selectedOrg("orgSelect");
}

function setStatus(id, message, warn = false) {
  const el = UI.$(id);
  if (!el) return;
  el.textContent = message;
  el.className = warn ? "status warn" : "status";
}

function setMuted(id, message) {
  const el = UI.$(id);
  if (!el) return;
  el.textContent = message;
  el.className = "muted";
}

function requireOrg(statusID, message = "Select an organization first.") {
  const orgID = selectedOrg();
  if (!orgID) {
    setMuted(statusID, message);
    return "";
  }
  return orgID;
}

function orgNameByID(orgID) {
  const select = UI.$("orgSelect");
  const option = [...(select?.options || [])].find((opt) => opt.value === orgID);
  if (!option) return orgID;
  const label = option.textContent || orgID;
  const idx = label.lastIndexOf(" (");
  if (idx > 0) return label.slice(0, idx);
  return label;
}

async function listOrgs(preserveCurrent = true) {
  const res = await UI.req("/v1/me/orgs");
  if (res.status !== 200 || !Array.isArray(res.data.memberships)) {
    setStatus("orgStatus", "Could not load organizations.", true);
    UI.$("orgList").innerHTML = "";
    UI.$("orgSelect").innerHTML = "";
    return;
  }

  const current = selectedOrg();
  const memberships = res.data.memberships;

  const orgList = UI.$("orgList");
  orgList.innerHTML = "";
  if (memberships.length === 0) {
    const li = document.createElement("li");
    li.textContent = "No organizations yet.";
    orgList.appendChild(li);
    setStatus("orgStatus", "No organizations yet.");
  } else {
    for (const item of memberships) {
      const li = document.createElement("li");
      li.textContent = `${item.org.name} (${item.membership.role})`;
      orgList.appendChild(li);
    }
    setStatus("orgStatus", `${memberships.length} organization(s) loaded.`);
  }

  const select = UI.$("orgSelect");
  select.innerHTML = "";
  for (const item of memberships) {
    const opt = document.createElement("option");
    opt.value = item.org.org_id;
    opt.textContent = `${item.org.name} (${item.membership.role})`;
    select.appendChild(opt);
  }

  if (memberships.length > 0) {
    if (preserveCurrent && current && memberships.some((m) => m.org.org_id === current)) {
      select.value = current;
    } else {
      select.value = memberships[0].org.org_id;
    }
    await refreshOrgData();
  } else {
    renderHumans([]);
    renderTrusts([]);
    renderAudit([]);
    renderStats(null);
  }
}

async function createOrg() {
  const name = UI.$("orgName").value.trim();
  if (!name) {
    setStatus("orgStatus", "Organization name is required.", true);
    return;
  }

  setStatus("orgStatus", "Creating organization...");
  const res = await UI.req("/v1/orgs", "POST", { name });
  if (res.status !== 201) {
    setStatus("orgStatus", "Could not create organization.", true);
    return;
  }

  UI.$("orgName").value = "";
  await listOrgs(false);
}

async function inviteHuman() {
  const orgID = requireOrg("inviteStatus");
  if (!orgID) return;

  const email = UI.$("inviteEmail").value.trim();
  const role = UI.$("inviteRole").value;
  if (!email) {
    setStatus("inviteStatus", "Email is required.", true);
    return;
  }

  setStatus("inviteStatus", "Sending invite...");
  const res = await UI.req(`/v1/orgs/${orgID}/invites`, "POST", { email, role });
  if (res.status !== 201) {
    setStatus("inviteStatus", "Could not create invite.", true);
    return;
  }

  UI.$("inviteEmail").value = "";
  setStatus("inviteStatus", `Invite sent to ${email}.`);
  await loadHumans();
}

function renderHumans(humans) {
  const root = UI.$("humansList");
  root.innerHTML = "";

  if (!Array.isArray(humans) || humans.length === 0) {
    setMuted("humansStatus", "No humans yet.");
    return;
  }

  setMuted("humansStatus", `${humans.length} human(s) in this organization.`);
  const ul = document.createElement("ul");
  ul.className = "list";
  for (const h of humans) {
    const li = document.createElement("li");
    li.textContent = `${h.email} (${h.role})`;
    ul.appendChild(li);
  }
  root.appendChild(ul);
}

async function loadHumans() {
  const orgID = requireOrg("humansStatus", "Select an organization to load humans.");
  if (!orgID) return;

  setMuted("humansStatus", "Loading humans...");
  const res = await UI.req(`/v1/orgs/${orgID}/humans`);
  if (res.status !== 200) {
    setMuted("humansStatus", "Could not load humans.");
    renderHumans([]);
    return;
  }

  renderHumans(res.data.humans || []);
}

function renderStats(statsPayload) {
  const kpiQueued = UI.$("kpiQueued");
  const kpiDropped = UI.$("kpiDropped");
  const empty = UI.$("statsEmpty");
  const hasD3 = typeof window.d3 !== "undefined";
  const svg = hasD3 ? window.d3.select("#statsChart") : null;
  if (svg) {
    svg.selectAll("*").remove();
  }

  if (!statsPayload || !statsPayload.stats) {
    kpiQueued.textContent = "-";
    kpiDropped.textContent = "-";
    empty.style.display = "block";
    return;
  }

  const stats = statsPayload.stats;
  kpiQueued.textContent = String(stats.queued_messages ?? 0);
  kpiDropped.textContent = String(stats.dropped_messages ?? 0);

  const points = Array.isArray(stats.last_7_days) ? stats.last_7_days : [];
  const hasData = points.some((p) => (p.queued_messages || 0) > 0 || (p.dropped_messages || 0) > 0);
  if (!hasData || !hasD3) {
    if (!hasD3) {
      empty.textContent = "Chart unavailable (D3 failed to load).";
    } else {
      empty.textContent = "No data yet.";
    }
    empty.style.display = "block";
    return;
  }
  empty.style.display = "none";

  const width = 560;
  const height = 220;
  const margin = { top: 10, right: 10, bottom: 24, left: 36 };

  const x = window.d3
    .scalePoint()
    .domain(points.map((d) => d.date.slice(5)))
    .range([margin.left, width - margin.right]);

  const maxY = window.d3.max(points, (d) => Math.max(d.queued_messages || 0, d.dropped_messages || 0)) || 1;
  const y = window.d3.scaleLinear().domain([0, maxY]).nice().range([height - margin.bottom, margin.top]);

  const lineQueued = window.d3
    .line()
    .x((d) => x(d.date.slice(5)))
    .y((d) => y(d.queued_messages || 0));

  const lineDropped = window.d3
    .line()
    .x((d) => x(d.date.slice(5)))
    .y((d) => y(d.dropped_messages || 0));

  svg
    .append("g")
    .attr("transform", `translate(0,${height - margin.bottom})`)
    .call(window.d3.axisBottom(x));

  svg
    .append("g")
    .attr("transform", `translate(${margin.left},0)`)
    .call(window.d3.axisLeft(y).ticks(4).tickFormat(window.d3.format("d")));

  svg
    .append("path")
    .datum(points)
    .attr("fill", "none")
    .attr("stroke", "#0b7285")
    .attr("stroke-width", 2)
    .attr("d", lineQueued);

  svg
    .append("path")
    .datum(points)
    .attr("fill", "none")
    .attr("stroke", "#ef4444")
    .attr("stroke-width", 2)
    .attr("d", lineDropped);
}

function renderTrusts(graphPayload) {
  const root = UI.$("trustList");
  root.innerHTML = "";

  if (!graphPayload || !Array.isArray(graphPayload.org_trusts)) {
    setMuted("trustStatus", "No data yet.");
    return;
  }

  const active = graphPayload.org_trusts.filter(
    (edge) => edge.state === "active" && edge.left_approved && edge.right_approved
  );

  if (active.length === 0) {
    setMuted("trustStatus", "No bi-directional trusted organization links yet.");
    return;
  }

  setMuted("trustStatus", `${active.length} trusted link(s).`);
  for (const edge of active) {
    const li = document.createElement("li");
    const left = edge.left_id === selectedOrg() ? orgNameByID(edge.left_id) : edge.left_id;
    const right = edge.right_id === selectedOrg() ? orgNameByID(edge.right_id) : edge.right_id;
    li.textContent = `${left} ↔ ${right}`;
    root.appendChild(li);
  }
}

function renderAudit(auditPayload) {
  const root = UI.$("auditList");
  root.innerHTML = "";

  if (!auditPayload || !Array.isArray(auditPayload.events)) {
    setMuted("auditStatus", "No data yet.");
    return;
  }

  const recent = [...auditPayload.events].slice(-10).reverse();
  if (recent.length === 0) {
    setMuted("auditStatus", "No data yet.");
    return;
  }

  setMuted("auditStatus", `${recent.length} recent event(s).`);
  for (const ev of recent) {
    const card = document.createElement("div");
    card.className = "card";

    const title = document.createElement("div");
    title.textContent = `${ev.category}:${ev.action}`;

    const meta = document.createElement("div");
    meta.className = "muted";
    const when = ev.created_at ? new Date(ev.created_at).toLocaleString() : "unknown time";
    meta.textContent = `${when} • ${ev.subject_id || ""}`;

    card.appendChild(title);
    card.appendChild(meta);
    root.appendChild(card);
  }
}

async function refreshMetrics() {
  const orgID = requireOrg("trustStatus", "Select an organization to load metrics.");
  if (!orgID) return;

  const [statsRes, trustRes, auditRes] = await Promise.all([
    UI.req(`/v1/orgs/${orgID}/stats`),
    UI.req(`/v1/orgs/${orgID}/trust-graph`),
    UI.req(`/v1/orgs/${orgID}/audit`),
  ]);

  renderStats(statsRes.status === 200 ? statsRes.data : null);
  renderTrusts(trustRes.status === 200 ? trustRes.data : null);
  renderAudit(auditRes.status === 200 ? auditRes.data : null);
}

async function refreshOrgData() {
  await Promise.all([loadHumans(), refreshMetrics()]);
}

async function init() {
  UI.initTopNav();

  UI.$("btnCreateOrg").onclick = createOrg;
  UI.$("btnInvite").onclick = inviteHuman;
  UI.$("btnRefreshMetrics").onclick = refreshMetrics;
  UI.$("orgSelect").onchange = refreshOrgData;

  await listOrgs(false);
}

init().catch((err) => {
  setStatus("orgStatus", `Unexpected error: ${String(err)}`, true);
});
