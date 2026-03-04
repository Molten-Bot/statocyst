const $ = (id) => document.getElementById(id);

const TOKEN_KEY = "statocyst_access_token";
const DEV_ID_KEY = "statocyst_dev_human_id";
const DEV_EMAIL_KEY = "statocyst_dev_human_email";
const DEFAULT_APP_NAME = "Statocyst";

function readStorage(key) {
  return localStorage.getItem(key) || "";
}

function loadSavedToken() {
  return readStorage(TOKEN_KEY);
}

function saveToken(token) {
  if (!token) return;
  localStorage.setItem(TOKEN_KEY, token);
}

function clearToken() {
  localStorage.removeItem(TOKEN_KEY);
}

const headers = () => {
  const h = { "Content-Type": "application/json" };
  const token = ($("authToken")?.value || loadSavedToken()).trim();
  if (token) h.Authorization = `Bearer ${token}`;

  const humanId = ($("humanId")?.value || readStorage(DEV_ID_KEY)).trim();
  const humanEmail = ($("humanEmail")?.value || readStorage(DEV_EMAIL_KEY)).trim();
  if (humanId) h["X-Human-Id"] = humanId;
  if (humanEmail) h["X-Human-Email"] = humanEmail;
  return h;
};

const selectedOrg = () => ($("orgSelect")?.value || "").trim();

async function req(path, method = "GET", body = null) {
  const res = await fetch(path, {
    method,
    headers: headers(),
    body: body ? JSON.stringify(body) : null,
  });
  const text = await res.text();
  let data = text;
  try {
    data = JSON.parse(text || "{}");
  } catch (_) {}
  return { status: res.status, data };
}

function out(el, obj) {
  if (!$(el)) return;
  $(el).textContent = JSON.stringify(obj, null, 2);
}

async function applyAppName() {
  let appName = DEFAULT_APP_NAME;
  try {
    const cfg = await req("/v1/ui/config");
    appName = String(cfg?.data?.app_name || "").trim() || DEFAULT_APP_NAME;
  } catch (_) {}

  const nodes = document.querySelectorAll("[data-app-name]");
  for (const node of nodes) {
    node.textContent = appName;
  }
}

async function listOrgs() {
  const r = await req("/v1/me/orgs");
  out("orgOut", r);
  const select = $("orgSelect");
  if (!select) return;
  select.innerHTML = "";
  if (r.status === 200 && Array.isArray(r.data.memberships)) {
    for (const m of r.data.memberships) {
      const opt = document.createElement("option");
      opt.value = m.org.org_id;
      opt.textContent = `${m.org.name} (${m.membership.role})`;
      select.appendChild(opt);
    }
  }
}

function showDomainsAccessBlocked() {
  const main = document.querySelector("main");
  if (!main) return;
  main.innerHTML =
    '<div class="banner"><strong>Domains (Legacy)</strong> is super-admin view-only. Access denied for this account.</div>';
}

function disableMutatingActions() {
  const mutatingIDs = [
    "btnCreateOrg",
    "btnInvite",
    "btnAcceptInvite",
    "btnRegisterAgent",
    "btnCreateBindToken",
    "btnRotateAgent",
    "btnRevokeAgent",
    "btnReqOrgTrust",
    "btnApproveOrgTrust",
    "btnBlockOrgTrust",
    "btnRevokeOrgTrust",
    "btnReqAgentTrust",
    "btnApproveAgentTrust",
    "btnBlockAgentTrust",
    "btnRevokeAgentTrust",
  ];
  for (const id of mutatingIDs) {
    const btn = $(id);
    if (!btn) continue;
    btn.disabled = true;
    btn.title = "Disabled: legacy page is view-only";
  }
}

function bindReadActions() {
  $("btnMe").onclick = async () => out("meOut", await req("/v1/me"));
  $("btnSaveToken").onclick = () => {
    saveToken($("authToken").value.trim());
    out("meOut", { status: "ok", message: "access token saved" });
  };
  $("btnClearToken").onclick = () => {
    $("authToken").value = "";
    clearToken();
    out("meOut", { status: "ok", message: "access token cleared" });
  };
  $("btnGoLogin").onclick = () => {
    window.location.assign("/");
  };
  $("btnListOrgs").onclick = listOrgs;

  $("btnOrgHumans").onclick = async () => out("inviteOut", await req(`/v1/orgs/${selectedOrg()}/humans`));
  $("btnListAgents").onclick = async () => out("agentOut", await req(`/v1/orgs/${selectedOrg()}/agents`));

  $("btnGraph").onclick = async () => out("graphOut", await req(`/v1/orgs/${selectedOrg()}/trust-graph`));
  $("btnAudit").onclick = async () => out("graphOut", await req(`/v1/orgs/${selectedOrg()}/audit`));
  $("btnStats").onclick = async () => out("graphOut", await req(`/v1/orgs/${selectedOrg()}/stats`));
  $("btnAdminSnapshot").onclick = async () => out("graphOut", await req("/v1/admin/snapshot"));
}

async function init() {
  await applyAppName();

  if ($("authToken")) $("authToken").value = loadSavedToken();
  if ($("humanId")) $("humanId").value = readStorage(DEV_ID_KEY);
  if ($("humanEmail")) $("humanEmail").value = readStorage(DEV_EMAIL_KEY);

  const me = await req("/v1/me");
  if (me.status !== 200 || !Boolean(me?.data?.is_super_admin)) {
    showDomainsAccessBlocked();
    return;
  }

  disableMutatingActions();
  bindReadActions();
  out("meOut", me);
  await listOrgs();
}

init().catch((err) => {
  out("meOut", { error: String(err) });
});
