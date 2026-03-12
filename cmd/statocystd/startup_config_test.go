package main

import (
	"strings"
	"testing"
)

func TestCollectLaunchDiagnostics_DefaultsStateAndQueueToMemoryWithWarnings(t *testing.T) {
	diagnostics, err := collectLaunchDiagnostics(mapLookup(nil))
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	assertDiagnosticContains(t, diagnostics, "WARN", "STATOCYST_STATE_BACKEND", "defaulting to in-memory state")
	assertDiagnosticContains(t, diagnostics, "WARN", "STATOCYST_QUEUE_BACKEND", "defaulting to in-memory queue")
	assertDiagnosticContains(t, diagnostics, "WARN", "STATOCYST_CANONICAL_BASE_URL", "entity uri fields will be omitted")
}

func TestCollectLaunchDiagnostics_FailsWhenSupabaseRequiredVarsMissing(t *testing.T) {
	diagnostics, err := collectLaunchDiagnostics(mapLookup(map[string]string{
		"HUMAN_AUTH_PROVIDER": "supabase",
	}))
	if err == nil {
		t.Fatal("expected error for missing supabase configuration")
	}
	if !strings.Contains(err.Error(), "SUPABASE_URL") || !strings.Contains(err.Error(), "SUPABASE_ANON_KEY") {
		t.Fatalf("expected supabase missing vars in error, got %v", err)
	}

	assertDiagnosticContains(t, diagnostics, "ERROR", "SUPABASE_URL", "cannot validate bearer tokens")
	assertDiagnosticContains(t, diagnostics, "ERROR", "SUPABASE_ANON_KEY", "cannot validate bearer tokens")
}

func TestCollectLaunchDiagnostics_FailsWhenS3BackendsMissingRequiredVars(t *testing.T) {
	diagnostics, err := collectLaunchDiagnostics(mapLookup(map[string]string{
		"STATOCYST_STATE_BACKEND": "s3",
		"STATOCYST_QUEUE_BACKEND": "s3",
	}))
	if err == nil {
		t.Fatal("expected error for missing s3 backend configuration")
	}
	for _, name := range []string{
		"STATOCYST_STATE_S3_ENDPOINT",
		"STATOCYST_STATE_S3_BUCKET",
		"STATOCYST_QUEUE_S3_ENDPOINT",
		"STATOCYST_QUEUE_S3_BUCKET",
	} {
		if !strings.Contains(err.Error(), name) {
			t.Fatalf("expected %s in error, got %v", name, err)
		}
	}

	assertDiagnosticContains(t, diagnostics, "ERROR", "STATOCYST_STATE_S3_ENDPOINT", "cannot start without an endpoint URL")
	assertDiagnosticContains(t, diagnostics, "ERROR", "STATOCYST_QUEUE_S3_BUCKET", "cannot start without a bucket name")
	assertDiagnosticContains(t, diagnostics, "WARN", "STATOCYST_STATE_S3_REGION", "default signing region")
	assertDiagnosticContains(t, diagnostics, "WARN", "STATOCYST_QUEUE_S3_ACCESS_KEY_ID/STATOCYST_QUEUE_S3_SECRET_ACCESS_KEY", "requests will be unsigned")
}

func TestCollectLaunchDiagnostics_FailsWhenS3SigningPairIsIncomplete(t *testing.T) {
	diagnostics, err := collectLaunchDiagnostics(mapLookup(map[string]string{
		"STATOCYST_STATE_BACKEND":          "s3",
		"STATOCYST_STATE_S3_ENDPOINT":      "http://localhost:9000",
		"STATOCYST_STATE_S3_BUCKET":        "state-bucket",
		"STATOCYST_STATE_S3_ACCESS_KEY_ID": "abc123",
	}))
	if err == nil {
		t.Fatal("expected error for incomplete s3 signing pair")
	}
	if !strings.Contains(err.Error(), "STATOCYST_STATE_S3_SECRET_ACCESS_KEY") {
		t.Fatalf("expected secret access key in error, got %v", err)
	}

	assertDiagnosticContains(t, diagnostics, "ERROR", "STATOCYST_STATE_S3_SECRET_ACCESS_KEY", "requires both access key id and secret access key")
}

func mapLookup(values map[string]string) func(string) (string, bool) {
	return func(name string) (string, bool) {
		if values == nil {
			return "", false
		}
		value, ok := values[name]
		return value, ok
	}
}

func assertDiagnosticContains(t *testing.T, diagnostics []launchDiagnostic, level, name, snippet string) {
	t.Helper()
	for _, diagnostic := range diagnostics {
		if diagnostic.level != level || diagnostic.name != name {
			continue
		}
		if !strings.Contains(diagnostic.message, snippet) {
			t.Fatalf("diagnostic %s %s missing snippet %q: %q", level, name, snippet, diagnostic.message)
		}
		return
	}
	t.Fatalf("missing diagnostic %s %s", level, name)
}
