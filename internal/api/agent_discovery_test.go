package api

import (
	"strings"
	"testing"
	"time"

	"statocyst/internal/model"
)

func TestBuildAgentDiscoveryMarkdownRendersTemplateTokens(t *testing.T) {
	agent := model.Agent{
		AgentUUID: "11111111-1111-1111-1111-111111111111",
		AgentID:   "alpha/alice/agent-a",
		Handle:    "agent-a",
		OrgID:     "org-alpha",
	}
	cp := agentControlPlaneView{
		APIBase:          "https://hub.example/v1",
		AgentUUID:        agent.AgentUUID,
		AgentID:          agent.AgentID,
		OrgID:            agent.OrgID,
		OwnerHumanID:     "human-alice",
		CanTalkTo:        []string{"22222222-2222-2222-2222-222222222222"},
		CanTalkToURIs:    []string{"https://hub.example/org-bob/agent-b"},
		Capabilities:     []string{"publish_messages", "pull_messages"},
		AdvertisedSkills: []agentSkillSummary{{Name: "weather_lookup", Description: "Get current weather for a location."}},
		PeerSkillCatalog: []agentPeerSkillSummary{{AgentID: "org-bob/agent-b", AgentURI: "https://hub.example/org-bob/agent-b", Skills: []agentSkillSummary{{Name: "math.add", Description: "Add two numbers."}}}},
	}
	manifest := buildAgentManifest(agent, cp, time.Date(2026, 3, 14, 0, 0, 0, 0, time.UTC))

	markdown := buildAgentDiscoveryMarkdown(manifest)

	if strings.Contains(markdown, "{{") {
		t.Fatalf("expected all discovery template tokens to be replaced, got markdown=%q", markdown)
	}
	if !strings.Contains(markdown, "Statocyst Agent Manifest") {
		t.Fatalf("expected discovery heading, got markdown=%q", markdown)
	}
	if !strings.Contains(markdown, "- manifest: `https://hub.example/v1/agents/me/manifest`") {
		t.Fatalf("expected manifest endpoint in markdown, got markdown=%q", markdown)
	}
	if !strings.Contains(markdown, "### POST /v1/messages/publish") {
		t.Fatalf("expected publish route contract in markdown, got markdown=%q", markdown)
	}
	if !strings.Contains(markdown, "- Request Content Types:") {
		t.Fatalf("expected request content types block in markdown, got markdown=%q", markdown)
	}
	if !strings.Contains(markdown, "- Response Content Types:") {
		t.Fatalf("expected response content types block in markdown, got markdown=%q", markdown)
	}
	if !strings.Contains(markdown, "## Reading Guidance") {
		t.Fatalf("expected reading guidance section in markdown, got markdown=%q", markdown)
	}
	if !strings.Contains(markdown, "## Retry Guidance") || !strings.Contains(markdown, "`store_error`: retryable=`true`") {
		t.Fatalf("expected retry guidance section in markdown, got markdown=%q", markdown)
	}
	if !strings.Contains(markdown, "## Advertised Skills") || !strings.Contains(markdown, "`weather_lookup`: Get current weather for a location.") {
		t.Fatalf("expected advertised skills section in markdown, got markdown=%q", markdown)
	}
	if !strings.Contains(markdown, "## Talkable Peer Skills") || !strings.Contains(markdown, "org-bob/agent-b") {
		t.Fatalf("expected peer skill catalog section in markdown, got markdown=%q", markdown)
	}
	if !strings.Contains(markdown, "## Skill Call Contract") || !strings.Contains(markdown, "\"type\": \"skill_request\"") {
		t.Fatalf("expected skill call contract in markdown, got markdown=%q", markdown)
	}
}

