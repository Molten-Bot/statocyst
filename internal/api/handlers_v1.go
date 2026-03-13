package api

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"statocyst/internal/auth"
	"statocyst/internal/model"
	"statocyst/internal/store"
)

type createOrgRequest struct {
	Handle      string `json:"handle"`
	DisplayName string `json:"display_name"`
}

type updateMyProfileRequest struct {
	Handle *string `json:"handle,omitempty"`
}

type createInviteRequest struct {
	Email         string `json:"email"`
	Role          string `json:"role"`
	ExpiresInDays *int   `json:"expires_in_days,omitempty"`
}

type redeemInviteRequest struct {
	InviteID   string `json:"invite_id,omitempty"`
	InviteCode string `json:"invite_code,omitempty"`
}

type createBindTokenRequest struct {
	OrgID        string  `json:"org_id"`
	OwnerHumanID *string `json:"owner_human_id,omitempty"`
}

type redeemBindTokenRequest struct {
	BindToken string `json:"bind_token"`
	Handle    string `json:"handle,omitempty"`
	AgentID   string `json:"agent_id,omitempty"`
	HubURL    string `json:"hub_url,omitempty"`
}

type registerAgentRequest struct {
	OrgID        string  `json:"org_id"`
	AgentID      string  `json:"agent_id"`
	OwnerHumanID *string `json:"owner_human_id,omitempty"`
}

type updateMetadataRequest struct {
	Metadata json.RawMessage `json:"metadata"`
}

type updateAgentProfileRequest struct {
	Handle   *string         `json:"handle,omitempty"`
	Metadata json.RawMessage `json:"metadata,omitempty"`
}

type trustOrgRequest struct {
	OrgID     string `json:"org_id"`
	PeerOrgID string `json:"peer_org_id"`
}

type trustAgentRequest struct {
	OrgID         string `json:"org_id"`
	AgentUUID     string `json:"agent_uuid"`
	PeerAgentUUID string `json:"peer_agent_uuid"`
	AgentID       string `json:"agent_id,omitempty"`
	PeerAgentID   string `json:"peer_agent_id,omitempty"`
}

var agentConnectPromptLines = []string{
	"Connect to MoltenBot Hub as my agent.",
	"Hub URL: {{hub_url}}",
	"Bind API URL: {{bind_api_url}}",
	"Agent Skill URL After Bind: {{skill_url}}",
	"Bind Scope: {{bind_scope}}",
	"Expires: {{expires_at}}",
	"",
	"Authorization:",
	"I authorize exactly one bind action with this one-time token.",
	"Do not perform unrelated actions.",
	"",
	"Use this one-time bind token exactly once:",
	"{{bind_token}}",
	"",
	"Steps:",
	"1. Choose a desired handle (for example `research-bot`) and redeem the bind token with:",
	"   curl -fsS -X POST {{bind_api_url}} -H \"Content-Type: application/json\" -d '{\"hub_url\":\"{{hub_url}}\",\"bind_token\":\"{{bind_token}}\",\"handle\":\"<your-agent-handle>\"}'",
	"2. If the bind response returns `agent_exists`, retry the same bind token with another handle permutation such as `<your-agent-handle>-2` or `<your-agent-handle>-bot`.",
	"3. Persist the returned bearer `token`, exact `api_base`, and `endpoints` exactly as returned.",
	"4. Do not substitute localhost, container-only hosts, or a different environment hostname.",
	"5. Immediately call `GET {api_base}/agents/me/skill` with `Authorization: Bearer <token>` and follow that skill exactly once to finish onboarding.",
	"6. Treat both the bind token and returned bearer token as secrets.",
}

type createOrgAccessKeyRequest struct {
	Label         string   `json:"label"`
	Scopes        []string `json:"scopes"`
	ExpiresInDays *int     `json:"expires_in_days,omitempty"`
}

type agentControlPlaneView struct {
	APIBase       string
	AgentUUID     string
	AgentID       string
	OrgID         string
	OwnerHumanID  string
	CanTalkTo     []string
	CanTalkToURIs []string
	Capabilities  []string
}

var (
	errMissingOrgName      = errors.New("missing_org_name")
	errMissingOrgAccessKey = errors.New("missing_org_access_key")
)

const (
	defaultInviteExpiryDays = 7
	maxInviteExpiryDays     = 365
	defaultUIAppName        = "Statocyst"
	maxMetadataBytes        = 192 * 1024
)

func configuredMetadataMaxBytes() int {
	raw := strings.TrimSpace(os.Getenv("STATOCYST_MAX_METADATA_BYTES"))
	if raw == "" {
		return maxMetadataBytes
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 {
		return maxMetadataBytes
	}
	return n
}

func (h *Handler) handleUIConfig(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeMethodNotAllowed(w)
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
		if strings.TrimSpace(h.supabaseURL) != "" {
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
	if hasUIConfigPrivilegedAccess(r) {
		superAdminEmails = setToSortedSlice(h.superAdminEmails)
	}
	adminConfig := map[string]any{
		"review_mode":  h.superAdminReview,
		"write_policy": "global_write",
	}
	if len(superAdminEmails) > 0 {
		adminConfig["emails"] = superAdminEmails
	}
	superAdminDomains := setToSortedSlice(h.superAdminDomains)
	if len(superAdminDomains) > 0 {
		adminConfig["domains"] = superAdminDomains
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"auth":               authConfig,
		"dev_auto_login":     strings.EqualFold(strings.TrimSpace(os.Getenv("DEV_LOGIN_AUTO")), "true"),
		"admin":              adminConfig,
		"bind_token_ttl_sec": int(h.bindTokenTTL.Seconds()),
		"headless_mode":      h.headlessMode,
	})
}

func hasUIConfigPrivilegedAccess(r *http.Request) bool {
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

func hasEntitiesMetadataAccess(r *http.Request) bool {
	expectedKey := strings.TrimSpace(os.Getenv("STATOCYST_ENTITIES_METADATA_KEY"))
	if expectedKey == "" {
		return false
	}
	presentedKey := strings.TrimSpace(r.Header.Get("X-Entities-Metadata-Key"))
	if presentedKey == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(presentedKey), []byte(expectedKey)) == 1
}

func (h *Handler) hasAdminSnapshotKeyAccess(r *http.Request) bool {
	expectedKey := strings.TrimSpace(h.adminSnapshotKey)
	if expectedKey == "" {
		return false
	}
	presentedKey := strings.TrimSpace(r.Header.Get("X-Admin-Snapshot-Key"))
	if presentedKey == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(presentedKey), []byte(expectedKey)) == 1
}

func uiAppName() string {
	name := strings.TrimSpace(os.Getenv("STATOCYST_APP_NAME"))
	if name == "" {
		return defaultUIAppName
	}
	return name
}

func decodeMetadataUpdateRequest(r *http.Request) (map[string]any, error) {
	var req updateMetadataRequest
	if err := decodeJSON(r, &req); err != nil {
		return nil, errors.New("invalid JSON request")
	}
	return decodeMetadataJSON(req.Metadata, true)
}

func decodeMetadataJSON(raw json.RawMessage, required bool) (map[string]any, error) {
	if len(raw) == 0 {
		if required {
			return nil, errors.New("metadata is required")
		}
		return nil, nil
	}
	var decoded any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return nil, errors.New("metadata must be a valid JSON object")
	}
	metadata, ok := decoded.(map[string]any)
	if !ok {
		return nil, errors.New("metadata must be a JSON object")
	}

	body, err := json.Marshal(metadata)
	if err != nil {
		return nil, errors.New("metadata must be a valid JSON object")
	}
	limit := configuredMetadataMaxBytes()
	if len(body) > limit {
		return nil, fmt.Errorf("metadata exceeds %d bytes", limit)
	}
	return metadata, nil
}

func decodeAgentProfileUpdateRequest(r *http.Request) (*string, map[string]any, error) {
	var req updateAgentProfileRequest
	if err := decodeJSON(r, &req); err != nil {
		return nil, nil, errors.New("invalid JSON request")
	}

	var handle *string
	if req.Handle != nil {
		candidate := normalizeHandle(*req.Handle)
		if !validateAgentID(candidate) {
			return nil, nil, errors.New("handle must be 2-64 chars, URL-safe (a-z, 0-9, ., _, -), and not blocked")
		}
		handle = &candidate
	}

	metadata, err := decodeMetadataJSON(req.Metadata, false)
	if err != nil {
		return nil, nil, err
	}
	if handle == nil && metadata == nil {
		return nil, nil, errors.New("at least one of handle or metadata is required")
	}
	return handle, metadata, nil
}

func meOnboardingPayload(handleConfirmedAt *time.Time) map[string]any {
	if handleConfirmedAt != nil {
		return nil
	}
	return map[string]any{
		"handle_required":  true,
		"handle_confirmed": false,
		"next_step":        "set_handle",
	}
}

func requestedBindHandle(req redeemBindTokenRequest) string {
	if handle := normalizeHandle(req.Handle); handle != "" {
		return handle
	}
	return normalizeHandle(req.AgentID)
}

func bindHandleSuggestions(handle string) []string {
	base := normalizeHandle(handle)
	if base == "" {
		return nil
	}
	suffixes := []string{"-2", "-bot", "-agent", "-01", "-svc"}
	out := make([]string, 0, len(suffixes))
	seen := map[string]struct{}{base: {}}
	for _, suffix := range suffixes {
		candidate := normalizeHandle(base + suffix)
		if candidate == "" || !validateAgentID(candidate) {
			continue
		}
		if _, exists := seen[candidate]; exists {
			continue
		}
		seen[candidate] = struct{}{}
		out = append(out, candidate)
	}
	return out
}

func meResponsePayload(human model.Human, isAdmin bool) map[string]any {
	payload := map[string]any{
		"human": human,
		"admin": isAdmin,
	}
	if onboarding := meOnboardingPayload(human.HandleConfirmedAt); onboarding != nil {
		payload["onboarding"] = onboarding
	}
	return payload
}

func agentOwnerPayload(agent model.Agent) map[string]any {
	if agent.OwnerHumanID != nil && strings.TrimSpace(*agent.OwnerHumanID) != "" {
		return map[string]any{
			"human_id": strings.TrimSpace(*agent.OwnerHumanID),
		}
	}
	if strings.TrimSpace(agent.OrgID) != "" {
		return map[string]any{
			"org_id": strings.TrimSpace(agent.OrgID),
		}
	}
	return nil
}

func agentResponsePayload(agent model.Agent) map[string]any {
	payload := map[string]any{
		"agent_uuid":          agent.AgentUUID,
		"agent_id":            agent.AgentID,
		"handle":              agent.Handle,
		"handle_finalized_at": agent.HandleFinalizedAt,
		"org_id":              agent.OrgID,
		"status":              agent.Status,
		"metadata":            agent.Metadata,
		"created_by":          agent.CreatedBy,
		"created_at":          agent.CreatedAt,
		"revoked_at":          agent.RevokedAt,
	}
	if owner := agentOwnerPayload(agent); owner != nil {
		payload["owner"] = owner
	}
	return payload
}

func (h *Handler) meResponsePayload(human model.Human, isAdmin bool) map[string]any {
	payload := map[string]any{
		"human": h.humanPayload(human),
		"admin": isAdmin,
	}
	if onboarding := meOnboardingPayload(human.HandleConfirmedAt); onboarding != nil {
		payload["onboarding"] = onboarding
	}
	return payload
}

