package api

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
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

type redeemInviteCodeRequest struct {
	InviteCode string `json:"invite_code"`
}

type createBindTokenRequest struct {
	OrgID        string  `json:"org_id"`
	OwnerHumanID *string `json:"owner_human_id,omitempty"`
}

type redeemBindTokenRequest struct {
	BindToken string `json:"bind_token"`
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

type createOrgAccessKeyRequest struct {
	Label         string   `json:"label"`
	Scopes        []string `json:"scopes"`
	ExpiresInDays *int     `json:"expires_in_days,omitempty"`
}

type agentControlPlaneView struct {
	APIBase      string
	AgentUUID    string
	AgentID      string
	OrgID        string
	OwnerHumanID string
	CanTalkTo    []string
	Capabilities []string
}

var (
	errMissingOrgName      = errors.New("missing_org_name")
	errMissingOrgAccessKey = errors.New("missing_org_access_key")
)

const (
	defaultInviteExpiryDays = 7
	maxInviteExpiryDays     = 365
	defaultUIAppName        = "Statocyst"
	maxMetadataBytes        = 64 * 1024
)

func (h *Handler) handleUIConfig(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeMethodNotAllowed(w)
		return
	}

	supabaseAnonKey := h.supabaseAnonKey
	devHumanEmail := ""
	superAdminEmails := []string{}
	if hasUIConfigPrivilegedAccess(r) {
		devHumanEmail = strings.TrimSpace(strings.ToLower(os.Getenv("DEV_LOGIN_HUMAN_EMAIL")))
		superAdminEmails = setToSortedSlice(h.superAdminEmails)
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"human_auth_provider":      h.humanAuth.Name(),
		"supabase_url":             h.supabaseURL,
		"supabase_anon_key":        supabaseAnonKey,
		"dev_human_id":             strings.TrimSpace(os.Getenv("DEV_LOGIN_HUMAN_ID")),
		"dev_human_email":          devHumanEmail,
		"dev_auto_login":           strings.EqualFold(strings.TrimSpace(os.Getenv("DEV_LOGIN_AUTO")), "true"),
		"super_admin_emails":       superAdminEmails,
		"super_admin_domains":      setToSortedSlice(h.superAdminDomains),
		"super_admin_review_mode":  h.superAdminReview,
		"super_admin_write_policy": "global_write",
		"bind_token_ttl_sec":       int(h.bindTokenTTL.Seconds()),
		"headless_mode":            h.headlessMode,
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
	if len(req.Metadata) == 0 {
		return nil, errors.New("metadata is required")
	}

	var decoded any
	if err := json.Unmarshal(req.Metadata, &decoded); err != nil {
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
	if len(body) > maxMetadataBytes {
		return nil, fmt.Errorf("metadata exceeds %d bytes", maxMetadataBytes)
	}
	return metadata, nil
}

func (h *Handler) handleMe(w http.ResponseWriter, r *http.Request) {
	actor, err := h.authenticateHuman(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "unauthorized", "missing or invalid human auth")
		return
	}

	switch r.Method {
	case http.MethodGet:
		onboarding := map[string]any{
			"handle_required":  true,
			"handle_confirmed": actor.Human.HandleConfirmedAt != nil,
			"next_step": func() string {
				if actor.Human.HandleConfirmedAt == nil {
					return "set_handle"
				}
				return "complete"
			}(),
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"human":          actor.Human,
			"is_super_admin": actor.IsSuperAdmin,
			"onboarding":     onboarding,
		})
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
		writeJSON(w, http.StatusOK, map[string]any{
			"human":          human,
			"is_super_admin": actor.IsSuperAdmin,
			"onboarding": map[string]any{
				"handle_required":  true,
				"handle_confirmed": human.HandleConfirmedAt != nil,
				"next_step":        "complete",
			},
		})
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
		"human": human,
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
		"memberships": h.control.ListMyMemberships(actor.Human.HumanID),
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
		writeJSON(w, http.StatusOK, map[string]any{
			"agents": h.control.ListHumanAgents(actor.Human.HumanID),
		})
		return
	case http.MethodPost:
		if h.requireHandleConfirmedForWrite(w, actor) {
			return
		}
		var req registerAgentRequest
		if err := decodeJSON(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_request", "invalid JSON request")
			return
		}

		req.OrgID = strings.TrimSpace(req.OrgID)
		req.AgentID = normalizeHandle(req.AgentID)
		if !validateAgentID(req.AgentID) {
			writeError(w, http.StatusBadRequest, "invalid_agent_id", "agent_id must be 2-64 chars, URL-safe (a-z, 0-9, ., _, -), and not blocked")
			return
		}

		ownerHumanID := actor.Human.HumanID
		if h.ensureHumanOwnedAgentLimit(w, ownerHumanID) {
			return
		}
		token, err := auth.GenerateToken()
		if err != nil {
			writeError(w, http.StatusInternalServerError, "token_generation_failed", "failed to generate token")
			return
		}
		agent, err := h.control.RegisterAgent(req.OrgID, req.AgentID, &ownerHumanID, auth.HashToken(token), actor.Human.HumanID, h.now().UTC(), actor.IsSuperAdmin)
		if err != nil {
			switch {
			case errors.Is(err, store.ErrOrgNotFound):
				writeError(w, http.StatusNotFound, "unknown_org", "org_id is not registered")
			case errors.Is(err, store.ErrUnauthorizedRole):
				writeError(w, http.StatusForbidden, "forbidden", "membership in org required")
			case errors.Is(err, store.ErrMembershipNotFound):
				writeError(w, http.StatusBadRequest, "invalid_owner_human_id", "owner_human_id must be active in org")
			case errors.Is(err, store.ErrAgentExists):
				writeError(w, http.StatusConflict, "agent_exists", "agent_id already registered")
			case errors.Is(err, store.ErrInvalidHandle):
				writeError(w, http.StatusBadRequest, "invalid_agent_id", "agent_id must be 2-64 chars, URL-safe (a-z, 0-9, ., _, -), and not blocked")
			case errors.Is(err, store.ErrAgentLimitExceeded):
				writeError(w, http.StatusConflict, "agent_limit_reached", "non-super-admin users can only own up to 2 active agents")
			default:
				writeError(w, http.StatusInternalServerError, "store_error", "failed to register agent")
			}
			return
		}

		writeJSON(w, http.StatusCreated, map[string]any{
			"agent_uuid":     agent.AgentUUID,
			"agent_id":       agent.AgentID,
			"handle":         agent.Handle,
			"org_id":         agent.OrgID,
			"owner_human_id": agent.OwnerHumanID,
			"token":          token,
			"status":         agent.Status,
		})
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
		h.handleAgentTrustCreate(w, actor, req, "owner/admin required for initiating agent")
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
		writeError(w, http.StatusConflict, "agent_limit_reached", "non-super-admin users can only own up to 2 active agents")
		return true
	}
	return false
}

