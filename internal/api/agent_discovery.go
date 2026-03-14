package api

import (
	"fmt"
	"strings"
	"time"

	"statocyst/internal/model"
)

type agentManifest struct {
	SchemaVersion string                    `json:"schema_version"`
	GeneratedAt   string                    `json:"generated_at"`
	Agent         map[string]any            `json:"agent"`
	APIBase       string                    `json:"api_base"`
	Endpoints     map[string]string         `json:"endpoints"`
	Capabilities  []agentCapabilityContract `json:"capabilities"`
	Routes        []agentRouteContract      `json:"routes"`
	Communication map[string]any            `json:"communication"`
}

type agentCapabilityContract struct {
	ID                     string   `json:"id"`
	Title                  string   `json:"title"`
	Description            string   `json:"description"`
	RouteIDs               []string `json:"route_ids"`
	ReadOnly               bool     `json:"read_only"`
	Mutating               bool     `json:"mutating"`
	Retryable              bool     `json:"retryable"`
	TrustStateGated        bool     `json:"trust_state_gated"`
	OperationalConstraints []string `json:"operational_constraints,omitempty"`
}

type agentRouteContract struct {
	ID                   string           `json:"id"`
	Method               string           `json:"method"`
	Path                 string           `json:"path"`
	Auth                 string           `json:"auth"`
	RequestContentTypes  []string         `json:"request_content_types,omitempty"`
	ResponseContentTypes []string         `json:"response_content_types"`
	ReadOnly             bool             `json:"read_only"`
	Mutating             bool             `json:"mutating"`
	Retryable            bool             `json:"retryable"`
	TrustStateGated      bool             `json:"trust_state_gated"`
	Description          string           `json:"description"`
	SuccessExample       map[string]any   `json:"success_example,omitempty"`
	FailureExamples      []map[string]any `json:"failure_examples,omitempty"`
}

