package store

import (
	"encoding/xml"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"testing"
)

func TestNewStoresFromEnv_DefaultsToMemory(t *testing.T) {
	t.Setenv("STATOCYST_STATE_BACKEND", "")
	t.Setenv("STATOCYST_QUEUE_BACKEND", "")

	control, queue, err := NewStoresFromEnv()
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	if _, ok := control.(*MemoryStore); !ok {
		t.Fatalf("expected memory control store, got %T", control)
	}
	if _, ok := queue.(*MemoryStore); !ok {
		t.Fatalf("expected memory queue store, got %T", queue)
	}
}

func TestNewStoresFromEnv_RejectsUnsupportedBackends(t *testing.T) {
	t.Setenv("STATOCYST_STATE_BACKEND", "unknown-state")
	t.Setenv("STATOCYST_QUEUE_BACKEND", "memory")

	if _, _, err := NewStoresFromEnv(); err == nil {
		t.Fatalf("expected error for unsupported state backend")
	}

	t.Setenv("STATOCYST_STATE_BACKEND", "memory")
	t.Setenv("STATOCYST_QUEUE_BACKEND", "unknown-queue")

	if _, _, err := NewStoresFromEnv(); err == nil {
		t.Fatalf("expected error for unsupported queue backend")
	}
}

func TestParseStorageStartupMode(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name    string
		raw     string
		want    StorageStartupMode
		wantErr bool
	}{
		{name: "default_empty", raw: "", want: StorageStartupModeStrict},
		{name: "strict", raw: "strict", want: StorageStartupModeStrict},
		{name: "degraded", raw: "degraded", want: StorageStartupModeDegraded},
		{name: "fallback_hyphen", raw: "fallback-memory", want: StorageStartupModeDegraded},
		{name: "fallback_underscore", raw: "fallback_memory", want: StorageStartupModeDegraded},
		{name: "fallback_compact", raw: "fallbackmemory", want: StorageStartupModeDegraded},
		{name: "invalid", raw: "unsafe", wantErr: true},
	}

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseStorageStartupMode(tc.raw)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected parse error for %q", tc.raw)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected parse error for %q: %v", tc.raw, err)
			}
			if got != tc.want {
				t.Fatalf("parse %q: got %q want %q", tc.raw, got, tc.want)
			}
		})
	}
}

func TestNewStoresFromEnv_S3QueueConfigured(t *testing.T) {
	t.Setenv("STATOCYST_STATE_BACKEND", "memory")
	t.Setenv("STATOCYST_QUEUE_BACKEND", "s3")
	t.Setenv("STATOCYST_QUEUE_S3_ENDPOINT", "http://localhost:9000")
	t.Setenv("STATOCYST_QUEUE_S3_BUCKET", "statocyst-queue")
	t.Setenv("STATOCYST_QUEUE_S3_PREFIX", "statocyst-queue")
	t.Setenv("STATOCYST_QUEUE_S3_PATH_STYLE", "true")

	control, queue, err := NewStoresFromEnv()
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if _, ok := control.(*MemoryStore); !ok {
		t.Fatalf("expected memory control store, got %T", control)
	}
	if _, ok := queue.(*s3QueueStore); !ok {
		t.Fatalf("expected s3 queue store, got %T", queue)
	}
}

func TestNewStoresFromEnv_S3QueueRequiresBucketAndEndpoint(t *testing.T) {
	t.Setenv("STATOCYST_STATE_BACKEND", "memory")
	t.Setenv("STATOCYST_QUEUE_BACKEND", "s3")
	t.Setenv("STATOCYST_QUEUE_S3_BUCKET", "")
	t.Setenv("STATOCYST_QUEUE_S3_ENDPOINT", "")

	if _, _, err := NewStoresFromEnv(); err == nil {
		t.Fatalf("expected error for missing s3 queue config")
	}
}

func TestNewStoresFromEnv_S3StateConfigured(t *testing.T) {
	server := newFakeS3StoreServer(t)
	defer server.Close()

	t.Setenv("STATOCYST_STATE_BACKEND", "s3")
	t.Setenv("STATOCYST_QUEUE_BACKEND", "memory")
	t.Setenv("STATOCYST_STATE_S3_ENDPOINT", server.URL)
	t.Setenv("STATOCYST_STATE_S3_BUCKET", "state-bucket")
	t.Setenv("STATOCYST_STATE_S3_PREFIX", "statocyst-state")
	t.Setenv("STATOCYST_STATE_S3_PATH_STYLE", "true")

	control, queue, err := NewStoresFromEnv()
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	state, ok := control.(*s3StateStore)
	if !ok {
		t.Fatalf("expected s3 state control store, got %T", control)
	}
	if queue != state {
		t.Fatalf("expected queue to reuse s3 state store when queue backend=memory")
	}
}