func (h *Handler) agentResponsePayload(agent model.Agent) map[string]any {
	payload := agentResponsePayload(agent)
	payload["uri"] = h.agentURI(agent)
	return payload
}

func (h *Handler) agentListResponsePayload(agents []model.Agent) []map[string]any {
	out := make([]map[string]any, 0, len(agents))
	for _, agent := range agents {
		out = append(out, h.agentResponsePayload(agent))
	}
	return out
}

func (h *Handler) handleMe(w http.ResponseWriter, r *http.Request) {
	actor, err := h.authenticateHuman(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "unauthorized", "missing or invalid human auth")
		return
	}

	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, h.meResponsePayload(actor.Human, actor.IsSuperAdmin))
		return
	case http.MethodPatch:
		var req updateMyProfileRequest
		if err := decodeJSON(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_request", "invalid JSON request")
			return
		}

		handle := ""
		if req.Handle != nil {
			handle = normalizeHandle(*req.Handle)
			if !validateHandle(handle) {
				writeError(w, http.StatusBadRequest, "invalid_handle", "handle must be 2-64 chars, URL-safe (a-z, 0-9, ., _, -), and not blocked")
				return
			}
		}

		human, err := h.control.UpdateHumanProfile(actor.Human.HumanID, handle, req.Handle != nil, h.now().UTC())
		if err != nil {
			switch {
			case errors.Is(err, store.ErrHumanNotFound):
				writeError(w, http.StatusNotFound, "unknown_human", "human not found")
			case errors.Is(err, store.ErrHumanHandleTaken):
				writeError(w, http.StatusConflict, "human_handle_exists", "handle is already taken")
			case errors.Is(err, store.ErrInvalidHandle):
				writeError(w, http.StatusBadRequest, "invalid_handle", "handle must be 2-64 chars, URL-safe (a-z, 0-9, ., _, -), and not blocked")
			default:
				writeError(w, http.StatusInternalServerError, "store_error", "failed to update profile")
			}
			return
		}
		writeJSON(w, http.StatusOK, h.meResponsePayload(human, actor.IsSuperAdmin))
		return
	default:
		writeMethodNotAllowed(w)
		return
	}
}

func (h *Handler) handleMeMetadata(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPatch {
		writeMethodNotAllowed(w)
		return
	}

	actor, err := h.authenticateHuman(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "unauthorized", "missing or invalid human auth")
		return
	}
	if h.requireHandleConfirmedForWrite(w, actor) {
		return
	}

	metadata, err := decodeMetadataUpdateRequest(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}

	human, err := h.control.UpdateHumanMetadata(actor.Human.HumanID, metadata, h.now().UTC())
	if err != nil {
		switch {
		case errors.Is(err, store.ErrHumanNotFound):
			writeError(w, http.StatusNotFound, "unknown_human", "human not found")
		default:
			writeError(w, http.StatusInternalServerError, "store_error", "failed to update human metadata")
		}
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"human": h.humanPayload(human),
	})
}

func (h *Handler) handleMyOrgs(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeMethodNotAllowed(w)
		return
	}
	actor, err := h.authenticateHuman(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "unauthorized", "missing or invalid human auth")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"memberships": h.membershipWithOrgListPayload(h.control.ListMyMemberships(actor.Human.HumanID)),
	})
}

func (h *Handler) handleMyAgents(w http.ResponseWriter, r *http.Request) {
	actor, err := h.authenticateHuman(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "unauthorized", "missing or invalid human auth")
		return
	}

	switch r.Method {
	case http.MethodGet:
		agents := h.control.ListHumanAgents(actor.Human.HumanID)
		writeJSON(w, http.StatusOK, map[string]any{
			"agents": h.agentListResponsePayload(agents),
		})
		return
	case http.MethodPost:
		writeError(w, http.StatusGone, "agent_create_disabled", "use POST /v1/agents/bind-tokens and POST /v1/agents/bind")
		return
	default:
		writeMethodNotAllowed(w)
		return
	}
}

func (h *Handler) handleMyAgentBindTokens(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeMethodNotAllowed(w)
		return
	}
	actor, err := h.authenticateHuman(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "unauthorized", "missing or invalid human auth")
		return
	}
	if h.requireHandleConfirmedForWrite(w, actor) {
		return
	}

	var req createBindTokenRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid JSON request")
		return
	}
	req.OrgID = strings.TrimSpace(req.OrgID)

	ownerHumanID := actor.Human.HumanID
	if h.ensureHumanOwnedAgentLimit(w, ownerHumanID) {
		return
	}
	bindSecret, err := auth.GenerateToken()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "token_generation_failed", "failed to generate bind token")
		return
	}
	bindID, err := h.idFactory()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "id_generation_failed", "failed to generate bind id")
		return
	}
	expiresAt := h.now().UTC().Add(h.bindTokenTTL)
	bind, err := h.control.CreateBindToken(req.OrgID, &ownerHumanID, actor.Human.HumanID, bindID, auth.HashToken(bindSecret), expiresAt, h.now().UTC(), actor.IsSuperAdmin)
	if err != nil {
		switch {
		case errors.Is(err, store.ErrOrgNotFound):
			writeError(w, http.StatusNotFound, "unknown_org", "org_id is not registered")
		case errors.Is(err, store.ErrUnauthorizedRole):
			writeError(w, http.StatusForbidden, "forbidden", "membership in org required")
		case errors.Is(err, store.ErrMembershipNotFound):
			writeError(w, http.StatusBadRequest, "invalid_owner_human_id", "owner_human_id must be active in org")
		default:
			writeError(w, http.StatusInternalServerError, "store_error", "failed to create bind token")
		}
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"bind_id":        bind.BindID,
		"bind_token":     bindSecret,
		"connect_prompt": h.buildAgentConnectPrompt(r, bind, bindSecret),
		"org_id":         bind.OrgID,
		"owner_human_id": bind.OwnerHumanID,
		"expires_at":     bind.ExpiresAt,
	})
}

func (h *Handler) handleMyAgentTrusts(w http.ResponseWriter, r *http.Request) {
	actor, err := h.authenticateHuman(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "unauthorized", "missing or invalid human auth")
		return
	}

	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, map[string]any{
			"agent_trusts": h.control.ListHumanAgentTrusts(actor.Human.HumanID),
		})
		return
	case http.MethodPost:
		if h.requireHandleConfirmedForWrite(w, actor) {
			return
		}
		var req trustAgentRequest
		if err := decodeJSON(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_request", "invalid JSON request")
			return
		}
		h.handleAgentTrustCreate(w, actor, req, "owner required for initiating agent")
		return
	default:
		writeMethodNotAllowed(w)
		return
	}
}

type handlerError struct {
	status  int
	code    string
	message string
}

func (h *Handler) handleAgentTrustCreate(w http.ResponseWriter, actor humanActor, req trustAgentRequest, unauthorizedMessage string) {
	req.OrgID = strings.TrimSpace(req.OrgID)
	agentUUID, herr := h.resolveTrustAgentUUID(req.AgentUUID, req.AgentID, "agent_uuid", "agent_id")
	if herr != nil {
		writeError(w, herr.status, herr.code, herr.message)
		return
	}
	peerAgentUUID, herr := h.resolveTrustAgentUUID(req.PeerAgentUUID, req.PeerAgentID, "peer_agent_uuid", "peer_agent_id")
	if herr != nil {
		writeError(w, herr.status, herr.code, herr.message)
		return
	}

	edgeID, err := h.idFactory()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "id_generation_failed", "failed to generate edge_id")
		return
	}
	edge, created, err := h.control.CreateOrJoinAgentTrust(req.OrgID, agentUUID, peerAgentUUID, actor.Human.HumanID, edgeID, h.now().UTC(), actor.IsSuperAdmin)
	if err != nil {
		switch {
		case errors.Is(err, store.ErrAgentNotFound):
			writeError(w, http.StatusNotFound, "unknown_agent", "agent_uuid or peer_agent_uuid is not registered")
		case errors.Is(err, store.ErrUnauthorizedRole):
			writeError(w, http.StatusForbidden, "forbidden", unauthorizedMessage)
		case errors.Is(err, store.ErrSelfTrust):
			writeError(w, http.StatusBadRequest, "invalid_peer_agent_uuid", "peer_agent_uuid cannot equal agent_uuid")
		default:
			writeError(w, http.StatusInternalServerError, "store_error", "failed to create agent trust")
		}
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	writeJSON(w, status, map[string]any{"trust": edge, "created": created})
}

func (h *Handler) resolveTrustAgentUUID(rawUUID, rawID, uuidField, idField string) (string, *handlerError) {
	uuidValue := normalizeUUID(rawUUID)
	idValue := strings.TrimSpace(rawID)

	if uuidValue != "" && !validateUUID(uuidValue) && idValue == "" {
		return "", &handlerError{
			status:  http.StatusBadRequest,
			code:    "invalid_agent_uuid",
			message: fmt.Sprintf("%s must be a valid UUID", uuidField),
		}
	}

	if idValue == "" {
		if uuidValue == "" {
			return "", &handlerError{
				status:  http.StatusBadRequest,
				code:    "invalid_agent_uuid",
				message: "agent_uuid and peer_agent_uuid must be valid UUIDs",
			}
		}
		return uuidValue, nil
	}

	if uuidFromID := normalizeUUID(idValue); validateUUID(uuidFromID) {
		if uuidValue != "" && validateUUID(uuidValue) && uuidValue != uuidFromID {
			return "", &handlerError{
				status:  http.StatusBadRequest,
				code:    "agent_ref_mismatch",
				message: fmt.Sprintf("%s and %s refer to different agents", uuidField, idField),
			}
		}
		return uuidFromID, nil
	}

	idRef := normalizeAgentRef(idValue)
	if !validateAgentRef(idRef) {
		return "", &handlerError{
			status:  http.StatusBadRequest,
			code:    "invalid_agent_id",
			message: fmt.Sprintf("%s must be a valid agent reference", idField),
		}
	}
	resolvedUUID, err := h.control.ResolveAgentUUID(idRef)
	if err != nil {
		switch {
		case errors.Is(err, store.ErrAgentNotFound):
			return "", &handlerError{
				status:  http.StatusNotFound,
				code:    "unknown_agent",
				message: fmt.Sprintf("%s is not registered", idField),
			}
		case errors.Is(err, store.ErrAgentAmbiguous):
			return "", &handlerError{
				status:  http.StatusConflict,
				code:    "ambiguous_agent_id",
				message: fmt.Sprintf("%s is ambiguous; provide %s", idField, uuidField),
			}
		default:
			return "", &handlerError{
				status:  http.StatusInternalServerError,
				code:    "store_error",
				message: "failed to resolve agent reference",
			}
		}
	}
	if uuidValue != "" && validateUUID(uuidValue) && uuidValue != resolvedUUID {
		return "", &handlerError{
			status:  http.StatusBadRequest,
			code:    "agent_ref_mismatch",
			message: fmt.Sprintf("%s and %s refer to different agents", uuidField, idField),
		}
	}
	return resolvedUUID, nil
}

func normalizeOptionalHumanID(value *string) *string {
	if value == nil {
		return nil
	}
	v := strings.TrimSpace(*value)
	if v == "" {
		return nil
	}
	return &v
}

