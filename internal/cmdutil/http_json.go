package cmdutil

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// JSONResponse represents an HTTP response decoded as a JSON object.
type JSONResponse struct {
	StatusCode int
	Payload    map[string]any
	Raw        string
}

// RequestJSON sends an HTTP request and decodes a JSON object response body.
func RequestJSON(client *http.Client, baseURL, method, path string, headers map[string]string, body any) (JSONResponse, error) {
	var requestBody io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return JSONResponse{}, fmt.Errorf("marshal request body: %w", err)
		}
		requestBody = bytes.NewReader(raw)
	}

	req, err := http.NewRequest(method, strings.TrimRight(baseURL, "/")+path, requestBody)
	if err != nil {
		return JSONResponse{}, fmt.Errorf("build request %s %s: %w", method, path, err)
	}
	req.Header.Set("Content-Type", "application/json")
	for key, value := range headers {
		req.Header.Set(key, value)
	}

	resp, err := client.Do(req)
	if err != nil {
		return JSONResponse{}, fmt.Errorf("perform request %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return JSONResponse{}, fmt.Errorf("read response %s %s: %w", method, path, err)
	}
	trimmedRaw := strings.TrimSpace(string(raw))
	if trimmedRaw == "" {
		return JSONResponse{StatusCode: resp.StatusCode, Payload: map[string]any{}}, nil
	}

	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		return JSONResponse{}, fmt.Errorf("decode response %s %s: %w body=%s", method, path, err, trimmedRaw)
	}
	return JSONResponse{StatusCode: resp.StatusCode, Payload: payload, Raw: trimmedRaw}, nil
}

// HumanHeaders returns request headers for dev human auth.
func HumanHeaders(humanID, email string) map[string]string {
	return map[string]string{
		"X-Human-Id":    humanID,
		"X-Human-Email": email,
	}
}

// AgentHeaders returns Authorization header for an agent token.
func AgentHeaders(token string) map[string]string {
	return map[string]string{
		"Authorization": "Bearer " + token,
	}
}

// RequireObject returns payload[key] as an object.
func RequireObject(payload map[string]any, key string) (map[string]any, error) {
	obj, ok := payload[key].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("expected %s object, got %T payload=%v", key, payload[key], payload)
	}
	return obj, nil
}

// AsString returns payload[key] as string, or empty string.
func AsString(payload map[string]any, key string) string {
	value, _ := payload[key].(string)
	return value
}
