package api

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
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
	maxPullTimeoutMS                   = 30000
	defaultPullTimeoutMS               = 5000
	defaultPeerOutboxBackgroundTimeout = 5 * time.Second
)

var uuidPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[1-5][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

type Handler struct {
	control           store.ControlPlaneStore
	queue             store.MessageQueueStore
	waiters           *longpoll.Waiters
	humanAuth         auth.HumanAuthProvider
	now               func() time.Time
	idFactory         func() (string, error)
	canonicalBaseURL  string
	supabaseURL       string
	supabaseAnonKey   string
	adminSnapshotKey  string
	superAdminEmails  map[string]struct{}
	superAdminDomains map[string]struct{}
	superAdminReview  bool
	bindTokenTTL      time.Duration
	headlessMode      bool
	headlessModeURL   string
	storageHealthMu   sync.RWMutex
	storageHealth     store.StorageHealthStatus
	startupSummary    map[string]any
	queueRuntimeError string
	peerHTTPClient    *http.Client
	peerOutboxMu      sync.Mutex
	peerOutboxRunning bool
	peerOutboxTimeout time.Duration
}

type requestIDContextKey struct{}

type errorHint struct {
	Retryable  bool
	NextAction string
}

type humanActor struct {
	Human        model.Human
	IsSuperAdmin bool
}

type RouterOptions struct {
	EnableLocalCORS    bool
	AllowedCORSOrigins map[string]struct{}
}

func NewHandler(
	control store.ControlPlaneStore,
	queue store.MessageQueueStore,
	waiters *longpoll.Waiters,
	humanAuth auth.HumanAuthProvider,
	canonicalBaseURL,
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
		canonicalBaseURL:  normalizeCanonicalBaseURL(canonicalBaseURL),
		supabaseURL:       strings.TrimSpace(supabaseURL),
		supabaseAnonKey:   strings.TrimSpace(supabaseAnonKey),
		adminSnapshotKey:  strings.TrimSpace(adminSnapshotKey),
		superAdminEmails:  parseEmails(superAdminEmailsCSV),
		superAdminDomains: parseDomains(superAdminDomainsCSV),
		superAdminReview:  superAdminReview,
		bindTokenTTL:      bindTokenTTL,
		headlessMode:      headlessMode,
		headlessModeURL:   "",
		storageHealth:     store.DefaultStorageHealthStatus(),
		startupSummary:    map[string]any{},
		peerHTTPClient:    &http.Client{Timeout: 5 * time.Second},
		peerOutboxTimeout: defaultPeerOutboxBackgroundTimeout,
	}
}

func NewRouter(handler *Handler) http.Handler {
	return NewRouterWithOptions(handler, RouterOptions{})
}