func TestBuildAgentSkillMarkdownRendersTemplateTokens(t *testing.T) {
	agent := model.Agent{
		AgentUUID: "11111111-1111-1111-1111-111111111111",
		AgentID:   "alpha/alice/agent-a",
		Handle:    "agent-a",
		OrgID:     "org-alpha",
	}
	cp := agentControlPlaneView{
		APIBase:          "https://hub.example/v1",
		AgentUUID:        agent.AgentUUID,
		AgentID:          agent.AgentID,
		OrgID:            agent.OrgID,
		OwnerHumanID:     "human-alice",
		CanTalkTo:        []string{"22222222-2222-2222-2222-222222222222"},
		CanTalkToURIs:    []string{"https://hub.example/org-bob/agent-b"},
		Capabilities:     []string{"publish_messages", "pull_messages"},
		AdvertisedSkills: []agentSkillSummary{{Name: "weather_lookup", Description: "Get current weather for a location."}},
		PeerSkillCatalog: []agentPeerSkillSummary{{AgentID: "org-bob/agent-b", AgentURI: "https://hub.example/org-bob/agent-b", Skills: []agentSkillSummary{{Name: "math.add", Description: "Add two numbers."}}}},
	}
	manifest := buildAgentManifest(agent, cp, time.Date(2026, 3, 14, 0, 0, 0, 0, time.UTC))

	markdown := buildAgentSkillMarkdown(agent, manifest)

	if strings.Contains(markdown, "{{") {
		t.Fatalf("expected all skill template tokens to be replaced, got markdown=%q", markdown)
	}
	if !strings.Contains(markdown, "SKILL: Statocyst Agent Control Plane") {
		t.Fatalf("expected skill heading, got markdown=%q", markdown)
	}
	if !strings.Contains(markdown, "GET https://hub.example/v1/agents/me/manifest") {
		t.Fatalf("expected manifest guidance URL in skill markdown, got markdown=%q", markdown)
	}
	if !strings.Contains(markdown, "PATCH https://hub.example/v1/agents/me") {
		t.Fatalf("expected profile patch guidance in skill markdown, got markdown=%q", markdown)
	}
	if !strings.Contains(markdown, "POST https://hub.example/v1/messages/publish") {
		t.Fatalf("expected publish guidance in skill markdown, got markdown=%q", markdown)
	}
	if !strings.Contains(markdown, "You can currently talk to") {
		t.Fatalf("expected communication graph section in skill markdown, got markdown=%q", markdown)
	}
	if !strings.Contains(markdown, "## Operating Rules") || !strings.Contains(markdown, "Honor retryable and next_action fields") {
		t.Fatalf("expected operating rules section in skill markdown, got markdown=%q", markdown)
	}
	if !strings.Contains(markdown, "never include secrets, keys, tokens, or passwords") {
		t.Fatalf("expected no-secrets guidance in skill markdown, got markdown=%q", markdown)
	}
	if !strings.Contains(markdown, "## Advertised Skills") || !strings.Contains(markdown, "`weather_lookup`: Get current weather for a location.") {
		t.Fatalf("expected advertised skills in skill markdown, got markdown=%q", markdown)
	}
	if !strings.Contains(markdown, "## Talkable Peer Skills") || !strings.Contains(markdown, "`math.add`: Add two numbers.") {
		t.Fatalf("expected peer skills in skill markdown, got markdown=%q", markdown)
	}
	if !strings.Contains(markdown, "## Skill Call Contract") || !strings.Contains(markdown, "\"type\": \"skill_result\"") {
		t.Fatalf("expected skill call contract in skill markdown, got markdown=%q", markdown)
	}
	if !strings.Contains(markdown, "set `metadata.agent_type` to `openclaw` and read this skill again") {
		t.Fatalf("expected OpenClaw opt-in onboarding guidance in skill markdown, got markdown=%q", markdown)
	}
	if strings.Contains(markdown, "## OpenClaw Node + Agent HTTP Path") {
		t.Fatalf("did not expect OpenClaw-only section for non-OpenClaw agent, got markdown=%q", markdown)
	}
}