func (h *Handler) ensureHumanOwnedAgentLimit(w http.ResponseWriter, ownerHumanID string) bool {
	if ownerHumanID == "" {
		return false
	}
	if h.isSuperAdminHumanID(ownerHumanID) {
		return false
	}
	if h.control.CountActiveHumanOwnedAgents(ownerHumanID) >= 2 {
		writeError(w, http.StatusConflict, "agent_limit_reached", "non-admin users can only own up to 2 active agents")
		return true
	}
	return false
}

func (h *Handler) handleAgentMe(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		agentUUID, err := h.authenticateAgent(r)
		if err != nil {
			writeError(w, http.StatusUnauthorized, "unauthorized", "missing or invalid bearer token")
			return
		}
		agent, err := h.control.GetAgentByUUID(agentUUID)
		if err != nil {
			if errors.Is(err, store.ErrAgentNotFound) {
				writeError(w, http.StatusNotFound, "unknown_agent", "agent_uuid is not registered")
				return
			}
			writeError(w, http.StatusInternalServerError, "store_error", "failed to load agent")
			return
		}
		var organization any = nil
		if strings.TrimSpace(agent.OrgID) != "" {
			org, err := h.control.GetOrganization(agent.OrgID)
			if err != nil {
				if errors.Is(err, store.ErrOrgNotFound) {
					writeError(w, http.StatusNotFound, "unknown_org", "org_id is not registered")
					return
				}
				writeError(w, http.StatusInternalServerError, "store_error", "failed to load organization")
				return
			}
			organization = org
		}

		var ownerHuman any = nil
		if agent.OwnerHumanID != nil && strings.TrimSpace(*agent.OwnerHumanID) != "" {
			human, err := h.control.GetHuman(strings.TrimSpace(*agent.OwnerHumanID))
			if err != nil {
				if errors.Is(err, store.ErrHumanNotFound) {
					writeError(w, http.StatusNotFound, "unknown_human", "owner_human_id is not registered")
					return
				}
				writeError(w, http.StatusInternalServerError, "store_error", "failed to load owner human")
				return
			}
			ownerHuman = human
		}

		payload := map[string]any{
			"agent": h.agentResponsePayload(agent),
		}
		if organization != nil {
			payload["organization"] = h.organizationPayload(organization.(model.Organization))
		}
		if ownerHuman != nil {
			payload["human"] = h.humanPayload(ownerHuman.(model.Human))
		}

		writeJSON(w, http.StatusOK, payload)
		return
	case http.MethodPatch:
		h.handleAgentMetadataSelfPatch(w, r, "")
		return
	default:
		writeMethodNotAllowed(w)
		return
	}
}

func (h *Handler) handleAgentMetadataSelfPatch(w http.ResponseWriter, r *http.Request, targetAgentUUID string) {
	agentUUID, err := h.authenticateAgent(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "unauthorized", "missing or invalid bearer token")
		return
	}
	if !requireJSONRequestContentType(w, r) {
		return
	}
	if targetAgentUUID != "" && normalizeUUID(targetAgentUUID) != agentUUID {
		writeError(w, http.StatusForbidden, "forbidden", "agent token can only update its own profile")
		return
	}

	handle, metadata, err := decodeAgentProfileUpdateRequest(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}

	var agent model.Agent
	if handle != nil {
		agent, err = h.control.FinalizeAgentHandleSelf(agentUUID, *handle, h.now().UTC())
		if err != nil {
			switch {
			case errors.Is(err, store.ErrAgentNotFound):
				writeError(w, http.StatusNotFound, "unknown_agent", "agent_uuid is not registered")
			case errors.Is(err, store.ErrInvalidHandle):
				writeError(w, http.StatusBadRequest, "invalid_handle", "handle must be 2-64 chars, URL-safe (a-z, 0-9, ., _, -), and not blocked")
			case errors.Is(err, store.ErrAgentExists):
				writeError(w, http.StatusConflict, "agent_exists", "agent_id already registered")
			case errors.Is(err, store.ErrAgentHandleLocked):
				writeError(w, http.StatusConflict, "agent_handle_locked", "agent handle is already finalized")
			default:
				writeError(w, http.StatusInternalServerError, "store_error", "failed to finalize agent handle")
			}
			return
		}
	}
	if metadata != nil {
		agent, err = h.control.UpdateAgentMetadataSelf(agentUUID, metadata, h.now().UTC())
		if err != nil {
			switch {
			case errors.Is(err, store.ErrAgentNotFound):
				writeError(w, http.StatusNotFound, "unknown_agent", "agent_uuid is not registered")
			case errors.Is(err, store.ErrInvalidAgentType):
				writeError(w, http.StatusBadRequest, "invalid_agent_type", "metadata.agent_type must be 2-64 chars: a-z, 0-9, ., _, -")
			default:
				writeError(w, http.StatusInternalServerError, "store_error", "failed to update agent metadata")
			}
			return
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"agent": h.agentResponsePayload(agent),
	})
}

func (h *Handler) handleAgentMeMetadata(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPatch {
		writeMethodNotAllowed(w)
		return
	}
	h.handleAgentMetadataSelfPatch(w, r, "")
}

func (h *Handler) handleAgentMeCapabilities(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeMethodNotAllowed(w)
		return
	}
	if !acceptsJSONDiscovery(r) {
		writeError(w, http.StatusNotAcceptable, "not_acceptable", "capabilities only supports application/json responses")
		return
	}

	agentUUID, err := h.authenticateAgent(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "unauthorized", "missing or invalid bearer token")
		return
	}
	agent, err := h.control.GetAgentByUUID(agentUUID)
	if err != nil {
		if errors.Is(err, store.ErrAgentNotFound) {
			writeError(w, http.StatusNotFound, "unknown_agent", "agent_uuid is not registered")
			return
		}
		writeError(w, http.StatusInternalServerError, "store_error", "failed to load agent")
		return
	}
	cp, err := h.buildAgentControlPlane(r, agent)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "store_error", "failed to load agent capabilities")
		return
	}
	manifest := buildAgentManifest(agent, cp, h.now())
	writeJSON(w, http.StatusOK, map[string]any{
		"agent":         h.agentResponsePayload(agent),
		"control_plane": h.agentControlPlanePayload(cp),
		"capabilities":  manifest.Capabilities,
		"routes":        manifest.Routes,
		"communication": manifest.Communication,
		"manifest_url":  cp.APIBase + "/agents/me/manifest",
	})
}

func (h *Handler) handleAgentMeSkill(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeMethodNotAllowed(w)
		return
	}

	agentUUID, err := h.authenticateAgent(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "unauthorized", "missing or invalid bearer token")
		return
	}
	agent, err := h.control.GetAgentByUUID(agentUUID)
	if err != nil {
		if errors.Is(err, store.ErrAgentNotFound) {
			writeError(w, http.StatusNotFound, "unknown_agent", "agent_uuid is not registered")
			return
		}
		writeError(w, http.StatusInternalServerError, "store_error", "failed to load agent")
		return
	}
	cp, err := h.buildAgentControlPlane(r, agent)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "store_error", "failed to load agent skill")
		return
	}
	format, ok := negotiateDiscoveryFormat(r)
	if !ok {
		writeError(w, http.StatusNotAcceptable, "not_acceptable", "discovery routes support application/json or text/markdown")
		return
	}
	manifest := buildAgentManifest(agent, cp, h.now())
	skill := buildAgentSkillMarkdown(agent, manifest)

	if format == "markdown" {
		w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(skill))
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"agent":         h.agentResponsePayload(agent),
		"control_plane": h.agentControlPlanePayload(cp),
		"manifest_url":  manifest.APIBase + "/agents/me/manifest",
		"skill": map[string]any{
			"schema_version": manifest.SchemaVersion,
			"format":         "markdown",
			"content":        skill,
		},
	})
}

func (h *Handler) handleAgentMeManifest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeMethodNotAllowed(w)
		return
	}
	format, ok := negotiateDiscoveryFormat(r)
	if !ok {
		writeError(w, http.StatusNotAcceptable, "not_acceptable", "manifest supports application/json or text/markdown")
		return
	}

	agentUUID, err := h.authenticateAgent(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "unauthorized", "missing or invalid bearer token")
		return
	}
	agent, err := h.control.GetAgentByUUID(agentUUID)
	if err != nil {
		if errors.Is(err, store.ErrAgentNotFound) {
			writeError(w, http.StatusNotFound, "unknown_agent", "agent_uuid is not registered")
			return
		}
		writeError(w, http.StatusInternalServerError, "store_error", "failed to load agent")
		return
	}
	cp, err := h.buildAgentControlPlane(r, agent)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "store_error", "failed to load agent manifest")
		return
	}

	manifest := buildAgentManifest(agent, cp, h.now())
	if format == "markdown" {
		w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(buildAgentDiscoveryMarkdown(manifest)))
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"manifest": manifest,
	})
}

func (h *Handler) buildAgentControlPlane(r *http.Request, agent model.Agent) (agentControlPlaneView, error) {
	peers, err := h.control.ListTalkablePeers(agent.AgentUUID)
	if err != nil {
		return agentControlPlaneView{}, err
	}
	talkableURIs := make([]string, 0, len(peers))
	for _, peerUUID := range peers {
		peerAgent, err := h.control.GetAgentByUUID(peerUUID)
		if err != nil {
			return agentControlPlaneView{}, err
		}
		talkableURIs = append(talkableURIs, h.agentURI(peerAgent))
	}
	remoteTrusts, err := h.control.ListRemoteAgentTrustsForLocalAgent(agent.AgentUUID)
	if err != nil {
		return agentControlPlaneView{}, err
	}
	for _, trust := range remoteTrusts {
		talkableURIs = append(talkableURIs, trust.RemoteAgentURI)
	}
	sort.Strings(talkableURIs)
	ownerHumanID := ""
	if agent.OwnerHumanID != nil {
		ownerHumanID = *agent.OwnerHumanID
	}
	return agentControlPlaneView{
		APIBase:       h.apiBaseURL(r),
		AgentUUID:     agent.AgentUUID,
		AgentID:       agent.AgentID,
		OrgID:         agent.OrgID,
		OwnerHumanID:  ownerHumanID,
		CanTalkTo:     peers,
		CanTalkToURIs: talkableURIs,
		Capabilities:  []string{"publish_messages", "pull_messages", "read_capabilities", "read_skill", "update_profile"},
	}, nil
}

func (h *Handler) agentControlPlanePayload(cp agentControlPlaneView) map[string]any {
	var owner map[string]any
	if strings.TrimSpace(cp.OwnerHumanID) != "" {
		owner = map[string]any{
			"human_id": strings.TrimSpace(cp.OwnerHumanID),
		}
	} else if strings.TrimSpace(cp.OrgID) != "" {
		owner = map[string]any{
			"org_id": strings.TrimSpace(cp.OrgID),
		}
	}
	return map[string]any{
		"api_base":         cp.APIBase,
		"agent_uuid":       cp.AgentUUID,
		"agent_id":         cp.AgentID,
		"org_id":           cp.OrgID,
		"owner":            owner,
		"can_talk_to":      cp.CanTalkTo,
		"can_talk_to_uris": cp.CanTalkToURIs,
		"capabilities":     cp.Capabilities,
		"can_communicate":  len(cp.CanTalkToURIs) > 0,
		"endpoints": map[string]string{
			"publish":      cp.APIBase + "/messages/publish",
			"pull":         cp.APIBase + "/messages/pull",
			"ack":          cp.APIBase + "/messages/ack",
			"nack":         cp.APIBase + "/messages/nack",
			"status":       cp.APIBase + "/messages/{message_id}",
			"profile":      cp.APIBase + "/agents/me",
			"manifest":     cp.APIBase + "/agents/me/manifest",
			"capabilities": cp.APIBase + "/agents/me/capabilities",
			"skill":        cp.APIBase + "/agents/me/skill",
		},
	}
}