func (h *Handler) handleAgentMe(w http.ResponseWriter, r *http.Request) {
	writeMethodNotAllowed(w)
}

func (h *Handler) handleAgentMeMetadata(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPatch {
		writeMethodNotAllowed(w)
		return
	}

	agentUUID, err := h.authenticateAgent(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "unauthorized", "missing or invalid bearer token")
		return
	}

	metadata, err := decodeMetadataUpdateRequest(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}

	agent, err := h.control.UpdateAgentMetadataSelf(agentUUID, metadata, h.now().UTC())
	if err != nil {
		switch {
		case errors.Is(err, store.ErrAgentNotFound):
			writeError(w, http.StatusNotFound, "unknown_agent", "agent_uuid is not registered")
		default:
			writeError(w, http.StatusInternalServerError, "store_error", "failed to update agent metadata")
		}
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"agent": agent,
	})
}

func (h *Handler) handleAgentMeCapabilities(w http.ResponseWriter, r *http.Request) {
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
		writeError(w, http.StatusInternalServerError, "store_error", "failed to load agent capabilities")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"agent": map[string]any{
			"agent_uuid":     cp.AgentUUID,
			"agent_id":       cp.AgentID,
			"org_id":         cp.OrgID,
			"owner_human_id": cp.OwnerHumanID,
		},
		"control_plane": h.agentControlPlanePayload(cp),
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
	skill := buildAgentSkillMarkdown(cp)

	if wantsMarkdownSkill(r) {
		w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(skill))
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"agent": map[string]any{
			"agent_uuid":     cp.AgentUUID,
			"agent_id":       cp.AgentID,
			"org_id":         cp.OrgID,
			"owner_human_id": cp.OwnerHumanID,
		},
		"control_plane": h.agentControlPlanePayload(cp),
		"skill": map[string]any{
			"schema_version": "1",
			"format":         "markdown",
			"content":        skill,
		},
	})
}

