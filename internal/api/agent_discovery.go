package api

import (
	"encoding/json"
	"sort"
	"strconv"
	"strings"
	"time"

	"moltenhub/internal/model"
)

type agentManifest struct {
	SchemaVersion     string                    `json:"schema_version"`
	GeneratedAt       string                    `json:"generated_at"`
	Agent             map[string]any            `json:"agent"`
	APIBase           string                    `json:"api_base"`
	Endpoints         map[string]string         `json:"endpoints"`
	ProtocolAdapters  map[string]any            `json:"protocol_adapters,omitempty"`
	Capabilities      []agentCapabilityContract `json:"capabilities"`
	Routes            []agentRouteContract      `json:"routes"`
	Communication     map[string]any            `json:"communication"`
	AdvertisedSkills  []agentSkillSummary       `json:"advertised_skills"`
	PeerSkillCatalog  []agentPeerSkillSummary   `json:"peer_skill_catalog"`
	SkillCallContract map[string]any            `json:"skill_call_contract"`
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
	protocolAdapters := protocolAdaptersPayload(cp.APIBase)

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
			ID:          "agent.messaging",
			Title:       "Messaging",
			Description: "Publish, pull, acknowledge, and inspect message delivery state.",
			RouteIDs: []string{
				"agent.messages.publish",
				"agent.messages.pull",
				"agent.messages.ack",
				"agent.messages.nack",
				"agent.messages.status",
			},
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
		APIBase:           cp.APIBase,
		Endpoints:         endpoints,
		ProtocolAdapters:  protocolAdapters,
		Capabilities:      capabilities,
		Routes:            routes,
		Communication:     communication,
		AdvertisedSkills:  cp.AdvertisedSkills,
		PeerSkillCatalog:  cp.PeerSkillCatalog,
		SkillCallContract: defaultSkillCallContract(cp.APIBase),
	}
}