func splitForwardedHeader(value string) string {
	parts := strings.Split(value, ",")
	return strings.TrimSpace(parts[0])
}

func canonicalURLParts(baseURL string) (string, string) {
	if strings.TrimSpace(baseURL) == "" {
		return "", ""
	}
	parsed, err := url.Parse(baseURL)
	if err != nil {
		return "", ""
	}
	return strings.TrimSpace(parsed.Scheme), strings.TrimSpace(parsed.Host)
}

func normalizeHostOnly(host string) string {
	host = strings.TrimSpace(host)
	if host == "" {
		return ""
	}
	if parsedHost, _, err := net.SplitHostPort(host); err == nil {
		return strings.Trim(parsedHost, "[]")
	}
	if strings.HasPrefix(host, "[") && strings.HasSuffix(host, "]") {
		return strings.Trim(host, "[]")
	}
	return host
}

func isLoopbackOrLocalHost(host string) bool {
	normalized := strings.ToLower(normalizeHostOnly(host))
	switch normalized {
	case "", "localhost":
		return true
	}
	addr, err := netip.ParseAddr(normalized)
	if err != nil {
		return false
	}
	return addr.IsLoopback() || addr.IsUnspecified()
}

func bindScopeLabel(bind model.BindToken) string {
	if strings.TrimSpace(bind.OrgID) == "" {
		return "Personal"
	}
	return "Organization " + strings.TrimSpace(bind.OrgID)
}

func (h *Handler) baseURL(r *http.Request) string {
	canonicalScheme, canonicalHost := canonicalURLParts(h.canonicalBaseURL)
	host := splitForwardedHeader(r.Header.Get("X-Forwarded-Host"))
	if host == "" {
		host = strings.TrimSpace(r.Host)
	}
	if (host == "" || isLoopbackOrLocalHost(host)) && canonicalHost != "" {
		host = canonicalHost
	}
	if host == "" {
		host = "localhost:8080"
	}

	scheme := strings.ToLower(splitForwardedHeader(r.Header.Get("X-Forwarded-Proto")))
	if scheme != "http" && scheme != "https" {
		scheme = ""
	}
	if scheme == "" && canonicalHost != "" && strings.EqualFold(host, canonicalHost) && canonicalScheme != "" {
		scheme = canonicalScheme
	}
	if scheme == "" {
		if r.TLS != nil {
			scheme = "https"
		} else {
			scheme = "http"
		}
	}
	return fmt.Sprintf("%s://%s", scheme, host)
}

func (h *Handler) apiBaseURL(r *http.Request) string {
	return h.baseURL(r) + "/v1"
}

func (h *Handler) hubBaseURL(r *http.Request) string {
	return h.baseURL(r)
}

func (h *Handler) buildAgentConnectPrompt(r *http.Request, bind model.BindToken, bindToken string) string {
	apiBase := h.apiBaseURL(r)
	hubBase := h.hubBaseURL(r)
	replacer := strings.NewReplacer(
		"{{hub_url}}", hubBase,
		"{{bind_api_url}}", apiBase+"/agents/bind",
		"{{skill_url}}", apiBase+"/agents/me/skill",
		"{{bind_scope}}", bindScopeLabel(bind),
		"{{expires_at}}", bind.ExpiresAt.UTC().Format(time.RFC3339),
		"{{bind_token}}", bindToken,
	)
	return replacer.Replace(strings.Join(agentConnectPromptLines, "\n"))
}

func wantsMarkdownDiscovery(r *http.Request) bool {
	format := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("format")))
	if format == "md" || format == "markdown" {
		return true
	}
	accept := strings.ToLower(r.Header.Get("Accept"))
	return strings.Contains(accept, "text/markdown")
}

func acceptsJSONDiscovery(r *http.Request) bool {
	accept := strings.ToLower(strings.TrimSpace(r.Header.Get("Accept")))
	if accept == "" || strings.Contains(accept, "*/*") {
		return true
	}
	return strings.Contains(accept, "application/json")
}

func negotiateDiscoveryFormat(r *http.Request) (string, bool) {
	format := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("format")))
	switch format {
	case "":
		if wantsMarkdownDiscovery(r) {
			return "markdown", true
		}
		if acceptsJSONDiscovery(r) {
			return "json", true
		}
		return "", false
	case "json":
		return "json", true
	case "md", "markdown":
		return "markdown", true
	default:
		return "", false
	}
}

func (h *Handler) handleAdminSnapshot(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeMethodNotAllowed(w)
		return
	}

	if !h.hasAdminSnapshotKeyAccess(r) {
		actor, err := h.authenticateHuman(r)
		if err != nil {
			writeError(w, http.StatusUnauthorized, "unauthorized", "missing or invalid human auth")
			return
		}
		if !actor.IsSuperAdmin {
			writeError(w, http.StatusForbidden, "forbidden", "admin required")
			return
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"snapshot": h.adminSnapshotPayload(h.control.AdminSnapshot()),
	})
}

func metadataPublicOrDefault(metadata map[string]any) bool {
	raw, ok := metadata["public"]
	if !ok {
		return true
	}
	publicValue, ok := raw.(bool)
	if !ok {
		return true
	}
	return publicValue
}

func metadataPublicStrict(metadata map[string]any) bool {
	raw, ok := metadata["public"]
	if !ok {
		return false
	}
	publicValue, ok := raw.(bool)
	if !ok {
		return false
	}
	return publicValue
}

func snapshotMetadataPublicView(metadata map[string]any) map[string]any {
	out := map[string]any{}
	if publicValue, ok := metadata["public"].(bool); ok {
		out["public"] = publicValue
	}
	if description, ok := metadata["description"].(string); ok {
		description = strings.TrimSpace(description)
		if description != "" {
			out["description"] = description
		}
	}
	if image, ok := metadata["image"].(string); ok {
		image = strings.TrimSpace(image)
		if image != "" {
			out["image"] = image
		}
	}
	return out
}

func entityMetadataForRender(metadata map[string]any) map[string]any {
	if len(metadata) == 0 {
		return map[string]any{}
	}
	out := make(map[string]any, len(metadata))
	for k, v := range metadata {
		out[k] = v
	}
	return out
}

func (h *Handler) handleEntitiesMetadata(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeMethodNotAllowed(w)
		return
	}
	if !hasEntitiesMetadataAccess(r) {
		writeError(w, http.StatusUnauthorized, "unauthorized", "missing or invalid entities metadata key")
		return
	}

	publicFilter := strings.TrimSpace(strings.ToLower(r.URL.Query().Get("public")))
	if publicFilter != "true" {
		writeError(w, http.StatusBadRequest, "invalid_request", "query parameter public=true is required")
		return
	}

	admin := h.control.AdminSnapshot()

	organizations := map[string]any{}
	for _, org := range admin.Organizations {
		if !metadataPublicStrict(org.Metadata) {
			continue
		}
		key := strings.TrimSpace(org.Handle)
		if key == "" {
			key = org.OrgID
		}
		row := map[string]any{
			"id":     org.OrgID,
			"handle": org.Handle,
			"uri":    h.organizationURI(org),
		}
		if metadata := entityMetadataForRender(org.Metadata); len(metadata) > 0 {
			row["metadata"] = metadata
		}
		organizations[key] = row
	}

	humans := map[string]any{}
	for _, human := range admin.Humans {
		if !metadataPublicStrict(human.Metadata) {
			continue
		}
		key := strings.TrimSpace(human.Handle)
		if key == "" {
			key = human.HumanID
		}
		row := map[string]any{
			"id":     human.HumanID,
			"handle": human.Handle,
			"uri":    h.humanURI(human),
		}
		if metadata := entityMetadataForRender(human.Metadata); len(metadata) > 0 {
			row["metadata"] = metadata
		}
		humans[key] = row
	}

	agents := map[string]any{}
	for _, agent := range admin.Agents {
		if !metadataPublicStrict(agent.Metadata) {
			continue
		}
		key := strings.TrimSpace(agent.AgentID)
		if key == "" {
			key = agent.AgentUUID
		}
		row := map[string]any{
			"id":     agent.AgentUUID,
			"handle": agent.Handle,
			"uri":    h.agentURI(agent),
		}
		if metadata := entityMetadataForRender(agent.Metadata); len(metadata) > 0 {
			row["metadata"] = metadata
		}
		agents[key] = row
	}

	entities := map[string]any{}
	if len(organizations) != 0 {
		entities["organizations"] = organizations
	}
	if len(humans) != 0 {
		entities["humans"] = humans
	}
	if len(agents) != 0 {
		entities["agents"] = agents
	}

	response := map[string]any{
		"generated_at": h.now().UTC().Format(time.RFC3339Nano),
		"filters": map[string]any{
			"public": true,
		},
		"entities": entities,
	}

	writeJSON(w, http.StatusOK, response)
}

