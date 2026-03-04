const UI = StatocystUI;

function selectedOrg() {
  return UI.selectedOrg("orgSelect");
}

function requireOrg(outputID) {
  const orgID = selectedOrg();
  if (!orgID) {
    UI.out(outputID, { status: 400, data: { error: "select an organization first" } });
    return "";
  }
  return orgID;
}

async function listOrgs() {
  await UI.populateOrgSelect("orgSelect", "orgOut");
}

async function listAgents() {
  const orgID = requireOrg("agentsOut");
  if (!orgID) return;

  const result = await UI.req(`/v1/orgs/${orgID}/agents`);
  UI.out("agentsOut", result);

  if (result.status === 200 && Array.isArray(result.data.agents) && result.data.agents.length > 0) {
    UI.$("agentId").value = result.data.agents[0].agent_id || "";
  }
}

async function registerAgent() {
  const orgID = requireOrg("agentOut");
  if (!orgID) return;

  const agentID = UI.$("agentId").value.trim();
  const ownerHumanID = UI.$("ownerHumanId").value.trim();
  if (!agentID) {
    UI.out("agentOut", { status: 400, data: { error: "agent_id required" } });
    return;
  }

  const payload = { org_id: orgID, agent_id: agentID };
  if (ownerHumanID) payload.owner_human_id = ownerHumanID;

  UI.out("agentOut", await UI.req("/v1/agents/register", "POST", payload));
  await listAgents();
}

async function rotateAgent() {
  const agentID = UI.$("agentId").value.trim();
  if (!agentID) {
    UI.out("agentOut", { status: 400, data: { error: "agent_id required" } });
    return;
  }
  UI.out("agentOut", await UI.req(`/v1/agents/${agentID}/rotate-token`, "POST"));
}

async function revokeAgent() {
  const agentID = UI.$("agentId").value.trim();
  if (!agentID) {
    UI.out("agentOut", { status: 400, data: { error: "agent_id required" } });
    return;
  }
  UI.out("agentOut", await UI.req(`/v1/agents/${agentID}`, "DELETE"));
  await listAgents();
}

function renderPendingRows(edges) {
  const root = UI.$("pendingList");
  root.innerHTML = "";

  if (!edges.length) {
    const p = document.createElement("p");
    p.className = "muted";
    p.textContent = "No pending agent trust approvals for this organization.";
    root.appendChild(p);
    return;
  }

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
  const orgID = requireOrg("pendingOut");
  if (!orgID) return;

  const result = await UI.req(`/v1/orgs/${orgID}/trust-graph`);
  UI.out("pendingOut", result);

  if (result.status !== 200 || !Array.isArray(result.data.agent_trusts)) {
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

  const result = await UI.req(path, method);
  UI.out("pendingOut", result);
  await loadPendingTrusts();
}

async function init() {
  UI.initTopNav();

  UI.$("btnLoadOrgs").onclick = listOrgs;
  UI.$("btnListAgents").onclick = listAgents;
  UI.$("btnRegisterAgent").onclick = registerAgent;
  UI.$("btnRotateAgent").onclick = rotateAgent;
  UI.$("btnRevokeAgent").onclick = revokeAgent;
  UI.$("btnRefreshPending").onclick = loadPendingTrusts;

  UI.$("pendingList").addEventListener("click", async (event) => {
    const button = event.target.closest("button[data-action]");
    if (!button) return;

    const edgeID = button.dataset.edgeId || "";
    const action = button.dataset.action || "";
    if (!edgeID || !action) return;

    await runTrustAction(edgeID, action);
  });

  await listOrgs();
}

init().catch((err) => UI.out("agentOut", { error: String(err) }));
