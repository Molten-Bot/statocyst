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