func (h *Handler) handlePublicSnapshot(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeMethodNotAllowed(w)
		return
	}

	admin := h.control.AdminSnapshot()

	publicOrgs := make(map[string]model.Organization)
	for _, org := range admin.Organizations {
		if !metadataPublicOrDefault(org.Metadata) {
			continue
		}
		publicOrgs[org.OrgID] = org
	}

	activeMembershipsByOrg := make(map[string]map[string]struct{})
	for _, membership := range admin.Memberships {
		if membership.Status != model.StatusActive {
			continue
		}
		if _, ok := publicOrgs[membership.OrgID]; !ok {
			continue
		}
		members := activeMembershipsByOrg[membership.OrgID]
		if members == nil {
			members = make(map[string]struct{})
			activeMembershipsByOrg[membership.OrgID] = members
		}
		members[membership.HumanID] = struct{}{}
	}

	activePublicHumans := make(map[string]model.Human)
	for _, human := range admin.Humans {
		if !metadataPublicOrDefault(human.Metadata) {
			continue
		}
		hasActivePublicMembership := false
		for _, members := range activeMembershipsByOrg {
			if _, ok := members[human.HumanID]; ok {
				hasActivePublicMembership = true
				break
			}
		}
		if !hasActivePublicMembership {
			continue
		}
		activePublicHumans[human.HumanID] = human
	}

	organizations := make([]map[string]any, 0, len(publicOrgs))
	for _, org := range publicOrgs {
		organizations = append(organizations, map[string]any{
			"org_id":       org.OrgID,
			"handle":       org.Handle,
			"uri":          h.organizationURI(org),
			"display_name": org.DisplayName,
			"metadata":     snapshotMetadataPublicView(org.Metadata),
		})
	}
	sort.Slice(organizations, func(i, j int) bool {
		return fmt.Sprintf("%v", organizations[i]["handle"]) < fmt.Sprintf("%v", organizations[j]["handle"])
	})

	humans := make([]map[string]any, 0, len(activePublicHumans))
	for _, human := range activePublicHumans {
		humans = append(humans, map[string]any{
			"human_id": human.HumanID,
			"handle":   human.Handle,
			"uri":      h.humanURI(human),
			"metadata": snapshotMetadataPublicView(human.Metadata),
		})
	}
	sort.Slice(humans, func(i, j int) bool {
		return fmt.Sprintf("%v", humans[i]["handle"]) < fmt.Sprintf("%v", humans[j]["handle"])
	})

	memberships := make([]map[string]any, 0)
	for _, membership := range admin.Memberships {
		if membership.Status != model.StatusActive {
			continue
		}
		if _, ok := publicOrgs[membership.OrgID]; !ok {
			continue
		}
		if _, ok := activePublicHumans[membership.HumanID]; !ok {
			continue
		}
		memberships = append(memberships, map[string]any{
			"org_id":   membership.OrgID,
			"human_id": membership.HumanID,
			"status":   membership.Status,
		})
	}
	sort.Slice(memberships, func(i, j int) bool {
		orgI := fmt.Sprintf("%v", memberships[i]["org_id"])
		orgJ := fmt.Sprintf("%v", memberships[j]["org_id"])
		if orgI != orgJ {
			return orgI < orgJ
		}
		return fmt.Sprintf("%v", memberships[i]["human_id"]) < fmt.Sprintf("%v", memberships[j]["human_id"])
	})

	agents := make([]map[string]any, 0)
	for _, agent := range admin.Agents {
		if agent.Status != model.StatusActive {
			continue
		}
		if _, ok := publicOrgs[agent.OrgID]; !ok {
			continue
		}
		if !metadataPublicOrDefault(agent.Metadata) {
			continue
		}
		if agent.OwnerHumanID != nil && strings.TrimSpace(*agent.OwnerHumanID) != "" {
			if _, ok := activePublicHumans[*agent.OwnerHumanID]; !ok {
				continue
			}
		}
		row := map[string]any{
			"agent_uuid": agent.AgentUUID,
			"agent_id":   agent.AgentID,
			"handle":     agent.Handle,
			"uri":        h.agentURI(agent),
			"org_id":     agent.OrgID,
			"status":     agent.Status,
			"metadata":   snapshotMetadataPublicView(agent.Metadata),
		}
		if owner := agentOwnerPayload(agent); owner != nil {
			row["owner"] = owner
		}
		agents = append(agents, row)
	}
	sort.Slice(agents, func(i, j int) bool {
		agentI := fmt.Sprintf("%v", agents[i]["agent_id"])
		agentJ := fmt.Sprintf("%v", agents[j]["agent_id"])
		if agentI != agentJ {
			return agentI < agentJ
		}
		return fmt.Sprintf("%v", agents[i]["org_id"]) < fmt.Sprintf("%v", agents[j]["org_id"])
	})

	writeJSON(w, http.StatusOK, map[string]any{
		"snapshot": map[string]any{
			"generated_at":   h.now().UTC().Format(time.RFC3339Nano),
			"organizations":  organizations,
			"humans":         humans,
			"memberships":    memberships,
			"agents":         agents,
			"org_trusts":     []any{},
			"agent_trusts":   []any{},
			"stats":          []any{},
			"snapshot_scope": "public",
		},
	})
}

func (h *Handler) handleOrgs(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeMethodNotAllowed(w)
		return
	}
	actor, err := h.authenticateHuman(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "unauthorized", "missing or invalid human auth")
		return
	}
	if h.requireHandleConfirmedForWrite(w, actor) {
		return
	}

	var req createOrgRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid JSON request")
		return
	}
	handle := normalizeHandle(req.Handle)
	displayName := strings.TrimSpace(req.DisplayName)
	if !validateHandle(handle) {
		writeError(w, http.StatusBadRequest, "invalid_handle", "handle must be 2-64 chars, URL-safe (a-z, 0-9, ., _, -), and not blocked")
		return
	}
	if displayName == "" {
		displayName = handle
	}
	orgID, err := h.idFactory()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "id_generation_failed", "failed to generate org_id")
		return
	}
	org, membership, err := h.control.CreateOrg(handle, displayName, actor.Human.HumanID, orgID, h.now().UTC())
	if err != nil {
		switch {
		case errors.Is(err, store.ErrOrgHandleTaken), errors.Is(err, store.ErrOrgNameTaken):
			writeError(w, http.StatusConflict, "org_handle_exists", "organization handle already exists")
		case errors.Is(err, store.ErrInvalidHandle):
			writeError(w, http.StatusBadRequest, "invalid_handle", "handle must be 2-64 chars, URL-safe (a-z, 0-9, ., _, -), and not blocked")
		default:
			writeError(w, http.StatusInternalServerError, "store_error", "failed to create org")
		}
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"organization": h.organizationPayload(org),
		"membership":   membership,
	})
}

