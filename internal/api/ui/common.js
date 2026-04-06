const MoltenHubUI = (() => {
  const TOKEN_KEY = "moltenhub_access_token";
  const DEV_ID_KEY = "moltenhub_dev_human_id";
  const DEV_EMAIL_KEY = "moltenhub_dev_human_email";

  const $ = (id) => document.getElementById(id);

  function readStorage(key) {
    return localStorage.getItem(key) || "";
  }

  function getSession() {
    return {
      token: readStorage(TOKEN_KEY).trim(),
      humanID: readStorage(DEV_ID_KEY).trim(),
      humanEmail: readStorage(DEV_EMAIL_KEY).trim(),
    };
  }

  function clearSession() {
    localStorage.removeItem(TOKEN_KEY);
    localStorage.removeItem(DEV_ID_KEY);
    localStorage.removeItem(DEV_EMAIL_KEY);
  }

  function clearSupabaseSessionKeys() {
    const keys = [];
    for (let i = 0; i < localStorage.length; i += 1) {
      const key = localStorage.key(i) || "";
      if (key.startsWith("sb-") || key.includes("supabase")) {
        keys.push(key);
      }
    }
    for (const key of keys) {
      localStorage.removeItem(key);
    }
  }

  function headers() {
    const session = getSession();
    const h = { "Content-Type": "application/json" };
    if (session.token) h.Authorization = `Bearer ${session.token}`;
    if (session.humanID) h["X-Human-Id"] = session.humanID;
    if (session.humanEmail) h["X-Human-Email"] = session.humanEmail;
    return h;
  }

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

  function out(elID, payload) {
    const el = $(elID);
    if (!el) return;
    el.textContent = JSON.stringify(payload, null, 2);
  }

  function initTopNav() {
    const profileBtn = $("btnProfile");
    if (profileBtn) {
      profileBtn.onclick = () => {
        window.location.assign("/profile");
      };
    }

    const logoutBtn = $("btnLogout");
    if (logoutBtn) {
      logoutBtn.onclick = () => {
        clearSession();
        clearSupabaseSessionKeys();
        window.location.assign("/");
      };
    }

    const adminOnlyLinks = document.querySelectorAll("[data-admin-only]");
    for (const node of adminOnlyLinks) {
      node.style.display = "none";
    }

    req("/v1/me")
      .then((r) => {
        const isAdmin = r.status === 200 && Boolean(r?.data?.admin);
        for (const node of adminOnlyLinks) {
          node.style.display = isAdmin ? "" : "none";
        }
      })
      .catch(() => {
        for (const node of adminOnlyLinks) {
          node.style.display = "none";
        }
      });

    populateAgentIdentityBadge().catch(() => {
      setAgentIdentityBadge(null);
    });
  }

  function metadataFrom(raw) {
    if (!raw || typeof raw !== "object" || Array.isArray(raw)) return {};
    return { ...raw };
  }

  function setAgentIdentityBadge(agent) {
    const root = $("agentIdentity");
    const emojiEl = $("agentIdentityEmoji");
    const nameEl = $("agentIdentityName");
    const uuidEl = $("agentIdentityUUID");
    if (!root || !emojiEl || !nameEl || !uuidEl) return;

    if (!agent) {
      root.hidden = true;
      return;
    }

    const metadata = metadataFrom(agent.metadata);
    const displayName = String(metadata.display_name || agent.handle || agent.agent_id || "Agent").trim();
    const emoji = String(metadata.emoji || "🤖").trim() || "🤖";
    const uuid = String(agent.agent_uuid || "").trim();

    emojiEl.textContent = emoji;
    nameEl.textContent = displayName;
    uuidEl.textContent = uuid ? `ID: ${uuid}` : "ID unavailable";
    root.hidden = false;
  }

  async function populateAgentIdentityBadge() {
    const agentsRes = await req("/v1/me/agents");
    if (agentsRes.status !== 200 || !Array.isArray(agentsRes?.data?.agents) || agentsRes.data.agents.length === 0) {
      setAgentIdentityBadge(null);
      return;
    }

    const agents = agentsRes.data.agents.slice().sort((left, right) => {
      const leftActive = String(left?.status || "").toLowerCase() === "active" ? 0 : 1;
      const rightActive = String(right?.status || "").toLowerCase() === "active" ? 0 : 1;
      if (leftActive !== rightActive) return leftActive - rightActive;
      return String(left?.created_at || "").localeCompare(String(right?.created_at || ""));
    });
    setAgentIdentityBadge(agents[0]);
  }

  async function populateOrgSelect(selectID, outputID = "") {
    const r = await req("/v1/me/orgs");
    if (outputID) out(outputID, r);

    const select = $(selectID);
    if (!select) return r;
    select.innerHTML = "";

    if (r.status !== 200 || !Array.isArray(r.data.memberships)) {
      return r;
    }

    for (const membership of r.data.memberships) {
      const opt = document.createElement("option");
      opt.value = membership.org.org_id;
      opt.textContent = `${membership.org.display_name || membership.org.handle} (${membership.membership.role})`;
      select.appendChild(opt);
    }

    return r;
  }

  function selectedOrg(selectID) {
    return ($(selectID)?.value || "").trim();
  }

  return {
    $,
    req,
    out,
    initTopNav,
    populateAgentIdentityBadge,
    populateOrgSelect,
    selectedOrg,
    clearSession,
  };
})();