func TestBuildAgentSkillMarkdownNoTalkPathsFallback(t *testing.T) {
	agent := model.Agent{
		AgentUUID: "11111111-1111-1111-1111-111111111111",
		AgentID:   "alpha/alice/agent-a",
		Handle:    "agent-a",
		OrgID:     "org-alpha",
	}
	cp := agentControlPlaneView{
		APIBase:      "https://hub.example/v1",
		AgentUUID:    agent.AgentUUID,
		AgentID:      agent.AgentID,
		OrgID:        agent.OrgID,
		OwnerHumanID: "human-alice",
	}
	manifest := buildAgentManifest(agent, cp, time.Date(2026, 3, 14, 0, 0, 0, 0, time.UTC))

	markdown := buildAgentSkillMarkdown(agent, manifest)
	if !strings.Contains(markdown, "No active talk paths yet. You are connected, but cannot deliver messages until bonded.") {
		t.Fatalf("expected no-talk-path fallback, got markdown=%q", markdown)
	}
	if !strings.Contains(markdown, "## Advertised Skills") || !strings.Contains(markdown, "- none advertised") {
		t.Fatalf("expected no-skills fallback in skill markdown, got markdown=%q", markdown)
	}
}

func TestBuildAgentSkillMarkdownOpenClawSection(t *testing.T) {
	agent := model.Agent{
		AgentUUID: "11111111-1111-1111-1111-111111111111",
		AgentID:   "alpha/alice/agent-a",
		Handle:    "agent-a",
		OrgID:     "org-alpha",
		Metadata: map[string]any{
			model.AgentMetadataKeyType: "OpenClaw",
		},
	}
	cp := agentControlPlaneView{
		APIBase:      "https://hub.example/v1",
		AgentUUID:    agent.AgentUUID,
		AgentID:      agent.AgentID,
		OrgID:        agent.OrgID,
		OwnerHumanID: "human-alice",
	}
	manifest := buildAgentManifest(agent, cp, time.Date(2026, 3, 14, 0, 0, 0, 0, time.UTC))

	adapters, ok := manifest.ProtocolAdapters["openclaw_http_v1"].(map[string]any)
	if !ok {
		t.Fatalf("expected openclaw_http_v1 protocol adapter in manifest, got %+v", manifest.ProtocolAdapters)
	}
	if protocol, _ := adapters["protocol"].(string); protocol != "openclaw.http.v1" {
		t.Fatalf("expected openclaw adapter protocol openclaw.http.v1, got %q", protocol)
	}
	endpoints, ok := adapters["endpoints"].(map[string]string)
	if !ok {
		t.Fatalf("expected openclaw adapter endpoints map[string]string, got %+v", adapters["endpoints"])
	}
	if endpoints["publish"] != "https://hub.example/v1/openclaw/messages/publish" {
		t.Fatalf("expected openclaw publish endpoint, got %+v", endpoints)
	}

	markdown := buildAgentSkillMarkdown(agent, manifest)
	if !strings.Contains(markdown, "## OpenClaw Node + Agent HTTP Path") {
		t.Fatalf("expected OpenClaw section in skill markdown, got markdown=%q", markdown)
	}
	if !strings.Contains(markdown, "POST https://hub.example/v1/openclaw/messages/publish") {
		t.Fatalf("expected OpenClaw publish endpoint in skill markdown, got markdown=%q", markdown)
	}
	if !strings.Contains(markdown, "openclaw devices list") || !strings.Contains(markdown, "openclaw nodes status") {
		t.Fatalf("expected OpenClaw CLI hints in skill markdown, got markdown=%q", markdown)
	}
}

func TestParseAdvertisedSkillsFiltersAndNormalizes(t *testing.T) {
	metadata := map[string]any{
		"skills": []any{
			map[string]any{"name": " Weather_Lookup ", "description": "Get weather."},
			map[string]any{"name": "bad skill!", "description": "invalid"},
			map[string]any{"name": "math.add", "description": "Add numbers."},
			map[string]any{"name": "math.add", "description": "Duplicate should overwrite."},
			map[string]any{"name": "x", "description": "too short"},
			map[string]any{"name": "ok-but-no-description"},
		},
	}

	skills := parseAdvertisedSkills(metadata)
	if len(skills) != 2 {
		t.Fatalf("expected exactly 2 normalized skills, got %d skills=%v", len(skills), skills)
	}
	if skills[0].Name != "math.add" || skills[0].Description != "Duplicate should overwrite." {
		t.Fatalf("unexpected first skill: %+v", skills[0])
	}
	if skills[1].Name != "weather_lookup" || skills[1].Description != "Get weather." {
		t.Fatalf("unexpected second skill: %+v", skills[1])
	}
}
