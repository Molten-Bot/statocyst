package api

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"statocyst/internal/auth"
	"statocyst/internal/handles"
	"statocyst/internal/longpoll"
	"statocyst/internal/model"
	"statocyst/internal/store"
)

const (
	maxPullTimeoutMS     = 30000
	defaultPullTimeoutMS = 5000
)

var uuidPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[1-5][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

type Handler struct {
	control           store.ControlPlaneStore
	queue             store.MessageQueueStore
	waiters           *longpoll.Waiters
	humanAuth         auth.HumanAuthProvider
	now               func() time.Time
	idFactory         func() (string, error)
	supabaseURL       string
	supabaseAnonKey   string
	adminSnapshotKey  string
	superAdminEmails  map[string]struct{}
	superAdminDomains map[string]struct{}
	superAdminReview  bool
	bindTokenTTL      time.Duration
	headlessMode      bool
	storageHealthMu   sync.RWMutex
	storageHealth     store.StorageHealthStatus
	queueRuntimeError string
}

type humanActor struct {
	Human        model.Human
	IsSuperAdmin bool
}

type RouterOptions struct {
	EnableLocalCORS bool
}

func NewHandler(
	control store.ControlPlaneStore,
	queue store.MessageQueueStore,
	waiters *longpoll.Waiters,
	humanAuth auth.HumanAuthProvider,
	supabaseURL,
	supabaseAnonKey,
	adminSnapshotKey,
	superAdminEmailsCSV,
	superAdminDomainsCSV string,
	superAdminReview bool,
	bindTokenTTL time.Duration,
	headlessMode bool,
) *Handler {
	if bindTokenTTL <= 0 {
		bindTokenTTL = 15 * time.Minute
	}
	return &Handler{
		control:           control,
		queue:             queue,
		waiters:           waiters,
		humanAuth:         humanAuth,
		now:               time.Now,
		idFactory:         newUUIDv7,
		supabaseURL:       strings.TrimSpace(supabaseURL),
		supabaseAnonKey:   strings.TrimSpace(supabaseAnonKey),
		adminSnapshotKey:  strings.TrimSpace(adminSnapshotKey),
		superAdminEmails:  parseEmails(superAdminEmailsCSV),
		superAdminDomains: parseDomains(superAdminDomainsCSV),
		superAdminReview:  superAdminReview,
		bindTokenTTL:      bindTokenTTL,
		headlessMode:      headlessMode,
		storageHealth:     store.DefaultStorageHealthStatus(),
	}
}

func NewRouter(handler *Handler) http.Handler {
	return NewRouterWithOptions(handler, RouterOptions{})
}

func NewRouterWithOptions(handler *Handler, opts RouterOptions) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/health", handler.handleHealthz)
	mux.HandleFunc("/openapi.yaml", handler.handleOpenAPIYAML)
	mux.HandleFunc("/v1/ui/config", handler.handleUIConfig)
	mux.HandleFunc("/v1/me", handler.handleMe)
	mux.HandleFunc("/v1/me/metadata", handler.handleMeMetadata)
	mux.HandleFunc("/v1/me/orgs", handler.handleMyOrgs)
	mux.HandleFunc("/v1/me/agents", handler.handleMyAgents)
	mux.HandleFunc("/v1/me/agents/bind-tokens", handler.handleMyAgentBindTokens)
	mux.HandleFunc("/v1/me/agent-trusts", handler.handleMyAgentTrusts)
	mux.HandleFunc("/v1/admin/snapshot", handler.handleAdminSnapshot)
	mux.HandleFunc("/v1/entities/metadata", handler.handleEntitiesMetadata)
	mux.HandleFunc("/v1/public/snapshot", handler.handlePublicSnapshot)
	mux.HandleFunc("/v1/orgs", handler.handleOrgs)
	mux.HandleFunc("/v1/orgs/", handler.handleOrgSubroutes)
	mux.HandleFunc("/v1/org-invites", handler.handleOrgInvites)
	mux.HandleFunc("/v1/org-invites/", handler.handleOrgInvites)
	mux.HandleFunc("/v1/org-access/humans", handler.handleOrgAccessHumans)
	mux.HandleFunc("/v1/org-access/agents", handler.handleOrgAccessAgents)
	mux.HandleFunc("/v1/agents/bind-tokens", handler.handleCreateBindToken)
	mux.HandleFunc("/v1/agents/bind", handler.handleRedeemBindToken)
	mux.HandleFunc("/v1/agents/me", handler.handleAgentMe)
	mux.HandleFunc("/v1/agents/me/metadata", handler.handleAgentMeMetadata)
	mux.HandleFunc("/v1/agents/me/capabilities", handler.handleAgentMeCapabilities)
	mux.HandleFunc("/v1/agents/me/skill", handler.handleAgentMeSkill)
	mux.HandleFunc("/v1/agents/", handler.handleAgentsSubroutes)
	mux.HandleFunc("/v1/org-trusts", handler.handleOrgTrusts)
	mux.HandleFunc("/v1/org-trusts/", handler.handleOrgTrustByID)
	mux.HandleFunc("/v1/agent-trusts", handler.handleAgentTrusts)
	mux.HandleFunc("/v1/agent-trusts/", handler.handleAgentTrustByID)
	mux.HandleFunc("/v1/messages/publish", handler.handlePublish)
	mux.HandleFunc("/v1/messages/pull", handler.handlePull)
	mux.HandleFunc("/", handler.handleUI)
	router := withAPICompression(mux)
	if opts.EnableLocalCORS {
		router = withAPICORS(router)
	}
	return router
}

func withAPICORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !isAPIPath(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}

		origin := strings.TrimSpace(r.Header.Get("Origin"))
		if allowsLocalCORSOrigin(origin) {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			addVaryOrigin(w.Header())
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PATCH, DELETE, OPTIONS")

			reqHeaders := strings.TrimSpace(r.Header.Get("Access-Control-Request-Headers"))
			if reqHeaders != "" {
				w.Header().Set("Access-Control-Allow-Headers", reqHeaders)
				addVaryAccessControlRequestHeaders(w.Header())
			} else {
				w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type, X-Human-Id, X-Human-Email, X-Entities-Metadata-Key, X-Org-Access-Key, X-Admin-Snapshot-Key, X-UI-Config-Key")
			}
			w.Header().Set("Access-Control-Max-Age", "600")
		}

		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}

		next.ServeHTTP(w, r)
	})
}

func allowsLocalCORSOrigin(origin string) bool {
	if origin == "" {
		return false
	}
	if origin == "null" {
		return true
	}
	u, err := url.Parse(origin)
	if err != nil {
		return false
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return false
	}
	host := strings.ToLower(strings.TrimSpace(u.Hostname()))
	return host == "localhost" || host == "127.0.0.1" || host == "::1"
}

func withAPICompression(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !isAPIPath(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}

		addVaryAcceptEncoding(w.Header())
		if !acceptsGzip(r.Header.Get("Accept-Encoding")) || strings.EqualFold(r.Method, http.MethodHead) {
			next.ServeHTTP(w, r)
			return
		}

		gz := gzip.NewWriter(w)
		defer func() { _ = gz.Close() }()

		gzw := &gzipResponseWriter{
			ResponseWriter: w,
			gz:             gz,
		}
		next.ServeHTTP(gzw, r)
	})
}

func isAPIPath(path string) bool {
	return path == "/health" || path == "/openapi.yaml" || strings.HasPrefix(path, "/v1/")
}

func addVaryAcceptEncoding(h http.Header) {
	for _, vary := range h.Values("Vary") {
		for _, token := range strings.Split(vary, ",") {
			if strings.EqualFold(strings.TrimSpace(token), "Accept-Encoding") {
				return
			}
		}
	}
	h.Add("Vary", "Accept-Encoding")
}

func addVaryOrigin(h http.Header) {
	for _, vary := range h.Values("Vary") {
		for _, token := range strings.Split(vary, ",") {
			if strings.EqualFold(strings.TrimSpace(token), "Origin") {
				return
			}
		}
	}
	h.Add("Vary", "Origin")
}

func addVaryAccessControlRequestHeaders(h http.Header) {
	for _, vary := range h.Values("Vary") {
		for _, token := range strings.Split(vary, ",") {
			if strings.EqualFold(strings.TrimSpace(token), "Access-Control-Request-Headers") {
				return
			}
		}
	}
	h.Add("Vary", "Access-Control-Request-Headers")
}

func acceptsGzip(raw string) bool {
	gzipQ := -1.0
	starQ := -1.0

	for _, token := range strings.Split(strings.ToLower(raw), ",") {
		token = strings.TrimSpace(token)
		if token == "" {
			continue
		}
		parts := strings.Split(token, ";")
		name := strings.TrimSpace(parts[0])
		q := 1.0
		for _, param := range parts[1:] {
			kv := strings.SplitN(strings.TrimSpace(param), "=", 2)
			if len(kv) != 2 || strings.TrimSpace(kv[0]) != "q" {
				continue
			}
			if parsed, err := strconv.ParseFloat(strings.TrimSpace(kv[1]), 64); err == nil {
				q = parsed
			}
		}
		switch name {
		case "gzip":
			gzipQ = q
		case "*":
			starQ = q
		}
	}

	if gzipQ >= 0 {
		return gzipQ > 0
	}
	if starQ >= 0 {
		return starQ > 0
	}
	return false
}

type gzipResponseWriter struct {
	http.ResponseWriter
	gz          *gzip.Writer
	wroteHeader bool
}

func (w *gzipResponseWriter) WriteHeader(statusCode int) {
	if w.wroteHeader {
		return
	}
	w.wroteHeader = true
	headers := w.ResponseWriter.Header()
	if headers.Get("Content-Encoding") == "" {
		headers.Set("Content-Encoding", "gzip")
	}
	headers.Del("Content-Length")
	w.ResponseWriter.WriteHeader(statusCode)
}

func (w *gzipResponseWriter) Write(p []byte) (int, error) {
	if !w.wroteHeader {
		w.WriteHeader(http.StatusOK)
	}
	return w.gz.Write(p)
}

