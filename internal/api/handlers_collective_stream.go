package api

import (
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"

	"moltenhub/internal/model"
	"moltenhub/internal/store"
)

const collectiveStreamEventType = "collective_event"

type collectiveStreamEvent struct {
	Type          string         `json:"type"`
	EventID       string         `json:"event_id"`
	At            time.Time      `json:"at"`
	Category      string         `json:"category"`
	Action        string         `json:"action"`
	AgentUUID     string         `json:"agent_uuid,omitempty"`
	PeerAgentUUID string         `json:"peer_agent_uuid,omitempty"`
	OrgID         string         `json:"org_id,omitempty"`
	PeerOrgID     string         `json:"peer_org_id,omitempty"`
	MessageID     string         `json:"message_id,omitempty"`
	DeliveryID    string         `json:"delivery_id,omitempty"`
	Details       map[string]any `json:"details,omitempty"`
}

type collectiveStreamSub struct {
	ch     chan collectiveStreamEvent
	filter func(collectiveStreamEvent) bool
}

type collectiveStreamHub struct {
	mu     sync.RWMutex
	nextID uint64
	subs   map[uint64]collectiveStreamSub
}

func newCollectiveStreamHub() *collectiveStreamHub {
	return &collectiveStreamHub{subs: map[uint64]collectiveStreamSub{}}
}

func (hub *collectiveStreamHub) subscribe(filter func(collectiveStreamEvent) bool) (uint64, <-chan collectiveStreamEvent, func()) {
	hub.mu.Lock()
	defer hub.mu.Unlock()
	hub.nextID++
	id := hub.nextID
	ch := make(chan collectiveStreamEvent, 64)
	hub.subs[id] = collectiveStreamSub{ch: ch, filter: filter}
	cancel := func() {
		hub.mu.Lock()
		if sub, ok := hub.subs[id]; ok {
			delete(hub.subs, id)
			close(sub.ch)
		}
		hub.mu.Unlock()
	}
	return id, ch, cancel
}

func (hub *collectiveStreamHub) publish(event collectiveStreamEvent) {
	if hub == nil {
		return
	}
	event.Type = collectiveStreamEventType
	if event.At.IsZero() {
		event.At = time.Now().UTC()
	}
	if event.EventID == "" {
		event.EventID = event.At.Format("20060102T150405.000000000Z07:00") + ":" + strings.TrimSpace(event.Category) + ":" + strings.TrimSpace(event.Action)
	}
	hub.mu.RLock()
	defer hub.mu.RUnlock()
	for _, sub := range hub.subs {
		if sub.filter != nil && !sub.filter(event) {
			continue
		}
		select {
		case sub.ch <- event:
		default:
		}
	}
}

func (h *Handler) handleCollectiveStream(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeMethodNotAllowed(w)
		return
	}
	filter, viewer, err := h.authorizeCollectiveStream(r)
	if err != nil {
		writeCollectiveStreamAuthError(w, err)
		return
	}

	conn, err := openClawWSUpgrader.Upgrade(openClawWSResponseWriter{ResponseWriter: w}, r, nil)
	if err != nil {
		return
	}
	defer conn.Close()

	_, events, cancel := h.collective.subscribe(filter)
	defer cancel()

	_ = conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
	if err := conn.WriteJSON(map[string]any{
		"type":   "session_ready",
		"viewer": viewer,
	}); err != nil {
		return
	}

	for {
		select {
		case <-r.Context().Done():
			return
		case event, ok := <-events:
			if !ok {
				return
			}
			_ = conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if err := conn.WriteJSON(event); err != nil {
				return
			}
		}
	}
}

func (h *Handler) authorizeCollectiveStream(r *http.Request) (func(collectiveStreamEvent) bool, map[string]any, error) {
	r = requestWithCollectiveStreamBearer(r)
	if agentUUID, err := h.authenticateAgent(r); err == nil {
		return h.authorizeAgentCollectiveStream(agentUUID, r)
	}
	actor, err := h.authenticateHuman(r)
	if err != nil {
		return nil, nil, err
	}
	return h.authorizeHumanCollectiveStream(actor, r)
}

func requestWithCollectiveStreamBearer(r *http.Request) *http.Request {
	if strings.TrimSpace(r.Header.Get("Authorization")) != "" {
		return r
	}

	token := strings.TrimSpace(r.URL.Query().Get("access_token"))
	if token == "" {
		token = strings.TrimSpace(r.URL.Query().Get("token"))
	}
	if token == "" {
		return r
	}

	clone := r.Clone(r.Context())
	clone.Header = r.Header.Clone()
	clone.Header.Set("Authorization", "Bearer "+token)
	return clone
}

