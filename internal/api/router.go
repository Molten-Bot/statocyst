package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"statocyst/internal/auth"
	"statocyst/internal/longpoll"
	"statocyst/internal/model"
	"statocyst/internal/store"
)

const (
	maxPullTimeoutMS     = 30000
	defaultPullTimeoutMS = 5000
)

var agentIDRegex = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,128}$`)

type Handler struct {
	store             *store.MemoryStore
	waiters           *longpoll.Waiters
	humanAuth         auth.HumanAuthProvider
	now               func() time.Time
	idFactory         func() (string, error)
	supabaseURL       string
	supabaseAnonKey   string
	superAdminEmails  map[string]struct{}
	superAdminDomains map[string]struct{}
	superAdminReview  bool
	bindTokenTTL      time.Duration
}

type humanActor struct {
	Human        model.Human
	IsSuperAdmin bool
}

func NewHandler(
	st *store.MemoryStore,
	waiters *longpoll.Waiters,
	humanAuth auth.HumanAuthProvider,
	supabaseURL,
	supabaseAnonKey,
	superAdminEmailsCSV,
	superAdminDomainsCSV string,
	superAdminReview bool,
	bindTokenTTL time.Duration,
) *Handler {
	if bindTokenTTL <= 0 {
		bindTokenTTL = 15 * time.Minute
	}
	return &Handler{
		store:             st,
		waiters:           waiters,
		humanAuth:         humanAuth,
		now:               time.Now,
		idFactory:         newUUIDv7,
		supabaseURL:       strings.TrimSpace(supabaseURL),
		supabaseAnonKey:   strings.TrimSpace(supabaseAnonKey),
		superAdminEmails:  parseEmails(superAdminEmailsCSV),
		superAdminDomains: parseDomains(superAdminDomainsCSV),
		superAdminReview:  superAdminReview,
		bindTokenTTL:      bindTokenTTL,
	}
}

func NewRouter(handler *Handler) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/health", handler.handleHealthz)
	mux.HandleFunc("/healthz", handler.handleHealthz)
	mux.HandleFunc("/openapi.yaml", handler.handleOpenAPIYAML)
	mux.HandleFunc("/v1/ui/config", handler.handleUIConfig)
	mux.HandleFunc("/v1/me", handler.handleMe)
	mux.HandleFunc("/v1/me/orgs", handler.handleMyOrgs)
	mux.HandleFunc("/v1/me/agents", handler.handleMyAgents)
	mux.HandleFunc("/v1/me/agents/bind-tokens", handler.handleMyAgentBindTokens)
	mux.HandleFunc("/v1/me/agent-trusts", handler.handleMyAgentTrusts)
	mux.HandleFunc("/v1/admin/snapshot", handler.handleAdminSnapshot)
	mux.HandleFunc("/v1/orgs", handler.handleOrgs)
	mux.HandleFunc("/v1/orgs/", handler.handleOrgSubroutes)
	mux.HandleFunc("/v1/org-invites", handler.handleOrgInvites)
	mux.HandleFunc("/v1/org-invites/", handler.handleOrgInvites)
	mux.HandleFunc("/v1/org-access/humans", handler.handleOrgAccessHumans)
	mux.HandleFunc("/v1/org-access/agents", handler.handleOrgAccessAgents)
	mux.HandleFunc("/v1/agents/bind-tokens", handler.handleCreateBindToken)
	mux.HandleFunc("/v1/agents/bind/redeem", handler.handleRedeemBindToken)
	mux.HandleFunc("/v1/agents/me/capabilities", handler.handleAgentMeCapabilities)
	mux.HandleFunc("/v1/agents/me/skill", handler.handleAgentMeSkill)
	mux.HandleFunc("/v1/agents/register", handler.handleRegisterAgent)
	mux.HandleFunc("/v1/agents/", handler.handleAgentsSubroutes)
	mux.HandleFunc("/v1/org-trusts", handler.handleOrgTrusts)
	mux.HandleFunc("/v1/org-trusts/", handler.handleOrgTrustByID)
	mux.HandleFunc("/v1/agent-trusts", handler.handleAgentTrusts)
	mux.HandleFunc("/v1/agent-trusts/", handler.handleAgentTrustByID)
	mux.HandleFunc("/v1/messages/publish", handler.handlePublish)
	mux.HandleFunc("/v1/messages/pull", handler.handlePull)
	mux.HandleFunc("/", handler.handleUI)
	return mux
}

func (h *Handler) handleHealthz(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeMethodNotAllowed(w)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func writeError(w http.ResponseWriter, status int, code string, message string) {
	writeJSON(w, status, map[string]string{
		"error":   code,
		"message": message,
	})
}

func writeMethodNotAllowed(w http.ResponseWriter) {
	writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
}

func decodeJSON(r *http.Request, out any) error {
	defer r.Body.Close()
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	return decoder.Decode(out)
}

func validateAgentID(agentID string) bool {
	return agentIDRegex.MatchString(agentID)
}

func parsePullTimeout(r *http.Request) (time.Duration, error) {
	raw := strings.TrimSpace(r.URL.Query().Get("timeout_ms"))
	if raw == "" {
		return time.Duration(defaultPullTimeoutMS) * time.Millisecond, nil
	}
	ms, err := strconv.Atoi(raw)
	if err != nil {
		return 0, errors.New("timeout_ms must be an integer")
	}
	if ms < 0 || ms > maxPullTimeoutMS {
		return 0, errors.New("timeout_ms must be in range 0..30000")
	}
	return time.Duration(ms) * time.Millisecond, nil
}

func (h *Handler) authenticateHuman(r *http.Request) (humanActor, error) {
	identity, err := h.humanAuth.Authenticate(r)
	if err != nil {
		return humanActor{}, err
	}
	human, err := h.store.UpsertHuman(identity.Provider, identity.Subject, identity.Email, identity.EmailVerified, h.now().UTC(), h.idFactory)
	if err != nil {
		return humanActor{}, err
	}
	return humanActor{
		Human:        human,
		IsSuperAdmin: h.isSuperAdmin(identity),
	}, nil
}

func (h *Handler) authenticateAgent(r *http.Request) (string, error) {
	token, err := auth.ExtractBearerToken(r.Header.Get("Authorization"))
	if err != nil {
		return "", err
	}
	tokenHash := auth.HashToken(token)
	return h.store.AgentIDForTokenHash(tokenHash)
}

func splitPath(path string) []string {
	trimmed := strings.Trim(path, "/")
	if trimmed == "" {
		return nil
	}
	return strings.Split(trimmed, "/")
}

func parseDomains(csv string) map[string]struct{} {
	out := make(map[string]struct{})
	for _, raw := range strings.Split(csv, ",") {
		d := strings.ToLower(strings.TrimSpace(raw))
		if d == "" {
			continue
		}
		if strings.HasPrefix(d, "@") {
			d = strings.TrimPrefix(d, "@")
		}
		out[d] = struct{}{}
	}
	return out
}

func parseEmails(csv string) map[string]struct{} {
	out := make(map[string]struct{})
	for _, raw := range strings.Split(csv, ",") {
		email := strings.ToLower(strings.TrimSpace(raw))
		if email == "" {
			continue
		}
		out[email] = struct{}{}
	}
	return out
}

func setToSortedSlice(set map[string]struct{}) []string {
	out := make([]string, 0, len(set))
	for key := range set {
		out = append(out, key)
	}
	sort.Strings(out)
	return out
}

func (h *Handler) isSuperAdmin(identity auth.HumanIdentity) bool {
	if !h.superAdminReview {
		return false
	}
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

	parts := strings.Split(email, "@")
	if len(parts) != 2 {
		return false
	}
	_, ok := h.superAdminDomains[parts[1]]
	return ok
}

func (h *Handler) denySuperAdminWrite(w http.ResponseWriter, actor humanActor) bool {
	if actor.IsSuperAdmin {
		writeError(w, http.StatusForbidden, "super_admin_read_only", "super admin is read-only")
		return true
	}
	return false
}
