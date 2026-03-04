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

async function createOrg() {
  const name = UI.$("orgName").value.trim();
  if (!name) {
    UI.out("orgOut", { status: 400, data: { error: "name is required" } });
    return;
  }
  UI.out("orgOut", await UI.req("/v1/orgs", "POST", { name }));
  await listOrgs();
}

async function inviteHuman() {
  const orgID = requireOrg("inviteOut");
  if (!orgID) return;

  const email = UI.$("inviteEmail").value.trim();
  const role = UI.$("inviteRole").value;
  if (!email) {
    UI.out("inviteOut", { status: 400, data: { error: "email is required" } });
    return;
  }

  UI.out(
    "inviteOut",
    await UI.req(`/v1/orgs/${orgID}/invites`, "POST", {
      email,
      role,
    })
  );
}

async function listHumans() {
  const orgID = requireOrg("humansOut");
  if (!orgID) return;
  UI.out("humansOut", await UI.req(`/v1/orgs/${orgID}/humans`));
}

async function loadStats() {
  const orgID = requireOrg("statsOut");
  if (!orgID) return;
  UI.out("statsOut", await UI.req(`/v1/orgs/${orgID}/stats`));
}

async function loadGraph() {
  const orgID = requireOrg("graphOut");
  if (!orgID) return;
  UI.out("graphOut", await UI.req(`/v1/orgs/${orgID}/trust-graph`));
}

async function loadAudit() {
  const orgID = requireOrg("auditOut");
  if (!orgID) return;
  UI.out("auditOut", await UI.req(`/v1/orgs/${orgID}/audit`));
}

async function init() {
  UI.initTopNav();

  UI.$("btnCreateOrg").onclick = createOrg;
  UI.$("btnListOrgs").onclick = listOrgs;
  UI.$("btnInvite").onclick = inviteHuman;
  UI.$("btnListHumans").onclick = listHumans;
  UI.$("btnStats").onclick = loadStats;
  UI.$("btnGraph").onclick = loadGraph;
  UI.$("btnAudit").onclick = loadAudit;

  await listOrgs();
}

init().catch((err) => UI.out("orgOut", { error: String(err) }));