func (h *Handler) handleOrgSubroutes(w http.ResponseWriter, r *http.Request) {
	parts := splitPath(r.URL.Path)
	if len(parts) < 3 || parts[0] != "v1" || parts[1] != "orgs" {
		writeError(w, http.StatusNotFound, "not_found", "route not found")
		return
	}
	orgID := parts[2]
	actor, err := h.authenticateHuman(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "unauthorized", "missing or invalid human auth")
		return
	}
	if len(parts) == 3 {
		if r.Method != http.MethodDelete {
			writeMethodNotAllowed(w)
			return
		}
		if h.requireHandleConfirmedForWrite(w, actor) {
			return
		}
		if err := h.control.DeleteOrg(orgID, actor.Human.HumanID, actor.IsSuperAdmin, h.now().UTC()); err != nil {
			switch {
			case errors.Is(err, store.ErrOrgNotFound):
				writeError(w, http.StatusNotFound, "unknown_org", "org_id is not registered")
			case errors.Is(err, store.ErrUnauthorizedRole):
				writeError(w, http.StatusForbidden, "forbidden", "owner role required")
			default:
				writeError(w, http.StatusInternalServerError, "store_error", "failed to delete organization")
			}
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"status": "ok",
			"org_id": orgID,
			"result": "deleted",
		})
		return
	}

	sub := parts[3]

	switch sub {
	case "metadata":
		if len(parts) != 4 {
			writeError(w, http.StatusNotFound, "not_found", "route not found")
			return
		}
		if r.Method != http.MethodPatch {
			writeMethodNotAllowed(w)
			return
		}
		if h.requireHandleConfirmedForWrite(w, actor) {
			return
		}
		metadata, err := decodeMetadataUpdateRequest(r)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
			return
		}
		org, err := h.control.UpdateOrgMetadata(orgID, metadata, actor.Human.HumanID, actor.IsSuperAdmin, h.now().UTC())
		if err != nil {
			switch {
			case errors.Is(err, store.ErrOrgNotFound):
				writeError(w, http.StatusNotFound, "unknown_org", "org_id is not registered")
			case errors.Is(err, store.ErrUnauthorizedRole):
				writeError(w, http.StatusForbidden, "forbidden", "owner role required")
			default:
				writeError(w, http.StatusInternalServerError, "store_error", "failed to update organization metadata")
			}
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"organization": h.organizationPayload(org)})
		return
	case "invites":
		if len(parts) != 4 {
			writeError(w, http.StatusNotFound, "not_found", "route not found")
			return
		}
		switch r.Method {
		case http.MethodGet:
			invites, err := h.control.ListOrgInvites(orgID, actor.Human.HumanID, actor.IsSuperAdmin)
			if err != nil {
				switch {
				case errors.Is(err, store.ErrOrgNotFound):
					writeError(w, http.StatusNotFound, "unknown_org", "org_id is not registered")
				case errors.Is(err, store.ErrUnauthorizedRole):
					writeError(w, http.StatusForbidden, "forbidden", "owner role required")
				default:
					writeError(w, http.StatusInternalServerError, "store_error", "failed to list invites")
				}
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"invites": invites})
			return
		case http.MethodPost:
			if h.requireHandleConfirmedForWrite(w, actor) {
				return
			}
			var req createInviteRequest
			if err := decodeJSON(r, &req); err != nil {
				writeError(w, http.StatusBadRequest, "invalid_request", "invalid JSON request")
				return
			}
			role := strings.ToLower(strings.TrimSpace(req.Role))
			email := strings.ToLower(strings.TrimSpace(req.Email))
			if email == "" {
				writeError(w, http.StatusBadRequest, "invalid_email", "email is required")
				return
			}
			expiryDays := defaultInviteExpiryDays
			if req.ExpiresInDays != nil {
				expiryDays = *req.ExpiresInDays
			}
			if expiryDays < 1 || expiryDays > maxInviteExpiryDays {
				writeError(w, http.StatusBadRequest, "invalid_expires_in_days", "expires_in_days must be in range 1..365")
				return
			}

			inviteCode, err := auth.GenerateToken()
			if err != nil {
				writeError(w, http.StatusInternalServerError, "token_generation_failed", "failed to generate invite code")
				return
			}
			inviteID, err := h.idFactory()
			if err != nil {
				writeError(w, http.StatusInternalServerError, "id_generation_failed", "failed to generate invite_id")
				return
			}
			now := h.now().UTC()
			expiresAt := now.AddDate(0, 0, expiryDays)
			invite, err := h.control.CreateInvite(
				orgID,
				email,
				role,
				actor.Human.HumanID,
				inviteID,
				auth.HashToken(inviteCode),
				expiresAt,
				now,
				actor.IsSuperAdmin,
			)
			if err != nil {
				switch {
				case errors.Is(err, store.ErrOrgNotFound):
					writeError(w, http.StatusNotFound, "unknown_org", "org_id is not registered")
				case errors.Is(err, store.ErrUnauthorizedRole):
					writeError(w, http.StatusForbidden, "forbidden", "owner role required")
				case errors.Is(err, store.ErrInviteExists):
					writeError(w, http.StatusConflict, "invite_exists", "invite already exists or invitee is already an active member")
				case errors.Is(err, store.ErrInvalidRole):
					writeError(w, http.StatusBadRequest, "invalid_role", "role must be admin|member|viewer")
				case errors.Is(err, store.ErrInviteInvalid):
					writeError(w, http.StatusBadRequest, "invalid_invite", "invite payload is invalid")
				default:
					writeError(w, http.StatusInternalServerError, "store_error", "failed to create invite")
				}
				return
			}
			writeJSON(w, http.StatusCreated, map[string]any{
				"invite": invite,
			})
			return
		default:
			writeMethodNotAllowed(w)
			return
		}
		return
	case "access-keys":
		if len(parts) == 4 {
			switch r.Method {
			case http.MethodGet:
				keys, err := h.control.ListOrgAccessKeys(orgID, actor.Human.HumanID, actor.IsSuperAdmin)
				if err != nil {
					switch {
					case errors.Is(err, store.ErrOrgNotFound):
						writeError(w, http.StatusNotFound, "unknown_org", "org_id is not registered")
					case errors.Is(err, store.ErrUnauthorizedRole):
						writeError(w, http.StatusForbidden, "forbidden", "owner role required")
					default:
						writeError(w, http.StatusInternalServerError, "store_error", "failed to list access keys")
					}
					return
				}
				writeJSON(w, http.StatusOK, map[string]any{"access_keys": keys})
				return
			case http.MethodPost:
				if h.requireHandleConfirmedForWrite(w, actor) {
					return
				}
				var req createOrgAccessKeyRequest
				if err := decodeJSON(r, &req); err != nil {
					writeError(w, http.StatusBadRequest, "invalid_request", "invalid JSON request")
					return
				}
				var expiresAt *time.Time
				if req.ExpiresInDays != nil {
					if *req.ExpiresInDays <= 0 || *req.ExpiresInDays > 3650 {
						writeError(w, http.StatusBadRequest, "invalid_expires_in_days", "expires_in_days must be in range 1..3650")
						return
					}
					expires := h.now().UTC().AddDate(0, 0, *req.ExpiresInDays)
					expiresAt = &expires
				}
				keySecret, err := auth.GenerateToken()
				if err != nil {
					writeError(w, http.StatusInternalServerError, "token_generation_failed", "failed to generate access key")
					return
				}
				keyID, err := h.idFactory()
				if err != nil {
					writeError(w, http.StatusInternalServerError, "id_generation_failed", "failed to generate key_id")
					return
				}
				key, err := h.control.CreateOrgAccessKey(
					orgID,
					req.Label,
					req.Scopes,
					expiresAt,
					actor.Human.HumanID,
					keyID,
					auth.HashToken(keySecret),
					h.now().UTC(),
					actor.IsSuperAdmin,
				)
				if err != nil {
					switch {
					case errors.Is(err, store.ErrOrgNotFound):
						writeError(w, http.StatusNotFound, "unknown_org", "org_id is not registered")
					case errors.Is(err, store.ErrUnauthorizedRole):
						writeError(w, http.StatusForbidden, "forbidden", "owner role required")
					case errors.Is(err, store.ErrOrgAccessScopeDenied):
						writeError(w, http.StatusBadRequest, "invalid_scopes", "at least one valid scope is required")
					default:
						writeError(w, http.StatusInternalServerError, "store_error", "failed to create access key")
					}
					return
				}
				writeJSON(w, http.StatusCreated, map[string]any{
					"access_key": key,
					"key":        keySecret,
				})
				return
			default:
				writeMethodNotAllowed(w)
				return
			}
		}

		if len(parts) == 5 {
			if r.Method != http.MethodDelete {
				writeMethodNotAllowed(w)
				return
			}
			if h.requireHandleConfirmedForWrite(w, actor) {
				return
			}
			keyID := parts[4]
			key, err := h.control.RevokeOrgAccessKey(orgID, keyID, actor.Human.HumanID, actor.IsSuperAdmin, h.now().UTC())
			if err != nil {
				switch {
				case errors.Is(err, store.ErrOrgNotFound):
					writeError(w, http.StatusNotFound, "unknown_org", "org_id is not registered")
				case errors.Is(err, store.ErrOrgAccessKeyNotFound):
					writeError(w, http.StatusNotFound, "unknown_access_key", "key_id is not registered")
				case errors.Is(err, store.ErrUnauthorizedRole):
					writeError(w, http.StatusForbidden, "forbidden", "owner role required")
				default:
					writeError(w, http.StatusInternalServerError, "store_error", "failed to revoke access key")
				}
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"access_key": key})
			return
		}
		writeError(w, http.StatusNotFound, "not_found", "route not found")
		return
	case "humans":
		if len(parts) == 4 {
			if r.Method != http.MethodGet {
				writeMethodNotAllowed(w)
				return
			}
			humans, err := h.control.ListOrgHumans(orgID, actor.Human.HumanID, actor.IsSuperAdmin)
			if err != nil {
				switch {
				case errors.Is(err, store.ErrOrgNotFound):
					writeError(w, http.StatusNotFound, "unknown_org", "org_id is not registered")
				case errors.Is(err, store.ErrUnauthorizedRole):
					writeError(w, http.StatusForbidden, "forbidden", "org membership required")
				default:
					writeError(w, http.StatusInternalServerError, "store_error", "failed to list humans")
				}
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"humans": h.orgHumanViewListPayload(humans)})
			return
		}

		if len(parts) == 5 {
			if r.Method != http.MethodDelete {
				writeMethodNotAllowed(w)
				return
			}
			if h.requireHandleConfirmedForWrite(w, actor) {
				return
			}
			targetHumanID := strings.TrimSpace(parts[4])
			if targetHumanID == "" {
				writeError(w, http.StatusBadRequest, "invalid_human_id", "human_id is required")
				return
			}
			membership, err := h.control.RevokeMembership(orgID, targetHumanID, actor.Human.HumanID, actor.IsSuperAdmin, h.now().UTC())
			if err != nil {
				switch {
				case errors.Is(err, store.ErrOrgNotFound):
					writeError(w, http.StatusNotFound, "unknown_org", "org_id is not registered")
				case errors.Is(err, store.ErrMembershipNotFound):
					writeError(w, http.StatusNotFound, "unknown_membership", "human is not an active member in this organization")
				case errors.Is(err, store.ErrCannotRevokeOwner):
					writeError(w, http.StatusBadRequest, "cannot_revoke_owner", "owner membership cannot be revoked")
				case errors.Is(err, store.ErrUnauthorizedRole):
					writeError(w, http.StatusForbidden, "forbidden", "owner required")
				default:
					writeError(w, http.StatusInternalServerError, "store_error", "failed to revoke human membership")
				}
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"membership": membership})
			return
		}
		writeError(w, http.StatusNotFound, "not_found", "route not found")
		return
	case "agents":
		if r.Method != http.MethodGet {
			writeMethodNotAllowed(w)
			return
		}
		agents, err := h.control.ListOrgAgents(orgID, actor.Human.HumanID, actor.IsSuperAdmin)
		if err != nil {
			switch {
			case errors.Is(err, store.ErrOrgNotFound):
				writeError(w, http.StatusNotFound, "unknown_org", "org_id is not registered")
			case errors.Is(err, store.ErrUnauthorizedRole):
				writeError(w, http.StatusForbidden, "forbidden", "org membership required")
			default:
				writeError(w, http.StatusInternalServerError, "store_error", "failed to list agents")
			}
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"agents": h.agentListResponsePayload(agents)})
		return
	case "trust-graph":
		if r.Method != http.MethodGet {
			writeMethodNotAllowed(w)
			return
		}
		orgEdges, agentEdges, err := h.control.ListOrgTrustGraph(orgID, actor.Human.HumanID, actor.IsSuperAdmin)
		if err != nil {
			switch {
			case errors.Is(err, store.ErrOrgNotFound):
				writeError(w, http.StatusNotFound, "unknown_org", "org_id is not registered")
			case errors.Is(err, store.ErrUnauthorizedRole):
				writeError(w, http.StatusForbidden, "forbidden", "org membership required")
			default:
				writeError(w, http.StatusInternalServerError, "store_error", "failed to load trust graph")
			}
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"org_trusts":   orgEdges,
			"agent_trusts": agentEdges,
		})
		return
	case "audit":
		if r.Method != http.MethodGet {
			writeMethodNotAllowed(w)
			return
		}
		events, err := h.control.ListAudit(orgID, actor.Human.HumanID, actor.IsSuperAdmin)
		if err != nil {
			switch {
			case errors.Is(err, store.ErrOrgNotFound):
				writeError(w, http.StatusNotFound, "unknown_org", "org_id is not registered")
			case errors.Is(err, store.ErrUnauthorizedRole):
				writeError(w, http.StatusForbidden, "forbidden", "org membership required")
			default:
				writeError(w, http.StatusInternalServerError, "store_error", "failed to load audit")
			}
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"events": events})
		return
	case "stats":
		if r.Method != http.MethodGet {
			writeMethodNotAllowed(w)
			return
		}
		stats, err := h.control.GetOrgStats(orgID, actor.Human.HumanID, actor.IsSuperAdmin)
		if err != nil {
			switch {
			case errors.Is(err, store.ErrOrgNotFound):
				writeError(w, http.StatusNotFound, "unknown_org", "org_id is not registered")
			case errors.Is(err, store.ErrUnauthorizedRole):
				writeError(w, http.StatusForbidden, "forbidden", "org membership required")
			default:
				writeError(w, http.StatusInternalServerError, "store_error", "failed to load stats")
			}
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"stats": stats})
		return
	default:
		writeError(w, http.StatusNotFound, "not_found", "route not found")
		return
	}
}

func (h *Handler) handleOrgAccessHumans(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeMethodNotAllowed(w)
		return
	}
	org, key, err := h.authorizeOrgAccess(r, model.OrgAccessScopeListHumans)
	if err != nil {
		h.writeOrgAccessErr(w, err)
		return
	}
	humans, err := h.control.ListOrgHumans(org.OrgID, "", true)
	if err != nil {
		if errors.Is(err, store.ErrOrgNotFound) {
			writeError(w, http.StatusNotFound, "unknown_org", "org_name is not registered")
			return
		}
		writeError(w, http.StatusInternalServerError, "store_error", "failed to list humans")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"organization": h.organizationPayload(org),
		"access_key":   key,
		"humans":       h.orgHumanViewListPayload(humans),
	})
}

func (h *Handler) handleOrgAccessAgents(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeMethodNotAllowed(w)
		return
	}
	org, key, err := h.authorizeOrgAccess(r, model.OrgAccessScopeListAgents)
	if err != nil {
		h.writeOrgAccessErr(w, err)
		return
	}
	agents, err := h.control.ListOrgAgents(org.OrgID, "", true)
	if err != nil {
		if errors.Is(err, store.ErrOrgNotFound) {
			writeError(w, http.StatusNotFound, "unknown_org", "org_name is not registered")
			return
		}
		writeError(w, http.StatusInternalServerError, "store_error", "failed to list agents")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"organization": h.organizationPayload(org),
		"access_key":   key,
		"agents":       h.agentListResponsePayload(agents),
	})
}

func (h *Handler) authorizeOrgAccess(r *http.Request, requiredScope string) (model.Organization, model.OrgAccessKey, error) {
	orgName := normalizeHandle(r.URL.Query().Get("org_name"))
	if orgName == "" {
		return model.Organization{}, model.OrgAccessKey{}, errMissingOrgName
	}
	secret := strings.TrimSpace(r.Header.Get("X-Org-Access-Key"))
	if secret == "" {
		if bearer, err := auth.ExtractBearerToken(r.Header.Get("Authorization")); err == nil {
			secret = bearer
		}
	}
	if secret == "" {
		return model.Organization{}, model.OrgAccessKey{}, errMissingOrgAccessKey
	}
	return h.control.AuthorizeOrgAccessByName(orgName, auth.HashToken(secret), requiredScope, h.now().UTC())
}

func (h *Handler) writeOrgAccessErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, errMissingOrgName):
		writeError(w, http.StatusBadRequest, "invalid_org_name", "org_name query parameter is required")
	case errors.Is(err, errMissingOrgAccessKey):
		writeError(w, http.StatusUnauthorized, "unauthorized", "missing org access key")
	case errors.Is(err, store.ErrOrgNotFound):
		writeError(w, http.StatusNotFound, "unknown_org", "org_name is not registered")
	case errors.Is(err, store.ErrOrgAccessScopeDenied):
		writeError(w, http.StatusForbidden, "forbidden", "access key lacks required scope")
	case errors.Is(err, store.ErrOrgAccessKeyNotFound), errors.Is(err, store.ErrOrgAccessKeyInvalid):
		writeError(w, http.StatusUnauthorized, "unauthorized", "invalid or expired org access key")
	default:
		writeError(w, http.StatusInternalServerError, "store_error", "failed org access authorization")
	}
}

