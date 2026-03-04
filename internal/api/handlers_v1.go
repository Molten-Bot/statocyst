package api

import (
	"errors"
	"net/http"
	"os"
	"strings"
	"time"

	"statocyst/internal/auth"
	"statocyst/internal/model"
	"statocyst/internal/store"
)

type createOrgRequest struct {
	Name string `json:"name"`
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
	AgentID   string `json:"agent_id"`
}

type registerAgentRequest struct {
	OrgID        string  `json:"org_id"`
	AgentID      string  `json:"agent_id"`
	OwnerHumanID *string `json:"owner_human_id,omitempty"`
}

type trustOrgRequest struct {
	OrgID     string `json:"org_id"`
	PeerOrgID string `json:"peer_org_id"`
}

type trustAgentRequest struct {
	OrgID       string `json:"org_id"`
	AgentID     string `json:"agent_id"`
	PeerAgentID string `json:"peer_agent_id"`
}

type createOrgAccessKeyRequest struct {
	Label         string   `json:"label"`
	Scopes        []string `json:"scopes"`
	ExpiresInDays *int     `json:"expires_in_days,omitempty"`
}

var (
	errMissingOrgName      = errors.New("missing_org_name")
	errMissingOrgAccessKey = errors.New("missing_org_access_key")
)

const (
	defaultInviteExpiryDays = 7
	maxInviteExpiryDays     = 365
	defaultUIAppName        = "Statocyst"
)

func (h *Handler) handleUIConfig(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeMethodNotAllowed(w)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"app_name":                 uiAppName(),
		"human_auth_provider":      h.humanAuth.Name(),
		"supabase_url":             h.supabaseURL,
		"supabase_anon_key":        h.supabaseAnonKey,
		"dev_human_id":             strings.TrimSpace(os.Getenv("DEV_LOGIN_HUMAN_ID")),
		"dev_human_email":          strings.TrimSpace(strings.ToLower(os.Getenv("DEV_LOGIN_HUMAN_EMAIL"))),
		"dev_auto_login":           strings.EqualFold(strings.TrimSpace(os.Getenv("DEV_LOGIN_AUTO")), "true"),
		"super_admin_emails":       setToSortedSlice(h.superAdminEmails),
		"super_admin_domains":      setToSortedSlice(h.superAdminDomains),
		"super_admin_review_mode":  h.superAdminReview,
		"super_admin_write_policy": "review_mode_read_only",
		"bind_token_ttl_sec":       int(h.bindTokenTTL.Seconds()),
	})
}

func uiAppName() string {
	name := strings.TrimSpace(os.Getenv("STATOCYST_APP_NAME"))
	if name == "" {
		return defaultUIAppName
	}
	return name
}

func (h *Handler) handleMe(w http.ResponseWriter, r *http.Request) {
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
		"human":          actor.Human,
		"is_super_admin": actor.IsSuperAdmin,
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
		"memberships": h.store.ListMyMemberships(actor.Human.HumanID),
	})
}

