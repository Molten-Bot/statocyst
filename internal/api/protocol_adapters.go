package api

func agentRuntimeEndpoints(apiBase string) map[string]string {
	return map[string]string{
		"profile":      apiBase + "/agents/me",
		"activity":     apiBase + "/agents/me/activities",
		"manifest":     apiBase + "/agents/me/manifest",
		"capabilities": apiBase + "/agents/me/capabilities",
		"skill":        apiBase + "/agents/me/skill",
		"publish":      apiBase + "/runtime/messages/publish",
		"pull":         apiBase + "/runtime/messages/pull",
		"ack":          apiBase + "/runtime/messages/ack",
		"nack":         apiBase + "/runtime/messages/nack",
		"status":       apiBase + "/runtime/messages/{message_id}",
	}
}

func agentManifestEndpoints(apiBase string) map[string]string {
	endpoints := agentRuntimeEndpoints(apiBase)
	endpoints["offline"] = apiBase + "/runtime/messages/offline"
	return endpoints
}

func protocolAdaptersPayload(apiBase string) map[string]any {
	return map[string]any{
		"a2a_v1": map[string]any{
			"protocol":    a2aProtocolAdapter,
			"mode":        "additive",
			"description": "Agent2Agent Protocol v1 adapter over JSON-RPC and HTTP+JSON; core /v1/messages/* routes remain available.",
			"bindings":    []string{"JSONRPC", "HTTP+JSON"},
			"endpoints":   a2aAdapterEndpoints(apiBase),
		},
		"runtime_v1": map[string]any{
			"protocol":    runtimeEnvelopeProtocol,
			"mode":        "canonical",
			"description": "Generic agent-runtime JSON envelope adapter over HTTP and websocket; core /v1/messages/* routes remain available.",
			"endpoints":   runtimeEnvelopeAdapterEndpoints(apiBase),
		},
	}
}

func runtimeEnvelopeAdapterEndpoints(apiBase string) map[string]string {
	return map[string]string{
		"publish":   apiBase + "/runtime/messages/publish",
		"pull":      apiBase + "/runtime/messages/pull",
		"ack":       apiBase + "/runtime/messages/ack",
		"nack":      apiBase + "/runtime/messages/nack",
		"status":    apiBase + "/runtime/messages/{message_id}",
		"websocket": apiBase + "/runtime/messages/ws",
		"offline":   apiBase + "/runtime/messages/offline",
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