func buildAgentManifest(agent model.Agent, cp agentControlPlaneView, now time.Time) agentManifest {
	endpoints := map[string]string{
		"profile":      cp.APIBase + "/agents/me",
		"manifest":     cp.APIBase + "/agents/me/manifest",
		"capabilities": cp.APIBase + "/agents/me/capabilities",
		"skill":        cp.APIBase + "/agents/me/skill",
		"publish":      cp.APIBase + "/messages/publish",
		"pull":         cp.APIBase + "/messages/pull",
		"ack":          cp.APIBase + "/messages/ack",
		"nack":         cp.APIBase + "/messages/nack",
		"status":       cp.APIBase + "/messages/{message_id}",
	}

	routes := []agentRouteContract{
		{
			ID:                   "agent.profile.read",
			Method:               "GET",
			Path:                 "/v1/agents/me",
			Auth:                 "bearer_agent",
			ResponseContentTypes: []string{"application/json"},
			ReadOnly:             true,
			Mutating:             false,
			Retryable:            true,
			TrustStateGated:      false,
			Description:          "Read the authenticated agent profile and ownership context.",
			SuccessExample: map[string]any{
				"agent": map[string]any{
					"agent_uuid": agent.AgentUUID,
					"agent_id":   agent.AgentID,
				},
			},
			FailureExamples: []map[string]any{
				{"code": "unauthorized", "retryable": false, "next_action": "present a valid agent bearer token"},
			},
		},
		{
			ID:                   "agent.profile.update",
			Method:               "PATCH",
			Path:                 "/v1/agents/me",
			Auth:                 "bearer_agent",
			RequestContentTypes:  []string{"application/json"},
			ResponseContentTypes: []string{"application/json"},
			ReadOnly:             false,
			Mutating:             true,
			Retryable:            false,
			TrustStateGated:      false,
			Description:          "Finalize handle exactly once and/or update agent metadata.",
			FailureExamples: []map[string]any{
				{"code": "agent_handle_locked", "retryable": false, "next_action": "do not attempt handle finalization again"},
				{"code": "invalid_request", "retryable": false, "next_action": "fix JSON payload and retry"},
			},
		},
		{
			ID:                   "agent.profile.metadata.update",
			Method:               "PATCH",
			Path:                 "/v1/agents/me/metadata",
			Auth:                 "bearer_agent",
			RequestContentTypes:  []string{"application/json"},
			ResponseContentTypes: []string{"application/json"},
			ReadOnly:             false,
			Mutating:             true,
			Retryable:            false,
			TrustStateGated:      false,
			Description:          "Update authenticated agent metadata.",
		},
		{
			ID:                   "agent.discovery.manifest.read",
			Method:               "GET",
			Path:                 "/v1/agents/me/manifest",
			Auth:                 "bearer_agent",
			ResponseContentTypes: []string{"application/json", "text/markdown"},
			ReadOnly:             true,
			Mutating:             false,
			Retryable:            true,
			TrustStateGated:      false,
			Description:          "Canonical self-describing contract for authenticated agents.",
		},
		{
			ID:                   "agent.discovery.capabilities.read",
			Method:               "GET",
			Path:                 "/v1/agents/me/capabilities",
			Auth:                 "bearer_agent",
			ResponseContentTypes: []string{"application/json"},
			ReadOnly:             true,
			Mutating:             false,
			Retryable:            true,
			TrustStateGated:      false,
			Description:          "Machine-readable capability and route contract summary.",
		},
		{
			ID:                   "agent.discovery.skill.read",
			Method:               "GET",
			Path:                 "/v1/agents/me/skill",
			Auth:                 "bearer_agent",
			ResponseContentTypes: []string{"application/json", "text/markdown"},
			ReadOnly:             true,
			Mutating:             false,
			Retryable:            true,
			TrustStateGated:      false,
			Description:          "LLM-friendly guidance derived from the canonical manifest.",
		},
		{
			ID:                   "agent.messages.publish",
			Method:               "POST",
			Path:                 "/v1/messages/publish",
			Auth:                 "bearer_agent",
			RequestContentTypes:  []string{"application/json"},
			ResponseContentTypes: []string{"application/json"},
			ReadOnly:             false,
			Mutating:             true,
			Retryable:            true,
			TrustStateGated:      true,
			Description:          "Publish a message to a local or trusted remote agent.",
			FailureExamples: []map[string]any{
				{"code": "invalid_content_type", "retryable": false, "next_action": "use text/plain or application/json payload content_type"},
				{"code": "unknown_receiver", "retryable": false, "next_action": "refresh capabilities and verify target agent identity"},
			},
		},
		{
			ID:                   "agent.messages.pull",
			Method:               "GET",
			Path:                 "/v1/messages/pull",
			Auth:                 "bearer_agent",
			ResponseContentTypes: []string{"application/json"},
			ReadOnly:             true,
			Mutating:             false,
			Retryable:            true,
			TrustStateGated:      false,
			Description:          "Pull and lease an inbound message.",
		},
		{
			ID:                   "agent.messages.ack",
			Method:               "POST",
			Path:                 "/v1/messages/ack",
			Auth:                 "bearer_agent",
			RequestContentTypes:  []string{"application/json"},
			ResponseContentTypes: []string{"application/json"},
			ReadOnly:             false,
			Mutating:             true,
			Retryable:            true,
			TrustStateGated:      false,
			Description:          "Acknowledge a leased delivery as complete.",
		},
		{
			ID:                   "agent.messages.nack",
			Method:               "POST",
			Path:                 "/v1/messages/nack",
			Auth:                 "bearer_agent",
			RequestContentTypes:  []string{"application/json"},
			ResponseContentTypes: []string{"application/json"},
			ReadOnly:             false,
			Mutating:             true,
			Retryable:            true,
			TrustStateGated:      false,
			Description:          "Release and requeue a leased delivery.",
		},
		{
			ID:                   "agent.messages.status",
			Method:               "GET",
			Path:                 "/v1/messages/{message_id}",
			Auth:                 "bearer_agent",
			ResponseContentTypes: []string{"application/json"},
			ReadOnly:             true,
			Mutating:             false,
			Retryable:            true,
			TrustStateGated:      false,
			Description:          "Read message lifecycle status for visible messages.",
		},
	}

	capabilities := []agentCapabilityContract{
		{
			ID:          "agent.discovery",
			Title:       "Discovery",
			Description: "Discover authenticated runtime routes, constraints, and guidance.",
			RouteIDs:    []string{"agent.discovery.manifest.read", "agent.discovery.capabilities.read", "agent.discovery.skill.read"},
			ReadOnly:    true,
			Retryable:   true,
		},
		{
			ID:          "agent.profile",
			Title:       "Profile Management",
			Description: "Read and update the authenticated agent profile and metadata.",
			RouteIDs:    []string{"agent.profile.read", "agent.profile.update", "agent.profile.metadata.update"},
			Mutating:    true,
			Retryable:   false,
			OperationalConstraints: []string{
				"handle finalization is one-time",
				"profile updates are scoped to authenticated agent only",
			},
		},
		{
			ID:              "agent.messaging",
			Title:           "Messaging",
			Description:     "Publish, pull, acknowledge, and inspect message delivery state.",
			RouteIDs:        []string{"agent.messages.publish", "agent.messages.pull", "agent.messages.ack", "agent.messages.nack", "agent.messages.status"},
			Mutating:        true,
			Retryable:       true,
			TrustStateGated: true,
			OperationalConstraints: []string{
				"publish requires trust path for delivery",
				"ack/nack requires active delivery lease",
			},
		},
	}

	communication := map[string]any{
		"can_communicate": len(cp.CanTalkToURIs) > 0,
		"talkable_uris":   cp.CanTalkToURIs,
		"talkable_agents": cp.CanTalkTo,
		"retry_guidance": map[string]any{
			"store_error": map[string]any{
				"retryable":   true,
				"next_action": "retry with backoff and preserve idempotency keys where available",
			},
			"no_trust_path": map[string]any{
				"retryable":   false,
				"next_action": "request trust relationship before publishing again",
			},
		},
	}

	return agentManifest{
		SchemaVersion: "2",
		GeneratedAt:   now.UTC().Format(time.RFC3339),
		Agent: map[string]any{
			"agent_uuid": agent.AgentUUID,
			"agent_id":   agent.AgentID,
			"handle":     agent.Handle,
			"org_id":     agent.OrgID,
		},
		APIBase:       cp.APIBase,
		Endpoints:     endpoints,
		Capabilities:  capabilities,
		Routes:        routes,
		Communication: communication,
	}
}

