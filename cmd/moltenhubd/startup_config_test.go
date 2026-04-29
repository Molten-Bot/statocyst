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

	assertDiagnosticContains(t, diagnostics, "WARN", "MOLTENHUB_STATE_BACKEND", "defaulting to in-memory state")
	assertDiagnosticContains(t, diagnostics, "WARN", "MOLTENHUB_QUEUE_BACKEND", "defaulting to in-memory queue")
	assertDiagnosticContains(t, diagnostics, "WARN", "MOLTENHUB_CANONICAL_BASE_URL", "entity uri fields will be omitted")
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

func TestCollectLaunchDiagnostics_WarnsWhenSupabaseAnonKeyIsSecretLike(t *testing.T) {
	// Fake secret-shaped fixture; required to verify secret-class Supabase keys stay redacted.
	diagnostics, err := collectLaunchDiagnostics(mapLookup(map[string]string{
		"HUMAN_AUTH_PROVIDER": "supabase",
		"SUPABASE_URL":        "https://example.supabase.co",
		"SUPABASE_ANON_KEY":   "sb_secret_not_allowed",
	}))
	if err != nil {
		t.Fatalf("expected no startup error for secret-class SUPABASE_ANON_KEY, got %v", err)
	}
	assertDiagnosticContains(t, diagnostics, "WARN", "SUPABASE_ANON_KEY", "/v1/ui/config will omit auth.supabase.anon_key")
}

func TestCollectLaunchDiagnostics_AcceptsSupabasePublishableKey(t *testing.T) {
	diagnostics, err := collectLaunchDiagnostics(mapLookup(map[string]string{
		"HUMAN_AUTH_PROVIDER": "supabase",
		"SUPABASE_URL":        "https://example.supabase.co",
		"SUPABASE_ANON_KEY":   "sb_publishable_ok",
	}))
	if err != nil {
		t.Fatalf("expected no error for publishable key, got %v", err)
	}
	for _, diagnostic := range diagnostics {
		if diagnostic.level == "ERROR" && diagnostic.name == "SUPABASE_ANON_KEY" {
			t.Fatalf("did not expect SUPABASE_ANON_KEY error diagnostic, got %+v", diagnostic)
		}
	}
}

func TestCollectLaunchDiagnostics_WarnsForUnknownSupabaseKeyFormat(t *testing.T) {
	diagnostics, err := collectLaunchDiagnostics(mapLookup(map[string]string{
		"HUMAN_AUTH_PROVIDER": "supabase",
		"SUPABASE_URL":        "https://example.supabase.co",
		"SUPABASE_ANON_KEY":   "unknown_key_format",
	}))
	if err != nil {
		t.Fatalf("expected no startup error for unknown SUPABASE_ANON_KEY format, got %v", err)
	}
	assertDiagnosticContains(t, diagnostics, "WARN", "SUPABASE_ANON_KEY", "/v1/ui/config will omit auth.supabase.anon_key")
}

func TestCollectLaunchDiagnostics_FailsWhenS3BackendsMissingRequiredVars(t *testing.T) {
	diagnostics, err := collectLaunchDiagnostics(mapLookup(map[string]string{
		"MOLTENHUB_STATE_BACKEND": "s3",
		"MOLTENHUB_QUEUE_BACKEND": "s3",
	}))
	if err == nil {
		t.Fatal("expected error for missing s3 backend configuration")
	}
	for _, name := range []string{
		"MOLTENHUB_STATE_S3_ENDPOINT",
		"MOLTENHUB_STATE_S3_BUCKET",
		"MOLTENHUB_QUEUE_S3_ENDPOINT",
		"MOLTENHUB_QUEUE_S3_BUCKET",
	} {
		if !strings.Contains(err.Error(), name) {
			t.Fatalf("expected %s in error, got %v", name, err)
		}
	}

	assertDiagnosticContains(t, diagnostics, "ERROR", "MOLTENHUB_STATE_S3_ENDPOINT", "cannot start without an endpoint URL")
	assertDiagnosticContains(t, diagnostics, "ERROR", "MOLTENHUB_QUEUE_S3_BUCKET", "cannot start without a bucket name")
	assertDiagnosticContains(t, diagnostics, "WARN", "MOLTENHUB_STATE_S3_REGION", "default signing region")
	assertDiagnosticContains(t, diagnostics, "WARN", "MOLTENHUB_QUEUE_S3_ACCESS_KEY_ID/MOLTENHUB_QUEUE_S3_SECRET_ACCESS_KEY", "requests will be unsigned")
}