func (w *gzipResponseWriter) Flush() {
	_ = w.gz.Flush()
	if flusher, ok := w.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

func (h *Handler) handleHealthz(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeMethodNotAllowed(w)
		return
	}
	health := h.currentStorageHealth()
	if health.StartupMode == "" {
		health = store.DefaultStorageHealthStatus()
	}

	statePayload := map[string]any{
		"backend": health.State.Backend,
		"healthy": health.State.Healthy,
	}
	if strings.TrimSpace(health.State.Error) != "" {
		statePayload["error"] = health.State.Error
	}

	queuePayload := map[string]any{
		"backend": health.Queue.Backend,
		"healthy": health.Queue.Healthy,
	}
	if strings.TrimSpace(health.Queue.Error) != "" {
		queuePayload["error"] = health.Queue.Error
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"status": health.OverallStatus(),
		"storage": map[string]any{
			"startup_mode": health.StartupMode,
			"state":        statePayload,
			"queue":        queuePayload,
		},
	})
}

func (h *Handler) SetStorageHealth(health store.StorageHealthStatus) {
	h.storageHealthMu.Lock()
	defer h.storageHealthMu.Unlock()
	if health.StartupMode == "" {
		h.storageHealth = store.DefaultStorageHealthStatus()
		return
	}
	h.storageHealth = health
}

func (h *Handler) currentStorageHealth() store.StorageHealthStatus {
	h.storageHealthMu.RLock()
	health := h.storageHealth
	runtimeErr := strings.TrimSpace(h.queueRuntimeError)
	h.storageHealthMu.RUnlock()

	if runtimeErr != "" {
		health.Queue.Healthy = false
		if strings.TrimSpace(health.Queue.Error) == "" {
			health.Queue.Error = runtimeErr
		} else {
			health.Queue.Error = health.Queue.Error + "; runtime: " + runtimeErr
		}
	}
	return health
}

func (h *Handler) setQueueRuntimeError(err error) {
	if err == nil {
		return
	}
	msg := strings.TrimSpace(err.Error())
	if msg == "" {
		return
	}
	h.storageHealthMu.Lock()
	h.queueRuntimeError = msg
	h.storageHealthMu.Unlock()
}

func (h *Handler) clearQueueRuntimeError() {
	h.storageHealthMu.Lock()
	h.queueRuntimeError = ""
	h.storageHealthMu.Unlock()
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	encoded := payload
	if pruned, err := pruneEmptyJSONObjectFields(payload); err == nil {
		encoded = pruned
	}
	_ = json.NewEncoder(w).Encode(encoded)
}

func pruneEmptyJSONObjectFields(payload any) (any, error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return payload, err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var decoded any
	if err := decoder.Decode(&decoded); err != nil {
		return payload, err
	}
	pruned, keep := pruneEmptyObjects(decoded)
	if !keep {
		return map[string]any{}, nil
	}
	return pruned, nil
}

func pruneEmptyObjects(value any) (any, bool) {
	switch typed := value.(type) {
	case nil:
		return nil, false
	case map[string]any:
		out := make(map[string]any, len(typed))
		for k, v := range typed {
			next, keep := pruneEmptyObjects(v)
			if !keep {
				continue
			}
			out[k] = next
		}
		if len(out) == 0 {
			return nil, false
		}
		return out, true
	case []any:
		out := make([]any, 0, len(typed))
		for _, v := range typed {
			next, keep := pruneEmptyObjects(v)
			if !keep {
				continue
			}
			out = append(out, next)
		}
		if len(out) == 0 {
			return nil, false
		}
		return out, true
	case string:
		if typed == "" {
			return nil, false
		}
		return typed, true
	default:
		return value, true
	}
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
	return handles.ValidateHandle(agentID) == nil
}

func validateAgentRef(agentRef string) bool {
	return handles.ValidateAgentRef(agentRef) == nil
}

func validateHandle(handle string) bool {
	return handles.ValidateHandle(handle) == nil
}

func normalizeUUID(raw string) string {
	return strings.ToLower(strings.TrimSpace(raw))
}

func validateUUID(raw string) bool {
	return uuidPattern.MatchString(raw)
}

func normalizeHandle(raw string) string {
	return handles.Normalize(raw)
}

func normalizeAgentRef(raw string) string {
	return handles.NormalizeAgentRef(raw)
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
	human, err := h.control.UpsertHuman(identity.Provider, identity.Subject, identity.Email, identity.EmailVerified, h.now().UTC(), h.idFactory)
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
	return h.control.AgentUUIDForTokenHash(tokenHash)
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

func (h *Handler) requireHandleConfirmedForWrite(w http.ResponseWriter, actor humanActor) bool {
	if actor.Human.HandleConfirmedAt != nil {
		return false
	}
	writeError(w, http.StatusConflict, "onboarding_required", "handle setup required before performing this action")
	return true
}

func (h *Handler) isSuperAdminHumanID(humanID string) bool {
	human, err := h.control.GetHuman(humanID)
	if err != nil {
		return false
	}
	return h.isSuperAdmin(auth.HumanIdentity{
		Email:         human.Email,
		EmailVerified: human.EmailVerified,
	})
}
