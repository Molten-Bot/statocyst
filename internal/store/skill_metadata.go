package store

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

var markdownSkillParameterLinePattern = regexp.MustCompile("^-\\s*(?:`([^`]+)`|\\*\\*([^*]+)\\*\\*|([a-zA-Z0-9_.-]+))\\s*:\\s*(.+)$")

func normalizeSkillParameters(raw any) (map[string]any, error) {
	switch typed := raw.(type) {
	case nil:
		return nil, nil
	case string:
		return normalizeMarkdownSkillParameters(strings.TrimSpace(typed))
	case map[string]any:
		format := strings.ToLower(strings.TrimSpace(stringValue(typed["format"])))
		if format == "markdown" || format == "json" {
			return normalizeFormattedSkillParameters(format, typed)
		}
		return normalizeJSONSkillParameters(typed)
	default:
		return nil, ErrInvalidAgentSkills
	}
}

func normalizeFormattedSkillParameters(format string, raw map[string]any) (map[string]any, error) {
	switch format {
	case "markdown":
		if schema, ok := raw["schema"]; ok {
			body, ok := schema.(string)
			if !ok {
				return nil, ErrInvalidAgentSkills
			}
			return normalizeMarkdownSkillParameters(strings.TrimSpace(body))
		}
		if body := strings.TrimSpace(stringValue(raw["body"])); body != "" {
			return normalizeMarkdownSkillParameters(body)
		}
		return nil, ErrInvalidAgentSkills
	case "json":
		if schema, ok := raw["schema"]; ok {
			obj, ok := schema.(map[string]any)
			if !ok {
				return nil, ErrInvalidAgentSkills
			}
			return normalizeJSONSkillParameters(obj)
		}
		if _, hasRequired := raw["required"]; hasRequired {
			return normalizeJSONSkillParameters(raw)
		}
		if _, hasOptional := raw["optional"]; hasOptional {
			return normalizeJSONSkillParameters(raw)
		}
		if _, hasSecretPolicy := raw["secret_policy"]; hasSecretPolicy {
			return normalizeJSONSkillParameters(raw)
		}
		if _, hasSecretsForbidden := raw["secrets_forbidden"]; hasSecretsForbidden {
			return normalizeJSONSkillParameters(raw)
		}
		return nil, ErrInvalidAgentSkills
	default:
		return nil, ErrInvalidAgentSkills
	}
}

func containsSecretProhibition(value string) bool {
	normalized := strings.ToLower(strings.TrimSpace(value))
	if normalized == "" {
		return false
	}
	phrases := []string{
		"never pass secrets",
		"never include secrets",
		"do not pass secrets",
		"do not include secrets",
		"don't pass secrets",
		"don't include secrets",
		"secrets are forbidden",
		"secret values are forbidden",
		"passing secrets is forbidden",
		"never pass tokens",
		"do not pass tokens",
		"never pass api keys",
		"do not pass api keys",
	}
	for _, phrase := range phrases {
		if strings.Contains(normalized, phrase) {
			return true
		}
	}
	return false
}

func containsStrongSecretMarker(value string) bool {
	normalized := strings.ToLower(strings.TrimSpace(value))
	if normalized == "" {
		return false
	}
	markers := []string{
		"api key",
		"api_key",
		"apikey",
		"access key",
		"password",
		"passwd",
		"private key",
		"bearer ",
		"token=",
		"token:",
		"authorization:",
	}
	for _, marker := range markers {
		if strings.Contains(normalized, marker) {
			return true
		}
	}
	return false
}

func containsGenericSecretMarker(value string) bool {
	normalized := strings.ToLower(strings.TrimSpace(value))
	if normalized == "" {
		return false
	}
	return strings.Contains(normalized, "secret")
}

func containsLikelySecret(value string) bool {
	normalized := strings.ToLower(strings.TrimSpace(value))
	if normalized == "" {
		return false
	}
	if containsStrongSecretMarker(normalized) {
		return true
	}
	if containsGenericSecretMarker(normalized) && !containsSecretProhibition(normalized) {
		return true
	}
	return false
}

func validateSkillParameterPayloadKeys(provided map[string]string, required []string, allowed map[string]struct{}) []string {
	errors := []string{}
	for _, name := range required {
		if strings.TrimSpace(provided[name]) == "" {
			errors = append(errors, fmt.Sprintf("missing required parameter %q", name))
		}
	}
	for name := range provided {
		if _, ok := allowed[name]; !ok {
			errors = append(errors, fmt.Sprintf("unknown parameter %q", name))
		}
	}
	sort.Strings(errors)
	return errors
}

func normalizeMarkdownSkillParameters(markdown string) (map[string]any, error) {
	if strings.TrimSpace(markdown) == "" {
		return nil, ErrInvalidAgentSkills
	}
	if !containsSecretProhibition(markdown) {
		return nil, ErrInvalidSkillDescription
	}
	required, optional, err := parseMarkdownSkillParameters(markdown)
	if err != nil {
		return nil, err
	}
	return buildNormalizedSkillParameters("markdown", markdown, required, optional), nil
}