func (h *Handler) buildAgentControlPlane(r *http.Request, agent model.Agent) (agentControlPlaneView, error) {
	peers, err := h.control.ListTalkablePeers(agent.AgentUUID)
	if err != nil {
		return agentControlPlaneView{}, err
	}
	ownerHumanID := ""
	if agent.OwnerHumanID != nil {
		ownerHumanID = *agent.OwnerHumanID
	}
	return agentControlPlaneView{
		APIBase:      apiBaseURL(r),
		AgentUUID:    agent.AgentUUID,
		AgentID:      agent.AgentID,
		OrgID:        agent.OrgID,
		OwnerHumanID: ownerHumanID,
		CanTalkTo:    peers,
		Capabilities: []string{"publish_messages", "pull_messages", "read_capabilities", "read_skill", "update_profile"},
	}, nil
}

func (h *Handler) agentControlPlanePayload(cp agentControlPlaneView) map[string]any {
	return map[string]any{
		"api_base":   cp.APIBase,
		"agent_uuid": cp.AgentUUID,
		"agent_id":   cp.AgentID,
		"org_id":     cp.OrgID,
		"owner_human_id": func() any {
			if cp.OwnerHumanID == "" {
				return nil
			}
			return cp.OwnerHumanID
		}(),
		"can_talk_to":     cp.CanTalkTo,
		"capabilities":    cp.Capabilities,
		"can_communicate": len(cp.CanTalkTo) > 0,
		"endpoints": map[string]string{
			"publish":      cp.APIBase + "/messages/publish",
			"pull":         cp.APIBase + "/messages/pull",
			"profile":      cp.APIBase + "/agents/me",
			"capabilities": cp.APIBase + "/agents/me/capabilities",
			"skill":        cp.APIBase + "/agents/me/skill",
		},
	}
}

func apiBaseURL(r *http.Request) string {
	scheme := "http"
	if forwarded := strings.TrimSpace(r.Header.Get("X-Forwarded-Proto")); forwarded != "" {
		parts := strings.Split(forwarded, ",")
		candidate := strings.ToLower(strings.TrimSpace(parts[0]))
		if candidate == "http" || candidate == "https" {
			scheme = candidate
		}
	} else if r.TLS != nil {
		scheme = "https"
	}

	host := strings.TrimSpace(r.Host)
	if host == "" {
		host = "localhost:8080"
	}
	return fmt.Sprintf("%s://%s/v1", scheme, host)
}

func wantsMarkdownSkill(r *http.Request) bool {
	format := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("format")))
	if format == "md" || format == "markdown" {
		return true
	}
	accept := strings.ToLower(r.Header.Get("Accept"))
	return strings.Contains(accept, "text/markdown")
}