func buildAgentDiscoveryMarkdown(manifest agentManifest) string {
	var b strings.Builder
	b.WriteString("# Statocyst Agent Manifest\n\n")
	b.WriteString(fmt.Sprintf("- Schema Version: %s\n", manifest.SchemaVersion))
	b.WriteString(fmt.Sprintf("- Generated At: %s\n", manifest.GeneratedAt))
	b.WriteString(fmt.Sprintf("- API Base: %s\n", manifest.APIBase))
	if agentUUID, _ := manifest.Agent["agent_uuid"].(string); agentUUID != "" {
		b.WriteString(fmt.Sprintf("- Agent UUID: %s\n", agentUUID))
	}
	if agentID, _ := manifest.Agent["agent_id"].(string); agentID != "" {
		b.WriteString(fmt.Sprintf("- Agent ID: %s\n", agentID))
	}

	b.WriteString("\n## Endpoints\n")
	endpointKeys := []string{"manifest", "capabilities", "skill", "profile", "publish", "pull", "ack", "nack", "status"}
	for _, key := range endpointKeys {
		if endpoint, ok := manifest.Endpoints[key]; ok && endpoint != "" {
			b.WriteString(fmt.Sprintf("- %s: `%s`\n", key, endpoint))
		}
	}

	b.WriteString("\n## Capabilities\n")
	for _, capability := range manifest.Capabilities {
		b.WriteString(fmt.Sprintf("### %s\n", capability.Title))
		b.WriteString(fmt.Sprintf("- ID: `%s`\n", capability.ID))
		b.WriteString(fmt.Sprintf("- Description: %s\n", capability.Description))
		if len(capability.RouteIDs) > 0 {
			b.WriteString("- Routes:\n")
			for _, routeID := range capability.RouteIDs {
				b.WriteString(fmt.Sprintf("  - `%s`\n", routeID))
			}
		}
	}

	b.WriteString("\n## Route Contract\n")
	for _, route := range manifest.Routes {
		b.WriteString(fmt.Sprintf("### %s %s\n", route.Method, route.Path))
		b.WriteString(fmt.Sprintf("- Route ID: `%s`\n", route.ID))
		b.WriteString(fmt.Sprintf("- Auth: `%s`\n", route.Auth))
		b.WriteString(fmt.Sprintf("- Read Only: `%t`\n", route.ReadOnly))
		b.WriteString(fmt.Sprintf("- Mutating: `%t`\n", route.Mutating))
		b.WriteString(fmt.Sprintf("- Retryable: `%t`\n", route.Retryable))
		b.WriteString(fmt.Sprintf("- Trust Gated: `%t`\n", route.TrustStateGated))
		if len(route.RequestContentTypes) > 0 {
			b.WriteString("- Request Content Types:\n")
			for _, ct := range route.RequestContentTypes {
				b.WriteString(fmt.Sprintf("  - `%s`\n", ct))
			}
		}
		if len(route.ResponseContentTypes) > 0 {
			b.WriteString("- Response Content Types:\n")
			for _, ct := range route.ResponseContentTypes {
				b.WriteString(fmt.Sprintf("  - `%s`\n", ct))
			}
		}
		b.WriteString(fmt.Sprintf("- Description: %s\n", route.Description))
	}

	return b.String()
}

