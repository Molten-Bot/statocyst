package cmdutil

import (
	"errors"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

type errReadCloser struct{}

func (errReadCloser) Read([]byte) (int, error) { return 0, errors.New("read failed") }
func (errReadCloser) Close() error             { return nil }

func TestRequestJSON(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/resource" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		if got := r.Header.Get("Content-Type"); got != "application/json" {
			t.Fatalf("unexpected content type: %q", got)
		}
		if got := r.Header.Get("X-Test"); got != "present" {
			t.Fatalf("missing test header: %q", got)
		}
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read request body: %v", err)
		}
		if strings.TrimSpace(string(raw)) != `{"name":"alice"}` {
			t.Fatalf("unexpected request body: %s", raw)
		}
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(` {"ok":true,"name":"alice"} `))
	}))
	defer server.Close()

	resp, err := RequestJSON(server.Client(), server.URL+"/", http.MethodPost, "/v1/resource", map[string]string{"X-Test": "present"}, map[string]any{"name": "alice"})
	if err != nil {
		t.Fatalf("RequestJSON returned error: %v", err)
	}
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("unexpected status: %d", resp.StatusCode)
	}
	if resp.Raw != `{"ok":true,"name":"alice"}` {
		t.Fatalf("unexpected raw payload: %q", resp.Raw)
	}
	if resp.Payload["name"] != "alice" || resp.Payload["ok"] != true {
		t.Fatalf("unexpected payload: %#v", resp.Payload)
	}
}

func TestRequestJSONEmptyResponse(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	resp, err := RequestJSON(server.Client(), server.URL, http.MethodGet, "/", nil, nil)
	if err != nil {
		t.Fatalf("RequestJSON returned error: %v", err)
	}
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("unexpected status: %d", resp.StatusCode)
	}
	if len(resp.Payload) != 0 {
		t.Fatalf("expected empty payload, got %#v", resp.Payload)
	}
	if resp.Raw != "" {
		t.Fatalf("expected empty raw body, got %q", resp.Raw)
	}
}

func TestRequestJSONErrors(t *testing.T) {
	t.Parallel()

	t.Run("marshal body", func(t *testing.T) {
		t.Parallel()
		_, err := RequestJSON(http.DefaultClient, "http://example.test", http.MethodPost, "/", nil, map[string]any{"bad": math.Inf(1)})
		if err == nil || !strings.Contains(err.Error(), "marshal request body") {
			t.Fatalf("expected marshal error, got %v", err)
		}
	})

	t.Run("build request", func(t *testing.T) {
		t.Parallel()
		_, err := RequestJSON(http.DefaultClient, "http://[::1", http.MethodGet, "/", nil, nil)
		if err == nil || !strings.Contains(err.Error(), "build request") {
			t.Fatalf("expected build request error, got %v", err)
		}
	})

	t.Run("perform request", func(t *testing.T) {
		t.Parallel()
		client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			return nil, errors.New("dial failed")
		})}
		_, err := RequestJSON(client, "http://example.test", http.MethodGet, "/", nil, nil)
		if err == nil || !strings.Contains(err.Error(), "perform request") {
			t.Fatalf("expected perform request error, got %v", err)
		}
	})

	t.Run("read response", func(t *testing.T) {
		t.Parallel()
		client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: http.StatusOK, Body: errReadCloser{}}, nil
		})}
		_, err := RequestJSON(client, "http://example.test", http.MethodGet, "/", nil, nil)
		if err == nil || !strings.Contains(err.Error(), "read response") {
			t.Fatalf("expected read response error, got %v", err)
		}
	})

	t.Run("decode response", func(t *testing.T) {
		t.Parallel()
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte("not json"))
		}))
		defer server.Close()

		_, err := RequestJSON(server.Client(), server.URL, http.MethodGet, "/", nil, nil)
		if err == nil || !strings.Contains(err.Error(), "decode response") || !strings.Contains(err.Error(), "body=not json") {
			t.Fatalf("expected decode response error with body, got %v", err)
		}
	})
}

func TestHeaderHelpers(t *testing.T) {
	t.Parallel()

	human := HumanHeaders("human-1", "human@example.com")
	if human["X-Human-Id"] != "human-1" || human["X-Human-Email"] != "human@example.com" {
		t.Fatalf("unexpected human headers: %#v", human)
	}

	agent := AgentHeaders("token-1")
	if agent["Authorization"] != "Bearer token-1" {
		t.Fatalf("unexpected agent headers: %#v", agent)
	}
}

func TestRequireObjectAndAsString(t *testing.T) {
	t.Parallel()

	payload := map[string]any{
		"object": map[string]any{"name": "alice"},
		"name":   "alice",
		"other":  12,
	}
	obj, err := RequireObject(payload, "object")
	if err != nil {
		t.Fatalf("RequireObject returned error: %v", err)
	}
	if obj["name"] != "alice" {
		t.Fatalf("unexpected object: %#v", obj)
	}

	if _, err := RequireObject(payload, "other"); err == nil || !strings.Contains(err.Error(), "expected other object") {
		t.Fatalf("expected object type error, got %v", err)
	}

	if got := AsString(payload, "name"); got != "alice" {
		t.Fatalf("unexpected string value: %q", got)
	}
	if got := AsString(payload, "other"); got != "" {
		t.Fatalf("expected empty string for non-string value, got %q", got)
	}
}