func buildAgentSkillMarkdown(cp agentControlPlaneView) string {
	var b strings.Builder
	b.WriteString("# SKILL: Statocyst Agent Control Plane\n\n")
	b.WriteString("## Connected To\n")
	b.WriteString("- Service: Statocyst\n")
	b.WriteString("- API Base: " + cp.APIBase + "\n")
	b.WriteString("- Agent UUID: " + cp.AgentUUID + "\n")
	b.WriteString("- Agent ID: " + cp.AgentID + "\n")
	b.WriteString("- Organization ID: " + cp.OrgID + "\n")
	if cp.OwnerHumanID != "" {
		b.WriteString("- Owner Human ID: " + cp.OwnerHumanID + "\n")
	}
	b.WriteString("\n## What You Can Do\n")
	b.WriteString("- Pull inbound messages.\n")
	b.WriteString("- Publish outbound messages to trusted peers.\n")
	b.WriteString("- Discover capabilities and communication graph.\n")
	b.WriteString("- Retrieve this skill doc anytime.\n")

	b.WriteString("\n## Communication Graph\n")
	if len(cp.CanTalkTo) == 0 {
		b.WriteString("- No active talk paths yet. You are connected, but cannot deliver messages until bonded.\n")
	} else {
		b.WriteString("- You can currently talk to:\n")
		for _, peer := range cp.CanTalkTo {
			b.WriteString("  - " + peer + "\n")
		}
	}

	b.WriteString("\n## API Quickstart\n")
	b.WriteString("```bash\n")
	b.WriteString("export STATOCYST_AGENT_TOKEN=\"<AGENT_TOKEN_FROM_BIND_RESPONSE>\"\n")
	b.WriteString("curl -sS \"" + cp.APIBase + "/agents/me/capabilities\" \\\n")
	b.WriteString("  -H \"Authorization: Bearer $STATOCYST_AGENT_TOKEN\"\n\n")
	b.WriteString("curl -sS \"" + cp.APIBase + "/messages/pull?timeout_ms=5000\" \\\n")
	b.WriteString("  -H \"Authorization: Bearer $STATOCYST_AGENT_TOKEN\"\n\n")
	b.WriteString("curl -sS -X POST \"" + cp.APIBase + "/messages/publish\" \\\n")
	b.WriteString("  -H \"Authorization: Bearer $STATOCYST_AGENT_TOKEN\" \\\n")
	b.WriteString("  -H \"Content-Type: application/json\" \\\n")
	b.WriteString("  -d '{\"to_agent_uuid\":\"<peer_agent_uuid>\",\"content_type\":\"text/plain\",\"payload\":\"hello\"}'\n")
	b.WriteString("```\n")

	return b.String()
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
			writeError(w, http.StatusForbidden, "forbidden", "super admin required")
			return
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"snapshot": h.control.AdminSnapshot(),
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
		"organization": org,
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
		writeError(w, http.StatusNotFound, "not_found", "route not found")
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
				writeError(w, http.StatusForbidden, "forbidden", "role owner/admin required")
			default:
				writeError(w, http.StatusInternalServerError, "store_error", "failed to update organization metadata")
			}
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"organization": org})
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
					writeError(w, http.StatusForbidden, "forbidden", "role owner/admin required")
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
					writeError(w, http.StatusForbidden, "forbidden", "role owner/admin required")
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
				"invite":      invite,
				"invite_code": inviteCode,
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
						writeError(w, http.StatusForbidden, "forbidden", "role owner/admin required")
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
						writeError(w, http.StatusForbidden, "forbidden", "role owner/admin required")
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
					writeError(w, http.StatusForbidden, "forbidden", "role owner/admin required")
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
			writeJSON(w, http.StatusOK, map[string]any{"humans": humans})
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
					writeError(w, http.StatusForbidden, "forbidden", "owner/admin required")
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
		writeJSON(w, http.StatusOK, map[string]any{"agents": agents})
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
		"organization": org,
		"access_key":   key,
		"humans":       humans,
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
		"organization": org,
		"access_key":   key,
		"agents":       agents,
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
		writeJSON(w, http.StatusOK, map[string]any{"invites": invites})
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
		var req redeemInviteCodeRequest
		if err := decodeJSON(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_request", "invalid JSON request")
			return
		}
		inviteCode := strings.TrimSpace(req.InviteCode)
		if inviteCode == "" {
			writeError(w, http.StatusBadRequest, "invalid_invite_code", "invite_code is required")
			return
		}
		membership, err := h.control.AcceptInviteBySecretHash(auth.HashToken(inviteCode), actor.Human.HumanID, actor.Human.Email, h.now().UTC(), h.idFactory)
		if err != nil {
			switch {
			case errors.Is(err, store.ErrInviteNotFound):
				writeError(w, http.StatusNotFound, "unknown_invite_code", "invite_code is not registered")
			case errors.Is(err, store.ErrInviteInvalid):
				writeError(w, http.StatusBadRequest, "invalid_invite_code", "invite code cannot be redeemed by this user")
			default:
				writeError(w, http.StatusInternalServerError, "store_error", "failed to redeem invite code")
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
				writeError(w, http.StatusForbidden, "forbidden", "invite recipient or org admin required")
			default:
				writeError(w, http.StatusInternalServerError, "store_error", "failed to revoke invite")
			}
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"invite": invite})
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
	var req redeemBindTokenRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid JSON request")
		return
	}
	req.BindToken = strings.TrimSpace(req.BindToken)
	req.AgentID = normalizeHandle(req.AgentID)
	if req.BindToken == "" {
		writeError(w, http.StatusBadRequest, "invalid_bind_token", "bind_token is required")
		return
	}
	if req.AgentID != "" && !validateAgentID(req.AgentID) {
		writeError(w, http.StatusBadRequest, "invalid_agent_id", "agent_id must be 2-64 chars, URL-safe (a-z, 0-9, ., _, -), and not blocked")
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
	agentID := req.AgentID
	for attempt := 0; attempt < 8; attempt++ {
		if agentID == "" {
			var generated string
			generated, err = h.generateDefaultAgentID()
			if err != nil {
				writeError(w, http.StatusInternalServerError, "id_generation_failed", "failed to generate agent_id")
				return
			}
			agentID = generated
		}

		_, err = h.control.RedeemBindToken(bindTokenHash, agentID, auth.HashToken(agentToken), h.now().UTC())
		if err == nil {
			writeJSON(w, http.StatusCreated, map[string]any{
				"token": agentToken,
			})
			return
		}
		if errors.Is(err, store.ErrAgentExists) && req.AgentID == "" {
			agentID = ""
			continue
		}
		switch {
		case errors.Is(err, store.ErrBindNotFound):
			writeError(w, http.StatusNotFound, "bind_not_found", "bind token not found")
		case errors.Is(err, store.ErrBindExpired):
			writeError(w, http.StatusBadRequest, "bind_expired", "bind token has expired")
		case errors.Is(err, store.ErrBindUsed):
			writeError(w, http.StatusConflict, "bind_used", "bind token already used")
		case errors.Is(err, store.ErrAgentExists):
			writeError(w, http.StatusConflict, "agent_exists", "agent_id already registered")
		case errors.Is(err, store.ErrInvalidHandle):
			writeError(w, http.StatusBadRequest, "invalid_agent_id", "agent_id must be 2-64 chars, URL-safe (a-z, 0-9, ., _, -), and not blocked")
		case errors.Is(err, store.ErrMembershipNotFound):
			writeError(w, http.StatusBadRequest, "invalid_owner_human_id", "owner_human_id is no longer active in org")
		default:
			writeError(w, http.StatusInternalServerError, "store_error", "failed to redeem bind token")
		}
		return
	}
	writeError(w, http.StatusConflict, "agent_id_generation_failed", "failed to allocate a unique agent_id")
}

