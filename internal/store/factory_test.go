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
