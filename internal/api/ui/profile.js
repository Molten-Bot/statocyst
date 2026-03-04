const UI = StatocystUI;
const PENDING_INVITE_CODE_KEY = "statocyst_pending_invite_code";
let currentHuman = null;

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
  currentHuman = human || null;
  UI.$("profileEmail").textContent = human?.email || "-";
  UI.$("profileHandle").textContent = human?.handle || "-";
  UI.$("profileJoined").textContent = daysAgoLabel(human?.created_at);
  UI.$("profileHandleInput").value = human?.handle || "";
  UI.$("profileIsPublic").checked = Boolean(human?.is_public);
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

function normalizeInviteCode(raw) {
  return String(raw || "").trim();
}

function savePendingInviteCode(inviteCode) {
  const value = normalizeInviteCode(inviteCode);
  if (!value) {
    localStorage.removeItem(PENDING_INVITE_CODE_KEY);
    return "";
  }
  localStorage.setItem(PENDING_INVITE_CODE_KEY, value);
  return value;
}

function readPendingInviteCode() {
  return normalizeInviteCode(localStorage.getItem(PENDING_INVITE_CODE_KEY));
}

function captureInviteCodeFromURL() {
  const url = new URL(window.location.href);
  const inviteCode = normalizeInviteCode(url.searchParams.get("invite") || url.searchParams.get("invite_code"));
  if (!inviteCode) return "";

  savePendingInviteCode(inviteCode);
  url.searchParams.delete("invite");
  url.searchParams.delete("invite_code");
  const nextURL = `${url.pathname}${url.search}${url.hash}`;
  window.history.replaceState({}, "", nextURL);
  return inviteCode;
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

async function saveProfile() {
  const handle = String(UI.$("profileHandleInput").value || "").trim();
  const isPublic = Boolean(UI.$("profileIsPublic").checked);
  if (!handle) {
    setStatus("Handle is required.", true);
    return;
  }

  setStatus("Saving profile...");
  const res = await UI.req("/v1/me", "PATCH", {
    handle,
    is_public: isPublic,
  });
  if (res.status !== 200) {
    const err = String(res?.data?.error || "");
    if (err === "human_handle_exists") {
      setStatus("Handle already exists. Choose another.", true);
      return;
    }
    if (err === "invalid_handle") {
      setStatus("Handle must be URL-safe (a-z, 0-9, ., _, -).", true);
      return;
    }
    setStatus("Could not update profile.", true);
    return;
  }
  setStatus("Profile updated.");
  await loadProfileData();
}

async function autoRedeemPendingInvite() {
  const inviteCode = readPendingInviteCode();
  if (!inviteCode) return false;

  setInviteStatus("Redeeming invite link...");
  const res = await UI.req("/v1/org-invites/redeem", "POST", { invite_code: inviteCode });
  if (res.status === 200) {
    localStorage.removeItem(PENDING_INVITE_CODE_KEY);
    setInviteStatus("Invite link redeemed. You were added to the organization.");
    return true;
  }

  const err = String(res?.data?.error || "");
  if (err === "unknown_invite_code") {
    localStorage.removeItem(PENDING_INVITE_CODE_KEY);
    setInviteStatus("Stored invite code is no longer valid.", true);
    return false;
  }
  if (err === "invalid_invite_code") {
    setInviteStatus("Stored invite code does not match this account email. Login with the invited email to redeem.", true);
    return false;
  }

  setInviteStatus("Could not redeem stored invite link.", true);
  return false;
}

async function init() {
  captureInviteCodeFromURL();
  UI.initTopNav();
  setStatus("Loading profile...");
  setInviteStatus("Loading invites...");

  UI.$("inviteList").addEventListener("click", async (event) => {
    const button = event.target.closest("button[data-action][data-invite-id]");
    if (!button) return;
    await runInviteAction(button.dataset.inviteId || "", button.dataset.action || "");
  });
  UI.$("btnSaveProfile").onclick = saveProfile;

  const loaded = await loadProfileData();
  if (!loaded) return;

  const redeemed = await autoRedeemPendingInvite();
  if (redeemed) {
    await loadProfileData();
  }
}

init().catch((err) => {
  setStatus(`Unexpected error: ${String(err)}`, true);
});