func TestNewStoresFromEnv_S3StateAlsoHandlesS3Queue(t *testing.T) {
	server := newFakeS3StoreServer(t)
	defer server.Close()

	t.Setenv("STATOCYST_STATE_BACKEND", "s3")
	t.Setenv("STATOCYST_QUEUE_BACKEND", "s3")
	t.Setenv("STATOCYST_STATE_S3_ENDPOINT", server.URL)
	t.Setenv("STATOCYST_STATE_S3_BUCKET", "state-bucket")
	t.Setenv("STATOCYST_STATE_S3_PREFIX", "statocyst-state")
	t.Setenv("STATOCYST_STATE_S3_PATH_STYLE", "true")

	control, queue, err := NewStoresFromEnv()
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	state, ok := control.(*s3StateStore)
	if !ok {
		t.Fatalf("expected s3 state control store, got %T", control)
	}
	if queue != state {
		t.Fatalf("expected queue to reuse s3 state store when state backend=s3")
	}
}

func TestNewStoresFromEnv_S3StateAuthRequiredEndpointFailsStartup(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/")
		if path == "state-bucket" && r.Method == http.MethodGet && r.URL.Query().Get("list-type") == "2" {
			w.Header().Set("Content-Type", "application/xml")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, `<?xml version="1.0" encoding="UTF-8"?><Error><Code>InvalidArgument</Code><Message>Authorization</Message></Error>`)
			return
		}
		http.NotFound(w, r)
	}))
	defer server.Close()

	t.Setenv("STATOCYST_STATE_BACKEND", "s3")
	t.Setenv("STATOCYST_QUEUE_BACKEND", "memory")
	t.Setenv("STATOCYST_STATE_S3_ENDPOINT", server.URL)
	t.Setenv("STATOCYST_STATE_S3_BUCKET", "state-bucket")
	t.Setenv("STATOCYST_STATE_S3_PREFIX", "statocyst-state")
	t.Setenv("STATOCYST_STATE_S3_PATH_STYLE", "true")

	_, _, err := NewStoresFromEnv()
	if err == nil {
		t.Fatalf("expected startup to fail when s3 state endpoint requires authorization")
	}
	if !strings.Contains(err.Error(), "list objects status 400") {
		t.Fatalf("expected list objects status error, got %v", err)
	}
	if !strings.Contains(strings.ToLower(err.Error()), "authorization") {
		t.Fatalf("expected authorization error context, got %v", err)
	}
}

func TestNewStoresFromEnvWithMode_DegradedFallbackForS3State(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/")
		if path == "state-bucket" && r.Method == http.MethodGet && r.URL.Query().Get("list-type") == "2" {
			w.Header().Set("Content-Type", "application/xml")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, `<?xml version="1.0" encoding="UTF-8"?><Error><Code>InvalidArgument</Code><Message>Authorization</Message></Error>`)
			return
		}
		http.NotFound(w, r)
	}))
	defer server.Close()

	t.Setenv("STATOCYST_STATE_BACKEND", "s3")
	t.Setenv("STATOCYST_QUEUE_BACKEND", "memory")
	t.Setenv("STATOCYST_STATE_S3_ENDPOINT", server.URL)
	t.Setenv("STATOCYST_STATE_S3_BUCKET", "state-bucket")
	t.Setenv("STATOCYST_STATE_S3_PREFIX", "statocyst-state")
	t.Setenv("STATOCYST_STATE_S3_PATH_STYLE", "true")

	control, queue, health, err := NewStoresFromEnvWithMode(StorageStartupModeDegraded)
	if err != nil {
		t.Fatalf("expected degraded mode to continue startup, got error: %v", err)
	}
	controlMem, ok := control.(*MemoryStore)
	if !ok {
		t.Fatalf("expected memory fallback control store, got %T", control)
	}
	queueMem, ok := queue.(*MemoryStore)
	if !ok {
		t.Fatalf("expected memory fallback queue store, got %T", queue)
	}
	if controlMem != queueMem {
		t.Fatalf("expected shared memory fallback store for state+queue")
	}
	if health.StartupMode != StorageStartupModeDegraded {
		t.Fatalf("expected startup mode degraded, got %q", health.StartupMode)
	}
	if health.State.Healthy {
		t.Fatalf("expected state backend to be unhealthy in degraded mode")
	}
	if !strings.Contains(strings.ToLower(health.State.Error), "authorization") {
		t.Fatalf("expected state health error to include authorization context, got %q", health.State.Error)
	}
	if !health.Queue.Healthy {
		t.Fatalf("expected queue backend to be healthy for memory mode")
	}
	if got := health.OverallStatus(); got != "degraded" {
		t.Fatalf("expected overall status degraded, got %q", got)
	}
}

