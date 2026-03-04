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

function requireOrg(statusID, message = "Select an organization first.") {
  const orgID = selectedOrg();
  if (!orgID) {
    setStatus(statusID, message, true);
    return "";
  }
  return orgID;
}

function setOrgPickerVisible(visible) {
  const picker = UI.$("orgPicker");
  const hint = UI.$("orgEmptyHint");
  if (picker) picker.style.display = visible ? "" : "none";
  if (hint) hint.style.display = visible ? "none" : "block";
}

function setAgentControlsEnabled(enabled) {
  const ids = [
    "orgSelect",
    "agentId",
    "ownerHumanId",
    "btnRegisterAgent",
    "btnRotateAgent",
    "btnRevokeAgent",
    "btnRefreshPending",
  ];
  for (const id of ids) {
    const el = UI.$(id);
    if (!el) continue;
    el.disabled = !enabled;
  }
}

async function loadCurrentHuman() {
  const res = await UI.req("/v1/me");
  if (res.status !== 200) return;
  currentHumanID = String(res?.data?.human?.human_id || "").trim();
  if (currentHumanID && !UI.$("ownerHumanId").value.trim()) {
    UI.$("ownerHumanId").value = currentHumanID;
  }
}

async function listOrgs(preserveCurrent = true) {
  const res = await UI.req("/v1/me/orgs");
  const select = UI.$("orgSelect");
  const current = selectedOrg();
  select.innerHTML = "";

  if (res.status !== 200 || !Array.isArray(res.data.memberships)) {
    setOrgPickerVisible(false);
    setAgentControlsEnabled(false);
    setStatus("agentStatus", "Could not load organizations.", true);
    setStatus("pendingStatus", "Could not load organizations.", true);
    UI.$("agentsList").innerHTML = "";
    UI.$("pendingList").innerHTML = "";
    return;
  }

  for (const membership of res.data.memberships) {
    const opt = document.createElement("option");
    opt.value = membership.org.org_id;
    opt.textContent = `${membership.org.name} (${membership.membership.role})`;
    select.appendChild(opt);
  }

  if (select.options.length === 0) {
    setOrgPickerVisible(false);
    setAgentControlsEnabled(false);
    setStatus("agentStatus", "Create an organization first to add agents.");
    setStatus("pendingStatus", "Create an organization first to view trust approvals.");
    UI.$("agentsList").innerHTML = "";
    UI.$("pendingList").innerHTML = "";
    return;
  }

  setOrgPickerVisible(true);
  setAgentControlsEnabled(true);

  if (preserveCurrent && current && [...select.options].some((opt) => opt.value === current)) {
    select.value = current;
  } else {
    select.value = select.options[0].value;
  }

  if (currentHumanID && !UI.$("ownerHumanId").value.trim()) {
    UI.$("ownerHumanId").value = currentHumanID;
  }

  await loadAgents();
  await loadPendingTrusts();
}

function renderAgents(agents) {
  const root = UI.$("agentsList");
  root.innerHTML = "";

  if (!Array.isArray(agents) || agents.length === 0) {
    const li = document.createElement("li");
    li.textContent = "No agents yet.";
    root.appendChild(li);
    setStatus("agentStatus", "No agents found for this organization.");
    return;
  }

  for (const agent of agents) {
    const li = document.createElement("li");
    const owner = agent.owner_human_id ? `owner: ${agent.owner_human_id}` : "org-owned";
    li.textContent = `${agent.agent_id} (${agent.status}, ${owner})`;
    li.dataset.agentId = agent.agent_id || "";
    root.appendChild(li);
  }

  if (!UI.$("agentId").value.trim()) {
    UI.$("agentId").value = agents[0].agent_id || "";
  }
  setStatus("agentStatus", `${agents.length} agent(s) loaded.`);
}

async function loadAgents() {
  const orgID = requireOrg("agentStatus");
  if (!orgID) return;

  setStatus("agentStatus", "Loading agents...");
  const result = await UI.req(`/v1/orgs/${orgID}/agents`);
  if (result.status !== 200) {
    setStatus("agentStatus", "Could not load agents.", true);
    renderAgents([]);
    return;
  }

  renderAgents(result.data.agents || []);
}