func (h *Handler) handleAdminSnapshot(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeMethodNotAllowed(w)
		return
	}
	actor, err := h.authenticateHuman(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "unauthorized", "missing or invalid human auth")
		return
	}
	if !actor.IsSuperAdmin {
		writeError(w, http.StatusForbidden, "forbidden", "super admin required")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"snapshot": h.store.AdminSnapshot(),
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
	if h.denySuperAdminWrite(w, actor) {
		return
	}

	var req createOrgRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid JSON request")
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		writeError(w, http.StatusBadRequest, "invalid_name", "name is required")
		return
	}
	orgID, err := h.idFactory()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "id_generation_failed", "failed to generate org_id")
		return
	}
	org, membership, err := h.store.CreateOrg(name, actor.Human.HumanID, orgID, h.now().UTC())
	if err != nil {
		switch {
		case errors.Is(err, store.ErrOrgNameTaken):
			writeError(w, http.StatusConflict, "org_name_exists", "organization name already exists")
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
	if len(parts) < 4 || parts[0] != "v1" || parts[1] != "orgs" {
		writeError(w, http.StatusNotFound, "not_found", "route not found")
		return
	}
	orgID := parts[2]
	sub := parts[3]
	actor, err := h.authenticateHuman(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "unauthorized", "missing or invalid human auth")
		return
	}

	switch sub {
	case "invites":
		if len(parts) != 4 {
			writeError(w, http.StatusNotFound, "not_found", "route not found")
			return
		}
		switch r.Method {
		case http.MethodGet:
			invites, err := h.store.ListOrgInvites(orgID, actor.Human.HumanID, actor.IsSuperAdmin)
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
			if h.denySuperAdminWrite(w, actor) {
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
			invite, err := h.store.CreateInvite(
				orgID,
				email,
				role,
				actor.Human.HumanID,
				inviteID,
				auth.HashToken(inviteCode),
				expiresAt,
				now,
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
				keys, err := h.store.ListOrgAccessKeys(orgID, actor.Human.HumanID, actor.IsSuperAdmin)
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
				if h.denySuperAdminWrite(w, actor) {
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
				key, err := h.store.CreateOrgAccessKey(
					orgID,
					req.Label,
					req.Scopes,
					expiresAt,
					actor.Human.HumanID,
					keyID,
					auth.HashToken(keySecret),
					h.now().UTC(),
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
			if h.denySuperAdminWrite(w, actor) {
				return
			}
			keyID := parts[4]
			key, err := h.store.RevokeOrgAccessKey(orgID, keyID, actor.Human.HumanID, actor.IsSuperAdmin, h.now().UTC())
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
			humans, err := h.store.ListOrgHumans(orgID, actor.Human.HumanID, actor.IsSuperAdmin)
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
			if h.denySuperAdminWrite(w, actor) {
				return
			}
			targetHumanID := strings.TrimSpace(parts[4])
			if targetHumanID == "" {
				writeError(w, http.StatusBadRequest, "invalid_human_id", "human_id is required")
				return
			}
			membership, err := h.store.RevokeMembership(orgID, targetHumanID, actor.Human.HumanID, actor.IsSuperAdmin, h.now().UTC())
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
		agents, err := h.store.ListOrgAgents(orgID, actor.Human.HumanID, actor.IsSuperAdmin)
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
		orgEdges, agentEdges, err := h.store.ListOrgTrustGraph(orgID, actor.Human.HumanID, actor.IsSuperAdmin)
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
		events, err := h.store.ListAudit(orgID, actor.Human.HumanID, actor.IsSuperAdmin)
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
		stats, err := h.store.GetOrgStats(orgID, actor.Human.HumanID, actor.IsSuperAdmin)
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
	humans, err := h.store.ListOrgHumans(org.OrgID, "", true)
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
	agents, err := h.store.ListOrgAgents(org.OrgID, "", true)
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
	orgName := strings.TrimSpace(r.URL.Query().Get("org_name"))
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
	return h.store.AuthorizeOrgAccessByName(orgName, auth.HashToken(secret), requiredScope, h.now().UTC())
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
		invites := h.store.ListInvitesForHuman(actor.Human.HumanID, actor.Human.Email, actor.IsSuperAdmin)
		writeJSON(w, http.StatusOK, map[string]any{"invites": invites})
		return
	}

	if len(parts) == 3 && parts[2] == "redeem" {
		if r.Method != http.MethodPost {
			writeMethodNotAllowed(w)
			return
		}
		if h.denySuperAdminWrite(w, actor) {
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
		membership, err := h.store.AcceptInviteBySecretHash(auth.HashToken(inviteCode), actor.Human.HumanID, actor.Human.Email, h.now().UTC(), h.idFactory)
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
		if h.denySuperAdminWrite(w, actor) {
			return
		}
		inviteID := parts[2]
		membership, err := h.store.AcceptInvite(inviteID, actor.Human.HumanID, actor.Human.Email, h.now().UTC(), h.idFactory)
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
		if h.denySuperAdminWrite(w, actor) {
			return
		}
		inviteID := parts[2]
		invite, err := h.store.RevokeInvite(inviteID, actor.Human.HumanID, actor.Human.Email, actor.IsSuperAdmin, h.now().UTC())
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
	if h.denySuperAdminWrite(w, actor) {
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
	bind, err := h.store.CreateBindToken(req.OrgID, req.OwnerHumanID, actor.Human.HumanID, bindID, auth.HashToken(bindSecret), expiresAt, h.now().UTC())
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
	req.AgentID = strings.TrimSpace(req.AgentID)
	if req.BindToken == "" {
		writeError(w, http.StatusBadRequest, "invalid_bind_token", "bind_token is required")
		return
	}
	if !validateAgentID(req.AgentID) {
		writeError(w, http.StatusBadRequest, "invalid_agent_id", "agent_id must match [A-Za-z0-9._:-]{1,128}")
		return
	}

	agentToken, err := auth.GenerateToken()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "token_generation_failed", "failed to generate agent token")
		return
	}
	agent, err := h.store.RedeemBindToken(auth.HashToken(req.BindToken), req.AgentID, auth.HashToken(agentToken), h.now().UTC())
	if err != nil {
		switch {
		case errors.Is(err, store.ErrBindNotFound):
			writeError(w, http.StatusNotFound, "bind_not_found", "bind token not found")
		case errors.Is(err, store.ErrBindExpired):
			writeError(w, http.StatusBadRequest, "bind_expired", "bind token has expired")
		case errors.Is(err, store.ErrBindUsed):
			writeError(w, http.StatusConflict, "bind_used", "bind token already used")
		case errors.Is(err, store.ErrAgentExists):
			writeError(w, http.StatusConflict, "agent_exists", "agent_id already registered")
		case errors.Is(err, store.ErrMembershipNotFound):
			writeError(w, http.StatusBadRequest, "invalid_owner_human_id", "owner_human_id is no longer active in org")
		default:
			writeError(w, http.StatusInternalServerError, "store_error", "failed to redeem bind token")
		}
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"status":         "ok",
		"agent_id":       agent.AgentID,
		"org_id":         agent.OrgID,
		"owner_human_id": agent.OwnerHumanID,
		"token":          agentToken,
	})
}

func (h *Handler) handleRegisterAgent(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeMethodNotAllowed(w)
		return
	}
	actor, err := h.authenticateHuman(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "unauthorized", "missing or invalid human auth")
		return
	}
	if h.denySuperAdminWrite(w, actor) {
		return
	}

	var req registerAgentRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid JSON request")
		return
	}

	req.OrgID = strings.TrimSpace(req.OrgID)
	req.AgentID = strings.TrimSpace(req.AgentID)
	if req.OrgID == "" {
		writeError(w, http.StatusBadRequest, "invalid_org_id", "org_id is required")
		return
	}
	if !validateAgentID(req.AgentID) {
		writeError(w, http.StatusBadRequest, "invalid_agent_id", "agent_id must match [A-Za-z0-9._:-]{1,128}")
		return
	}
	token, err := auth.GenerateToken()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "token_generation_failed", "failed to generate token")
		return
	}
	agent, err := h.store.RegisterAgent(req.OrgID, req.AgentID, req.OwnerHumanID, auth.HashToken(token), actor.Human.HumanID, h.now().UTC())
	if err != nil {
		switch {
		case errors.Is(err, store.ErrOrgNotFound):
			writeError(w, http.StatusNotFound, "unknown_org", "org_id is not registered")
		case errors.Is(err, store.ErrUnauthorizedRole):
			writeError(w, http.StatusForbidden, "forbidden", "role member/admin/owner required")
		case errors.Is(err, store.ErrMembershipNotFound):
			writeError(w, http.StatusBadRequest, "invalid_owner_human_id", "owner_human_id must be active in org")
		case errors.Is(err, store.ErrAgentExists):
			writeError(w, http.StatusConflict, "agent_exists", "agent_id already registered")
		default:
			writeError(w, http.StatusInternalServerError, "store_error", "failed to register agent")
		}
		return
	}

	writeJSON(w, http.StatusCreated, map[string]any{
		"agent_id":       agent.AgentID,
		"org_id":         agent.OrgID,
		"owner_human_id": agent.OwnerHumanID,
		"token":          token,
		"status":         agent.Status,
	})
}

