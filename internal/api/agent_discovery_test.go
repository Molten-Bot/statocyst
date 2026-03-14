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
		APIBase:       "https://hub.example/v1",
		AgentUUID:     agent.AgentUUID,
		AgentID:       agent.AgentID,
		OrgID:         agent.OrgID,
		OwnerHumanID:  "human-alice",
		CanTalkTo:     []string{"22222222-2222-2222-2222-222222222222"},
		CanTalkToURIs: []string{"https://hub.example/org-bob/agent-b"},
		Capabilities:  []string{"publish_messages", "pull_messages"},
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
}

func TestBuildAgentSkillMarkdownRendersTemplateTokens(t *testing.T) {
	agent := model.Agent{
		AgentUUID: "11111111-1111-1111-1111-111111111111",
		AgentID:   "alpha/alice/agent-a",
		Handle:    "agent-a",
		OrgID:     "org-alpha",
	}
	cp := agentControlPlaneView{
		APIBase:       "https://hub.example/v1",
		AgentUUID:     agent.AgentUUID,
		AgentID:       agent.AgentID,
		OrgID:         agent.OrgID,
		OwnerHumanID:  "human-alice",
		CanTalkTo:     []string{"22222222-2222-2222-2222-222222222222"},
		CanTalkToURIs: []string{"https://hub.example/org-bob/agent-b"},
		Capabilities:  []string{"publish_messages", "pull_messages"},
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
}
