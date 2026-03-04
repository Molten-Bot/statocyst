const UI = StatocystUI;

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

function formatDateTime(raw) {
  if (!raw) return "unknown";
  const d = new Date(raw);
  if (Number.isNaN(d.getTime())) return String(raw);
  return d.toLocaleString();
}

function buildAgentBindPrompt(bindToken, expiresAt, redeemURL) {
  return [
    "Agent Onboarding Prompt",
    "",
    "Goal: Bind this agent to Statocyst and return your new agent token.",
    "",
    `Bind code: ${bindToken}`,
    `Expires: ${expiresAt}`,
    `Bind API URL: ${redeemURL}`,
    "",
    "Instructions for agent:",
    "1. Pick your stable agent_id (URL-safe: a-z, 0-9, ., _, -).",
    "2. Redeem the bind code with this command (replace <agent_id>):",
    "",
    `curl -sS -X POST "${redeemURL}" \\`,
    "  -H 'Content-Type: application/json' \\",
    `  -d '{"bind_token":"${bindToken}","agent_id":"<agent_id>"}'`,
    "",
    "3. Parse JSON response and return:",
    "- agent_id",
    "- org_id",
    "- token",
    "",
    "4. Keep token secret and use it as Bearer auth for message APIs.",
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
    const text = `${agent.agent_id || ""} (${agent.owner_human_id || "org-owned"})`;

    const leftOpt = document.createElement("option");
    leftOpt.value = agent.agent_id || "";
    leftOpt.textContent = text;
    left.appendChild(leftOpt);

    const rightOpt = document.createElement("option");
    rightOpt.value = agent.agent_id || "";
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

  if (!Array.isArray(agents) || agents.length === 0) {
    const tr = document.createElement("tr");
    const td = document.createElement("td");
    td.colSpan = 6;
    td.className = "muted";
    td.textContent = "No agents yet.";
    tr.appendChild(td);
    body.appendChild(tr);
    setStatus("agentStatus", "No agents found.");
    syncBondSelectors([]);
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

    const tdPublic = document.createElement("td");
    tdPublic.textContent = agent.is_public ? "public" : "private";
    tr.appendChild(tdPublic);

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

    const visibilityBtn = document.createElement("button");
    visibilityBtn.textContent = agent.is_public ? "Make Private" : "Make Public";
    visibilityBtn.dataset.agentAction = "visibility";
    visibilityBtn.dataset.agentId = agent.agent_id || "";
    visibilityBtn.dataset.makePublic = agent.is_public ? "false" : "true";
    visibilityBtn.disabled = String(agent.status || "").toLowerCase() === "revoked";
    actionWrap.appendChild(visibilityBtn);

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

async function createBindCode() {
  setBindCodeStatus("Creating one-time bind code...");
  const result = await UI.req("/v1/me/agents/bind-tokens", "POST", {});
  if (result.status !== 201) {
    setBindCodeStatus("Could not create bind code.", true);
    return;
  }

  const token = String(result?.data?.bind_token || "").trim();
  const expiresAt = formatDateTime(result?.data?.expires_at);
  const redeemURL = `${window.location.origin}/v1/agents/bind/redeem`;
  if (!token) {
    setBindCodeStatus("Bind code was not returned.", true);
    return;
  }

  setBindCodeStatus(buildAgentBindPrompt(token, expiresAt, redeemURL));
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

async function setAgentVisibility(agentID, makePublic) {
  if (!agentID) {
    setStatus("agentStatus", "agent_id required", true);
    return;
  }
  setStatus("agentStatus", `Updating visibility for ${agentID}...`);
  const result = await UI.req(`/v1/agents/${encodeURIComponent(agentID)}`, "PATCH", {
    is_public: makePublic,
  });
  if (result.status !== 200) {
    setStatus("agentStatus", "Could not update agent visibility.", true);
    return;
  }
  setStatus("agentStatus", `Visibility updated for ${agentID}.`);
  await loadAgents();
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
    setStatus("pendingStatus", "Select both agents.", true);
    return;
  }
  if (agentID === peerAgentID) {
    setStatus("pendingStatus", "Choose two different agents.", true);
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

  UI.$("btnCreateBindCode").onclick = createBindCode;
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
    if (action === "visibility") {
      const makePublic = String(button.dataset.makePublic || "false") === "true";
      await setAgentVisibility(agentID, makePublic);
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