async function registerAgent() {
  const orgID = requireOrg("agentStatus");
  if (!orgID) return;

  const agentID = UI.$("agentId").value.trim();
  const ownerHumanID = UI.$("ownerHumanId").value.trim();
  if (!agentID) {
    setStatus("agentStatus", "agent_id required", true);
    return;
  }

  const payload = { org_id: orgID, agent_id: agentID };
  if (ownerHumanID) payload.owner_human_id = ownerHumanID;

  setStatus("agentStatus", "Registering agent...");
  const result = await UI.req("/v1/agents/register", "POST", payload);
  if (result.status !== 201) {
    setStatus("agentStatus", "Could not register agent.", true);
    return;
  }

  setStatus("agentStatus", `Registered ${agentID}.`);
  await loadAgents();
}

async function rotateAgent() {
  const agentID = UI.$("agentId").value.trim();
  if (!agentID) {
    setStatus("agentStatus", "agent_id required", true);
    return;
  }

  setStatus("agentStatus", "Rotating token...");
  const result = await UI.req(`/v1/agents/${agentID}/rotate-token`, "POST");
  if (result.status !== 200) {
    setStatus("agentStatus", "Could not rotate token.", true);
    return;
  }

  setStatus("agentStatus", `Token rotated for ${agentID}.`);
}

async function revokeAgent() {
  const agentID = UI.$("agentId").value.trim();
  if (!agentID) {
    setStatus("agentStatus", "agent_id required", true);
    return;
  }

  setStatus("agentStatus", "Revoking agent...");
  const result = await UI.req(`/v1/agents/${agentID}`, "DELETE");
  if (result.status !== 200) {
    setStatus("agentStatus", "Could not revoke agent.", true);
    return;
  }

  setStatus("agentStatus", `Revoked ${agentID}.`);
  await loadAgents();
}

function renderPendingRows(edges) {
  const root = UI.$("pendingList");
  root.innerHTML = "";

  if (!edges.length) {
    const p = document.createElement("p");
    p.className = "muted";
    p.textContent = "No pending agent trust approvals for this organization.";
    root.appendChild(p);
    setStatus("pendingStatus", "No pending requests.");
    return;
  }

  setStatus("pendingStatus", `${edges.length} pending request(s).`);

  const table = document.createElement("table");
  const thead = document.createElement("thead");
  thead.innerHTML = "<tr><th>Edge</th><th>Agents</th><th>State</th><th>Actions</th></tr>";
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
      btn.dataset.edgeId = edge.edge_id;
      btn.dataset.action = item.action;
      wrap.appendChild(btn);
    }

    tdActions.appendChild(wrap);
    tr.appendChild(tdEdge);
    tr.appendChild(tdAgents);
    tr.appendChild(tdState);
    tr.appendChild(tdActions);
    tbody.appendChild(tr);
  }

  table.appendChild(tbody);
  root.appendChild(table);
}

async function loadPendingTrusts() {
  const orgID = requireOrg("pendingStatus", "Select an organization first.");
  if (!orgID) return;

  setStatus("pendingStatus", "Loading pending requests...");
  const result = await UI.req(`/v1/orgs/${orgID}/trust-graph`);

  if (result.status !== 200 || !Array.isArray(result.data.agent_trusts)) {
    setStatus("pendingStatus", "Could not load pending requests.", true);
    renderPendingRows([]);
    return;
  }

  const pending = result.data.agent_trusts.filter((edge) => edge.state === "pending");
  renderPendingRows(pending);
}

async function runTrustAction(edgeID, action) {
  let method = "POST";
  let path = `/v1/agent-trusts/${edgeID}/${action}`;

  if (action === "revoke") {
    method = "DELETE";
    path = `/v1/agent-trusts/${edgeID}`;
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
  await loadCurrentHuman();

  UI.$("btnRegisterAgent").onclick = registerAgent;
  UI.$("btnRotateAgent").onclick = rotateAgent;
  UI.$("btnRevokeAgent").onclick = revokeAgent;
  UI.$("btnRefreshPending").onclick = loadPendingTrusts;
  UI.$("orgSelect").onchange = async () => {
    await loadAgents();
    await loadPendingTrusts();
  };

  UI.$("agentsList").addEventListener("click", (event) => {
    const li = event.target.closest("li[data-agent-id]");
    if (!li) return;
    UI.$("agentId").value = li.dataset.agentId || "";
  });

  UI.$("pendingList").addEventListener("click", async (event) => {
    const button = event.target.closest("button[data-action]");
    if (!button) return;

    const edgeID = button.dataset.edgeId || "";
    const action = button.dataset.action || "";
    if (!edgeID || !action) return;

    await runTrustAction(edgeID, action);
  });

  await listOrgs(false);
}

init().catch((err) => {
  setStatus("agentStatus", `Unexpected error: ${String(err)}`, true);
});