func TestCollectLaunchDiagnostics_FailsWhenS3SigningPairIsIncomplete(t *testing.T) {
	diagnostics, err := collectLaunchDiagnostics(mapLookup(map[string]string{
		"MOLTENHUB_STATE_BACKEND":          "s3",
		"MOLTENHUB_STATE_S3_ENDPOINT":      "http://localhost:9000",
		"MOLTENHUB_STATE_S3_BUCKET":        "state-bucket",
		"MOLTENHUB_STATE_S3_ACCESS_KEY_ID": "abc123",
	}))
	if err == nil {
		t.Fatal("expected error for incomplete s3 signing pair")
	}
	if !strings.Contains(err.Error(), "MOLTENHUB_STATE_S3_SECRET_ACCESS_KEY") {
		t.Fatalf("expected secret access key in error, got %v", err)
	}

	assertDiagnosticContains(t, diagnostics, "ERROR", "MOLTENHUB_STATE_S3_SECRET_ACCESS_KEY", "requires both access key id and secret access key")
}

func TestCollectLaunchDiagnostics_FailsWhenCORSAllowedOriginsIsInvalid(t *testing.T) {
	diagnostics, err := collectLaunchDiagnostics(mapLookup(map[string]string{
		"MOLTENHUB_CORS_ALLOWED_ORIGINS": "app.example.com/path",
	}))
	if err == nil {
		t.Fatal("expected error for invalid CORS allowed origins")
	}
	if !strings.Contains(err.Error(), "MOLTENHUB_CORS_ALLOWED_ORIGINS") {
		t.Fatalf("expected MOLTENHUB_CORS_ALLOWED_ORIGINS in error, got %v", err)
	}

	assertDiagnosticContains(t, diagnostics, "ERROR", "MOLTENHUB_CORS_ALLOWED_ORIGINS", "path is not allowed")
}

func TestCollectLaunchDiagnostics_AcceptsHostShorthandCORSAllowedOrigins(t *testing.T) {
	diagnostics, err := collectLaunchDiagnostics(mapLookup(map[string]string{
		"MOLTENHUB_CORS_ALLOWED_ORIGINS": "x.site.com,y.site.com",
	}))
	if err != nil {
		t.Fatalf("expected no startup error for host shorthand CORS allowed origins, got %v", err)
	}
	for _, diagnostic := range diagnostics {
		if diagnostic.level == "ERROR" && diagnostic.name == "MOLTENHUB_CORS_ALLOWED_ORIGINS" {
			t.Fatalf("did not expect MOLTENHUB_CORS_ALLOWED_ORIGINS error diagnostic, got %+v", diagnostic)
		}
	}
}

func TestDiagnosticLogValueRedactsSensitiveValues(t *testing.T) {
	// Fake secret-shaped fixture; required to verify diagnostic logging redaction.
	if got := diagnosticLogValue("SUPABASE_ANON_KEY", "secret-value"); got != "<redacted>" {
		t.Fatalf("expected sensitive config to be redacted, got %q", got)
	}
	if got := diagnosticLogValue("HUMAN_AUTH_PROVIDER", "supabase"); got != "supabase" {
		t.Fatalf("expected non-sensitive config to remain visible, got %q", got)
	}
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
