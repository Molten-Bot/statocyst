package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"moltenhub/internal/api"
	"moltenhub/internal/auth"
	"moltenhub/internal/store"
)

func TestSetStartupPhaseAndStorageHealth(t *testing.T) {
	h := newBootstrapHandler(store.StorageStartupModeStrict, "", "")

	initialPhase := h.phase
	h.SetStartupPhase(" ", time.Time{})
	if h.phase != initialPhase {
		t.Fatalf("expected empty phase update to be ignored, got %q", h.phase)
	}

	phaseStarted := time.Now().UTC().Add(-time.Minute).Truncate(time.Second)
	h.SetStartupPhase("hydrating", phaseStarted)
	if h.phase != "hydrating" {
		t.Fatalf("expected phase to update, got %q", h.phase)
	}
	if !h.phaseStartedAt.Equal(phaseStarted) {
		t.Fatalf("expected phase start time %v, got %v", phaseStarted, h.phaseStartedAt)
	}

	health := store.StorageHealthStatus{
		StartupMode: store.StorageStartupModeDegraded,
		State: store.StorageBackendHealth{
			Backend: "s3",
			Healthy: true,
			Error:   "",
		},
		Queue: store.StorageBackendHealth{
			Backend: "",
			Healthy: false,
			Error:   "queue startup failed",
		},
	}
	h.SetStartupStorageHealth(health)
	if h.startupMode != store.StorageStartupModeDegraded {
		t.Fatalf("expected startup mode to update, got %q", h.startupMode)
	}
	if h.stateBackend != "s3" {
		t.Fatalf("expected state backend s3, got %q", h.stateBackend)
	}
	if h.queueBackend != "memory" {
		t.Fatalf("expected queue backend to keep default memory when health backend empty, got %q", h.queueBackend)
	}
	if h.queueError != "queue startup failed" {
		t.Fatalf("unexpected queue error: %q", h.queueError)
	}
}

func TestConfiguredBackendFromEnv(t *testing.T) {
	if got := configuredBackendFromEnv("", "memory"); got != "memory" {
		t.Fatalf("expected fallback backend, got %q", got)
	}
	if got := configuredBackendFromEnv("  S3 ", "memory"); got != "s3" {
		t.Fatalf("expected normalized backend s3, got %q", got)
	}
}

func TestHasStartupUIConfigPrivilegedAccess(t *testing.T) {
	t.Setenv("UI_CONFIG_API_KEY", "")
	req := httptest.NewRequest(http.MethodGet, "/v1/ui/config", nil)
	if api.HasUIConfigPrivilegedAccess(req) {
		t.Fatal("expected privileged access disabled when api key is unset")
	}

	t.Setenv("UI_CONFIG_API_KEY", "secret-key")
	if api.HasUIConfigPrivilegedAccess(req) {
		t.Fatal("expected privileged access to fail without header")
	}

	req.Header.Set("X-UI-Config-Key", "wrong")
	if api.HasUIConfigPrivilegedAccess(req) {
		t.Fatal("expected privileged access to fail with wrong header")
	}

	req.Header.Set("X-UI-Config-Key", "secret-key")
	if !api.HasUIConfigPrivilegedAccess(req) {
		t.Fatal("expected privileged access with matching API key")
	}
}

func TestParseCSVSetSortedValuesAndSuperAdminResolution(t *testing.T) {
	values := auth.ParseCSVSet(" Alice@example.com, @Example.com,alice@example.com, ", true)
	sorted := auth.SortedSetValues(values)
	if len(sorted) != 2 {
		t.Fatalf("expected two unique set values, got %v", sorted)
	}
	if sorted[0] != "alice@example.com" || sorted[1] != "example.com" {
		t.Fatalf("unexpected sorted values: %v", sorted)
	}

	h := newBootstrapHandler(store.StorageStartupModeStrict, "memory", "memory", bootstrapOptions{
		humanAuth:         auth.NewDevHumanAuthProvider(),
		superAdminEmails:  "alice@example.com",
		superAdminDomains: "admins.test",
	})
	if !h.isSuperAdmin(auth.HumanIdentity{Email: "alice@example.com", EmailVerified: true}) {
		t.Fatal("expected exact email super-admin match")
	}
	if !h.isSuperAdmin(auth.HumanIdentity{Email: "bob@admins.test", EmailVerified: true}) {
		t.Fatal("expected domain super-admin match")
	}
	if h.isSuperAdmin(auth.HumanIdentity{Email: "bob@admins.test", EmailVerified: false}) {
		t.Fatal("expected unverified identity to be non-admin")
	}
	if h.isSuperAdmin(auth.HumanIdentity{Email: "invalid-email", EmailVerified: true}) {
		t.Fatal("expected malformed email to be non-admin")
	}
}
