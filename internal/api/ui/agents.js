const UI = MoltenHubUI;
let agentOrgByUUID = {};
let agentsByUUID = {};
let latestBindPrompt = "";
let activeAgentProfileUUID = "";

function setStatus(id, message, warn = false) {
  const el = UI.$(id);
  if (!el) return;
  el.textContent = message;
  el.className = warn ? "status warn" : "status";
}

function setBindCodeStatus(message, warn = false) {
  const el = UI.$("bindCodeStatus");
  if (!el) return;
  el.textContent = message;
  el.className = warn ? "status warn code" : "status code";
}

function setCopyPromptEnabled(enabled) {
  const button = UI.$("btnCopyBindPrompt");
  if (!button) return;
  button.disabled = !enabled;
}

function metadataFrom(raw) {
  if (!raw || typeof raw !== "object" || Array.isArray(raw)) return {};
  return { ...raw };
}

function metadataPublic(raw) {
  const value = metadataFrom(raw).public;
  return typeof value === "boolean" ? value : true;
}

function metadataHireMe(raw) {
  const value = metadataFrom(raw).hire_me;
  return typeof value === "boolean" ? value : false;
}

function metadataProfileMarkdown(raw) {
  const value = metadataFrom(raw).profile_markdown;
  if (typeof value !== "string") return "";
  return value.trim();
}

function metadataDisplayName(raw) {
  const value = metadataFrom(raw).display_name;
  if (typeof value !== "string") return "";
  return value.trim();
}

function metadataEmoji(raw) {
  const value = metadataFrom(raw).emoji;
  if (typeof value !== "string") return "";
  return value.trim();
}

function metadataPresence(raw) {
  const value = metadataFrom(raw).presence;
  if (!value || typeof value !== "object" || Array.isArray(value)) return {};
  return { ...value };
}

function metadataActivities(raw) {
  const value = metadataFrom(raw).activities;
  if (!Array.isArray(value)) return [];
  const out = [];
  for (const entry of value) {
    if (typeof entry === "string") {
      const text = entry.trim();
      if (text) out.push(text);
      continue;
    }
    if (!entry || typeof entry !== "object" || Array.isArray(entry)) continue;
    const text = String(entry.text || entry.title || entry.activity || "").trim();
    const at = String(entry.at || entry.timestamp || "").trim();
    if (!text) continue;
    out.push(at ? `${text} (${at})` : text);
  }
  return out;
}

function metadataSkills(raw) {
  const value = metadataFrom(raw).skills;
  if (!Array.isArray(value)) return [];
  const names = [];
  for (const entry of value) {
    if (!entry || typeof entry !== "object" || Array.isArray(entry)) continue;
    const name = String(entry.name || "").trim();
    if (!name) continue;
    names.push(name);
  }
  return names;
}

function truncateText(value, maxLen) {
  const raw = String(value || "").trim();
  if (raw.length <= maxLen) return raw;
  return `${raw.slice(0, Math.max(0, maxLen - 1)).trimEnd()}…`;
}

async function copyTextToClipboard(text) {
  if (navigator?.clipboard?.writeText) {
    await navigator.clipboard.writeText(text);
    return;
  }

  const fallback = document.createElement("textarea");
  fallback.value = text;
  fallback.setAttribute("readonly", "true");
  fallback.style.position = "fixed";
  fallback.style.opacity = "0";
  document.body.appendChild(fallback);
  fallback.select();
  const copied = document.execCommand("copy");
  document.body.removeChild(fallback);
  if (!copied) throw new Error("clipboard copy failed");
}

async function copyBindPrompt() {
  if (!latestBindPrompt) {
    setBindCodeStatus("No prompt available to copy yet.", true);
    return;
  }
  try {
    await copyTextToClipboard(latestBindPrompt);
    setBindCodeStatus("Agent self-signup prompt copied to clipboard.");
  } catch (err) {
    setBindCodeStatus(`Could not copy prompt: ${String(err)}`, true);
  }
}

function agentOwnerLabel(agent) {
  const ownerHuman = agent?.owner?.human_id || agent?.owner_human_id;
  if (ownerHuman) return ownerHuman;
  return "org-owned";
}

function setAgentProfileError(message) {
  const el = UI.$("agentProfileFooterError");
  if (!el) return;
  el.textContent = String(message || "").trim();
}

