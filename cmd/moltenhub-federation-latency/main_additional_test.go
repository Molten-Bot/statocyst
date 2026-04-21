package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"moltenhub/internal/cmdutil"
)

func TestRequestJSON(t *testing.T) {
	type recorded struct {
		method  string
		auth    string
		hasBody bool
	}
	var got recorded

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/empty":
			w.WriteHeader(http.StatusNoContent)
			return
		case "/badjson":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte("not-json"))
			return
		case "/echo":
			got.method = r.Method
			got.auth = r.Header.Get("Authorization")
			body, _ := io.ReadAll(r.Body)
			got.hasBody = len(body) > 0
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"status":"ok"}`))
			return
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	r := runner{client: server.Client()}

	status, payload, raw, err := r.requestJSON(server.URL, http.MethodGet, "/empty", nil, nil)
	if err != nil {
		t.Fatalf("requestJSON empty response returned error: %v", err)
	}
	if status != http.StatusNoContent || raw != "" || len(payload) != 0 {
		t.Fatalf("unexpected empty response handling: status=%d raw=%q payload=%v", status, raw, payload)
	}

	_, _, _, err = r.requestJSON(server.URL, http.MethodGet, "/badjson", nil, nil)
	if err == nil || !strings.Contains(err.Error(), "decode") {
		t.Fatalf("expected decode error for bad json response, got %v", err)
	}

	status, payload, _, err = r.requestJSON(server.URL, http.MethodPost, "/echo", map[string]string{"Authorization": "Bearer token-a"}, map[string]any{"hello": "world"})
	if err != nil {
		t.Fatalf("requestJSON echo returned error: %v", err)
	}
	if status != http.StatusOK || payload["status"] != "ok" {
		t.Fatalf("unexpected echo response: status=%d payload=%v", status, payload)
	}
	if got.method != http.MethodPost || got.auth != "Bearer token-a" || !got.hasBody {
		t.Fatalf("unexpected outbound request capture: %+v", got)
	}
}

func TestProbeOnceAndRunDirection(t *testing.T) {
	var mu sync.Mutex
	latestPayload := ""

	sender := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/messages/publish" {
			http.NotFound(w, r)
			return
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode sender publish request: %v", err)
		}
		mu.Lock()
		latestPayload = cmdutil.AsString(body, "payload")
		mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"status":"accepted"}`))
	}))
	defer sender.Close()

	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/messages/pull":
			mu.Lock()
			payload := latestPayload
			mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"message":{"payload":"` + payload + `"},"delivery":{"delivery_id":"d-1"}}`))
		case r.Method == http.MethodPost && r.URL.Path == "/messages/ack":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"status":"ok"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer receiver.Close()

	r := runner{
		client:        sender.Client(),
		iterations:    2,
		pullTimeoutMS: 100,
	}

	sample, err := r.probeOnce(sender.URL, "sender-token", receiver.URL, "receiver-token", "agent://receiver", "payload-1")
	if err != nil {
		t.Fatalf("probeOnce() error = %v", err)
	}
	if sample.PublishMS < 0 || sample.EndToEndMS < 0 || sample.AckMS < 0 {
		t.Fatalf("expected non-negative sample timings, got %+v", sample)
	}

	report, err := r.runDirection("na_to_eu", sender.URL, "sender-token", receiver.URL, "receiver-token", "agent://receiver")
	if err != nil {
		t.Fatalf("runDirection() error = %v", err)
	}
	if report.Label != "na_to_eu" {
		t.Fatalf("unexpected report label: %q", report.Label)
	}
	if report.EndToEnd.Count != 2 || report.Publish.Count != 2 || report.Ack.Count != 2 {
		t.Fatalf("unexpected report counts: %+v", report)
	}
}

func TestProbeOnceErrorWhenPublishDropped(t *testing.T) {
	sender := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"result":{"status":"dropped"},"reason":"queue full"}`))
	}))
	defer sender.Close()

	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	defer receiver.Close()

	r := runner{client: sender.Client(), pullTimeoutMS: 100}
	_, err := r.probeOnce(sender.URL, "sender-token", receiver.URL, "receiver-token", "agent://receiver", "payload")
	if err == nil || !strings.Contains(err.Error(), "publish dropped") {
		t.Fatalf("expected publish dropped error, got %v", err)
	}
}

