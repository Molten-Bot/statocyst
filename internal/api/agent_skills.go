package api

import (
	"encoding/json"
	"sort"
	"strings"

	"moltenhub/internal/model"
)

type agentSkillSummary struct {
	Name        string                `json:"name"`
	Description string                `json:"description"`
	Parameters  *agentSkillParameters `json:"parameters,omitempty"`
}

type agentSkillParameters struct {
	Format       string                `json:"format"`
	Schema       any                   `json:"schema"`
	Required     []agentSkillParameter `json:"required,omitempty"`
	Optional     []agentSkillParameter `json:"optional,omitempty"`
	SecretPolicy string                `json:"secret_policy"`
}

type agentSkillParameter struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

type agentPeerSkillSummary struct {
	AgentUUID string              `json:"agent_uuid,omitempty"`
	AgentID   string              `json:"agent_id,omitempty"`
	AgentURI  string              `json:"agent_uri"`
	Skills    []agentSkillSummary `json:"skills"`
}

func parseAdvertisedSkills(metadata map[string]any) []agentSkillSummary {
	raw, ok := metadata[model.AgentMetadataKeySkills]
	if !ok {
		return []agentSkillSummary{}
	}
	items := []map[string]any{}
	switch typed := raw.(type) {
	case []any:
		for _, entry := range typed {
			obj, ok := entry.(map[string]any)
			if ok {
				items = append(items, obj)
			}
		}
	case []map[string]any:
		items = append(items, typed...)
	default:
		return []agentSkillSummary{}
	}

	skillsByName := map[string]agentSkillSummary{}
	for _, item := range items {
		rawName, _ := item["name"].(string)
		name, valid := normalizeSkillName(rawName)
		if !valid {
			continue
		}
		description := strings.TrimSpace(asStringValue(item["description"]))
		if description == "" {
			continue
		}
		if len(description) > 240 {
			description = strings.TrimSpace(description[:240])
		}
		skillsByName[name] = agentSkillSummary{
			Name:        name,
			Description: description,
			Parameters:  parseSkillParameters(item["parameters"]),
		}
	}

	if len(skillsByName) == 0 {
		return []agentSkillSummary{}
	}
	names := make([]string, 0, len(skillsByName))
	for name := range skillsByName {
		names = append(names, name)
	}
	sort.Strings(names)

	out := make([]agentSkillSummary, 0, len(names))
	for _, name := range names {
		out = append(out, skillsByName[name])
	}
	return out
}

func parseSkillParameters(raw any) *agentSkillParameters {
	value, ok := raw.(map[string]any)
	if !ok || len(value) == 0 {
		return nil
	}
	format := strings.ToLower(strings.TrimSpace(asStringValue(value["format"])))
	if format != "markdown" && format != "json" {
		return nil
	}
	params := &agentSkillParameters{
		Format:       format,
		Schema:       cloneSkillParameterSchema(value["schema"]),
		Required:     parseSkillParameterList(value["required"]),
		Optional:     parseSkillParameterList(value["optional"]),
		SecretPolicy: strings.TrimSpace(asStringValue(value["secret_policy"])),
	}
	if params.SecretPolicy == "" {
		params.SecretPolicy = "forbidden"
	}
	return params
}

func cloneSkillParameterSchema(raw any) any {
	switch typed := raw.(type) {
	case string:
		return typed
	case map[string]any:
		out := make(map[string]any, len(typed))
		for key, value := range typed {
			out[key] = value
		}
		return out
	default:
		return nil
	}
}

func parseSkillParameterList(raw any) []agentSkillParameter {
	items := []map[string]any{}
	switch typed := raw.(type) {
	case []map[string]any:
		items = append(items, typed...)
	case []any:
		for _, entry := range typed {
			obj, ok := entry.(map[string]any)
			if ok {
				items = append(items, obj)
			}
		}
	}
	if len(items) == 0 {
		return nil
	}
	out := make([]agentSkillParameter, 0, len(items))
	for _, item := range items {
		name, ok := normalizeSkillName(asStringValue(item["name"]))
		if !ok {
			continue
		}
		description := strings.TrimSpace(asStringValue(item["description"]))
		if description == "" {
			continue
		}
		out = append(out, agentSkillParameter{
			Name:        name,
			Description: description,
		})
	}
	if len(out) == 0 {
		return nil
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].Name < out[j].Name
	})
	return out
}

func parseAdvertisedSkillsByName(metadata map[string]any) map[string]agentSkillSummary {
	items := parseAdvertisedSkills(metadata)
	if len(items) == 0 {
		return map[string]agentSkillSummary{}
	}
	out := make(map[string]agentSkillSummary, len(items))
	for _, item := range items {
		out[item.Name] = item
	}
	return out
}

