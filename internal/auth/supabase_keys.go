package auth

import (
	"encoding/base64"
	"encoding/json"
	"strings"
)

// IsSafeSupabaseBrowserKey reports whether key is safe to use in browser contexts.
// It rejects secret/service-role keys and accepts publishable/anon variants.
func IsSafeSupabaseBrowserKey(key string) bool {
	trimmed := strings.TrimSpace(key)
	if trimmed == "" {
		return false
	}

	lower := strings.ToLower(trimmed)
	if strings.HasPrefix(lower, "sb_secret_") || strings.HasPrefix(lower, "sb_service_role_") {
		return false
	}
	if strings.HasPrefix(lower, "sb_publishable_") || strings.HasPrefix(lower, "sb_anon_") {
		return true
	}

	role, ok := jwtRole(trimmed)
	if !ok {
		return false
	}
	return role == "anon"
}

func jwtRole(token string) (string, bool) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return "", false
	}

	payload, err := decodeJWTPart(parts[1])
	if err != nil {
		return "", false
	}

	var claims struct {
		Role string `json:"role"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil {
		return "", false
	}

	role := strings.ToLower(strings.TrimSpace(claims.Role))
	if role == "" {
		return "", false
	}
	return role, true
}

func decodeJWTPart(part string) ([]byte, error) {
	payload, err := base64.RawURLEncoding.DecodeString(part)
	if err == nil {
		return payload, nil
	}
	return base64.URLEncoding.DecodeString(part)
}
