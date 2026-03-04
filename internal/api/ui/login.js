const TOKEN_KEY = "statocyst_access_token";

const $ = (id) => document.getElementById(id);

let supabaseClient = null;
let oauthEnabled = false;

function setStatus(message, kind = "info") {
  const el = $("status");
  el.textContent = message;
  el.className = `status${kind === "warn" ? " warn" : ""}`;
}

function loadSavedToken() {
  return localStorage.getItem(TOKEN_KEY) || "";
}

function saveToken(token) {
  if (!token) {
    return;
  }
  localStorage.setItem(TOKEN_KEY, token);
}

function clearToken() {
  localStorage.removeItem(TOKEN_KEY);
}

async function fetchJSON(path, token = "") {
  const headers = {};
  if (token) {
    headers.Authorization = `Bearer ${token}`;
  }
  const res = await fetch(path, { headers });
  const text = await res.text();
  let data = text;
  try {
    data = JSON.parse(text || "{}");
  } catch (_) {}
  return { status: res.status, data };
}

async function trySavedSession() {
  const token = loadSavedToken();
  if (!token) {
    return false;
  }
  const result = await fetchJSON("/v1/me", token);
  if (result.status === 200) {
    setStatus("Existing session found. Redirecting to /profile ...");
    window.location.assign("/profile");
    return true;
  }
  clearToken();
  return false;
}

async function cacheSupabaseSessionIfPresent() {
  if (!supabaseClient) {
    return false;
  }
  const { data, error } = await supabaseClient.auth.getSession();
  if (error) {
    setStatus(`Session check failed: ${error.message}`, "warn");
    return false;
  }
  const token = data?.session?.access_token || "";
  if (!token) {
    return false;
  }
  saveToken(token);
  setStatus("Supabase session active. Redirecting to /profile ...");
  window.location.assign("/profile");
  return true;
}

async function startGoogleLogin() {
  if (!supabaseClient) {
    setStatus("Supabase client not available. Continuing locally.", "warn");
    window.location.assign("/profile");
    return;
  }

  setStatus("Redirecting to Google login ...");
  const { error } = await supabaseClient.auth.signInWithOAuth({
    provider: "google",
    options: {
      redirectTo: `${window.location.origin}/`,
    },
  });
  if (error) {
    setStatus(`Google login failed: ${error.message}`, "warn");
  }
}

async function init() {
  $("loginBtn").onclick = async () => {
    if (oauthEnabled) {
      await startGoogleLogin();
      return;
    }
    window.location.assign("/profile");
  };

  const cfg = await fetchJSON("/v1/ui/config");
  if (cfg.status !== 200) {
    setStatus("Could not read /v1/ui/config. Login will continue locally.", "warn");
    return;
  }

  const provider = String(cfg.data.human_auth_provider || "").toLowerCase();
  const supabaseURL = String(cfg.data.supabase_url || "").trim();
  const supabaseAnonKey = String(cfg.data.supabase_anon_key || "").trim();

  if (await trySavedSession()) {
    return;
  }

  if (provider !== "supabase") {
    $("loginBtn").textContent = "Login (Local Dev Skip)";
    setStatus("Supabase auth not enabled. Login button will continue to /profile.", "warn");
    return;
  }

  if (!supabaseURL || !supabaseAnonKey) {
    $("loginBtn").textContent = "Login (Config Missing, Skip)";
    setStatus("SUPABASE_URL or SUPABASE_ANON_KEY missing. Login button will continue locally.", "warn");
    return;
  }

  if (!window.supabase || typeof window.supabase.createClient !== "function") {
    $("loginBtn").textContent = "Login (SDK Missing, Skip)";
    setStatus("Supabase JS SDK failed to load. Login button will continue locally.", "warn");
    return;
  }

  supabaseClient = window.supabase.createClient(supabaseURL, supabaseAnonKey, {
    auth: {
      detectSessionInUrl: true,
      persistSession: true,
      autoRefreshToken: true,
    },
  });

  supabaseClient.auth.onAuthStateChange((_event, session) => {
    const token = session?.access_token || "";
    if (!token) {
      return;
    }
    saveToken(token);
  });

  if (await cacheSupabaseSessionIfPresent()) {
    return;
  }

  oauthEnabled = true;
  $("loginBtn").textContent = "Login with Google";
  setStatus("Click Login with Google to authenticate via Supabase.");
}

init().catch((err) => {
  setStatus(`Unexpected error: ${err?.message || String(err)}`, "warn");
});