func validateSkillActivationRequest(receiver model.Agent, contentType, payload string) *runtimeHandlerError {
	if strings.TrimSpace(contentType) != "application/json" || strings.TrimSpace(payload) == "" {
		return nil
	}

	var envelope map[string]any
	if err := json.Unmarshal([]byte(payload), &envelope); err != nil || len(envelope) == 0 {
		return nil
	}

	if err := normalizeOpenClawSkillActivationEnvelope(envelope); err != nil {
		return skillValidationRuntimeError([]string{err.Error()})
	}
	if !isSkillActivationEnvelope(envelope) {
		return nil
	}

	skillName, _ := normalizeSkillName(asStringValue(envelope["skill_name"]))
	skillsByName := parseAdvertisedSkillsByName(receiver.Metadata)
	skill, ok := skillsByName[skillName]
	if !ok {
		return skillValidationRuntimeError([]string{"receiver does not advertise skill " + strconvQuote(skillName)})
	}
	if skill.Parameters == nil {
		return nil
	}

	errors := validateSkillActivationPayload(skill, envelope["payload"], asStringValue(envelope["payload_format"]))
	if len(errors) > 0 {
		return skillValidationRuntimeError(errors)
	}
	return nil
}

func isSkillActivationEnvelope(envelope map[string]any) bool {
	envelopeType := strings.ToLower(strings.TrimSpace(asStringAny(envelope["type"])))
	envelopeKind := strings.ToLower(strings.TrimSpace(asStringAny(envelope["kind"])))
	return envelopeType == "skill_request" || envelopeType == "skill_activation" ||
		envelopeKind == "skill_request" || envelopeKind == "skill_activation"
}

func validateSkillActivationPayload(skill agentSkillSummary, rawPayload any, payloadFormat string) []string {
	params := skill.Parameters
	if params == nil {
		return nil
	}
	switch params.Format {
	case "json":
		payload, ok := rawPayload.(map[string]any)
		if !ok {
			if len(params.Required) == 0 && rawPayload == nil {
				return nil
			}
			return []string{"skill " + strconvQuote(skill.Name) + " requires a JSON object payload"}
		}
		provided := map[string]string{}
		for key, value := range payload {
			switch typed := value.(type) {
			case string:
				provided[key] = strings.TrimSpace(typed)
			default:
				body, _ := json.Marshal(typed)
				provided[key] = strings.TrimSpace(string(body))
			}
		}
		errors := validateParameterKeysAndSecrets(provided, params, marshalForSecretScan(payload))
		if payloadFormat != "" && payloadFormat != "json" {
			errors = append(errors, "payload_format must be json for skill "+strconvQuote(skill.Name))
		}
		return sortValidationErrors(errors)
	case "markdown":
		body, ok := rawPayload.(string)
		if !ok {
			if len(params.Required) == 0 && rawPayload == nil {
				return nil
			}
			return []string{"skill " + strconvQuote(skill.Name) + " requires a markdown string payload"}
		}
		provided := parseMarkdownParameterPayload(body)
		errors := validateParameterKeysAndSecrets(provided, params, body)
		if len(provided) == 0 && (len(params.Required) > 0 || len(params.Optional) > 0) {
			errors = append(errors, "markdown payload must use `parameter: value` lines for skill "+strconvQuote(skill.Name))
		}
		if payloadFormat != "" && payloadFormat != "markdown" {
			errors = append(errors, "payload_format must be markdown for skill "+strconvQuote(skill.Name))
		}
		return sortValidationErrors(errors)
	default:
		return nil
	}
}

func validateParameterKeysAndSecrets(provided map[string]string, params *agentSkillParameters, rawText string) []string {
	allowed := map[string]struct{}{}
	required := make([]string, 0, len(params.Required))
	for _, item := range params.Required {
		required = append(required, item.Name)
		allowed[item.Name] = struct{}{}
	}
	for _, item := range params.Optional {
		allowed[item.Name] = struct{}{}
	}
	errors := validateSkillParameterPayloadKeys(provided, required, allowed)
	if strings.TrimSpace(rawText) != "" && containsLikelySecretPayload(rawText) {
		errors = append(errors, "payload contains forbidden secret-like content")
	}
	return errors
}

func parseMarkdownParameterPayload(markdown string) map[string]string {
	out := map[string]string{}
	for _, rawLine := range strings.Split(markdown, "\n") {
		line := strings.TrimSpace(rawLine)
		if line == "" {
			continue
		}
		line = strings.TrimPrefix(line, "- ")
		parts := strings.SplitN(line, ":", 2)
		if len(parts) != 2 {
			continue
		}
		name, ok := normalizeSkillName(strings.Trim(strings.TrimSpace(parts[0]), "`"))
		if !ok {
			continue
		}
		out[name] = strings.TrimSpace(parts[1])
	}
	return out
}