func parseMarkdownSkillParameters(markdown string) ([]map[string]any, []map[string]any, error) {
	lines := strings.Split(markdown, "\n")
	required := []map[string]any{}
	optional := []map[string]any{}
	seen := map[string]struct{}{}
	section := ""

	for _, rawLine := range lines {
		line := strings.TrimSpace(rawLine)
		if line == "" {
			continue
		}
		switch markdownParameterSection(line) {
		case "required":
			section = "required"
			continue
		case "optional":
			section = "optional"
			continue
		}
		match := markdownSkillParameterLinePattern.FindStringSubmatch(line)
		if len(match) == 0 {
			continue
		}
		if section == "" {
			return nil, nil, ErrInvalidAgentSkills
		}
		name := firstNonEmpty(match[1], match[2], match[3])
		normalizedName, ok := normalizeAgentSkillName(name)
		if !ok {
			return nil, nil, ErrInvalidAgentSkills
		}
		if _, exists := seen[normalizedName]; exists {
			return nil, nil, ErrInvalidAgentSkills
		}
		description := strings.TrimSpace(match[4])
		if description == "" || len(description) > 240 {
			return nil, nil, ErrInvalidAgentSkills
		}
		if containsLikelySecret(description) {
			return nil, nil, ErrInvalidSkillDescription
		}
		entry := map[string]any{
			"name":        normalizedName,
			"description": description,
		}
		if section == "required" {
			required = append(required, entry)
		} else {
			optional = append(optional, entry)
		}
		seen[normalizedName] = struct{}{}
	}
	if len(required) == 0 && len(optional) == 0 {
		return nil, nil, ErrInvalidAgentSkills
	}
	sortSkillParameterEntries(required)
	sortSkillParameterEntries(optional)
	return required, optional, nil
}

func markdownParameterSection(line string) string {
	normalized := strings.ToLower(strings.TrimSpace(line))
	normalized = strings.TrimLeft(normalized, "#")
	normalized = strings.TrimSpace(normalized)
	normalized = strings.TrimSuffix(normalized, ":")
	normalized = strings.TrimSpace(normalized)
	switch normalized {
	case "required", "required parameters":
		return "required"
	case "optional", "optional parameters":
		return "optional"
	default:
		return ""
	}
}

func normalizeJSONSkillParameters(raw map[string]any) (map[string]any, error) {
	secretPolicy := strings.ToLower(strings.TrimSpace(stringValue(raw["secret_policy"])))
	secretsForbidden, _ := raw["secrets_forbidden"].(bool)
	if secretPolicy != "forbidden" && !secretsForbidden {
		return nil, ErrInvalidSkillDescription
	}

	required, err := normalizeSkillParameterList(raw["required"])
	if err != nil {
		return nil, err
	}
	optional, err := normalizeSkillParameterList(raw["optional"])
	if err != nil {
		return nil, err
	}
	if len(required) == 0 && len(optional) == 0 {
		return nil, ErrInvalidAgentSkills
	}

	schema := map[string]any{
		"required":          required,
		"optional":          optional,
		"secret_policy":     "forbidden",
		"secrets_forbidden": true,
	}
	return buildNormalizedSkillParameters("json", schema, required, optional), nil
}

func normalizeSkillParameterList(raw any) ([]map[string]any, error) {
	if raw == nil {
		return []map[string]any{}, nil
	}
	items, ok := raw.([]any)
	if !ok {
		if typed, ok := raw.([]map[string]any); ok {
			items = make([]any, 0, len(typed))
			for _, entry := range typed {
				items = append(items, entry)
			}
		} else {
			return nil, ErrInvalidAgentSkills
		}
	}
	out := make([]map[string]any, 0, len(items))
	seen := map[string]struct{}{}
	for _, item := range items {
		entry, ok := item.(map[string]any)
		if !ok {
			return nil, ErrInvalidAgentSkills
		}
		name, ok := normalizeAgentSkillName(stringValue(entry["name"]))
		if !ok {
			return nil, ErrInvalidAgentSkills
		}
		if _, exists := seen[name]; exists {
			return nil, ErrInvalidAgentSkills
		}
		description := strings.TrimSpace(stringValue(entry["description"]))
		if description == "" || len(description) > 240 {
			return nil, ErrInvalidAgentSkills
		}
		if containsLikelySecret(description) {
			return nil, ErrInvalidSkillDescription
		}
		out = append(out, map[string]any{
			"name":        name,
			"description": description,
		})
		seen[name] = struct{}{}
	}
	sortSkillParameterEntries(out)
	return out, nil
}

func buildNormalizedSkillParameters(format string, schema any, required []map[string]any, optional []map[string]any) map[string]any {
	out := map[string]any{
		"format":        format,
		"schema":        schema,
		"required":      required,
		"optional":      optional,
		"secret_policy": "forbidden",
	}
	return out
}

func sortSkillParameterEntries(entries []map[string]any) {
	sort.Slice(entries, func(i, j int) bool {
		return stringValue(entries[i]["name"]) < stringValue(entries[j]["name"])
	})
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