func (h *Handler) handleOrgInvites(w http.ResponseWriter, r *http.Request) {
	parts := splitPath(r.URL.Path)
	if len(parts) < 2 || parts[0] != "v1" || parts[1] != "org-invites" {
		writeError(w, http.StatusNotFound, "not_found", "route not found")
		return
	}
	actor, err := h.authenticateHuman(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "unauthorized", "missing or invalid human auth")
		return
	}

	if len(parts) == 2 {
		if r.Method != http.MethodGet {
			writeMethodNotAllowed(w)
			return
		}
		invites := h.control.ListInvitesForHuman(actor.Human.HumanID, actor.Human.Email, actor.IsSuperAdmin)
		writeJSON(w, http.StatusOK, map[string]any{"invites": h.inviteWithOrgListPayload(invites)})
		return
	}

	if len(parts) == 3 && parts[2] == "redeem" {
		if r.Method != http.MethodPost {
			writeMethodNotAllowed(w)
			return
		}
		if h.requireHandleConfirmedForWrite(w, actor) {
			return
		}
		var req redeemInviteRequest
		if err := decodeJSON(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_request", "invalid JSON request")
			return
		}
		inviteID := strings.TrimSpace(req.InviteID)
		inviteCode := strings.TrimSpace(req.InviteCode)
		if inviteID == "" && inviteCode == "" {
			writeError(w, http.StatusBadRequest, "invalid_invite", "invite_id or invite_code is required")
			return
		}
		var membership model.Membership
		if inviteID != "" {
			membership, err = h.control.AcceptInvite(inviteID, actor.Human.HumanID, actor.Human.Email, h.now().UTC(), h.idFactory)
		} else {
			membership, err = h.control.AcceptInviteBySecretHash(auth.HashToken(inviteCode), actor.Human.HumanID, actor.Human.Email, h.now().UTC(), h.idFactory)
		}
		if err != nil {
			switch {
			case errors.Is(err, store.ErrInviteNotFound):
				writeError(w, http.StatusNotFound, "unknown_invite", "invite is not registered")
			case errors.Is(err, store.ErrInviteInvalid):
				writeError(w, http.StatusBadRequest, "invalid_invite", "invite cannot be redeemed by this user")
			default:
				writeError(w, http.StatusInternalServerError, "store_error", "failed to redeem invite")
			}
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"membership": membership})
		return
	}

	if len(parts) == 4 && parts[3] == "accept" {
		if r.Method != http.MethodPost {
			writeMethodNotAllowed(w)
			return
		}
		if h.requireHandleConfirmedForWrite(w, actor) {
			return
		}
		inviteID := parts[2]
		membership, err := h.control.AcceptInvite(inviteID, actor.Human.HumanID, actor.Human.Email, h.now().UTC(), h.idFactory)
		if err != nil {
			switch {
			case errors.Is(err, store.ErrInviteNotFound):
				writeError(w, http.StatusNotFound, "unknown_invite", "invite_id is not registered")
			case errors.Is(err, store.ErrInviteInvalid):
				writeError(w, http.StatusBadRequest, "invalid_invite", "invite cannot be accepted by this user")
			default:
				writeError(w, http.StatusInternalServerError, "store_error", "failed to accept invite")
			}
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"membership": membership})
		return
	}

	if len(parts) == 3 && r.Method == http.MethodDelete {
		if h.requireHandleConfirmedForWrite(w, actor) {
			return
		}
		inviteID := parts[2]
		invite, err := h.control.RevokeInvite(inviteID, actor.Human.HumanID, actor.Human.Email, actor.IsSuperAdmin, h.now().UTC())
		if err != nil {
			switch {
			case errors.Is(err, store.ErrInviteNotFound):
				writeError(w, http.StatusNotFound, "unknown_invite", "invite_id is not registered")
			case errors.Is(err, store.ErrUnauthorizedRole):
				writeError(w, http.StatusForbidden, "forbidden", "invite recipient or org owner required")
			default:
				writeError(w, http.StatusInternalServerError, "store_error", "failed to revoke invite")
			}
			return
		}
		result := "revoked"
		if strings.EqualFold(strings.TrimSpace(actor.Human.Email), strings.TrimSpace(invite.Email)) {
			result = "denied"
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"invite": invite,
			"result": result,
		})
		return
	}

	writeError(w, http.StatusNotFound, "not_found", "route not found")
}

func (h *Handler) handleCreateBindToken(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeMethodNotAllowed(w)
		return
	}
	actor, err := h.authenticateHuman(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "unauthorized", "missing or invalid human auth")
		return
	}
	if h.requireHandleConfirmedForWrite(w, actor) {
		return
	}
	var req createBindTokenRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid JSON request")
		return
	}
	req.OrgID = strings.TrimSpace(req.OrgID)
	if req.OrgID == "" {
		writeError(w, http.StatusBadRequest, "invalid_org_id", "org_id is required")
		return
	}
	if req.OwnerHumanID != nil {
		v := strings.TrimSpace(*req.OwnerHumanID)
		if v == "" {
			req.OwnerHumanID = nil
		} else {
			req.OwnerHumanID = &v
		}
	}
	if req.OwnerHumanID != nil {
		if h.ensureHumanOwnedAgentLimit(w, *req.OwnerHumanID) {
			return
		}
	}

	bindSecret, err := auth.GenerateToken()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "token_generation_failed", "failed to generate bind token")
		return
	}
	bindID, err := h.idFactory()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "id_generation_failed", "failed to generate bind id")
		return
	}
	expiresAt := h.now().UTC().Add(h.bindTokenTTL)
	bind, err := h.control.CreateBindToken(req.OrgID, req.OwnerHumanID, actor.Human.HumanID, bindID, auth.HashToken(bindSecret), expiresAt, h.now().UTC(), actor.IsSuperAdmin)
	if err != nil {
		switch {
		case errors.Is(err, store.ErrOrgNotFound):
			writeError(w, http.StatusNotFound, "unknown_org", "org_id is not registered")
		case errors.Is(err, store.ErrUnauthorizedRole):
			writeError(w, http.StatusForbidden, "forbidden", "role member/admin/owner required")
		case errors.Is(err, store.ErrMembershipNotFound):
			writeError(w, http.StatusBadRequest, "invalid_owner_human_id", "owner_human_id must be active in org")
		default:
			writeError(w, http.StatusInternalServerError, "store_error", "failed to create bind token")
		}
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"bind_id":        bind.BindID,
		"bind_token":     bindSecret,
		"connect_prompt": h.buildAgentConnectPrompt(r, bind, bindSecret),
		"org_id":         bind.OrgID,
		"owner_human_id": bind.OwnerHumanID,
		"expires_at":     bind.ExpiresAt,
	})
}

func (h *Handler) handleRedeemBindToken(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeMethodNotAllowed(w)
		return
	}
	if !requireJSONRequestContentType(w, r) {
		return
	}
	var req redeemBindTokenRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid JSON request")
		return
	}
	req.BindToken = strings.TrimSpace(req.BindToken)
	if req.BindToken == "" {
		writeError(w, http.StatusBadRequest, "invalid_bind_token", "bind_token is required")
		return
	}
	requestedHandle := requestedBindHandle(req)
	if requestedHandle != "" && !validateAgentID(requestedHandle) {
		writeError(w, http.StatusBadRequest, "invalid_handle", "handle must be 2-64 chars, URL-safe (a-z, 0-9, ., _, -), and not blocked")
		return
	}

	agentToken, err := auth.GenerateToken()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "token_generation_failed", "failed to generate agent token")
		return
	}
	bindTokenHash := auth.HashToken(req.BindToken)
	bind, err := h.control.PeekBindToken(bindTokenHash)
	if err == nil && bind.OwnerHumanID != nil {
		if h.ensureHumanOwnedAgentLimit(w, *bind.OwnerHumanID) {
			return
		}
	}
	writeBindSuccess := func(agent model.Agent) {
		apiBase := h.apiBaseURL(r)
		writeJSON(w, http.StatusCreated, map[string]any{
			"token":    agentToken,
			"api_base": apiBase,
			"agent":    h.agentResponsePayload(agent),
			"endpoints": map[string]string{
				"profile":      apiBase + "/agents/me",
				"manifest":     apiBase + "/agents/me/manifest",
				"capabilities": apiBase + "/agents/me/capabilities",
				"skill":        apiBase + "/agents/me/skill",
				"publish":      apiBase + "/messages/publish",
				"pull":         apiBase + "/messages/pull",
				"ack":          apiBase + "/messages/ack",
				"nack":         apiBase + "/messages/nack",
				"status":       apiBase + "/messages/{message_id}",
			},
		})
	}
	writeBindError := func(err error) {
		switch {
		case errors.Is(err, store.ErrBindNotFound):
			writeError(w, http.StatusNotFound, "bind_not_found", "bind token not found")
		case errors.Is(err, store.ErrBindExpired):
			writeError(w, http.StatusBadRequest, "bind_expired", "bind token has expired")
		case errors.Is(err, store.ErrBindUsed):
			writeError(w, http.StatusConflict, "bind_used", "bind token already used")
		case errors.Is(err, store.ErrAgentExists):
			writeJSON(w, http.StatusConflict, map[string]any{
				"error":             "agent_exists",
				"message":           "requested handle already exists; retry bind with another handle permutation",
				"retryable":         true,
				"suggested_handles": bindHandleSuggestions(requestedHandle),
			})
		case errors.Is(err, store.ErrInvalidHandle):
			writeError(w, http.StatusBadRequest, "invalid_handle", "handle must be 2-64 chars, URL-safe (a-z, 0-9, ., _, -), and not blocked")
		case errors.Is(err, store.ErrMembershipNotFound):
			writeError(w, http.StatusBadRequest, "invalid_owner_human_id", "owner_human_id is no longer active in org")
		default:
			writeError(w, http.StatusInternalServerError, "store_error", "failed to redeem bind token")
		}
	}
	if requestedHandle != "" {
		agent, err := h.control.RedeemBindToken(bindTokenHash, requestedHandle, auth.HashToken(agentToken), h.now().UTC())
		if err != nil {
			writeBindError(err)
			return
		}
		writeBindSuccess(agent)
		return
	}
	for attempt := 0; attempt < 8; attempt++ {
		agentID, genErr := h.generateTemporaryAgentID()
		if genErr != nil {
			writeError(w, http.StatusInternalServerError, "id_generation_failed", "failed to generate temporary agent_id")
			return
		}

		agent, err := h.control.RedeemBindToken(bindTokenHash, agentID, auth.HashToken(agentToken), h.now().UTC())
		if err == nil {
			writeBindSuccess(agent)
			return
		}
		if errors.Is(err, store.ErrAgentExists) {
			continue
		}
		writeBindError(err)
		return
	}
	writeError(w, http.StatusConflict, "agent_id_generation_failed", "failed to allocate a unique agent_id")
}

func (h *Handler) generateTemporaryAgentID() (string, error) {
	id, err := h.idFactory()
	if err != nil {
		return "", err
	}
	clean := strings.ToLower(strings.ReplaceAll(strings.TrimSpace(id), "-", ""))
	if clean == "" {
		return "", errors.New("empty generated id")
	}
	if len(clean) > 12 {
		clean = clean[:12]
	}
	agentID := "tmp-" + clean
	if !validateAgentID(agentID) {
		return "", errors.New("invalid generated agent_id")
	}
	return agentID, nil
}

