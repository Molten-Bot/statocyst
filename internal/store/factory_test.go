package store

import "testing"

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