func (h *Handler) generateDefaultAgentID() (string, error) {
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
	agentID := "agent-" + clean
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
	if action != "bind" && !validateUUID(agentUUID) {
		writeError(w, http.StatusBadRequest, "invalid_agent_uuid", "agent_uuid must be a valid UUID")
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

	if action == "bind" {
		var req trustAgentRequest
		if err := decodeJSON(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_request", "invalid JSON request")
			return
		}
		if validateUUID(normalizeUUID(agentRef)) {
			req.AgentUUID = agentRef
		} else {
			req.AgentID = agentRef
		}
		h.handleAgentTrustCreate(w, actor, req, "owner/admin required for initiating agent")
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
		metadata, err := decodeMetadataUpdateRequest(r)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
			return
		}
		agent, err := h.control.UpdateAgentMetadata(agentUUID, metadata, actor.Human.HumanID, h.now().UTC(), actor.IsSuperAdmin)
		if err != nil {
			switch {
			case errors.Is(err, store.ErrAgentNotFound):
				writeError(w, http.StatusNotFound, "unknown_agent", "agent_uuid is not registered")
			case errors.Is(err, store.ErrUnauthorizedRole):
				writeError(w, http.StatusForbidden, "forbidden", "admin/owner required")
			default:
				writeError(w, http.StatusInternalServerError, "store_error", "failed to update agent metadata")
			}
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"agent": agent,
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
			writeError(w, http.StatusForbidden, "forbidden", "owner/admin required")
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
	h.handleAgentTrustCreate(w, actor, req, "owner/admin required in org")
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
		writeError(w, http.StatusForbidden, "forbidden", "owner/admin required")
	case errors.Is(err, store.ErrAgentNotFound):
		writeError(w, http.StatusNotFound, "unknown_agent", "agent not found")
	default:
		writeError(w, http.StatusInternalServerError, "store_error", "failed trust operation")
	}
}