function closeAgentProfileDialog() {
  const dialog = UI.$("agentProfileDialog");
  if (!dialog) return;
  activeAgentProfileUUID = "";
  setAgentProfileError("");
  hideEmojiPicker();
  if (typeof dialog.close === "function" && dialog.open) {
    dialog.close();
  }
}

function openAgentProfileDialog(agentUUID) {
  const agent = agentsByUUID[agentUUID];
  const dialog = UI.$("agentProfileDialog");
  if (!agent || !dialog) return;

  activeAgentProfileUUID = agentUUID;
  setAgentProfileError("");
  UI.$("agentProfileHandle").value = String(agent.handle || "").trim();
  UI.$("agentProfileDisplayName").value = metadataDisplayName(agent.metadata);
  UI.$("agentProfileEmoji").value = metadataEmoji(agent.metadata);
  UI.$("agentProfileMarkdown").value = metadataProfileMarkdown(agent.metadata);
  if (typeof dialog.showModal === "function") {
    dialog.showModal();
  }
}

function hideEmojiPicker() {
  const panel = UI.$("agentProfileEmojiPickerPanel");
  if (panel) panel.hidden = true;
}

function toggleEmojiPicker() {
  const panel = UI.$("agentProfileEmojiPickerPanel");
  if (!panel) return;
  const nextHidden = !panel.hidden;
  panel.hidden = nextHidden;
}

function bindEmojiPicker() {
  const picker = UI.$("agentProfileEmojiPicker");
  const fallback = UI.$("agentProfileEmojiPickerFallback");
  if (!picker) return;

  if (!window.customElements || !customElements.whenDefined) {
    if (fallback) fallback.hidden = false;
    return;
  }

  customElements.whenDefined("emoji-picker")
    .then(() => {
      picker.addEventListener("emoji-click", (event) => {
        UI.$("agentProfileEmoji").value = String(event?.detail?.unicode || "").trim();
        hideEmojiPicker();
      });
    })
    .catch(() => {
      if (fallback) fallback.hidden = false;
    });
}

function errorMessageFrom(result, fallbackMessage) {
  const message = String(result?.data?.message || result?.data?.error || "").trim();
  return message || fallbackMessage;
}

async function loadBindOrganizations() {
  const select = UI.$("bindOrgSelect");
  if (!select) return;

  const result = await UI.req("/v1/me/orgs");
  if (result.status !== 200 || !Array.isArray(result?.data?.memberships)) {
    setBindCodeStatus("Could not load organizations.", true);
    return;
  }

  select.innerHTML = "";
  const personal = document.createElement("option");
  personal.value = "";
  personal.textContent = "Personal (human-owned)";
  select.appendChild(personal);

  for (const membership of result.data.memberships) {
    const org = membership?.org;
    const role = membership?.membership?.role || "member";
    if (!org?.org_id) continue;
    const option = document.createElement("option");
    option.value = org.org_id;
    option.textContent = `${org.display_name || org.handle} (${role})`;
    select.appendChild(option);
  }

  select.value = "";
  latestBindPrompt = "";
  setCopyPromptEnabled(false);
  setBindCodeStatus("No bind code issued yet.");
}

function formatDateTime(raw) {
  if (!raw) return "unknown";
  const d = new Date(raw);
  if (Number.isNaN(d.getTime())) return String(raw);
  return d.toLocaleString();
}

function buildAgentBindPrompt(bindToken, expiresAt, redeemURL) {
  return [
    "Agent Self-Signup Prompt",
    "",
    "Goal: self-register this agent with MoltenHub and return the new token.",
    "",
    `Bind token: ${bindToken}`,
    `Expires: ${expiresAt}`,
    `Bind API URL: ${redeemURL}`,
    "",
    "Steps:",
    "1. Redeem token with a preferred handle:",
    "",
    `curl -sS -X POST "${redeemURL}" \\`,
    "  -H 'Content-Type: application/json' \\",
    `  -d '{"bind_token":"${bindToken}","handle":"<your-agent-handle>"}'`,
    "",
    "2. If response is `agent_exists`, retry with another handle variation.",
    "3. Persist returned `token`, exact `api_base`, and `endpoints`.",
    "4. Call GET {api_base}/agents/me/skill and follow that onboarding guidance.",
    "",
    "Optional OpenClaw-only hints (not required):",
    "- Install plugin package `@moltenbot/openclaw-plugin-moltenhub` if supported.",
    "- Optional config file: workspace/.moltenhub/config.json",
    '  {"baseUrl":"<api_base>","token":"<token>","sessionKey":"main","timeoutMs":20000}',
  ].join("\n");
}