func TestRuntimeStatusHelpersAndReporting(t *testing.T) {
	if got := runtimeStatus(nil); got != "" {
		t.Fatalf("expected empty runtime status for nil payload, got %q", got)
	}
	if got := runtimeStatus(map[string]any{"status": "ok"}); got != "ok" {
		t.Fatalf("expected top-level runtime status, got %q", got)
	}
	if got := runtimeStatus(map[string]any{"result": map[string]any{"status": "queued"}}); got != "queued" {
		t.Fatalf("expected nested runtime status, got %q", got)
	}

	obj, err := cmdutil.RequireObject(map[string]any{"message": map[string]any{"payload": "x"}}, "message")
	if err != nil {
		t.Fatalf("unexpected requireObject error: %v", err)
	}
	if cmdutil.AsString(obj, "payload") != "x" {
		t.Fatalf("unexpected payload value: %v", obj)
	}
	if _, err := cmdutil.RequireObject(map[string]any{"message": "bad"}, "message"); err == nil {
		t.Fatal("expected requireObject to fail for non-object")
	}

	if got := percentileNearestRank(nil, 0.9); got != 0 {
		t.Fatalf("expected empty percentile to return zero, got %d", got)
	}

	oldStdout := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("create pipe: %v", err)
	}
	os.Stdout = w

	printDirectionReport(directionReport{Label: "na_to_eu", EndToEnd: latencyStats{Count: 1}, Publish: latencyStats{}, Ack: latencyStats{}})

	_ = w.Close()
	os.Stdout = oldStdout
	out, _ := io.ReadAll(r)
	if !strings.Contains(string(out), "na_to_eu") {
		t.Fatalf("expected report output to contain label, got %q", string(out))
	}
}

func TestRunDirectionReturnsIterationError(t *testing.T) {
	sender := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":"boom"}`))
	}))
	defer sender.Close()

	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	defer receiver.Close()

	r := runner{client: sender.Client(), iterations: 1, pullTimeoutMS: 100, verbose: true}
	_, err := r.runDirection("broken", sender.URL, "sender-token", receiver.URL, "receiver-token", "agent://receiver")
	if err == nil || !strings.Contains(err.Error(), "iteration 1") {
		t.Fatalf("expected iteration-wrapped error, got %v", err)
	}
}

func TestPrintDirectionReportStableFormat(t *testing.T) {
	oldStdout := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe error: %v", err)
	}
	os.Stdout = w

	printDirectionReport(directionReport{
		Label:    "eu_to_na",
		Publish:  latencyStats{Avg: 10.5, P50: 9, P95: 14, P99: 15, Max: 20},
		EndToEnd: latencyStats{Count: 3, Avg: 12.0, P50: 11, P95: 18, P99: 18, Max: 21},
		Ack:      latencyStats{Avg: 2.0, P50: 2, P95: 3, P99: 3, Max: 4},
	})

	_ = w.Close()
	os.Stdout = oldStdout
	out, _ := io.ReadAll(r)
	text := string(out)
	if !strings.Contains(text, "eu_to_na") || !strings.Contains(text, "samples=3") {
		t.Fatalf("unexpected report text: %q", text)
	}
}

func TestProbeOnceAckStatusError(t *testing.T) {
	sender := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"status":"accepted"}`))
	}))
	defer sender.Close()

	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/messages/pull":
			_, _ = w.Write([]byte(`{"message":{"payload":"payload"},"delivery":{"delivery_id":"d-1"}}`))
		case "/messages/ack":
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"error":"ack_failed"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer receiver.Close()

	r := runner{client: &http.Client{Timeout: 2 * time.Second}, pullTimeoutMS: 100}
	_, err := r.probeOnce(sender.URL, "sender-token", receiver.URL, "receiver-token", "agent://receiver", "payload")
	if err == nil || !strings.Contains(err.Error(), "ack expected 200") {
		t.Fatalf("expected ack status error, got %v", err)
	}
}