func marshalForSecretScan(value any) string {
	body, err := json.Marshal(value)
	if err != nil {
		return ""
	}
	return string(body)
}

func containsLikelySecretPayload(value string) bool {
	normalized := strings.ToLower(strings.TrimSpace(value))
	if normalized == "" {
		return false
	}
	if strings.Contains(normalized, "do not pass secrets") || strings.Contains(normalized, "never pass secrets") {
		return false
	}
	markers := []string{"api key", "access key", "secret", "password", "private key", "bearer ", "token:", "token=", "authorization:"}
	for _, marker := range markers {
		if strings.Contains(normalized, marker) {
			return true
		}
	}
	return false
}

func sortValidationErrors(errors []string) []string {
	if len(errors) == 0 {
		return nil
	}
	sort.Strings(errors)
	return errors
}

func validateSkillParameterPayloadKeys(provided map[string]string, required []string, allowed map[string]struct{}) []string {
	errors := []string{}
	for _, name := range required {
		if strings.TrimSpace(provided[name]) == "" {
			errors = append(errors, "missing required parameter "+strconvQuote(name))
		}
	}
	for name := range provided {
		if _, ok := allowed[name]; !ok {
			errors = append(errors, "unknown parameter "+strconvQuote(name))
		}
	}
	return errors
}

func skillValidationRuntimeError(details []string) *runtimeHandlerError {
	return &runtimeHandlerError{
		status:  400,
		code:    "invalid_skill_request",
		message: "skill activation validation failed",
		extras: map[string]any{
			"failure":           true,
			"validation_errors": details,
		},
	}
}

func strconvQuote(value string) string {
	body, _ := json.Marshal(value)
	return string(body)
}

func normalizeSkillName(raw string) (string, bool) {
	normalized := strings.ToLower(strings.TrimSpace(raw))
	if len(normalized) < 2 || len(normalized) > 64 {
		return "", false
	}
	for _, ch := range normalized {
		switch {
		case ch >= 'a' && ch <= 'z':
		case ch >= '0' && ch <= '9':
		case ch == '-', ch == '_', ch == '.':
		default:
			return "", false
		}
	}
	return normalized, true
}

func asStringValue(value any) string {
	str, _ := value.(string)
	return str
}

func defaultSkillCallContract(apiBase string) map[string]any {
	return map[string]any{
		"schema_version": "1",
		"transport": map[string]any{
			"channel":          "agent_message",
			"publish_endpoint": apiBase + "/messages/publish",
			"pull_endpoint":    apiBase + "/messages/pull",
		},
		"request": map[string]any{
			"type":            "skill_request",
			"required_fields": []string{"type", "request_id", "skill_name", "reply_required"},
			"field_notes": map[string]any{
				"request_id":          "caller-generated correlation id",
				"skill_name":          "must match the peer's advertised skill name",
				"payload":             "optional skill activation payload: markdown string or JSON object",
				"payload_format":      "optional payload format hint: markdown or json (auto-inferred when omitted)",
				"reply_required":      "boolean; set true when a result must be returned",
				"reply_to_agent_id":   "optional preferred logical return target",
				"reply_to_agent_uuid": "optional preferred UUID return target",
				"input":               "legacy alias for payload (accepted for compatibility)",
			},
			"json_example": map[string]any{
				"type":                "skill_request",
				"request_id":          "req-20260317-001",
				"skill_name":          "weather_lookup",
				"payload":             "Seattle, WA",
				"payload_format":      "markdown",
				"reply_required":      true,
				"reply_to_agent_uuid": "11111111-1111-1111-1111-111111111111",
			},
			"markdown_example": strings.Join([]string{
				"type: skill_request",
				"request_id: req-20260317-001",
				"skill_name: weather_lookup",
				"payload: Seattle, WA",
				"payload_format: markdown",
				"reply_required: true",
				"reply_to_agent_uuid: 11111111-1111-1111-1111-111111111111",
			}, "\n"),
		},
		"result": map[string]any{
			"type":            "skill_result",
			"required_fields": []string{"type", "request_id", "skill_name", "status", "output"},
			"field_notes": map[string]any{
				"request_id": "must match the incoming skill_request.request_id",
				"status":     "ok or error",
				"output":     "result body from executed skill",
				"error":      "optional error detail when status=error",
			},
			"json_example": map[string]any{
				"type":       "skill_result",
				"request_id": "req-20260317-001",
				"skill_name": "weather_lookup",
				"status":     "ok",
				"output":     "Seattle 8C and overcast",
			},
			"markdown_example": strings.Join([]string{
				"type: skill_result",
				"request_id: req-20260317-001",
				"skill_name: weather_lookup",
				"status: ok",
				"output: Seattle 8C and overcast",
			}, "\n"),
		},
	}
}