func (h *Handler) handleAgentsSubroutes(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimSuffix(r.URL.Path, "/")
	const prefix = "/v1/agents/"
	if !strings.HasPrefix(path, prefix) {
		writeError(w, http.StatusNotFound, "not_found", "route not found")
		return
	}
	tail := strings.Trim(strings.TrimPrefix(path, prefix), "/")
	if tail == "" {
		writeError(w, http.StatusNotFound, "not_found", "route not found")
		return
	}
	agentRef := tail
	action := ""
	if r.Method == http.MethodPost && strings.HasSuffix(tail, "/rotate-token") {
		action = "rotate-token"
		agentRef = strings.Trim(strings.TrimSuffix(tail, "/rotate-token"), "/")
	}
	if r.Method == http.MethodDelete && strings.HasSuffix(tail, "/record") {
		action = "record"
		agentRef = strings.Trim(strings.TrimSuffix(tail, "/record"), "/")
	}
	if r.Method == http.MethodPost && strings.HasSuffix(tail, "/bind") {
		action = "bind"
		agentRef = strings.Trim(strings.TrimSuffix(tail, "/bind"), "/")
	}
	if r.Method == http.MethodPatch && strings.HasSuffix(tail, "/metadata") {
		action = "metadata"
		agentRef = strings.Trim(strings.TrimSuffix(tail, "/metadata"), "/")
	}
	agentRef = strings.TrimSpace(agentRef)
	if agentRef == "" {
		writeError(w, http.StatusNotFound, "not_found", "route not found")
		return
	}
	agentUUID := normalizeUUID(agentRef)
	if r.Method == http.MethodPatch && action == "" {
		if !validateUUID(agentUUID) {
			writeError(w, http.StatusBadRequest, "invalid_agent_uuid", "agent_uuid must be a valid UUID")
			return
		}
		h.handleAgentMetadataSelfPatch(w, r, agentUUID)
		return
	}
	if action != "bind" && !validateUUID(agentUUID) {
		writeError(w, http.StatusBadRequest, "invalid_agent_uuid", "agent_uuid must be a valid UUID")
		return
	}
	if action == "bind" {
		writeError(w, http.StatusGone, "agent_bind_disabled", "use POST /v1/agent-trusts or POST /v1/me/agent-trusts")
		return
	}

	actor, err := h.authenticateHuman(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "unauthorized", "missing or invalid human auth")
		return
	}
	if h.requireHandleConfirmedForWrite(w, actor) {
		return
	}

	if action == "rotate-token" {
		if r.Method != http.MethodPost {
			writeMethodNotAllowed(w)
			return
		}
		token, err := auth.GenerateToken()
		if err != nil {
			writeError(w, http.StatusInternalServerError, "token_generation_failed", "failed to generate token")
			return
		}
		if err := h.control.RotateAgentToken(agentUUID, actor.Human.HumanID, auth.HashToken(token), h.now().UTC(), actor.IsSuperAdmin); err != nil {
			switch {
			case errors.Is(err, store.ErrAgentNotFound):
				writeError(w, http.StatusNotFound, "unknown_agent", "agent_uuid is not registered")
			case errors.Is(err, store.ErrUnauthorizedRole):
				writeError(w, http.StatusForbidden, "forbidden", "admin/owner required")
			default:
				writeError(w, http.StatusInternalServerError, "store_error", "failed to rotate token")
			}
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"status":     "ok",
			"agent_uuid": agentUUID,
			"token":      token,
		})
		return
	}

	if action == "metadata" {
		writeError(w, http.StatusForbidden, "forbidden", "human metadata updates for agents are not allowed")
		return
	}
	if action == "record" {
		if r.Method != http.MethodDelete {
			writeMethodNotAllowed(w)
			return
		}
		if err := h.control.DeleteAgent(agentUUID, actor.Human.HumanID, h.now().UTC(), actor.IsSuperAdmin); err != nil {
			switch {
			case errors.Is(err, store.ErrAgentNotFound):
				writeError(w, http.StatusNotFound, "unknown_agent", "agent_uuid is not registered")
			case errors.Is(err, store.ErrUnauthorizedRole):
				writeError(w, http.StatusForbidden, "forbidden", "agent owner, org owner, or super-admin required")
			default:
				writeError(w, http.StatusInternalServerError, "store_error", "failed to delete agent")
			}
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"status":     "ok",
			"agent_uuid": agentUUID,
			"result":     "deleted",
		})
		return
	}

	switch r.Method {
	case http.MethodDelete:
		if err := h.control.RevokeAgent(agentUUID, actor.Human.HumanID, h.now().UTC(), actor.IsSuperAdmin); err != nil {
			switch {
			case errors.Is(err, store.ErrAgentNotFound):
				writeError(w, http.StatusNotFound, "unknown_agent", "agent_uuid is not registered")
			case errors.Is(err, store.ErrUnauthorizedRole):
				writeError(w, http.StatusForbidden, "forbidden", "admin/owner required")
			default:
				writeError(w, http.StatusInternalServerError, "store_error", "failed to revoke agent")
			}
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"status":     "ok",
			"agent_uuid": agentUUID,
			"result":     "revoked",
		})
		return
	default:
		writeMethodNotAllowed(w)
		return
	}
}

func (h *Handler) handleOrgTrusts(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeMethodNotAllowed(w)
		return
	}
	actor, err := h.authenticateHuman(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "unauthorized", "missing or invalid human auth")
		return
	}
	if h.requireHandleConfirmedForWrite(w, actor) {
		return
	}
	var req trustOrgRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid JSON request")
		return
	}
	req.OrgID = strings.TrimSpace(req.OrgID)
	req.PeerOrgID = strings.TrimSpace(req.PeerOrgID)
	edgeID, err := h.idFactory()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "id_generation_failed", "failed to generate edge_id")
		return
	}
	edge, created, err := h.control.CreateOrJoinOrgTrust(req.OrgID, req.PeerOrgID, actor.Human.HumanID, edgeID, h.now().UTC(), actor.IsSuperAdmin)
	if err != nil {
		switch {
		case errors.Is(err, store.ErrOrgNotFound):
			writeError(w, http.StatusNotFound, "unknown_org", "org_id or peer_org_id is not registered")
		case errors.Is(err, store.ErrUnauthorizedRole):
			writeError(w, http.StatusForbidden, "forbidden", "owner required")
		case errors.Is(err, store.ErrSelfTrust):
			writeError(w, http.StatusBadRequest, "invalid_peer_org_id", "peer_org_id cannot equal org_id")
		default:
			writeError(w, http.StatusInternalServerError, "store_error", "failed to create org trust")
		}
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	writeJSON(w, status, map[string]any{"trust": edge, "created": created})
}

func (h *Handler) handleOrgTrustByID(w http.ResponseWriter, r *http.Request) {
	parts := splitPath(r.URL.Path)
	if len(parts) < 3 || parts[0] != "v1" || parts[1] != "org-trusts" {
		writeError(w, http.StatusNotFound, "not_found", "route not found")
		return
	}
	edgeID := parts[2]
	actor, err := h.authenticateHuman(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "unauthorized", "missing or invalid human auth")
		return
	}
	if h.requireHandleConfirmedForWrite(w, actor) {
		return
	}

	if len(parts) == 4 {
		switch parts[3] {
		case "approve":
			if r.Method != http.MethodPost {
				writeMethodNotAllowed(w)
				return
			}
			edge, err := h.control.ApproveOrgTrust(edgeID, actor.Human.HumanID, h.now().UTC(), actor.IsSuperAdmin)
			if err != nil {
				h.writeTrustErr(w, err, "org")
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"trust": edge})
			return
		case "block":
			if r.Method != http.MethodPost {
				writeMethodNotAllowed(w)
				return
			}
			edge, err := h.control.BlockOrgTrust(edgeID, actor.Human.HumanID, h.now().UTC(), actor.IsSuperAdmin)
			if err != nil {
				h.writeTrustErr(w, err, "org")
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"trust": edge})
			return
		default:
			writeError(w, http.StatusNotFound, "not_found", "route not found")
			return
		}
	}

	if len(parts) == 3 && r.Method == http.MethodDelete {
		edge, err := h.control.RevokeOrgTrust(edgeID, actor.Human.HumanID, h.now().UTC(), actor.IsSuperAdmin)
		if err != nil {
			h.writeTrustErr(w, err, "org")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"trust": edge})
		return
	}

	writeMethodNotAllowed(w)
}

func (h *Handler) handleAgentTrusts(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeMethodNotAllowed(w)
		return
	}
	actor, err := h.authenticateHuman(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "unauthorized", "missing or invalid human auth")
		return
	}
	if h.requireHandleConfirmedForWrite(w, actor) {
		return
	}
	var req trustAgentRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid JSON request")
		return
	}
	h.handleAgentTrustCreate(w, actor, req, "owner required in org")
}

func (h *Handler) handleAgentTrustByID(w http.ResponseWriter, r *http.Request) {
	parts := splitPath(r.URL.Path)
	if len(parts) < 3 || parts[0] != "v1" || parts[1] != "agent-trusts" {
		writeError(w, http.StatusNotFound, "not_found", "route not found")
		return
	}
	edgeID := parts[2]
	actor, err := h.authenticateHuman(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "unauthorized", "missing or invalid human auth")
		return
	}
	if h.requireHandleConfirmedForWrite(w, actor) {
		return
	}

	if len(parts) == 4 {
		switch parts[3] {
		case "approve":
			if r.Method != http.MethodPost {
				writeMethodNotAllowed(w)
				return
			}
			edge, err := h.control.ApproveAgentTrust(edgeID, actor.Human.HumanID, h.now().UTC(), actor.IsSuperAdmin)
			if err != nil {
				h.writeTrustErr(w, err, "agent")
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"trust": edge})
			return
		case "block":
			if r.Method != http.MethodPost {
				writeMethodNotAllowed(w)
				return
			}
			edge, err := h.control.BlockAgentTrust(edgeID, actor.Human.HumanID, h.now().UTC(), actor.IsSuperAdmin)
			if err != nil {
				h.writeTrustErr(w, err, "agent")
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"trust": edge})
			return
		default:
			writeError(w, http.StatusNotFound, "not_found", "route not found")
			return
		}
	}

	if len(parts) == 3 && r.Method == http.MethodDelete {
		edge, err := h.control.RevokeAgentTrust(edgeID, actor.Human.HumanID, h.now().UTC(), actor.IsSuperAdmin)
		if err != nil {
			h.writeTrustErr(w, err, "agent")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"trust": edge})
		return
	}

	writeMethodNotAllowed(w)
}

func (h *Handler) writeTrustErr(w http.ResponseWriter, err error, kind string) {
	switch {
	case errors.Is(err, store.ErrTrustNotFound):
		writeError(w, http.StatusNotFound, "unknown_trust", kind+"_trust id is not registered")
	case errors.Is(err, store.ErrUnauthorizedRole):
		writeError(w, http.StatusForbidden, "forbidden", "owner required")
	case errors.Is(err, store.ErrAgentNotFound):
		writeError(w, http.StatusNotFound, "unknown_agent", "agent not found")
	default:
		writeError(w, http.StatusInternalServerError, "store_error", "failed trust operation")
	}
}