function syncBondSelectors(agents) {
  const left = UI.$("trustAgentId");
  const right = UI.$("trustPeerAgentId");
  if (!left || !right) return;

  const leftCurrent = left.value;
  const rightCurrent = right.value;
  left.innerHTML = "";
  right.innerHTML = "";

  const activeAgents = (Array.isArray(agents) ? agents : []).filter((agent) => String(agent?.status || "").toLowerCase() !== "revoked");
  if (activeAgents.length === 0) {
    const optLeft = document.createElement("option");
    optLeft.value = "";
    optLeft.textContent = "No active agents available";
    left.appendChild(optLeft);

    const optRight = document.createElement("option");
    optRight.value = "";
    optRight.textContent = "No active agents available";
    right.appendChild(optRight);

    left.disabled = true;
    right.disabled = true;
    return;
  }

  left.disabled = false;
  right.disabled = false;

  for (const agent of activeAgents) {
    const text = `${agent.agent_id || ""} [${agent.agent_uuid || ""}] (${agentOwnerLabel(agent)})`;
    const value = agent.agent_uuid || "";

    const leftOpt = document.createElement("option");
    leftOpt.value = value;
    leftOpt.textContent = text;
    left.appendChild(leftOpt);

    const rightOpt = document.createElement("option");
    rightOpt.value = value;
    rightOpt.textContent = text;
    right.appendChild(rightOpt);
  }

  if (leftCurrent && [...left.options].some((opt) => opt.value === leftCurrent)) {
    left.value = leftCurrent;
  }
  if (rightCurrent && [...right.options].some((opt) => opt.value === rightCurrent)) {
    right.value = rightCurrent;
  }
  if (!left.value && left.options.length > 0) {
    left.value = left.options[0].value;
  }
  if (!right.value && right.options.length > 1) {
    right.value = right.options[1].value;
  } else if (!right.value && right.options.length > 0) {
    right.value = right.options[0].value;
  }
}

function renderAgents(agents) {
  const body = UI.$("agentsBody");
  body.innerHTML = "";
  agentOrgByUUID = {};
  agentsByUUID = {};

  if (!Array.isArray(agents) || agents.length === 0) {
    const tr = document.createElement("tr");
    const td = document.createElement("td");
    td.colSpan = 10;
    td.className = "muted";
    td.textContent = "No agents yet.";
    tr.appendChild(td);
    body.appendChild(tr);
    setStatus("agentStatus", "No agents found.");
    syncBondSelectors([]);
    return;
  }

  for (const agent of agents) {
    const agentUUID = String(agent?.agent_uuid || "").trim();
    if (agentUUID) {
      agentOrgByUUID[agentUUID] = String(agent?.org_id || "").trim();
      agentsByUUID[agentUUID] = agent;
    }
    const tr = document.createElement("tr");

    const tdID = document.createElement("td");
    tdID.textContent = `${agent.agent_id || ""}\n${agent.agent_uuid || ""}`;
    tr.appendChild(tdID);

    const tdOrg = document.createElement("td");
    tdOrg.textContent = agent.org_id || "";
    tr.appendChild(tdOrg);

    const tdStatus = document.createElement("td");
    tdStatus.textContent = agent.status || "";
    tr.appendChild(tdStatus);

    const tdOwner = document.createElement("td");
    tdOwner.textContent = agentOwnerLabel(agent);
    tr.appendChild(tdOwner);

    const tdPublic = document.createElement("td");
    const isPublic = metadataPublic(agent.metadata);
    tdPublic.textContent = isPublic ? "public" : "private";
    tr.appendChild(tdPublic);

    const tdHireMe = document.createElement("td");
    tdHireMe.textContent = metadataHireMe(agent.metadata) ? "true" : "false";
    tr.appendChild(tdHireMe);

    const tdProfile = document.createElement("td");
    tdProfile.className = "metadataPreview";
    const profile = metadataProfileMarkdown(agent.metadata);
    tdProfile.textContent = profile ? truncateText(profile, 140) : "-";
    tr.appendChild(tdProfile);

    const tdActivities = document.createElement("td");
    tdActivities.className = "metadataPreview";
    const activities = metadataActivities(agent.metadata);
    tdActivities.textContent = activities.length > 0 ? truncateText(activities.slice(0, 3).join(" | "), 180) : "-";
    tr.appendChild(tdActivities);

    const tdSkills = document.createElement("td");
    tdSkills.className = "metadataPreview";
    const skills = metadataSkills(agent.metadata);
    tdSkills.textContent = skills.length > 0 ? truncateText(skills.join(", "), 140) : "-";
    tr.appendChild(tdSkills);

    const tdActions = document.createElement("td");
    const actionWrap = document.createElement("div");
    actionWrap.className = "row-actions";
    const isRevoked = String(agent.status || "").toLowerCase() === "revoked";

    const editBtn = document.createElement("button");
    editBtn.textContent = "Agent Profile";
    editBtn.className = "secondary";
    editBtn.dataset.agentAction = "edit";
    editBtn.dataset.agentUuid = agent.agent_uuid || "";
    editBtn.disabled = isRevoked;
    actionWrap.appendChild(editBtn);

    const rotateBtn = document.createElement("button");
    rotateBtn.textContent = "Rotate Token";
    rotateBtn.dataset.agentAction = "rotate";
    rotateBtn.dataset.agentUuid = agent.agent_uuid || "";
    rotateBtn.disabled = isRevoked;
    actionWrap.appendChild(rotateBtn);

    const revokeBtn = document.createElement("button");
    revokeBtn.textContent = "Revoke Agent";
    revokeBtn.dataset.agentAction = "revoke";
    revokeBtn.dataset.agentUuid = agent.agent_uuid || "";
    revokeBtn.disabled = isRevoked;
    actionWrap.appendChild(revokeBtn);

    tdActions.appendChild(actionWrap);
    tr.appendChild(tdActions);
    body.appendChild(tr);
  }

  setStatus("agentStatus", `${agents.length} agent(s) loaded.`);
  syncBondSelectors(agents);
}

