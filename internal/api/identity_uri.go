package api

import (
	"net/url"
	"strings"

	"moltenhub/internal/model"
)

const defaultCanonicalScheme = "https"

func normalizeCanonicalBaseURL(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return ""
	}
	if !strings.Contains(trimmed, "://") {
		trimmed = defaultCanonicalScheme + "://" + trimmed
	}
	parsed, err := url.Parse(trimmed)
	if err != nil {
		return ""
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return ""
	}
	if strings.TrimSpace(parsed.Host) == "" {
		return ""
	}
	parsed.Path = ""
	parsed.RawPath = ""
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return strings.TrimRight(parsed.String(), "/")
}

func buildCanonicalEntityPathURI(baseURL, path string) string {
	baseURL = normalizeCanonicalBaseURL(baseURL)
	path = strings.TrimSpace(path)
	if baseURL == "" || path == "" {
		return ""
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	return baseURL + path
}

func escapePathSegments(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	parts := strings.Split(value, "/")
	for index, part := range parts {
		parts[index] = url.PathEscape(part)
	}
	return strings.Join(parts, "/")
}

func (h *Handler) organizationURI(org model.Organization) string {
	return buildCanonicalEntityPathURI(h.canonicalBaseURL, "/orgs/"+url.PathEscape(strings.TrimSpace(org.Handle)))
}

func (h *Handler) humanURI(human model.Human) string {
	return buildCanonicalEntityPathURI(h.canonicalBaseURL, "/humans/"+url.PathEscape(strings.TrimSpace(human.Handle)))
}

func (h *Handler) agentURI(agent model.Agent) string {
	return buildCanonicalEntityPathURI(h.canonicalBaseURL, "/"+escapePathSegments(agent.AgentID))
}

func (h *Handler) organizationPayload(org model.Organization) map[string]any {
	return map[string]any{
		"org_id":       org.OrgID,
		"handle":       org.Handle,
		"uri":          h.organizationURI(org),
		"display_name": org.DisplayName,
		"metadata":     org.Metadata,
		"created_at":   org.CreatedAt,
		"created_by":   org.CreatedBy,
	}
}

func (h *Handler) humanPayload(human model.Human) map[string]any {
	metadata := humanMetadataForRender(human.Metadata)
	if presence := h.currentHumanPresence(human.HumanID, human.Metadata); presence != nil {
		metadata[model.HumanMetadataKeyPresence] = presence
	}
	return map[string]any{
		"human_id":            human.HumanID,
		"handle":              human.Handle,
		"uri":                 h.humanURI(human),
		"handle_confirmed_at": human.HandleConfirmedAt,
		"auth_provider":       human.AuthProvider,
		"auth_subject":        human.AuthSubject,
		"email":               human.Email,
		"email_verified":      human.EmailVerified,
		"metadata":            metadata,
		"created_at":          human.CreatedAt,
	}
}

func (h *Handler) adminSnapshotHumanPayload(human model.Human) map[string]any {
	metadata := humanMetadataForRender(human.Metadata)
	if presence := h.currentHumanPresence(human.HumanID, human.Metadata); presence != nil {
		metadata[model.HumanMetadataKeyPresence] = presence
	}
	return map[string]any{
		"human_id":            human.HumanID,
		"handle":              human.Handle,
		"uri":                 h.humanURI(human),
		"handle_confirmed_at": human.HandleConfirmedAt,
		"auth_provider":       human.AuthProvider,
		"email_verified":      human.EmailVerified,
		"metadata":            metadata,
		"created_at":          human.CreatedAt,
	}
}

func (h *Handler) membershipWithOrgPayload(item model.MembershipWithOrg) map[string]any {
	return map[string]any{
		"membership": item.Membership,
		"org":        h.organizationPayload(item.Org),
	}
}

func (h *Handler) membershipWithOrgListPayload(items []model.MembershipWithOrg) []map[string]any {
	out := make([]map[string]any, 0, len(items))
	for _, item := range items {
		out = append(out, h.membershipWithOrgPayload(item))
	}
	return out
}

func (h *Handler) inviteWithOrgPayload(item model.InviteWithOrg) map[string]any {
	return map[string]any{
		"invite": item.Invite,
		"org":    h.organizationPayload(item.Org),
	}
}

func (h *Handler) inviteWithOrgListPayload(items []model.InviteWithOrg) []map[string]any {
	out := make([]map[string]any, 0, len(items))
	now := h.now().UTC()
	for _, item := range items {
		status := strings.ToLower(strings.TrimSpace(item.Invite.Status))
		if status == model.StatusExpired || (status == model.StatusPending && !item.Invite.ExpiresAt.IsZero() && now.After(item.Invite.ExpiresAt)) {
			continue
		}
		out = append(out, h.inviteWithOrgPayload(item))
	}
	return out
}

func (h *Handler) orgHumanViewPayload(view model.OrgHumanView) map[string]any {
	metadata := humanMetadataForRender(view.Metadata)
	if presence := h.currentHumanPresence(view.HumanID, view.Metadata); presence != nil {
		metadata[model.HumanMetadataKeyPresence] = presence
	}
	return map[string]any{
		"human_id":      view.HumanID,
		"handle":        view.Handle,
		"uri":           buildCanonicalEntityPathURI(h.canonicalBaseURL, "/humans/"+url.PathEscape(strings.TrimSpace(view.Handle))),
		"email":         view.Email,
		"role":          view.Role,
		"status":        view.Status,
		"auth_provider": view.AuthProvider,
		"metadata":      metadata,
	}
}

func (h *Handler) orgHumanViewListPayload(items []model.OrgHumanView) []map[string]any {
	out := make([]map[string]any, 0, len(items))
	for _, item := range items {
		out = append(out, h.orgHumanViewPayload(item))
	}
	return out
}

func (h *Handler) adminSnapshotPayload(snapshot model.AdminSnapshot) map[string]any {
	organizations := make([]map[string]any, 0, len(snapshot.Organizations))
	for _, org := range snapshot.Organizations {
		organizations = append(organizations, h.organizationPayload(org))
	}

	humans := make([]map[string]any, 0, len(snapshot.Humans))
	for _, human := range snapshot.Humans {
		humans = append(humans, h.adminSnapshotHumanPayload(human))
	}

	agents := make([]map[string]any, 0, len(snapshot.Agents))
	for _, agent := range snapshot.Agents {
		agents = append(agents, h.agentResponsePayload(agent))
	}

	archivedOrganizations := make([]map[string]any, 0, len(snapshot.ArchivedOrganizations))
	for _, org := range snapshot.ArchivedOrganizations {
		archivedOrganizations = append(archivedOrganizations, h.organizationPayload(org))
	}

	archivedHumans := make([]map[string]any, 0, len(snapshot.ArchivedHumans))
	for _, human := range snapshot.ArchivedHumans {
		archivedHumans = append(archivedHumans, h.adminSnapshotHumanPayload(human))
	}

	archivedAgents := make([]map[string]any, 0, len(snapshot.ArchivedAgents))
	for _, agent := range snapshot.ArchivedAgents {
		archivedAgents = append(archivedAgents, h.agentResponsePayload(agent))
	}

	return map[string]any{
		"organizations":          organizations,
		"humans":                 humans,
		"memberships":            snapshot.Memberships,
		"agents":                 agents,
		"archived_organizations": archivedOrganizations,
		"archived_humans":        archivedHumans,
		"archived_agents":        archivedAgents,
		"org_trusts":             snapshot.OrgTrusts,
		"agent_trusts":           snapshot.AgentTrusts,
		"stats":                  snapshot.Stats,
		"message_metrics":        snapshot.MessageMetrics,
		"activity_feed":          snapshot.ActivityFeed,
	}
}
