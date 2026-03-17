package main

import (
	"crypto/subtle"
	"encoding/json"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"statocyst/internal/api"
	"statocyst/internal/auth"
	"statocyst/internal/store"
)

type bootstrapOptions struct {
	humanAuth         auth.HumanAuthProvider
	supabaseURL       string
	supabaseAnonKey   string
	superAdminEmails  string
	superAdminDomains string
	superAdminReview  bool
	bindTokenTTL      time.Duration
	headlessMode      bool
}

type bootstrapHandler struct {
	active atomic.Value

	startupMu      sync.RWMutex
	startupMode    store.StorageStartupMode
	stateBackend   string
	stateHealthy   bool
	stateError     string
	queueBackend   string
	queueHealthy   bool
	queueError     string
	startedAt      time.Time
	phase          string
	phaseStartedAt time.Time

	humanAuth         auth.HumanAuthProvider
	supabaseURL       string
	supabaseAnonKey   string
	superAdminEmails  map[string]struct{}
	superAdminDomains map[string]struct{}
	superAdminReview  bool
	bindTokenTTL      time.Duration
	headlessMode      bool
}

func newBootstrapHandler(startupMode store.StorageStartupMode, stateBackend, queueBackend string, opts ...bootstrapOptions) *bootstrapHandler {
	if strings.TrimSpace(stateBackend) == "" {
		stateBackend = "memory"
	}
	if strings.TrimSpace(queueBackend) == "" {
		queueBackend = "memory"
	}
	option := bootstrapOptions{
		humanAuth:    auth.NewDevHumanAuthProvider(),
		bindTokenTTL: 15 * time.Minute,
	}
	if len(opts) > 0 {
		option = opts[0]
		if option.humanAuth == nil {
			option.humanAuth = auth.NewDevHumanAuthProvider()
		}
		if option.bindTokenTTL <= 0 {
			option.bindTokenTTL = 15 * time.Minute
		}
	}
	startedAt := time.Now().UTC()

	return &bootstrapHandler{
		startupMode:       startupMode,
		stateBackend:      stateBackend,
		stateHealthy:      false,
		stateError:        "startup in progress",
		queueBackend:      queueBackend,
		queueHealthy:      false,
		queueError:        "startup in progress",
		startedAt:         startedAt,
		phase:             "boot",
		phaseStartedAt:    startedAt,
		humanAuth:         option.humanAuth,
		supabaseURL:       strings.TrimSpace(option.supabaseURL),
		supabaseAnonKey:   strings.TrimSpace(option.supabaseAnonKey),
		superAdminEmails:  parseCSVSet(option.superAdminEmails),
		superAdminDomains: parseCSVSet(option.superAdminDomains),
		superAdminReview:  option.superAdminReview,
		bindTokenTTL:      option.bindTokenTTL,
		headlessMode:      option.headlessMode,
	}
}

func (h *bootstrapHandler) SetReady(next http.Handler) {
	h.active.Store(next)
}

func (h *bootstrapHandler) SetStartupPhase(phase string, now time.Time) {
	phase = strings.TrimSpace(phase)
	if phase == "" {
		return
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	h.startupMu.Lock()
	h.phase = phase
	h.phaseStartedAt = now
	h.startupMu.Unlock()
}

func (h *bootstrapHandler) SetStartupStorageHealth(health store.StorageHealthStatus) {
	h.startupMu.Lock()
	if health.StartupMode != "" {
		h.startupMode = health.StartupMode
	}
	if backend := strings.TrimSpace(health.State.Backend); backend != "" {
		h.stateBackend = backend
	}
	h.stateHealthy = health.State.Healthy
	h.stateError = strings.TrimSpace(health.State.Error)
	if backend := strings.TrimSpace(health.Queue.Backend); backend != "" {
		h.queueBackend = backend
	}
	h.queueHealthy = health.Queue.Healthy
	h.queueError = strings.TrimSpace(health.Queue.Error)
	h.startupMu.Unlock()
}

func (h *bootstrapHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if next := h.active.Load(); next != nil {
		next.(http.Handler).ServeHTTP(w, r)
		return
	}

	switch r.URL.Path {
	case "/ping":
		handlePing(w, r)
	case "/health":
		h.handleStartupHealth(w, r)
	case "/openapi.yaml":
		api.WriteOpenAPIYAML(w, r)
	case "/openapi.md":
		api.WriteOpenAPIMarkdown(w, r)
	case "/v1/ui/config":
		h.handleStartupUIConfig(w, r)
	case "/v1/me":
		h.handleStartupMe(w, r)
	default:
		if api.ServeStartupStaticUI(w, r, h.headlessMode) {
			return
		}
		h.handleStarting(w, r)
	}
}

func handlePing(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *bootstrapHandler) handleStartupHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET")
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	now := time.Now().UTC()
	h.startupMu.RLock()
	startedAt := h.startedAt
	phase := h.phase
	phaseStartedAt := h.phaseStartedAt
	startupMode := h.startupMode
	stateBackend := h.stateBackend
	stateHealthy := h.stateHealthy
	stateError := h.stateError
	queueBackend := h.queueBackend
	queueHealthy := h.queueHealthy
	queueError := h.queueError
	h.startupMu.RUnlock()

	status := "degraded"
	if stateHealthy && queueHealthy {
		status = "ok"
	}

	statePayload := map[string]any{
		"backend": stateBackend,
		"healthy": stateHealthy,
	}
	if strings.TrimSpace(stateError) != "" {
		statePayload["error"] = stateError
	}
	queuePayload := map[string]any{
		"backend":            queueBackend,
		"healthy":            queueHealthy,
		"available_messages": 0,
		"leased_messages":    0,
	}
	if strings.TrimSpace(queueError) != "" {
		queuePayload["error"] = queueError
	}

	phaseElapsed := now.Sub(phaseStartedAt).Milliseconds()
	if phaseElapsed < 0 {
		phaseElapsed = 0
	}
	totalElapsed := now.Sub(startedAt).Milliseconds()
	if totalElapsed < 0 {
		totalElapsed = 0
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"status":      status,
		"boot_status": "starting",
		"startup": map[string]any{
			"phase":            phase,
			"started_at":       startedAt,
			"phase_started_at": phaseStartedAt,
			"phase_elapsed_ms": phaseElapsed,
			"total_elapsed_ms": totalElapsed,
		},
		"storage": map[string]any{
			"startup_mode": startupMode,
			"state":        statePayload,
			"queue":        queuePayload,
		},
	})
}