async function loadAgents() {
  setStatus("agentStatus", "Loading agents...");
  const result = await UI.req("/v1/me/agents");
  if (result.status !== 200 || !Array.isArray(result?.data?.agents)) {
    setStatus("agentStatus", "Could not load agents.", true);
    renderAgents([]);
    return;
  }
  renderAgents(result.data.agents || []);
}

async function saveAgentProfile() {
  const agentUUID = activeAgentProfileUUID;
  const agent = agentsByUUID[agentUUID];
  if (!agentUUID || !agent) return;

  const nextHandle = String(UI.$("agentProfileHandle").value || "").trim();
  const nextDisplayName = String(UI.$("agentProfileDisplayName").value || "").trim();
  const nextEmoji = String(UI.$("agentProfileEmoji").value || "").trim();
  const nextProfile = String(UI.$("agentProfileMarkdown").value || "").trim();
  if (!nextHandle) {
    setAgentProfileError("Handle is required.");
    return;
  }

  setAgentProfileError("");
  const metadata = {
    display_name: nextDisplayName || null,
    emoji: nextEmoji || null,
    profile_markdown: nextProfile || null,
  };
  const payload = { metadata };
  if (nextHandle !== String(agent.handle || "").trim()) {
    payload.handle = nextHandle;
  }

  const result = await UI.req(`/v1/me/agents/${encodeURIComponent(agentUUID)}`, "PATCH", payload);
  if (result.status !== 200) {
    setAgentProfileError(errorMessageFrom(result, "Could not save agent profile."));
    return;
  }

  await loadAgents();
  closeAgentProfileDialog();
}

async function disconnectAgentProfile() {
  const agentUUID = activeAgentProfileUUID;
  const agent = agentsByUUID[agentUUID];
  if (!agentUUID || !agent) return;

  setAgentProfileError("");
  const presence = metadataPresence(agent.metadata);
  const result = await UI.req(`/v1/me/agents/${encodeURIComponent(agentUUID)}/disconnect`, "POST", {
    session_key: String(presence.session_key || "main").trim() || "main",
    reason: "Disconnected from Agents UI",
  });
  if (result.status !== 200) {
    setAgentProfileError(errorMessageFrom(result, "Could not disconnect agent runtime."));
    return;
  }

  await loadAgents();
  closeAgentProfileDialog();
}