func NewRouterWithOptions(handler *Handler, opts RouterOptions) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/ping", handlePing)
	mux.HandleFunc("/health", handler.handleHealthz)
	mux.HandleFunc("/openapi.yaml", handler.handleOpenAPIYAML)
	mux.HandleFunc("/openapi.md", handler.handleOpenAPIMarkdown)
	mux.HandleFunc("/v1/ui/config", handler.handleUIConfig)
	mux.HandleFunc("/v1/me", handler.handleMe)
	mux.HandleFunc("/v1/me/metadata", handler.handleMeMetadata)
	mux.HandleFunc("/v1/me/orgs", handler.handleMyOrgs)
	mux.HandleFunc("/v1/me/agents", handler.handleMyAgents)
	mux.HandleFunc("/v1/me/agents/bind-tokens", handler.handleMyAgentBindTokens)
	mux.HandleFunc("/v1/me/agent-trusts", handler.handleMyAgentTrusts)
	mux.HandleFunc("/v1/admin/snapshot", handler.handleAdminSnapshot)
	mux.HandleFunc("/v1/admin/peers", handler.handleAdminPeers)
	mux.HandleFunc("/v1/admin/peers/", handler.handleAdminPeerByID)
	mux.HandleFunc("/v1/admin/remote-org-trusts", handler.handleAdminRemoteOrgTrusts)
	mux.HandleFunc("/v1/admin/remote-org-trusts/", handler.handleAdminRemoteOrgTrustByID)
	mux.HandleFunc("/v1/admin/remote-agent-trusts", handler.handleAdminRemoteAgentTrusts)
	mux.HandleFunc("/v1/admin/remote-agent-trusts/", handler.handleAdminRemoteAgentTrustByID)
	mux.HandleFunc("/v1/entities/metadata", handler.handleEntitiesMetadata)
	mux.HandleFunc("/v1/public/peers", handler.handlePublicPeers)
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
	mux.HandleFunc("/v1/agents/me/manifest", handler.handleAgentMeManifest)
	mux.HandleFunc("/v1/agents/me/capabilities", handler.handleAgentMeCapabilities)
	mux.HandleFunc("/v1/agents/me/skill", handler.handleAgentMeSkill)
	mux.HandleFunc("/v1/agents/", handler.handleAgentsSubroutes)
	mux.HandleFunc("/v1/org-trusts", handler.handleOrgTrusts)
	mux.HandleFunc("/v1/org-trusts/", handler.handleOrgTrustByID)
	mux.HandleFunc("/v1/agent-trusts", handler.handleAgentTrusts)
	mux.HandleFunc("/v1/agent-trusts/", handler.handleAgentTrustByID)
	mux.HandleFunc("/v1/messages/publish", handler.handlePublish)
	mux.HandleFunc("/v1/messages/pull", handler.handlePull)
	mux.HandleFunc("/v1/messages/", handler.handleMessageSubroutes)
	mux.HandleFunc("/v1/openclaw/messages/publish", handler.handleOpenClawPublish)
	mux.HandleFunc("/v1/openclaw/messages/pull", handler.handleOpenClawPull)
	mux.HandleFunc("/v1/openclaw/messages/", handler.handleOpenClawMessageSubroutes)
	mux.HandleFunc("/v1/peer/messages", handler.handlePeerInboundMessage)
	mux.HandleFunc("/", handler.handleUI)
	router := withAPICompression(mux)
	router = withPeerOutboxProcessing(handler, router)
	if opts.EnableLocalCORS || len(opts.AllowedCORSOrigins) > 0 {
		router = withAPICORS(router, opts.EnableLocalCORS, opts.AllowedCORSOrigins)
	}
	router = withRequestCorrelation(router)
	return router
}

func (h *Handler) SetHeadlessModeRedirectURL(raw string) {
	h.headlessModeURL = strings.TrimSpace(raw)
}

func withPeerOutboxProcessing(handler *Handler, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/v1/") && r.URL.Path != "/v1/peer/messages" {
			handler.kickPeerOutboxProcessing(16)
		}
		next.ServeHTTP(w, r)
	})
}

func (h *Handler) kickPeerOutboxProcessing(limit int) {
	h.peerOutboxMu.Lock()
	if h.peerOutboxRunning {
		h.peerOutboxMu.Unlock()
		return
	}
	h.peerOutboxRunning = true
	timeout := h.peerOutboxTimeout
	if timeout <= 0 {
		timeout = defaultPeerOutboxBackgroundTimeout
	}
	h.peerOutboxMu.Unlock()

	go func() {
		defer func() {
			h.peerOutboxMu.Lock()
			h.peerOutboxRunning = false
			h.peerOutboxMu.Unlock()
		}()
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()
		h.processPeerOutboxes(ctx, limit)
	}()
}