func (h *bootstrapHandler) handleStartupUIConfig(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET")
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	authConfig := map[string]any{
		"human": h.humanAuth.Name(),
	}
	if h.humanAuth.Name() == "dev" {
		devConfig := map[string]any{}
		if devHumanID := strings.TrimSpace(os.Getenv("DEV_LOGIN_HUMAN_ID")); devHumanID != "" {
			devConfig["human_id"] = devHumanID
		}
		if devHumanEmail := strings.ToLower(strings.TrimSpace(os.Getenv("DEV_LOGIN_HUMAN_EMAIL"))); devHumanEmail != "" {
			devConfig["human_email"] = devHumanEmail
		}
		if len(devConfig) > 0 {
			authConfig["dev"] = devConfig
		}
	}
	if h.humanAuth.Name() == "supabase" {
		supabaseConfig := map[string]any{}
		if h.supabaseURL != "" {
			supabaseConfig["url"] = h.supabaseURL
		}
		if auth.IsSafeSupabaseBrowserKey(h.supabaseAnonKey) {
			supabaseConfig["anon_key"] = h.supabaseAnonKey
		}
		if len(supabaseConfig) > 0 {
			authConfig["supabase"] = supabaseConfig
		}
	}

	superAdminEmails := []string{}
	if hasStartupUIConfigPrivilegedAccess(r) {
		superAdminEmails = sortedSetValues(h.superAdminEmails)
	}
	adminConfig := map[string]any{
		"review_mode":  h.superAdminReview,
		"write_policy": "global_write",
	}
	if len(superAdminEmails) > 0 {
		adminConfig["emails"] = superAdminEmails
	}
	superAdminDomains := sortedSetValues(h.superAdminDomains)
	if len(superAdminDomains) > 0 {
		adminConfig["domains"] = superAdminDomains
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"auth":               authConfig,
		"dev_auto_login":     strings.EqualFold(strings.TrimSpace(os.Getenv("DEV_LOGIN_AUTO")), "true"),
		"admin":              adminConfig,
		"bind_token_ttl_sec": int(h.bindTokenTTL.Seconds()),
		"headless_mode":      h.headlessMode,
		"boot_status":        "starting",
	})
}

func (h *bootstrapHandler) handleStartupMe(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Retry-After", "1")
	switch r.Method {
	case http.MethodGet:
		identity, err := h.humanAuth.Authenticate(r)
		if err != nil {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"error":   "unauthorized",
				"message": "missing or invalid human auth",
			})
			return
		}
		email := strings.ToLower(strings.TrimSpace(identity.Email))
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status":      "starting",
			"boot_status": "starting",
			"ready":       false,
			"identity": map[string]any{
				"provider":       identity.Provider,
				"subject":        identity.Subject,
				"email":          email,
				"email_verified": identity.EmailVerified,
			},
			"admin": h.isSuperAdmin(identity),
		})
	case http.MethodPatch:
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status":  "starting",
			"message": "statocyst is starting",
		})
	default:
		w.Header().Set("Allow", "GET, PATCH")
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func (h *bootstrapHandler) handleStarting(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Retry-After", "1")
	if strings.HasPrefix(r.URL.Path, "/v1/") {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status":  "starting",
			"message": "statocyst is starting",
		})
		return
	}

	http.Error(w, "statocyst is starting", http.StatusServiceUnavailable)
}

func configuredBackendFromEnv(raw, fallback string) string {
	value := strings.ToLower(strings.TrimSpace(raw))
	if value == "" {
		return fallback
	}
	return value
}

func hasStartupUIConfigPrivilegedAccess(r *http.Request) bool {
	expectedKey := strings.TrimSpace(os.Getenv("UI_CONFIG_API_KEY"))
	if expectedKey == "" {
		return false
	}
	presentedKey := strings.TrimSpace(r.Header.Get("X-UI-Config-Key"))
	if presentedKey == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(presentedKey), []byte(expectedKey)) == 1
}

func parseCSVSet(csv string) map[string]struct{} {
	out := make(map[string]struct{})
	for _, raw := range strings.Split(csv, ",") {
		value := strings.ToLower(strings.TrimSpace(raw))
		if value == "" {
			continue
		}
		if strings.HasPrefix(value, "@") {
			value = strings.TrimPrefix(value, "@")
		}
		out[value] = struct{}{}
	}
	return out
}

func sortedSetValues(values map[string]struct{}) []string {
	out := make([]string, 0, len(values))
	for value := range values {
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}

func (h *bootstrapHandler) isSuperAdmin(identity auth.HumanIdentity) bool {
	if !identity.EmailVerified {
		return false
	}
	email := strings.ToLower(strings.TrimSpace(identity.Email))
	if email == "" {
		return false
	}
	if _, ok := h.superAdminEmails[email]; ok {
		return true
	}
	at := strings.LastIndex(email, "@")
	if at <= 0 || at == len(email)-1 {
		return false
	}
	domain := email[at+1:]
	_, ok := h.superAdminDomains[domain]
	return ok
}