async function createBindCode() {
  const orgID = UI.selectedOrg("bindOrgSelect");
  setBindCodeStatus("Creating one-time bind code...");
  setCopyPromptEnabled(false);
  const body = orgID ? { org_id: orgID } : {};
  const result = await UI.req("/v1/me/agents/bind-tokens", "POST", body);
  if (result.status !== 201) {
    setBindCodeStatus("Could not create bind code.", true);
    return;
  }

  const token = String(result?.data?.bind_token || "").trim();
  const connectPrompt = String(result?.data?.connect_prompt || "").trim();
  const expiresAt = formatDateTime(result?.data?.expires_at);
  const redeemURL = `${window.location.origin}/v1/agents/bind`;
  if (!token) {
    latestBindPrompt = "";
    setCopyPromptEnabled(false);
    setBindCodeStatus("Bind code was not returned.", true);
    return;
  }

  latestBindPrompt = connectPrompt || buildAgentBindPrompt(token, expiresAt, redeemURL);
  setCopyPromptEnabled(true);
  setBindCodeStatus(latestBindPrompt);
}

async function rotateAgent(agentUUID) {
  if (!agentUUID) {
    setStatus("agentStatus", "agent_uuid required", true);
    return;
  }

  setStatus("agentStatus", `Rotating token for ${agentUUID}...`);
  const result = await UI.req(`/v1/agents/${encodeURIComponent(agentUUID)}/rotate-token`, "POST");
  if (result.status !== 200) {
    setStatus("agentStatus", "Could not rotate token.", true);
    return;
  }
  setStatus("agentStatus", `Token rotated for ${agentUUID}.`);
}

async function revokeAgent(agentUUID) {
  if (!agentUUID) {
    setStatus("agentStatus", "agent_uuid required", true);
    return;
  }

  setStatus("agentStatus", `Revoking ${agentUUID}...`);
  const result = await UI.req(`/v1/agents/${encodeURIComponent(agentUUID)}`, "DELETE");
  if (result.status !== 200) {
    setStatus("agentStatus", "Could not revoke agent.", true);
    return;
  }

  setStatus("agentStatus", `Revoked ${agentUUID}.`);
  await loadAgents();
  await loadPendingTrusts();
}

function renderPendingRows(edges) {
  const root = UI.$("pendingList");
  root.innerHTML = "";

  if (!Array.isArray(edges) || edges.length === 0) {
    const p = document.createElement("p");
    p.className = "muted";
    p.textContent = "No bonds yet.";
    root.appendChild(p);
    setStatus("pendingStatus", "No bonds found.");
    return;
  }

  setStatus("pendingStatus", `${edges.length} bond(s) loaded.`);

  const table = document.createElement("table");
  const thead = document.createElement("thead");
  thead.innerHTML = "<tr><th>Edge</th><th>Agents</th><th>State</th><th>Route Ready</th><th>Actions</th></tr>";
  table.appendChild(thead);

  const tbody = document.createElement("tbody");
  for (const edge of edges) {
    const tr = document.createElement("tr");

    const tdEdge = document.createElement("td");
    tdEdge.textContent = edge.edge_id || "";

    const tdAgents = document.createElement("td");
    tdAgents.textContent = `${edge.left_id || "?"} -> ${edge.right_id || "?"}`;

    const tdState = document.createElement("td");
    tdState.textContent = `${edge.state || ""} | L:${edge.left_approved ? "Y" : "N"} R:${edge.right_approved ? "Y" : "N"}`;

    const tdTalk = document.createElement("td");
    const state = String(edge.state || "").toLowerCase();
    if (typeof edge.can_talk === "boolean") {
      tdTalk.textContent = edge.can_talk ? "Yes" : "No";
    } else if (state === "active") {
      tdTalk.textContent = "Check caps";
    } else {
      tdTalk.textContent = "No";
    }

    const tdActions = document.createElement("td");
    const wrap = document.createElement("div");
    wrap.className = "row-actions";

    const actions = [
      { label: "Approve", action: "approve" },
      { label: "Block", action: "block" },
      { label: "Revoke", action: "revoke" },
    ];

    for (const item of actions) {
      const btn = document.createElement("button");
      btn.textContent = item.label;
      btn.dataset.edgeId = edge.edge_id || "";
      btn.dataset.action = item.action;
      wrap.appendChild(btn);
    }

    tdActions.appendChild(wrap);
    tr.appendChild(tdEdge);
    tr.appendChild(tdAgents);
    tr.appendChild(tdState);
    tr.appendChild(tdTalk);
    tr.appendChild(tdActions);
    tbody.appendChild(tr);
  }

  table.appendChild(tbody);
  root.appendChild(table);
}

