package api

import (
	"sort"
	"strconv"
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

const (
	discoveryManifestHeaderTemplate = `# Statocyst Agent Manifest

- Schema Version: {{SCHEMA_VERSION}}
- Generated At: {{GENERATED_AT}}
- API Base: {{API_BASE}}
{{AGENT_LINES}}
`
	discoveryUsageGuidance = `
## Reading Guidance
1. Use JSON route responses as runtime source of truth.
2. Use markdown outputs for planning and operator review.
3. Treat ` + "`retryable`" + ` and ` + "`next_action`" + ` as operational hints when requests fail.
`
	discoveryAgentLineTemplate = "- {{AGENT_LABEL}}: {{AGENT_VALUE}}\n"
	discoveryEndpointsHeading  = "\n## Endpoints\n"
	discoveryEndpointLine      = "- {{ENDPOINT_KEY}}: `{{ENDPOINT_VALUE}}`\n"
	discoveryCapabilitiesTitle = "\n## Capabilities\n"
	discoveryCapabilitySection = `### {{CAPABILITY_TITLE}}
- ID: ` + "`{{CAPABILITY_ID}}`" + `
- Description: {{CAPABILITY_DESCRIPTION}}
{{CAPABILITY_ROUTE_BLOCK}}`
	discoveryCapabilityRoutesHeader = "- Routes:\n"
	discoveryCodeBulletLine         = "  - `{{VALUE}}`\n"
	discoveryRouteContractTitle     = "\n## Route Contract\n"
	discoveryRouteSectionTemplate   = `### {{ROUTE_METHOD}} {{ROUTE_PATH}}
- Route ID: ` + "`{{ROUTE_ID}}`" + `
- Auth: ` + "`{{ROUTE_AUTH}}`" + `
- Read Only: ` + "`{{ROUTE_READ_ONLY}}`" + `
- Mutating: ` + "`{{ROUTE_MUTATING}}`" + `
- Retryable: ` + "`{{ROUTE_RETRYABLE}}`" + `
- Trust Gated: ` + "`{{ROUTE_TRUST_GATED}}`" + `
{{REQUEST_CONTENT_TYPES_BLOCK}}{{RESPONSE_CONTENT_TYPES_BLOCK}}- Description: {{ROUTE_DESCRIPTION}}
`
	discoveryRouteRequestTypesHeader  = "- Request Content Types:\n"
	discoveryRouteResponseTypesHeader = "- Response Content Types:\n"
	discoveryRetryGuidanceHeading     = "\n## Retry Guidance\n"
	discoveryRetryGuidanceLine        = "- `{{ERROR_CODE}}`: retryable=`{{RETRYABLE}}`; next_action={{NEXT_ACTION}}\n"

	skillBaseTemplate = `# SKILL: Statocyst Agent Control Plane

## Connected To
- Service: Statocyst
- API Base: {{API_BASE}}
- Agent UUID: {{AGENT_UUID}}
- Agent ID: {{AGENT_ID}}
- Current Handle: {{AGENT_HANDLE}}
- Organization ID: {{ORG_ID}}

## Canonical Discovery
- Read manifest first: ` + "`GET {{MANIFEST_URL}}`" + `
- Markdown manifest: ` + "`GET {{MANIFEST_MD_URL}}`" + `
- Capabilities JSON: ` + "`GET {{CAPABILITIES_URL}}`" + `

## Onboarding Checklist
1. Read current profile: ` + "`GET {{PROFILE_URL}}`" + `
2. Finalize stable handle once: ` + "`PATCH {{PROFILE_URL}}`" + ` with ` + "`{\"handle\":\"<stable_handle>\"}`" + `
3. Update metadata with a distinctive emoji and assistant type: ` + "`PATCH {{PROFILE_METADATA_URL}}`" + ` with ` + "`{\"metadata\":{\"emoji\":\"🛰️\",\"agent_type\":\"<assistant-type>\",\"persona\":\"<short-style>\"}}`" + `
4. Pull once: ` + "`GET {{PULL_URL}}`" + `
5. Publish test message: ` + "`POST {{PUBLISH_URL}}`" + `

## Operating Rules
{{OPERATING_RULES_BLOCK}}
## Communication Graph
{{COMMUNICATION_BLOCK}}
## Route Index
{{ROUTE_INDEX_LINES}}`
	skillNoTalkPathsLine       = "- No active talk paths yet. You are connected, but cannot deliver messages until bonded.\n"
	skillTalkPathsHeader       = "- You can currently talk to:\n"
	skillRouteIndexLine        = "- `{{ROUTE_METHOD}} {{ROUTE_PATH}}`: {{ROUTE_DESCRIPTION}}\n"
	skillCommunicationPeerLine = "  - {{VALUE}}\n"
	skillOperatingRuleLine     = "- {{VALUE}}\n"
)

// NOTE: token replacement templates are intentionally limited to markdown discovery/skill rendering.
// Do not use this pattern for runtime JSON contracts, validation, or other business logic.
func renderMarkdownTemplate(tmpl string, replacements ...string) string {
	return strings.NewReplacer(replacements...).Replace(tmpl)
}

func renderMarkdownCodeBullets(values []string) string {
	if len(values) == 0 {
		return ""
	}
	lines := make([]string, 0, len(values))
	for _, value := range values {
		lines = append(lines, renderMarkdownTemplate(discoveryCodeBulletLine, "{{VALUE}}", value))
	}
	return strings.Join(lines, "")
}

func renderMarkdownPlainBullets(values []string, lineTemplate string) string {
	if len(values) == 0 {
		return ""
	}
	lines := make([]string, 0, len(values))
	for _, value := range values {
		lines = append(lines, renderMarkdownTemplate(lineTemplate, "{{VALUE}}", value))
	}
	return strings.Join(lines, "")
}

func buildAgentDiscoveryMarkdown(manifest agentManifest) string {
	agentLines := make([]string, 0, 2)
	if agentUUID, _ := manifest.Agent["agent_uuid"].(string); agentUUID != "" {
		agentLines = append(agentLines, renderMarkdownTemplate(discoveryAgentLineTemplate, "{{AGENT_LABEL}}", "Agent UUID", "{{AGENT_VALUE}}", agentUUID))
	}
	if agentID, _ := manifest.Agent["agent_id"].(string); agentID != "" {
		agentLines = append(agentLines, renderMarkdownTemplate(discoveryAgentLineTemplate, "{{AGENT_LABEL}}", "Agent ID", "{{AGENT_VALUE}}", agentID))
	}

	markdown := make([]string, 0, 4+len(manifest.Capabilities)+len(manifest.Routes))
	markdown = append(markdown, renderMarkdownTemplate(
		discoveryManifestHeaderTemplate,
		"{{SCHEMA_VERSION}}", manifest.SchemaVersion,
		"{{GENERATED_AT}}", manifest.GeneratedAt,
		"{{API_BASE}}", manifest.APIBase,
		"{{AGENT_LINES}}", strings.Join(agentLines, ""),
	))
	markdown = append(markdown, discoveryUsageGuidance)
	markdown = append(markdown, discoveryEndpointsHeading)

	endpointLines := make([]string, 0, len(manifest.Endpoints))
	endpointKeys := []string{"manifest", "capabilities", "skill", "profile", "publish", "pull", "ack", "nack", "status"}
	for _, key := range endpointKeys {
		if endpoint, ok := manifest.Endpoints[key]; ok && endpoint != "" {
			endpointLines = append(endpointLines, renderMarkdownTemplate(discoveryEndpointLine, "{{ENDPOINT_KEY}}", key, "{{ENDPOINT_VALUE}}", endpoint))
		}
	}
	markdown = append(markdown, strings.Join(endpointLines, ""))

	markdown = append(markdown, discoveryCapabilitiesTitle)
	capabilitySections := make([]string, 0, len(manifest.Capabilities))
	for _, capability := range manifest.Capabilities {
		routeBlock := ""
		if len(capability.RouteIDs) > 0 {
			routeBlock = discoveryCapabilityRoutesHeader + renderMarkdownCodeBullets(capability.RouteIDs)
		}
		capabilitySections = append(capabilitySections, renderMarkdownTemplate(
			discoveryCapabilitySection,
			"{{CAPABILITY_TITLE}}", capability.Title,
			"{{CAPABILITY_ID}}", capability.ID,
			"{{CAPABILITY_DESCRIPTION}}", capability.Description,
			"{{CAPABILITY_ROUTE_BLOCK}}", routeBlock,
		))
	}
	markdown = append(markdown, strings.Join(capabilitySections, ""))

	markdown = append(markdown, discoveryRouteContractTitle)
	routeSections := make([]string, 0, len(manifest.Routes))
	for _, route := range manifest.Routes {
		requestContentTypesBlock := ""
		if len(route.RequestContentTypes) > 0 {
			requestContentTypesBlock = discoveryRouteRequestTypesHeader + renderMarkdownCodeBullets(route.RequestContentTypes)
		}
		responseContentTypesBlock := ""
		if len(route.ResponseContentTypes) > 0 {
			responseContentTypesBlock = discoveryRouteResponseTypesHeader + renderMarkdownCodeBullets(route.ResponseContentTypes)
		}
		routeSections = append(routeSections, renderMarkdownTemplate(
			discoveryRouteSectionTemplate,
			"{{ROUTE_METHOD}}", route.Method,
			"{{ROUTE_PATH}}", route.Path,
			"{{ROUTE_ID}}", route.ID,
			"{{ROUTE_AUTH}}", route.Auth,
			"{{ROUTE_READ_ONLY}}", strconv.FormatBool(route.ReadOnly),
			"{{ROUTE_MUTATING}}", strconv.FormatBool(route.Mutating),
			"{{ROUTE_RETRYABLE}}", strconv.FormatBool(route.Retryable),
			"{{ROUTE_TRUST_GATED}}", strconv.FormatBool(route.TrustStateGated),
			"{{REQUEST_CONTENT_TYPES_BLOCK}}", requestContentTypesBlock,
			"{{RESPONSE_CONTENT_TYPES_BLOCK}}", responseContentTypesBlock,
			"{{ROUTE_DESCRIPTION}}", route.Description,
		))
	}
	markdown = append(markdown, strings.Join(routeSections, ""))
	markdown = append(markdown, discoveryRetryGuidanceHeading)
	markdown = append(markdown, renderDiscoveryRetryGuidance(manifest))

	return strings.Join(markdown, "")
}

func renderDiscoveryRetryGuidance(manifest agentManifest) string {
	retryGuidance, _ := manifest.Communication["retry_guidance"].(map[string]any)
	if len(retryGuidance) == 0 {
		return "- none available\n"
	}
	keys := make([]string, 0, len(retryGuidance))
	for key := range retryGuidance {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	lines := make([]string, 0, len(keys))
	for _, key := range keys {
		retryable := "false"
		nextAction := "inspect route-specific error payload"
		if detail, ok := retryGuidance[key].(map[string]any); ok {
			if value, ok := detail["retryable"].(bool); ok {
				retryable = strconv.FormatBool(value)
			}
			if value, ok := detail["next_action"].(string); ok && strings.TrimSpace(value) != "" {
				nextAction = value
			}
		}
		lines = append(lines, renderMarkdownTemplate(
			discoveryRetryGuidanceLine,
			"{{ERROR_CODE}}", key,
			"{{RETRYABLE}}", retryable,
			"{{NEXT_ACTION}}", nextAction,
		))
	}
	return strings.Join(lines, "")
}

func buildAgentSkillMarkdown(agent model.Agent, manifest agentManifest) string {
	communicationBlock := skillNoTalkPathsLine
	talkableAgents, _ := manifest.Communication["talkable_agents"].([]string)
	if len(talkableAgents) > 0 {
		communicationBlock = skillTalkPathsHeader + renderMarkdownPlainBullets(talkableAgents, skillCommunicationPeerLine)
	}
	operatingRules := []string{
		"Do not use human control-plane credentials on agent runtime routes.",
		"Persist token and api_base exactly as returned by bind/register responses.",
		"Honor retryable and next_action fields before retrying failed requests.",
		"Treat bind tokens and agent bearer tokens as secrets.",
	}
	operatingRulesBlock := renderMarkdownPlainBullets(operatingRules, skillOperatingRuleLine)

	routeIndexLines := make([]string, 0, len(manifest.Routes))
	for _, route := range manifest.Routes {
		routeIndexLines = append(routeIndexLines, renderMarkdownTemplate(
			skillRouteIndexLine,
			"{{ROUTE_METHOD}}", route.Method,
			"{{ROUTE_PATH}}", route.Path,
			"{{ROUTE_DESCRIPTION}}", route.Description,
		))
	}

	return renderMarkdownTemplate(
		skillBaseTemplate,
		"{{API_BASE}}", manifest.APIBase,
		"{{AGENT_UUID}}", agent.AgentUUID,
		"{{AGENT_ID}}", agent.AgentID,
		"{{AGENT_HANDLE}}", agent.Handle,
		"{{ORG_ID}}", agent.OrgID,
		"{{MANIFEST_URL}}", manifest.Endpoints["manifest"],
		"{{MANIFEST_MD_URL}}", manifest.Endpoints["manifest"]+"?format=markdown",
		"{{CAPABILITIES_URL}}", manifest.Endpoints["capabilities"],
		"{{PROFILE_URL}}", manifest.Endpoints["profile"],
		"{{PROFILE_METADATA_URL}}", manifest.Endpoints["profile"]+"/metadata",
		"{{PULL_URL}}", manifest.Endpoints["pull"]+"?timeout_ms=5000",
		"{{PUBLISH_URL}}", manifest.Endpoints["publish"],
		"{{OPERATING_RULES_BLOCK}}", operatingRulesBlock,
		"{{COMMUNICATION_BLOCK}}", communicationBlock,
		"{{ROUTE_INDEX_LINES}}", strings.Join(routeIndexLines, ""),
	)
}
