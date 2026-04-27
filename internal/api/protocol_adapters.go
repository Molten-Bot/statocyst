package api

func agentRuntimeEndpoints(apiBase string) map[string]string {
	return map[string]string{
		"profile":      apiBase + "/agents/me",
		"activity":     apiBase + "/agents/me/activities",
		"manifest":     apiBase + "/agents/me/manifest",
		"capabilities": apiBase + "/agents/me/capabilities",
		"skill":        apiBase + "/agents/me/skill",
		"publish":      apiBase + "/messages/publish",
		"pull":         apiBase + "/messages/pull",
		"ack":          apiBase + "/messages/ack",
		"nack":         apiBase + "/messages/nack",
		"status":       apiBase + "/messages/{message_id}",
	}
}

func agentManifestEndpoints(apiBase string) map[string]string {
	endpoints := agentRuntimeEndpoints(apiBase)
	endpoints["offline"] = apiBase + "/openclaw/messages/offline"
	return endpoints
}

func protocolAdaptersPayload(apiBase string) map[string]any {
	endpoints := openClawAdapterEndpoints(apiBase)
	return map[string]any{
		"a2a_v1": map[string]any{
			"protocol":    a2aProtocolAdapter,
			"mode":        "additive",
			"description": "Agent2Agent Protocol v1 adapter over JSON-RPC and HTTP+JSON; core /v1/messages/* routes remain available.",
			"bindings":    []string{"JSONRPC", "HTTP+JSON"},
			"endpoints":   a2aAdapterEndpoints(apiBase),
		},
		"openclaw_http_v1": map[string]any{
			"protocol":    openClawHTTPProtocol,
			"mode":        "additive",
			"description": "OpenClaw JSON envelope adapter over HTTP; core /v1/messages/* routes remain available.",
			"endpoints":   endpoints,
		},
	}
}

func openClawAdapterEndpoints(apiBase string) map[string]string {
	return map[string]string{
		"publish": apiBase + "/openclaw/messages/publish",
		"pull":    apiBase + "/openclaw/messages/pull",
		"ack":     apiBase + "/openclaw/messages/ack",
		"nack":    apiBase + "/openclaw/messages/nack",
		"status":  apiBase + "/openclaw/messages/{message_id}",
		"offline": apiBase + "/openclaw/messages/offline",
	}
}

func a2aAdapterEndpoints(apiBase string) map[string]string {
	return map[string]string{
		"well_known_agent_card": "/.well-known/agent-card.json",
		"generic_jsonrpc":       apiBase + "/a2a",
		"generic_rest":          apiBase + "/a2a",
		"agent_card":            apiBase + "/a2a/agents/{agent_uuid}/agent-card",
		"agent_jsonrpc":         apiBase + "/a2a/agents/{agent_uuid}",
		"agent_rest":            apiBase + "/a2a/agents/{agent_uuid}",
		"send_message":          apiBase + "/a2a/agents/{agent_uuid}/message:send",
		"get_task":              apiBase + "/a2a/agents/{agent_uuid}/tasks/{task_id}",
		"list_tasks":            apiBase + "/a2a/agents/{agent_uuid}/tasks",
		"extended_agent_card":   apiBase + "/a2a/agents/{agent_uuid}/extendedAgentCard",
	}
}