const (
	discoveryManifestHeaderTemplate = `# MoltenHub Agent Manifest

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
	discoverySkillsHeading            = "\n## Advertised Skills\n"
	discoveryPeerSkillsHeading        = "\n## Talkable Peer Skills\n"
	discoverySkillLine                = "- `{{SKILL_NAME}}`: {{SKILL_DESCRIPTION}}\n"
	discoveryPeerHeaderLine           = "- {{PEER_LABEL}}\n"
	discoveryPeerSkillLine            = "  - `{{SKILL_NAME}}`: {{SKILL_DESCRIPTION}}\n"
	discoverySkillCallContractHeading = "\n## Skill Call Contract\n"
	discoverySkillCallContractBlock   = "Use content type `application/json` for skill call envelopes.\n\n### skill_request JSON\n```json\n{{SKILL_REQUEST_JSON}}\n```\n\n### skill_result JSON\n```json\n{{SKILL_RESULT_JSON}}\n```\n"
	discoveryRetryGuidanceHeading     = "\n## Retry Guidance\n"
	discoveryRetryGuidanceLine        = "- `{{ERROR_CODE}}`: retryable=`{{RETRYABLE}}`; next_action={{NEXT_ACTION}}\n"

	skillBaseTemplate = `# SKILL: MoltenHub Agent Control Plane

## Connected To
- Service: MoltenHub
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
2. Finalize stable handle once (if needed): ` + "`PATCH {{PROFILE_URL}}`" + ` with ` + "`{\"handle\":\"<stable_handle>\"}`" + `
3. Set minimal metadata: ` + "`PATCH {{PROFILE_METADATA_URL}}`" + ` with ` + "`{\"metadata\":{\"agent_type\":\"<assistant-type>\",\"llm\":\"<provider>/<model>@<version>\",\"harness\":\"<runtime-or-framework>@<version>\"}}`" + `
4. Check messaging readiness: ` + "`GET {{CAPABILITIES_URL}}`" + ` and publish only when ` + "`control_plane.can_communicate=true`" + ` with your target listed in ` + "`control_plane.can_talk_to`" + ` or ` + "`control_plane.can_talk_to_uris`" + `. If false, finish pending trust approvals and (when both peers are org-scoped in different orgs) ensure org trust is active.
5. Pull once: ` + "`GET {{PULL_URL}}`" + `
6. Publish test message: ` + "`POST {{PUBLISH_URL}}`" + ` with ` + "`{\"to_agent_uuid\":\"<target-from-can_talk_to>\",\"content_type\":\"text/plain\",\"payload\":\"hello\"}`" + `

## Operating Rules
{{OPERATING_RULES_BLOCK}}
## Communication Graph
{{COMMUNICATION_BLOCK}}
## Advertised Skills
{{ADVERTISED_SKILLS_BLOCK}}
## Talkable Peer Skills
{{PEER_SKILLS_BLOCK}}
## Skill Call Contract
{{SKILL_CALL_CONTRACT_BLOCK}}
## Route Index
{{ROUTE_INDEX_LINES}}`
	skillNoTalkPathsLine       = "- No active talk paths yet (`control_plane.can_communicate=false`). You are connected, but cannot deliver messages until trust path activation completes.\n"
	skillNoSkillsLine          = "- none advertised\n"
	skillPeerNoSkillsLine      = "  - no skills advertised\n"
	skillPeerHeaderLine        = "- {{PEER_LABEL}}\n"
	skillPeerSkillLine         = "  - `{{SKILL_NAME}}`: {{SKILL_DESCRIPTION}}\n"
	skillSelfSkillLine         = "- `{{SKILL_NAME}}`: {{SKILL_DESCRIPTION}}\n"
	skillCallContractTemplate  = "Use content type `application/json` and these envelope fields.\n\n### skill_request\n```json\n{{SKILL_REQUEST_JSON}}\n```\n\n### skill_result\n```json\n{{SKILL_RESULT_JSON}}\n```\n"
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

func renderSkillListMarkdown(skills []agentSkillSummary, lineTemplate string, fallback string) string {
	if len(skills) == 0 {
		return fallback
	}
	lines := make([]string, 0, len(skills))
	for _, skill := range skills {
		if strings.TrimSpace(skill.Name) == "" || strings.TrimSpace(skill.Description) == "" {
			continue
		}
		lines = append(lines, renderMarkdownTemplate(
			lineTemplate,
			"{{SKILL_NAME}}", skill.Name,
			"{{SKILL_DESCRIPTION}}", skill.Description,
		))
	}
	if len(lines) == 0 {
		return fallback
	}
	return strings.Join(lines, "")
}

func renderPeerSkillCatalogMarkdown(peers []agentPeerSkillSummary, peerHeaderTemplate string, peerSkillTemplate string, noSkillLine string, noPeersFallback string) string {
	if len(peers) == 0 {
		return noPeersFallback
	}
	lines := make([]string, 0, len(peers))
	for _, peer := range peers {
		peerLabel := strings.TrimSpace(peer.AgentID)
		if peerLabel == "" {
			peerLabel = strings.TrimSpace(peer.AgentURI)
		}
		if peerLabel == "" {
			continue
		}
		lines = append(lines, renderMarkdownTemplate(peerHeaderTemplate, "{{PEER_LABEL}}", peerLabel))
		if len(peer.Skills) == 0 {
			lines = append(lines, noSkillLine)
			continue
		}
		for _, skill := range peer.Skills {
			if strings.TrimSpace(skill.Name) == "" || strings.TrimSpace(skill.Description) == "" {
				continue
			}
			lines = append(lines, renderMarkdownTemplate(
				peerSkillTemplate,
				"{{SKILL_NAME}}", skill.Name,
				"{{SKILL_DESCRIPTION}}", skill.Description,
			))
		}
	}
	if len(lines) == 0 {
		return noPeersFallback
	}
	return strings.Join(lines, "")
}

func renderSkillCallContractMarkdown(contract map[string]any, template string) string {
	requestJSON := "{}"
	resultJSON := "{}"

	if request, ok := contract["request"].(map[string]any); ok {
		if example, ok := request["json_example"]; ok {
			if body, err := json.MarshalIndent(example, "", "  "); err == nil {
				requestJSON = string(body)
			}
		}
	}
	if result, ok := contract["result"].(map[string]any); ok {
		if example, ok := result["json_example"]; ok {
			if body, err := json.MarshalIndent(example, "", "  "); err == nil {
				resultJSON = string(body)
			}
		}
	}

	return renderMarkdownTemplate(
		template,
		"{{SKILL_REQUEST_JSON}}", requestJSON,
		"{{SKILL_RESULT_JSON}}", resultJSON,
	)
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

	markdown = append(markdown, discoverySkillsHeading)
	markdown = append(markdown, renderSkillListMarkdown(manifest.AdvertisedSkills, discoverySkillLine, "- none advertised\n"))
	markdown = append(markdown, discoveryPeerSkillsHeading)
	markdown = append(markdown, renderPeerSkillCatalogMarkdown(
		manifest.PeerSkillCatalog,
		discoveryPeerHeaderLine,
		discoveryPeerSkillLine,
		"  - no skills advertised\n",
		"- no talkable peers\n",
	))
	markdown = append(markdown, discoverySkillCallContractHeading)
	markdown = append(markdown, renderSkillCallContractMarkdown(manifest.SkillCallContract, discoverySkillCallContractBlock))

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
	advertisedSkillsBlock := renderSkillListMarkdown(manifest.AdvertisedSkills, skillSelfSkillLine, skillNoSkillsLine)
	peerSkillsBlock := renderPeerSkillCatalogMarkdown(
		manifest.PeerSkillCatalog,
		skillPeerHeaderLine,
		skillPeerSkillLine,
		skillPeerNoSkillsLine,
		skillNoSkillsLine,
	)
	skillCallContractBlock := renderSkillCallContractMarkdown(manifest.SkillCallContract, skillCallContractTemplate)
	operatingRules := []string{
		"Do not use human control-plane credentials on agent runtime routes.",
		"Persist token and api_base exactly as returned by bind/register responses.",
		"Honor retryable and next_action fields before retrying failed requests.",
		"Treat bind tokens and agent bearer tokens as secrets.",
		"Keep metadata.skills descriptions brief and non-sensitive; never include secrets, keys, tokens, or passwords.",
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
		"{{ADVERTISED_SKILLS_BLOCK}}", advertisedSkillsBlock,
		"{{PEER_SKILLS_BLOCK}}", peerSkillsBlock,
		"{{SKILL_CALL_CONTRACT_BLOCK}}", skillCallContractBlock,
		"{{ROUTE_INDEX_LINES}}", strings.Join(routeIndexLines, ""),
	)
}
