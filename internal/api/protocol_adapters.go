package api

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
	}
}