func withAPICORS(next http.Handler, enableLocalCORS bool, allowedOrigins map[string]struct{}) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !isAPIPath(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}

		origin := strings.TrimSpace(r.Header.Get("Origin"))
		if allowsCORSOrigin(origin, enableLocalCORS, allowedOrigins) {
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

func ParseCORSAllowedOrigins(raw string) (map[string]struct{}, error) {
	origins := make(map[string]struct{})
	for _, part := range strings.FieldsFunc(raw, func(r rune) bool {
		return r == ',' || r == '\n'
	}) {
		normalized, err := normalizeAllowedOrigin(part)
		if err != nil {
			return nil, err
		}
		if normalized == "" {
			continue
		}
		origins[normalized] = struct{}{}
	}
	return origins, nil
}

func allowsCORSOrigin(origin string, enableLocalCORS bool, allowedOrigins map[string]struct{}) bool {
	if origin == "" {
		return false
	}
	if enableLocalCORS && allowsLocalCORSOrigin(origin) {
		return true
	}
	normalized, err := normalizeAllowedOrigin(origin)
	if err != nil {
		return false
	}
	_, ok := allowedOrigins[normalized]
	return ok
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

func normalizeAllowedOrigin(raw string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", nil
	}
	u, err := url.Parse(trimmed)
	if err != nil {
		return "", fmt.Errorf("invalid CORS origin %q: %w", raw, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", fmt.Errorf("invalid CORS origin %q: scheme must be http or https", raw)
	}
	if strings.TrimSpace(u.Host) == "" {
		return "", fmt.Errorf("invalid CORS origin %q: host is required", raw)
	}
	if u.User != nil {
		return "", fmt.Errorf("invalid CORS origin %q: userinfo is not allowed", raw)
	}
	if u.RawQuery != "" || u.Fragment != "" {
		return "", fmt.Errorf("invalid CORS origin %q: query and fragment are not allowed", raw)
	}
	if u.Path != "" && u.Path != "/" {
		return "", fmt.Errorf("invalid CORS origin %q: path is not allowed", raw)
	}
	return strings.ToLower(u.Scheme) + "://" + strings.ToLower(u.Host), nil
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
	return path == "/health" || path == "/openapi.yaml" || path == "/openapi.md" || strings.HasPrefix(path, "/v1/")
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

func handlePing(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		writeMethodNotAllowed(w)
		return
	}
	w.WriteHeader(http.StatusNoContent)
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
	if stateErr := store.SanitizeErrorText(health.State.Error); stateErr != "" {
		statePayload["error"] = stateErr
	}

	queuePayload := map[string]any{
		"backend": health.Queue.Backend,
		"healthy": health.Queue.Healthy,
	}
	queueMetrics := h.control.GetQueueMetrics()
	queuePayload["available_messages"] = queueMetrics.AvailableMessages
	queuePayload["leased_messages"] = queueMetrics.LeasedMessages
	if queueMetrics.OldestQueuedAt != nil {
		queuePayload["oldest_queued_at"] = queueMetrics.OldestQueuedAt
	}
	if queueMetrics.OldestLeaseExpiryAt != nil {
		queuePayload["oldest_lease_expires_at"] = queueMetrics.OldestLeaseExpiryAt
	}
	if queueErr := store.SanitizeErrorText(health.Queue.Error); queueErr != "" {
		queuePayload["error"] = queueErr
	}

	payload := map[string]any{
		"status": health.OverallStatus(),
		"storage": map[string]any{
			"startup_mode": health.StartupMode,
			"state":        statePayload,
			"queue":        queuePayload,
		},
	}
	for key, value := range h.currentStartupSummary() {
		payload[key] = value
	}
	writeJSON(w, http.StatusOK, payload)
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

func (h *Handler) SetStartupSummary(summary map[string]any) {
	h.storageHealthMu.Lock()
	if len(summary) == 0 {
		h.startupSummary = map[string]any{}
		h.storageHealthMu.Unlock()
		return
	}
	out := make(map[string]any, len(summary))
	for key, value := range summary {
		out[key] = value
	}
	h.startupSummary = out
	h.storageHealthMu.Unlock()
}

func (h *Handler) currentStartupSummary() map[string]any {
	h.storageHealthMu.RLock()
	defer h.storageHealthMu.RUnlock()
	if len(h.startupSummary) == 0 {
		return nil
	}
	out := make(map[string]any, len(h.startupSummary))
	for key, value := range h.startupSummary {
		out[key] = value
	}
	return out
}

func (h *Handler) setQueueRuntimeError(msg string) {
	msg = strings.TrimSpace(msg)
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

func writeAgentRuntimeSuccess(w http.ResponseWriter, status int, result map[string]any) {
	if result == nil {
		result = map[string]any{}
	}
	payload := map[string]any{
		"ok":     true,
		"result": result,
	}
	for key, value := range result {
		if _, exists := payload[key]; exists {
			continue
		}
		payload[key] = value
	}
	writeJSON(w, status, payload)
}

func writeError(w http.ResponseWriter, status int, code string, message string) {
	writeErrorWithHintAndExtras(w, status, code, message, nil, nil)
}

func writeErrorWithHintAndExtras(w http.ResponseWriter, status int, code string, message string, overrideHint *errorHint, extras map[string]any) {
	payload := map[string]any{
		"error":   code,
		"message": message,
	}
	if requestID := strings.TrimSpace(w.Header().Get("X-Request-ID")); requestID != "" {
		payload["request_id"] = requestID
	}
	hint, ok := defaultErrorHint(code)
	if overrideHint != nil {
		hint = *overrideHint
		ok = true
	}
	if ok {
		payload["retryable"] = hint.Retryable
		if hint.NextAction != "" {
			payload["next_action"] = hint.NextAction
		}
	}
	detail := map[string]any{
		"code":    code,
		"message": message,
	}
	if requestID, ok := payload["request_id"]; ok {
		detail["request_id"] = requestID
	}
	if retryable, ok := payload["retryable"]; ok {
		detail["retryable"] = retryable
	}
	if nextAction, ok := payload["next_action"]; ok {
		detail["next_action"] = nextAction
	}
	for key, value := range extras {
		if strings.TrimSpace(key) == "" {
			continue
		}
		payload[key] = value
		detail[key] = value
	}
	payload["error_detail"] = detail
	writeJSON(w, status, payload)
}

func writeMethodNotAllowed(w http.ResponseWriter) {
	writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
}

func defaultErrorHint(code string) (errorHint, bool) {
	switch code {
	case "store_error":
		return errorHint{
			Retryable:  true,
			NextAction: "retry with backoff; if repeated, check statocyst health and storage backends",
		}, true
	case "unsupported_media_type":
		return errorHint{
			Retryable:  false,
			NextAction: "send Content-Type: application/json",
		}, true
	case "not_acceptable":
		return errorHint{
			Retryable:  false,
			NextAction: "request application/json or text/markdown on discovery routes",
		}, true
	case "invalid_request":
		return errorHint{
			Retryable:  false,
			NextAction: "fix request payload shape and retry",
		}, true
	case "unauthorized":
		return errorHint{
			Retryable:  false,
			NextAction: "present valid credentials for this route class",
		}, true
	case "forbidden":
		return errorHint{
			Retryable:  false,
			NextAction: "verify caller identity and route ownership/trust requirements",
		}, true
	case "id_generation_failed", "token_generation_failed":
		return errorHint{
			Retryable:  true,
			NextAction: "retry with backoff; if repeated, check statocyst runtime health",
		}, true
	case "invalid_timeout":
		return errorHint{
			Retryable:  false,
			NextAction: "set timeout_ms to an integer in range 0..30000",
		}, true
	case "invalid_bind_token":
		return errorHint{
			Retryable:  false,
			NextAction: "provide a non-empty bind_token from bind token creation",
		}, true
	case "bind_not_found", "bind_expired", "bind_used":
		return errorHint{
			Retryable:  false,
			NextAction: "request a new one-time bind token from the human control-plane",
		}, true
	case "agent_exists":
		return errorHint{
			Retryable:  true,
			NextAction: "retry with a different handle or agent_id permutation",
		}, true
	case "invalid_to_agent_uuid":
		return errorHint{
			Retryable:  false,
			NextAction: "supply a valid UUID in to_agent_uuid or use to_agent_uri",
		}, true
	case "invalid_to_agent_uri":
		return errorHint{
			Retryable:  false,
			NextAction: "supply a canonical agent URI for to_agent_uri",
		}, true
	case "invalid_from_agent_uri":
		return errorHint{
			Retryable:  false,
			NextAction: "supply a canonical sender URI scoped to the authenticated peer",
		}, true
	case "unknown_receiver":
		return errorHint{
			Retryable:  false,
			NextAction: "refresh capabilities and trust state, then verify receiver identity",
		}, true
	case "invalid_content_type":
		return errorHint{
			Retryable:  false,
			NextAction: "set content_type to text/plain or application/json",
		}, true
	case "invalid_delivery_id":
		return errorHint{
			Retryable:  false,
			NextAction: "use delivery_id returned by the latest successful pull",
		}, true
	case "unknown_delivery":
		return errorHint{
			Retryable:  false,
			NextAction: "pull a message to obtain an active delivery_id before ack/nack",
		}, true
	case "unknown_message":
		return errorHint{
			Retryable:  false,
			NextAction: "query a visible message_id from publish/pull activity",
		}, true
	case "agent_ref_mismatch":
		return errorHint{
			Retryable:  false,
			NextAction: "provide matching agent UUID and canonical agent URI references",
		}, true
	case "invalid_handle":
		return errorHint{
			Retryable:  false,
			NextAction: "use a 2-64 char URL-safe handle and avoid blocked terms",
		}, true
	case "invalid_agent_type":
		return errorHint{
			Retryable:  false,
			NextAction: "set metadata.agent_type to 2-64 chars matching [a-z0-9._-]",
		}, true
	case "invalid_agent_skills":
		return errorHint{
			Retryable:  false,
			NextAction: "set metadata.skills as [{name,description}] with short non-sensitive descriptions; do not include secrets",
		}, true
	case "invalid_skill_description":
		return errorHint{
			Retryable:  false,
			NextAction: "remove secret-like content from metadata.skills[].description; never include keys, tokens, or passwords",
		}, true
	case "agent_handle_locked":
		return errorHint{
			Retryable:  false,
			NextAction: "do not retry handle finalization; update metadata only",
		}, true
	case "invalid_owner_human_id":
		return errorHint{
			Retryable:  false,
			NextAction: "request a new bind token with an active owner_human_id",
		}, true
	case "agent_id_generation_failed":
		return errorHint{
			Retryable:  true,
			NextAction: "retry bind with backoff or specify an explicit handle",
		}, true
	}
	return errorHint{}, false
}

func withRequestCorrelation(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestID := strings.TrimSpace(r.Header.Get("X-Request-ID"))
		if requestID == "" {
			generated, err := newUUIDv7()
			if err == nil {
				requestID = generated
			}
		}
		if requestID != "" {
			w.Header().Set("X-Request-ID", requestID)
			r = r.WithContext(context.WithValue(r.Context(), requestIDContextKey{}, requestID))
		}
		next.ServeHTTP(w, r)
	})
}

func requireJSONRequestContentType(w http.ResponseWriter, r *http.Request) bool {
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || !strings.EqualFold(strings.TrimSpace(mediaType), "application/json") {
		writeError(w, http.StatusUnsupportedMediaType, "unsupported_media_type", "request content type must be application/json")
		return false
	}
	return true
}

func decodeJSON(r *http.Request, out any) error {
	defer r.Body.Close()
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(out); err != nil {
		return err
	}
	// Force EOF so the request stream is fully consumed before responding.
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		if err == nil {
			return errors.New("invalid JSON request")
		}
		return err
	}
	return nil
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