async function loadPendingTrusts() {
  setStatus("pendingStatus", "Loading bonds...");
  const result = await UI.req("/v1/me/agent-trusts");

  if (result.status !== 200 || !Array.isArray(result?.data?.agent_trusts)) {
    setStatus("pendingStatus", "Could not load bonds.", true);
    renderPendingRows([]);
    return;
  }

  renderPendingRows(result.data.agent_trusts || []);
}

async function createTrust() {
  const agentUUID = UI.$("trustAgentId").value.trim();
  const peerAgentUUID = UI.$("trustPeerAgentId").value.trim();
  if (!agentUUID || !peerAgentUUID) {
    setStatus("pendingStatus", "Select both agents.", true);
    return;
  }
  if (agentUUID === peerAgentUUID) {
    setStatus("pendingStatus", "Choose two different agents.", true);
    return;
  }

  setStatus("pendingStatus", "Creating bond...");
  const orgID = agentOrgByUUID[agentUUID] || "";
  const result = await UI.req("/v1/me/agent-trusts", "POST", {
    org_id: orgID,
    agent_uuid: agentUUID,
    peer_agent_uuid: peerAgentUUID,
  });
  if (result.status !== 200 && result.status !== 201) {
    setStatus("pendingStatus", "Could not create bond.", true);
    return;
  }

  await loadPendingTrusts();
}

async function runTrustAction(edgeID, action) {
  let method = "POST";
  let path = `/v1/agent-trusts/${encodeURIComponent(edgeID)}/${action}`;

  if (action === "revoke") {
    method = "DELETE";
    path = `/v1/agent-trusts/${encodeURIComponent(edgeID)}`;
  }

  setStatus("pendingStatus", `${action} in progress...`);
  const result = await UI.req(path, method);
  if (result.status !== 200) {
    setStatus("pendingStatus", `Could not ${action} trust request.`, true);
    return;
  }
  await loadPendingTrusts();
}

async function init() {
  UI.initTopNav();

  UI.$("btnCreateBindCode").onclick = createBindCode;
  UI.$("btnCopyBindPrompt").onclick = copyBindPrompt;
  UI.$("btnCreateTrust").onclick = createTrust;
  UI.$("btnCloseAgentProfile").onclick = closeAgentProfileDialog;
  UI.$("btnSaveAgentProfile").onclick = saveAgentProfile;
  UI.$("btnDisconnectAgentProfile").onclick = disconnectAgentProfile;
  UI.$("btnAgentProfileEmojiPicker").onclick = toggleEmojiPicker;
  UI.$("agentProfileDialog").addEventListener("click", (event) => {
    if (event.target === UI.$("agentProfileDialog")) {
      closeAgentProfileDialog();
    }
  });
  document.addEventListener("click", (event) => {
    const panel = UI.$("agentProfileEmojiPickerPanel");
    const toggle = UI.$("btnAgentProfileEmojiPicker");
    if (!panel || panel.hidden) return;
    if (panel.contains(event.target) || toggle.contains(event.target)) return;
    hideEmojiPicker();
  });
  bindEmojiPicker();

  UI.$("agentsBody").addEventListener("click", async (event) => {
    const button = event.target.closest("button[data-agent-action]");
    if (!button) return;

    const action = button.dataset.agentAction || "";
    const agentUUID = button.dataset.agentUuid || "";
    if (!action || !agentUUID) return;

    if (action === "edit") {
      openAgentProfileDialog(agentUUID);
      return;
    }
    if (action === "rotate") {
      await rotateAgent(agentUUID);
      return;
    }
    if (action === "revoke") {
      await revokeAgent(agentUUID);
      return;
    }
  });

  UI.$("pendingList").addEventListener("click", async (event) => {
    const button = event.target.closest("button[data-action]");
    if (!button) return;

    const edgeID = button.dataset.edgeId || "";
    const action = button.dataset.action || "";
    if (!edgeID || !action) return;

    await runTrustAction(edgeID, action);
  });

  await Promise.all([loadBindOrganizations(), loadAgents(), loadPendingTrusts()]);
}

init().catch((err) => {
  setStatus("agentStatus", `Unexpected error: ${String(err)}`, true);
});
