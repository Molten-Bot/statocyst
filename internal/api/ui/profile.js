const UI = StatocystUI;

async function loadMe() {
  UI.out("meOut", await UI.req("/v1/me"));
}

async function loadOrgs() {
  await UI.populateOrgSelect("orgSelect", "orgOut");
}

async function acceptInvite() {
  const inviteID = UI.$("inviteId").value.trim();
  if (!inviteID) {
    UI.out("inviteOut", { status: 400, data: { error: "invite_id required" } });
    return;
  }
  const res = await UI.req(`/v1/org-invites/${inviteID}/accept`, "POST");
  UI.out("inviteOut", res);
  if (res.status === 200) {
    await loadOrgs();
  }
}

async function init() {
  UI.initTopNav();

  UI.$("btnLoadMe").onclick = loadMe;
  UI.$("btnLoadOrgs").onclick = loadOrgs;
  UI.$("btnAcceptInvite").onclick = acceptInvite;

  await loadMe();
  await loadOrgs();
}

init().catch((err) => UI.out("meOut", { error: String(err) }));