func (h *Handler) authorizeAgentCollectiveStream(agentUUID string, r *http.Request) (func(collectiveStreamEvent) bool, map[string]any, error) {
	scope := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("scope")))
	targetAgentUUID := normalizeUUID(r.URL.Query().Get("agent_uuid"))
	if scope == "org" {
		return nil, nil, store.ErrUnauthorizedRole
	}
	if targetAgentUUID != "" {
		if !validateUUID(targetAgentUUID) {
			return nil, nil, errInvalidCollectiveTarget
		}
		if !h.agentCanViewCollectiveAgent(agentUUID, targetAgentUUID) {
			return nil, nil, store.ErrUnauthorizedRole
		}
		return collectiveAgentFilter(map[string]struct{}{targetAgentUUID: {}}), map[string]any{
			"kind":       "agent",
			"agent_uuid": agentUUID,
			"scope":      "agent",
			"target":     targetAgentUUID,
		}, nil
	}
	return func(event collectiveStreamEvent) bool {
			for _, candidate := range []string{event.AgentUUID, event.PeerAgentUUID} {
				if h.agentCanViewCollectiveAgent(agentUUID, candidate) {
					return true
				}
			}
			return false
		}, map[string]any{
			"kind":       "agent",
			"agent_uuid": agentUUID,
			"scope":      "peers",
		}, nil
}

func (h *Handler) authorizeHumanCollectiveStream(actor humanActor, r *http.Request) (func(collectiveStreamEvent) bool, map[string]any, error) {
	scope := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("scope")))
	orgID := strings.TrimSpace(r.URL.Query().Get("org_id"))
	targetAgentUUID := normalizeUUID(r.URL.Query().Get("agent_uuid"))
	if scope == "org" || orgID != "" {
		if orgID == "" {
			return nil, nil, errInvalidCollectiveTarget
		}
		if !actor.IsSuperAdmin && !h.humanOwnsOrg(actor.Human.HumanID, orgID) {
			return nil, nil, store.ErrUnauthorizedRole
		}
		return func(event collectiveStreamEvent) bool {
				return event.OrgID == orgID || event.PeerOrgID == orgID
			}, map[string]any{
				"kind":     "human",
				"human_id": actor.Human.HumanID,
				"scope":    "org",
				"org_id":   orgID,
			}, nil
	}
	if targetAgentUUID != "" {
		if !validateUUID(targetAgentUUID) {
			return nil, nil, errInvalidCollectiveTarget
		}
		if !actor.IsSuperAdmin && !h.humanCanManageAgent(actor.Human.HumanID, targetAgentUUID) {
			return nil, nil, store.ErrUnauthorizedRole
		}
		return collectiveAgentFilter(map[string]struct{}{targetAgentUUID: {}}), map[string]any{
			"kind":       "human",
			"human_id":   actor.Human.HumanID,
			"scope":      "agent",
			"agent_uuid": targetAgentUUID,
		}, nil
	}
	agents := h.control.ListHumanAgents(actor.Human.HumanID)
	allowed := map[string]struct{}{}
	for _, agent := range agents {
		allowed[agent.AgentUUID] = struct{}{}
	}
	if actor.IsSuperAdmin {
		return func(collectiveStreamEvent) bool { return true }, map[string]any{
			"kind":     "human",
			"human_id": actor.Human.HumanID,
			"scope":    "all",
		}, nil
	}
	return collectiveAgentFilter(allowed), map[string]any{
		"kind":     "human",
		"human_id": actor.Human.HumanID,
		"scope":    "self",
	}, nil
}

var errInvalidCollectiveTarget = errors.New("invalid collective stream target")

func writeCollectiveStreamAuthError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, errInvalidCollectiveTarget):
		writeError(w, http.StatusBadRequest, "invalid_target", "scope target is invalid")
	case errors.Is(err, store.ErrUnauthorizedRole):
		writeError(w, http.StatusForbidden, "forbidden", "stream scope is not visible to this caller")
	default:
		writeError(w, http.StatusUnauthorized, "unauthorized", "missing or invalid auth")
	}
}

func (h *Handler) humanOwnsOrg(humanID, orgID string) bool {
	for _, membership := range h.control.ListMyMemberships(humanID) {
		if membership.Membership.OrgID == orgID && membership.Membership.Role == model.RoleOwner && membership.Membership.Status == model.StatusActive {
			return true
		}
	}
	return false
}

func (h *Handler) agentCanViewCollectiveAgent(viewerAgentUUID, targetAgentUUID string) bool {
	viewerAgentUUID = strings.TrimSpace(viewerAgentUUID)
	targetAgentUUID = strings.TrimSpace(targetAgentUUID)
	if viewerAgentUUID == "" || targetAgentUUID == "" {
		return false
	}
	if viewerAgentUUID == targetAgentUUID {
		return true
	}
	if _, _, err := h.control.CanPublish(viewerAgentUUID, targetAgentUUID); err == nil {
		return true
	}
	if _, _, err := h.control.CanPublish(targetAgentUUID, viewerAgentUUID); err == nil {
		return true
	}
	return false
}

func collectiveAgentFilter(allowed map[string]struct{}) func(collectiveStreamEvent) bool {
	return func(event collectiveStreamEvent) bool {
		if len(allowed) == 0 {
			return false
		}
		if _, ok := allowed[event.AgentUUID]; ok {
			return true
		}
		if _, ok := allowed[event.PeerAgentUUID]; ok {
			return true
		}
		return false
	}
}

func (h *Handler) publishCollectiveEvent(event collectiveStreamEvent) {
	if h.collective == nil {
		return
	}
	h.collective.publish(event)
}
