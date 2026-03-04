const UI = StatocystUI;

let currentHumanID = "";

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

function setInviteCodeStatus(message, warn = false) {
  const el = UI.$("inviteCodeStatus");
  if (!el) return;
  if (!message) {
    el.style.display = "none";
    el.textContent = "";
    return;
  }
  el.style.display = "block";
  el.textContent = message;
  el.className = warn ? "status warn" : "status";
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

function escapeHTML(input) {
  return String(input || "")
    .replace(/&/g, "&amp;")
    .replace(/</g, "&lt;")
    .replace(/>/g, "&gt;")
    .replace(/\"/g, "&quot;")
    .replace(/'/g, "&#39;");
}

function formatDateTime(raw) {
  if (!raw) return "unknown";
  const d = new Date(raw);
  if (Number.isNaN(d.getTime())) return "unknown";
  return d.toLocaleString();
}

function titleCaseInviteStatus(status) {
  const v = String(status || "").toLowerCase();
  if (v === "pending") return "Pending";
  if (v === "active") return "Accepted";
  if (v === "revoked") return "Revoked";
  if (v === "expired") return "Expired";
  return v || "Unknown";
}

async function loadCurrentHuman() {
  const res = await UI.req("/v1/me");
  if (res.status === 200) {
    currentHumanID = String(res?.data?.human?.human_id || "");
  }
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
    UI.$("partnerOrgName").value = orgNameByID(select.value);
    await refreshOrgData();
  } else {
    renderHumans([]);
    renderOrgInvites([]);
    renderTrusts([]);
    renderAudit([]);
    renderStats(null);
    renderAccessKeys([]);
    setInviteCodeStatus("");
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
    const err = String(res?.data?.error || "");
    if (res.status === 409 || err === "org_name_exists") {
      setStatus("orgStatus", "Organization name already exists. Choose a different name.", true);
      return;
    }
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

  setStatus("inviteStatus", "Creating invite code...");
  setInviteCodeStatus("");
  const res = await UI.req(`/v1/orgs/${orgID}/invites`, "POST", { email, role });
  if (res.status !== 201) {
    setStatus("inviteStatus", "Could not create invite.", true);
    setInviteCodeStatus("Invite creation failed.", true);
    return;
  }

  UI.$("inviteEmail").value = "";
  const invite = res.data?.invite || {};
  const inviteCode = String(res.data?.invite_code || "");
  setStatus("inviteStatus", `Invite created for ${email}.`);
  if (inviteCode) {
    const inviteLink = `${window.location.origin}/?invite=${encodeURIComponent(inviteCode)}`;
    setInviteCodeStatus(`Share this invite link with ${email}: ${inviteLink} (expires ${formatDateTime(invite.expires_at)}).`);
  }
  await Promise.all([loadOrgInvites(), loadHumans()]);
}

function renderOrgInvites(invites) {
  const root = UI.$("orgInvitesList");
  root.innerHTML = "";

  if (!Array.isArray(invites) || invites.length === 0) {
    setMuted("orgInvitesStatus", "No invites yet.");
    return;
  }

  setMuted("orgInvitesStatus", `${invites.length} invite(s).`);
  for (const invite of invites) {
    const card = document.createElement("div");
    card.className = "rowItem";

    const title = document.createElement("div");
    title.className = "rowTitle";
    title.textContent = `${invite.email || "(email missing)"} • ${invite.role || "member"}`;

    const meta = document.createElement("div");
    meta.className = "rowMeta";
    meta.textContent = `${titleCaseInviteStatus(invite.status)} • Created ${formatDateTime(invite.created_at)} • Expires ${formatDateTime(invite.expires_at)}`;

    card.appendChild(title);
    card.appendChild(meta);

    if (String(invite.status).toLowerCase() === "pending") {
      const revokeBtn = document.createElement("button");
      revokeBtn.type = "button";
      revokeBtn.className = "smallBtn";
      revokeBtn.textContent = "Revoke Invite";
      revokeBtn.dataset.revokeInviteId = invite.invite_id || "";
      card.appendChild(revokeBtn);
    }

    root.appendChild(card);
  }
}

async function loadOrgInvites() {
  const orgID = requireOrg("orgInvitesStatus", "Select an organization to load invites.");
  if (!orgID) {
    renderOrgInvites([]);
    return;
  }

  setMuted("orgInvitesStatus", "Loading invites...");
  const res = await UI.req(`/v1/orgs/${orgID}/invites`);
  if (res.status !== 200) {
    setMuted("orgInvitesStatus", "Could not load invites.");
    renderOrgInvites([]);
    return;
  }

  renderOrgInvites(res.data?.invites || []);
}

async function revokeInvite(inviteID) {
  if (!inviteID) return;

  setStatus("inviteStatus", "Revoking invite...");
  const res = await UI.req(`/v1/org-invites/${inviteID}`, "DELETE");
  if (res.status !== 200) {
    setStatus("inviteStatus", "Could not revoke invite.", true);
    return;
  }

  setStatus("inviteStatus", "Invite revoked.");
  await loadOrgInvites();
}

function renderHumans(humans) {
  const root = UI.$("humansList");
  root.innerHTML = "";

  if (!Array.isArray(humans) || humans.length === 0) {
    setMuted("humansStatus", "No humans yet.");
    return;
  }

  setMuted("humansStatus", `${humans.length} human(s) in this organization.`);
  for (const h of humans) {
    const card = document.createElement("div");
    card.className = "rowItem";

    const title = document.createElement("div");
    title.className = "rowTitle";
    title.textContent = h.email || h.human_id || "unknown";

    const meta = document.createElement("div");
    meta.className = "rowMeta";
    meta.textContent = `${h.role || "unknown"} • ${h.status || "unknown"} • ${h.auth_provider || "unknown provider"}`;

    card.appendChild(title);
    card.appendChild(meta);

    if (h.role !== "owner" && h.human_id !== currentHumanID) {
      const revokeBtn = document.createElement("button");
      revokeBtn.type = "button";
      revokeBtn.className = "smallBtn";
      revokeBtn.textContent = "Revoke Human";
      revokeBtn.dataset.revokeHumanId = h.human_id || "";
      revokeBtn.dataset.revokeHumanEmail = h.email || "";
      card.appendChild(revokeBtn);
    }

    root.appendChild(card);
  }
}

async function loadHumans() {
  const orgID = requireOrg("humansStatus", "Select an organization to load humans.");
  if (!orgID) {
    renderHumans([]);
    return;
  }

  setMuted("humansStatus", "Loading humans...");
  const res = await UI.req(`/v1/orgs/${orgID}/humans`);
  if (res.status !== 200) {
    setMuted("humansStatus", "Could not load humans.");
    renderHumans([]);
    return;
  }

  renderHumans(res.data.humans || []);
}

async function revokeHuman(humanID, humanEmail) {
  if (!humanID) return;
  const orgID = requireOrg("humansStatus", "Select an organization first.");
  if (!orgID) return;

  setMuted("humansStatus", `Revoking ${humanEmail || humanID}...`);
  const res = await UI.req(`/v1/orgs/${orgID}/humans/${encodeURIComponent(humanID)}`, "DELETE");
  if (res.status !== 200) {
    setStatus("humansStatus", "Could not revoke human.", true);
    return;
  }

  setMuted("humansStatus", `Revoked ${humanEmail || humanID}.`);
  await Promise.all([loadHumans(), listOrgs(true)]);
}

function selectedScopes() {
  const scopes = [];
  if (UI.$("scopeListHumans").checked) scopes.push("list_humans");
  if (UI.$("scopeListAgents").checked) scopes.push("list_agents");
  return scopes;
}

function renderAccessKeys(keys) {
  const root = UI.$("accessKeysList");
  root.innerHTML = "";

  if (!Array.isArray(keys) || keys.length === 0) {
    const empty = document.createElement("div");
    empty.className = "muted";
    empty.textContent = "No access keys yet.";
    root.appendChild(empty);
    return;
  }

  for (const key of keys) {
    const card = document.createElement("div");
    card.className = "card";

    const title = document.createElement("div");
    title.textContent = `${key.label || "Access Key"} (${key.status || "unknown"})`;

    const meta = document.createElement("div");
    meta.className = "muted";
    const scopes = Array.isArray(key.scopes) ? key.scopes.join(", ") : "none";
    const exp = key.expires_at ? new Date(key.expires_at).toLocaleString() : "never";
    meta.textContent = `Scopes: ${scopes} • Expires: ${exp}`;

    card.appendChild(title);
    card.appendChild(meta);

    if (key.status === "active") {
      const actions = document.createElement("div");
      actions.className = "inlineActions";
      const revokeBtn = document.createElement("button");
      revokeBtn.type = "button";
      revokeBtn.textContent = "Revoke";
      revokeBtn.dataset.revokeKeyId = key.key_id || "";
      actions.appendChild(revokeBtn);
      card.appendChild(actions);
    }

    root.appendChild(card);
  }
}

async function loadAccessKeys() {
  const orgID = requireOrg("accessKeyStatus", "Select an organization to manage access keys.");
  if (!orgID) return;

  const res = await UI.req(`/v1/orgs/${orgID}/access-keys`);
  if (res.status !== 200) {
    setStatus("accessKeyStatus", "Could not load access keys.", true);
    renderAccessKeys([]);
    return;
  }
  renderAccessKeys(res.data.access_keys || []);
}

async function createAccessKey() {
  const orgID = requireOrg("accessKeyStatus", "Select an organization to create an access key.");
  if (!orgID) return;

  const scopes = selectedScopes();
  if (scopes.length === 0) {
    setStatus("accessKeyStatus", "Select at least one scope.", true);
    return;
  }

  const label = UI.$("accessKeyLabel").value.trim();
  const expiryRaw = UI.$("accessKeyExpiryDays").value.trim();
  const payload = { label, scopes };
  if (expiryRaw) {
    const days = Number(expiryRaw);
    if (!Number.isFinite(days) || days < 1 || days > 3650) {
      setStatus("accessKeyStatus", "Expiry days must be in range 1..3650.", true);
      return;
    }
    payload.expires_in_days = Math.floor(days);
  }

  setStatus("accessKeyStatus", "Creating access key...");
  const res = await UI.req(`/v1/orgs/${orgID}/access-keys`, "POST", payload);
  if (res.status !== 201) {
    setStatus("accessKeyStatus", "Could not create access key.", true);
    UI.$("accessKeySecret").textContent = "";
    return;
  }

  const secret = res.data.key || "";
  UI.$("accessKeySecret").innerHTML = secret
    ? `Key (copy now): <span class="keySecret">${escapeHTML(secret)}</span>`
    : "";
  setStatus("accessKeyStatus", "Access key created.");
  await loadAccessKeys();
}

async function revokeAccessKey(keyID) {
  if (!keyID) return;
  const orgID = requireOrg("accessKeyStatus");
  if (!orgID) return;

  setStatus("accessKeyStatus", "Revoking access key...");
  const res = await UI.req(`/v1/orgs/${orgID}/access-keys/${keyID}`, "DELETE");
  if (res.status !== 200) {
    setStatus("accessKeyStatus", "Could not revoke access key.", true);
    return;
  }
  setStatus("accessKeyStatus", "Access key revoked.");
  await loadAccessKeys();
}

async function partnerReq(kind) {
  const orgName = UI.$("partnerOrgName").value.trim();
  const orgKey = UI.$("partnerOrgKey").value.trim();
  if (!orgName || !orgKey) {
    setStatus("partnerStatus", "Organization name and key are required.", true);
    return null;
  }

  const res = await fetch(`/v1/org-access/${kind}?org_name=${encodeURIComponent(orgName)}`, {
    headers: {
      "X-Org-Access-Key": orgKey,
    },
  });
  const text = await res.text();
  let data = text;
  try {
    data = JSON.parse(text || "{}");
  } catch (_) {}
  return { status: res.status, data };
}

function renderPartnerList(kind, payload) {
  const root = UI.$("partnerList");
  root.innerHTML = "";

  const items = Array.isArray(payload?.[kind]) ? payload[kind] : [];
  if (items.length === 0) {
    const empty = document.createElement("div");
    empty.className = "muted";
    empty.textContent = "No data yet.";
    root.appendChild(empty);
    return;
  }

  const ul = document.createElement("ul");
  ul.className = "list";
  for (const item of items) {
    const li = document.createElement("li");
    if (kind === "humans") {
      li.textContent = `${item.email || item.human_id} (${item.role || "unknown"})`;
    } else {
      li.textContent = `${item.agent_id || "agent"} (${item.status || "unknown"})`;
    }
    ul.appendChild(li);
  }
  root.appendChild(ul);
}

async function runPartnerQuery(kind) {
  setStatus("partnerStatus", "Loading partner access...");
  const res = await partnerReq(kind);
  if (!res || res.status !== 200) {
    setStatus("partnerStatus", "Partner access request failed.", true);
    UI.$("partnerList").innerHTML = "";
    return;
  }
  setStatus("partnerStatus", `Partner ${kind} loaded.`);
  renderPartnerList(kind, res.data);
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
  await Promise.all([loadHumans(), loadOrgInvites(), refreshMetrics(), loadAccessKeys()]);
}

async function init() {
  UI.initTopNav();
  await loadCurrentHuman();

  UI.$("btnCreateOrg").onclick = createOrg;
  UI.$("btnInvite").onclick = inviteHuman;
  UI.$("btnRefreshMetrics").onclick = refreshMetrics;
  UI.$("btnCreateAccessKey").onclick = createAccessKey;
  UI.$("btnPartnerHumans").onclick = () => runPartnerQuery("humans");
  UI.$("btnPartnerAgents").onclick = () => runPartnerQuery("agents");
  UI.$("orgSelect").onchange = async () => {
    UI.$("partnerOrgName").value = orgNameByID(selectedOrg());
    setInviteCodeStatus("");
    await refreshOrgData();
  };

  UI.$("accessKeysList").addEventListener("click", async (event) => {
    const button = event.target.closest("button[data-revoke-key-id]");
    if (!button) return;
    await revokeAccessKey(button.dataset.revokeKeyId || "");
  });

  UI.$("orgInvitesList").addEventListener("click", async (event) => {
    const button = event.target.closest("button[data-revoke-invite-id]");
    if (!button) return;
    await revokeInvite(button.dataset.revokeInviteId || "");
  });

  UI.$("humansList").addEventListener("click", async (event) => {
    const button = event.target.closest("button[data-revoke-human-id]");
    if (!button) return;
    await revokeHuman(button.dataset.revokeHumanId || "", button.dataset.revokeHumanEmail || "");
  });

  await listOrgs(false);
}

init().catch((err) => {
  setStatus("orgStatus", `Unexpected error: ${String(err)}`, true);
});