func TestNewStoresFromEnvWithMode_DegradedFallbackForS3Queue(t *testing.T) {
	t.Setenv("STATOCYST_STATE_BACKEND", "memory")
	t.Setenv("STATOCYST_QUEUE_BACKEND", "s3")
	t.Setenv("STATOCYST_QUEUE_S3_ENDPOINT", "")
	t.Setenv("STATOCYST_QUEUE_S3_BUCKET", "")

	control, queue, health, err := NewStoresFromEnvWithMode(StorageStartupModeDegraded)
	if err != nil {
		t.Fatalf("expected degraded mode to continue startup on queue failure, got: %v", err)
	}
	if _, ok := control.(*MemoryStore); !ok {
		t.Fatalf("expected memory control store, got %T", control)
	}
	if _, ok := queue.(*MemoryStore); !ok {
		t.Fatalf("expected memory queue fallback, got %T", queue)
	}
	if !health.State.Healthy {
		t.Fatalf("expected state backend healthy for memory mode")
	}
	if health.Queue.Healthy {
		t.Fatalf("expected queue backend unhealthy in degraded mode")
	}
	if !strings.Contains(strings.ToLower(health.Queue.Error), "required") {
		t.Fatalf("expected queue health error context, got %q", health.Queue.Error)
	}
	if got := health.OverallStatus(); got != "degraded" {
		t.Fatalf("expected overall status degraded, got %q", got)
	}
}

func newFakeS3StoreServer(t *testing.T) *httptest.Server {
	t.Helper()
	type obj struct {
		key  string
		data []byte
	}
	var (
		mu      sync.Mutex
		objects = make(map[string][]byte)
	)

	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/")
		if strings.HasPrefix(path, "state-bucket/") {
			key := strings.TrimPrefix(path, "state-bucket/")
			switch r.Method {
			case http.MethodPut:
				body, _ := io.ReadAll(r.Body)
				mu.Lock()
				objects[key] = body
				mu.Unlock()
				w.WriteHeader(http.StatusOK)
				return
			case http.MethodGet:
				mu.Lock()
				body, ok := objects[key]
				mu.Unlock()
				if !ok {
					http.NotFound(w, r)
					return
				}
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write(body)
				return
			case http.MethodDelete:
				mu.Lock()
				delete(objects, key)
				mu.Unlock()
				w.WriteHeader(http.StatusNoContent)
				return
			}
		}

		if path == "state-bucket" && r.Method == http.MethodGet && r.URL.Query().Get("list-type") == "2" {
			prefix := r.URL.Query().Get("prefix")
			mu.Lock()
			items := make([]obj, 0)
			for key, data := range objects {
				if strings.HasPrefix(key, prefix) {
					items = append(items, obj{key: key, data: data})
				}
			}
			mu.Unlock()
			sort.Slice(items, func(i, j int) bool { return items[i].key < items[j].key })
			type content struct {
				Key string `xml:"Key"`
			}
			type listResult struct {
				XMLName     xml.Name  `xml:"ListBucketResult"`
				IsTruncated bool      `xml:"IsTruncated"`
				Contents    []content `xml:"Contents"`
			}
			out := listResult{IsTruncated: false}
			for _, item := range items {
				_ = item.data
				out.Contents = append(out.Contents, content{Key: item.key})
			}
			w.WriteHeader(http.StatusOK)
			_ = xml.NewEncoder(w).Encode(out)
			return
		}

		http.NotFound(w, r)
	}))
}