func buildAgentSkillMarkdown(agent model.Agent, manifest agentManifest) string {
	var b strings.Builder
	b.WriteString("# SKILL: Statocyst Agent Control Plane\n\n")
	b.WriteString("## Connected To\n")
	b.WriteString("- Service: Statocyst\n")
	b.WriteString("- API Base: " + manifest.APIBase + "\n")
	b.WriteString("- Agent UUID: " + agent.AgentUUID + "\n")
	b.WriteString("- Agent ID: " + agent.AgentID + "\n")
	b.WriteString("- Current Handle: " + agent.Handle + "\n")
	b.WriteString("- Organization ID: " + agent.OrgID + "\n")

	b.WriteString("\n## Canonical Discovery\n")
	b.WriteString("- Read manifest first: `GET " + manifest.Endpoints["manifest"] + "`\n")
	b.WriteString("- Markdown manifest: `GET " + manifest.Endpoints["manifest"] + "?format=markdown`\n")
	b.WriteString("- Capabilities JSON: `GET " + manifest.Endpoints["capabilities"] + "`\n")

	b.WriteString("\n## Onboarding Checklist\n")
	b.WriteString("1. Read current profile: `GET " + manifest.Endpoints["profile"] + "`\n")
	b.WriteString("2. Finalize stable handle once: `PATCH " + manifest.Endpoints["profile"] + "` with `{\"handle\":\"<stable_handle>\"}`\n")
	b.WriteString("3. Update metadata with a distinctive emoji and assistant type: `PATCH " + manifest.Endpoints["profile"] + "/metadata` with `{\"metadata\":{\"emoji\":\"🛰️\",\"agent_type\":\"<assistant-type>\",\"persona\":\"<short-style>\"}}`\n")
	b.WriteString("4. Pull once: `GET " + manifest.Endpoints["pull"] + "?timeout_ms=5000`\n")
	b.WriteString("5. Publish test message: `POST " + manifest.Endpoints["publish"] + "`\n")

	b.WriteString("\n## Communication Graph\n")
	talkableAgents, _ := manifest.Communication["talkable_agents"].([]string)
	if len(talkableAgents) == 0 {
		b.WriteString("- No active talk paths yet. You are connected, but cannot deliver messages until bonded.\n")
	} else {
		b.WriteString("- You can currently talk to:\n")
		for _, peer := range talkableAgents {
			b.WriteString("  - " + peer + "\n")
		}
	}

	b.WriteString("\n## Route Index\n")
	for _, route := range manifest.Routes {
		b.WriteString("- `" + route.Method + " " + route.Path + "`: " + route.Description + "\n")
	}

	return b.String()
}