func (h *Handler) handleAgentsSubroutes(w http.ResponseWriter, r *http.Request) {
	parts := splitPath(r.URL.Path)
	if len(parts) < 3 || parts[0] != "v1" || parts[1] != "agents" {
		writeError(w, http.StatusNotFound, "not_found", "route not found")
		return
	}
	agentID := parts[2]
	actor, err := h.authenticateHuman(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "unauthorized", "missing or invalid human auth")
		return
	}
	if h.denySuperAdminWrite(w, actor) {
		return
	}

	if len(parts) == 4 && parts[3] == "rotate-token" {
		if r.Method != http.MethodPost {
			writeMethodNotAllowed(w)
			return
		}
		token, err := auth.GenerateToken()
		if err != nil {
			writeError(w, http.StatusInternalServerError, "token_generation_failed", "failed to generate token")
			return
		}
		if err := h.store.RotateAgentToken(agentID, actor.Human.HumanID, auth.HashToken(token), h.now().UTC()); err != nil {
			switch {
			case errors.Is(err, store.ErrAgentNotFound):
				writeError(w, http.StatusNotFound, "unknown_agent", "agent_id is not registered")
			case errors.Is(err, store.ErrUnauthorizedRole):
				writeError(w, http.StatusForbidden, "forbidden", "admin/owner required")
			default:
				writeError(w, http.StatusInternalServerError, "store_error", "failed to rotate token")
			}
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"status":   "ok",
			"agent_id": agentID,
			"token":    token,
		})
		return
	}

	if len(parts) == 3 {
		if r.Method != http.MethodDelete {
			writeMethodNotAllowed(w)
			return
		}
		if err := h.store.RevokeAgent(agentID, actor.Human.HumanID, h.now().UTC()); err != nil {
			switch {
			case errors.Is(err, store.ErrAgentNotFound):
				writeError(w, http.StatusNotFound, "unknown_agent", "agent_id is not registered")
			case errors.Is(err, store.ErrUnauthorizedRole):
				writeError(w, http.StatusForbidden, "forbidden", "admin/owner required")
			default:
				writeError(w, http.StatusInternalServerError, "store_error", "failed to revoke agent")
			}
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"status":   "ok",
			"agent_id": agentID,
			"result":   "revoked",
		})
		return
	}

	writeError(w, http.StatusNotFound, "not_found", "route not found")
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
	if h.denySuperAdminWrite(w, actor) {
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
	edge, created, err := h.store.CreateOrJoinOrgTrust(req.OrgID, req.PeerOrgID, actor.Human.HumanID, edgeID, h.now().UTC())
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
	if h.denySuperAdminWrite(w, actor) {
		return
	}

	if len(parts) == 4 {
		switch parts[3] {
		case "approve":
			if r.Method != http.MethodPost {
				writeMethodNotAllowed(w)
				return
			}
			edge, err := h.store.ApproveOrgTrust(edgeID, actor.Human.HumanID, h.now().UTC())
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
			edge, err := h.store.BlockOrgTrust(edgeID, actor.Human.HumanID, h.now().UTC())
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
		edge, err := h.store.RevokeOrgTrust(edgeID, actor.Human.HumanID, h.now().UTC())
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
	if h.denySuperAdminWrite(w, actor) {
		return
	}
	var req trustAgentRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid JSON request")
		return
	}
	req.OrgID = strings.TrimSpace(req.OrgID)
	req.AgentID = strings.TrimSpace(req.AgentID)
	req.PeerAgentID = strings.TrimSpace(req.PeerAgentID)
	if !validateAgentID(req.AgentID) || !validateAgentID(req.PeerAgentID) {
		writeError(w, http.StatusBadRequest, "invalid_agent_id", "agent ids must match [A-Za-z0-9._:-]{1,128}")
		return
	}
	edgeID, err := h.idFactory()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "id_generation_failed", "failed to generate edge_id")
		return
	}
	edge, created, err := h.store.CreateOrJoinAgentTrust(req.OrgID, req.AgentID, req.PeerAgentID, actor.Human.HumanID, edgeID, h.now().UTC())
	if err != nil {
		switch {
		case errors.Is(err, store.ErrAgentNotFound):
			writeError(w, http.StatusNotFound, "unknown_agent", "agent_id or peer_agent_id is not registered")
		case errors.Is(err, store.ErrUnauthorizedRole):
			writeError(w, http.StatusForbidden, "forbidden", "owner/admin required in org")
		case errors.Is(err, store.ErrSelfTrust):
			writeError(w, http.StatusBadRequest, "invalid_peer_agent_id", "peer_agent_id cannot equal agent_id")
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
	if h.denySuperAdminWrite(w, actor) {
		return
	}

	if len(parts) == 4 {
		switch parts[3] {
		case "approve":
			if r.Method != http.MethodPost {
				writeMethodNotAllowed(w)
				return
			}
			edge, err := h.store.ApproveAgentTrust(edgeID, actor.Human.HumanID, h.now().UTC())
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
			edge, err := h.store.BlockAgentTrust(edgeID, actor.Human.HumanID, h.now().UTC())
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
		edge, err := h.store.RevokeAgentTrust(edgeID, actor.Human.HumanID, h.now().UTC())
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
