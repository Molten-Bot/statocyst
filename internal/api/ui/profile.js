const UI = StatocystUI;

function setStatus(message, warn = false) {
  const el = UI.$("profileStatus");
  if (!warn && message === "Profile loaded.") {
    el.style.display = "none";
    return;
  }
  el.style.display = "block";
  el.textContent = message;
  el.className = warn ? "status warn" : "status";
}

function daysAgoLabel(raw) {
  if (!raw) return "Joined recently";
  const d = new Date(raw);
  if (Number.isNaN(d.getTime())) return "Joined recently";
  const msPerDay = 24 * 60 * 60 * 1000;
  const days = Math.max(0, Math.floor((Date.now() - d.getTime()) / msPerDay));
  const unit = days === 1 ? "day" : "days";
  return `Joined ${days} ${unit} ago`;
}

function renderOrgs(memberships) {
  const target = UI.$("profileOrgs");
  target.innerHTML = "";

  function renderEmpty() {
    const link = document.createElement("a");
    link.href = "/organization";
    link.textContent = "Create one on /organization";
    target.append(link);
  }

  if (!Array.isArray(memberships) || memberships.length === 0) {
    renderEmpty();
    return;
  }

  const list = memberships
    .map((m) => m?.org?.name)
    .filter((name) => typeof name === "string" && name.trim() !== "");

  if (list.length === 0) {
    renderEmpty();
    return;
  }

  const ul = document.createElement("ul");
  ul.className = "list";
  for (const name of list) {
    const li = document.createElement("li");
    li.textContent = name;
    ul.appendChild(li);
  }
  target.appendChild(ul);
}

function renderProfile(me, orgs) {
  const human = me?.data?.human;
  UI.$("profileEmail").textContent = human?.email || "-";
  UI.$("profileJoined").textContent = daysAgoLabel(human?.created_at);
  renderOrgs(orgs?.data?.memberships);

  const isSuperAdmin = Boolean(me?.data?.is_super_admin);
  UI.$("superAdminRow").style.display = isSuperAdmin ? "block" : "none";
}

function setInviteStatus(message, warn = false) {
  const el = UI.$("inviteStatus");
  el.textContent = message;
  el.className = warn ? "status warn" : "muted";
}

function titleCaseStatus(status) {
  if (status === "pending") return "Pending";
  if (status === "active") return "Accepted";
  if (status === "revoked") return "Revoked";
  return status || "Unknown";
}

function renderInvites(invites) {
  const root = UI.$("inviteList");
  root.innerHTML = "";

  const visible = (Array.isArray(invites) ? invites : []).filter((entry) => {
    const status = String(entry?.invite?.status || "").toLowerCase();
    return status === "pending" || status === "active";
  });

  if (visible.length === 0) {
    setInviteStatus("No pending or accepted invites.");
    return;
  }

  setInviteStatus(`${visible.length} invite(s).`);
  for (const entry of visible) {
    const invite = entry.invite || {};
    const org = entry.org || {};

    const card = document.createElement("div");
    card.className = "inviteItem";

    const title = document.createElement("div");
    title.className = "value";
    title.textContent = org.name || "Organization";
    card.appendChild(title);

    const meta = document.createElement("div");
    meta.className = "inviteMeta";
    meta.textContent = `${titleCaseStatus(invite.status)} invite as ${invite.role || "member"}`;
    card.appendChild(meta);

    const actions = document.createElement("div");
    actions.className = "inviteActions";

    if (invite.status === "pending") {
      const acceptBtn = document.createElement("button");
      acceptBtn.className = "primary";
      acceptBtn.textContent = "Accept";
      acceptBtn.dataset.action = "accept";
      acceptBtn.dataset.inviteId = invite.invite_id || "";
      actions.appendChild(acceptBtn);
    }

    if (invite.status === "pending" || invite.status === "active") {
      const revokeBtn = document.createElement("button");
      revokeBtn.textContent = "Revoke";
      revokeBtn.dataset.action = "revoke";
      revokeBtn.dataset.inviteId = invite.invite_id || "";
      actions.appendChild(revokeBtn);
    }

    card.appendChild(actions);
    root.appendChild(card);
  }
}

async function loadProfileData() {
  const [me, orgs, invites] = await Promise.all([UI.req("/v1/me"), UI.req("/v1/me/orgs"), UI.req("/v1/org-invites")]);

  if (me.status !== 200) {
    setStatus("Could not load profile. Please login again.", true);
    return false;
  }

  if (orgs.status !== 200) {
    setStatus("Profile loaded, but organizations could not be loaded.", true);
    renderProfile(me, { data: { memberships: [] } });
  } else {
    renderProfile(me, orgs);
  }

  if (invites.status !== 200) {
    setInviteStatus("Could not load invites.", true);
  } else {
    renderInvites(invites.data?.invites || []);
  }

  setStatus("Profile loaded.");
  return true;
}

async function runInviteAction(inviteID, action) {
  if (!inviteID) return;
  if (action === "accept") {
    setInviteStatus("Accepting invite...");
    const res = await UI.req(`/v1/org-invites/${inviteID}/accept`, "POST");
    if (res.status !== 200) {
      setInviteStatus("Could not accept invite.", true);
      return;
    }
  } else if (action === "revoke") {
    setInviteStatus("Revoking invite...");
    const res = await UI.req(`/v1/org-invites/${inviteID}`, "DELETE");
    if (res.status !== 200) {
      setInviteStatus("Could not revoke invite.", true);
      return;
    }
  } else {
    return;
  }
  await loadProfileData();
}

async function init() {
  UI.initTopNav();
  setStatus("Loading profile...");
  setInviteStatus("Loading invites...");

  UI.$("inviteList").addEventListener("click", async (event) => {
    const button = event.target.closest("button[data-action][data-invite-id]");
    if (!button) return;
    await runInviteAction(button.dataset.inviteId || "", button.dataset.action || "");
  });

  await loadProfileData();
}

init().catch((err) => {
  setStatus(`Unexpected error: ${String(err)}`, true);
});
