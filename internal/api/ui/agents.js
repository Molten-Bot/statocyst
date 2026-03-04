const UI = StatocystUI;

function setStatus(id, message, warn = false) {
  const el = UI.$(id);
  if (!el) return;
  el.textContent = message;
  el.className = warn ? "status warn" : "status";
}

function setAgentInputIfEmpty(agentID) {
  const input = UI.$("agentId");
  if (!input) return;
  if (!input.value.trim()) {
    input.value = agentID;
  }
}

function renderAgents(agents) {
  const body = UI.$("agentsBody");
  body.innerHTML = "";

  if (!Array.isArray(agents) || agents.length === 0) {
    const tr = document.createElement("tr");
    const td = document.createElement("td");
    td.colSpan = 5;
    td.className = "muted";
    td.textContent = "No agents yet.";
    tr.appendChild(td);
    body.appendChild(tr);
    setStatus("agentStatus", "No agents found.");
    return;
  }

  for (const agent of agents) {
    const tr = document.createElement("tr");

    const tdID = document.createElement("td");
    tdID.textContent = agent.agent_id || "";
    tr.appendChild(tdID);

    const tdOrg = document.createElement("td");
    tdOrg.textContent = agent.org_id || "";
    tr.appendChild(tdOrg);

    const tdStatus = document.createElement("td");
    tdStatus.textContent = agent.status || "";
    tr.appendChild(tdStatus);

    const tdOwner = document.createElement("td");
    tdOwner.textContent = agent.owner_human_id || "org-owned";
    tr.appendChild(tdOwner);

    const tdActions = document.createElement("td");
    const actionWrap = document.createElement("div");
    actionWrap.className = "row-actions";

    const rotateBtn = document.createElement("button");
    rotateBtn.textContent = "Rotate Token";
    rotateBtn.dataset.agentAction = "rotate";
    rotateBtn.dataset.agentId = agent.agent_id || "";
    rotateBtn.disabled = String(agent.status || "").toLowerCase() === "revoked";
    actionWrap.appendChild(rotateBtn);

    const revokeBtn = document.createElement("button");
    revokeBtn.textContent = "Revoke Agent";
    revokeBtn.dataset.agentAction = "revoke";
    revokeBtn.dataset.agentId = agent.agent_id || "";
    revokeBtn.disabled = String(agent.status || "").toLowerCase() === "revoked";
    actionWrap.appendChild(revokeBtn);

    tdActions.appendChild(actionWrap);
    tr.appendChild(tdActions);
    body.appendChild(tr);
  }

  setAgentInputIfEmpty(agents[0].agent_id || "");
  setStatus("agentStatus", `${agents.length} agent(s) loaded.`);
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

async function registerAgent() {
  const agentID = UI.$("agentId").value.trim();
  if (!agentID) {
    setStatus("agentStatus", "agent_id required", true);
    return;
  }

  setStatus("agentStatus", "Registering agent...");
  const result = await UI.req("/v1/me/agents", "POST", { agent_id: agentID });
  if (result.status !== 201) {
    setStatus("agentStatus", "Could not register agent.", true);
    return;
  }

  setStatus("agentStatus", `Registered ${agentID}.`);
  await loadAgents();
}

async function rotateAgent(agentID) {
  if (!agentID) {
    setStatus("agentStatus", "agent_id required", true);
    return;
  }

  setStatus("agentStatus", `Rotating token for ${agentID}...`);
  const result = await UI.req(`/v1/agents/${encodeURIComponent(agentID)}/rotate-token`, "POST");
  if (result.status !== 200) {
    setStatus("agentStatus", "Could not rotate token.", true);
    return;
  }
  setStatus("agentStatus", `Token rotated for ${agentID}.`);
}

async function revokeAgent(agentID) {
  if (!agentID) {
    setStatus("agentStatus", "agent_id required", true);
    return;
  }

  setStatus("agentStatus", `Revoking ${agentID}...`);
  const result = await UI.req(`/v1/agents/${encodeURIComponent(agentID)}`, "DELETE");
  if (result.status !== 200) {
    setStatus("agentStatus", "Could not revoke agent.", true);
    return;
  }

  setStatus("agentStatus", `Revoked ${agentID}.`);
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
  thead.innerHTML = "<tr><th>Edge</th><th>Agents</th><th>State</th><th>Can Talk</th><th>Actions</th></tr>";
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
    tdTalk.textContent = String(edge.state || "").toLowerCase() === "active" ? "Yes" : "No";

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
  const agentID = UI.$("trustAgentId").value.trim();
  const peerAgentID = UI.$("trustPeerAgentId").value.trim();
  if (!agentID || !peerAgentID) {
    setStatus("pendingStatus", "agent_id and peer_agent_id are required.", true);
    return;
  }

  setStatus("pendingStatus", "Creating bond...");
  const result = await UI.req("/v1/me/agent-trusts", "POST", {
    agent_id: agentID,
    peer_agent_id: peerAgentID,
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

  UI.$("btnRegisterAgent").onclick = registerAgent;
  UI.$("btnRefreshTrusts").onclick = loadPendingTrusts;
  UI.$("btnCreateTrust").onclick = createTrust;

  UI.$("agentsBody").addEventListener("click", async (event) => {
    const button = event.target.closest("button[data-agent-action]");
    if (!button) return;

    const action = button.dataset.agentAction || "";
    const agentID = button.dataset.agentId || "";
    if (!action || !agentID) return;

    if (action === "rotate") {
      await rotateAgent(agentID);
      return;
    }
    if (action === "revoke") {
      await revokeAgent(agentID);
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

  await loadAgents();
  await loadPendingTrusts();
}

init().catch((err) => {
  setStatus("agentStatus", `Unexpected error: ${String(err)}`, true);
});
