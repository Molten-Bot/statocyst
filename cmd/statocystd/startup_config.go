package main

import (
	"fmt"
	"log"
	"os"
	"sort"
	"strings"

	"statocyst/internal/api"
	"statocyst/internal/auth"
	"statocyst/internal/store"
)

const (
	defaultAddr             = ":8080"
	defaultBindTTLMinutes   = "15"
	defaultMetadataMaxBytes = "196608"
	defaultAppName          = "Statocyst"
	defaultStateBackend     = "memory"
	defaultQueueBackend     = "memory"
	defaultStateS3Region    = "us-east-1"
	defaultStateS3Prefix    = "statocyst-state"
	defaultQueueS3Region    = "us-east-1"
	defaultQueueS3Prefix    = "statocyst-queue"
)

type launchDiagnostic struct {
	level   string
	name    string
	value   string
	message string
}

func validateLaunchConfiguration() error {
	diagnostics, err := collectLaunchDiagnostics(os.LookupEnv)
	for _, diagnostic := range diagnostics {
		if diagnostic.level == "" {
			continue
		}
		log.Printf("%s: %s value=%q: %s", diagnostic.level, diagnostic.name, diagnosticLogValue(diagnostic.name, diagnostic.value), diagnostic.message)
	}
	return err
}

func diagnosticLogValue(name, value string) string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return trimmed
	}

	sensitiveHints := []string{"SECRET", "TOKEN", "KEY", "PASSWORD", "PRIVATE", "BEARER"}
	upperName := strings.ToUpper(strings.TrimSpace(name))
	for _, hint := range sensitiveHints {
		if strings.Contains(upperName, hint) {
			return "<redacted>"
		}
	}

	return trimmed
}

func collectLaunchDiagnostics(lookup func(string) (string, bool)) ([]launchDiagnostic, error) {
	var diagnostics []launchDiagnostic
	var failures []string

	providerRaw := envValue(lookup, "HUMAN_AUTH_PROVIDER")
	provider := strings.ToLower(providerRaw)
	switch provider {
	case "":
		provider = "dev"
		diagnostics = append(diagnostics, warnUnset(
			"HUMAN_AUTH_PROVIDER",
			"dev",
			"defaulting to dev auth; callers must present X-Human-Id/X-Human-Email headers instead of Supabase bearer tokens",
		))
	case "dev", "supabase":
	default:
		provider = "dev"
		diagnostics = append(diagnostics, launchDiagnostic{
			level:   "WARN",
			name:    "HUMAN_AUTH_PROVIDER",
			value:   providerRaw,
			message: `unsupported auth provider; defaulting to "dev" and expecting X-Human-Id/X-Human-Email headers`,
		})
	}

	if raw := envValue(lookup, "STATOCYST_STORAGE_STARTUP_MODE"); raw == "" {
		diagnostics = append(diagnostics, warnUnset(
			"STATOCYST_STORAGE_STARTUP_MODE",
			string(store.StorageStartupModeStrict),
			"defaulting to strict startup; configured storage dependency failures will abort launch",
		))
	} else if _, err := store.ParseStorageStartupMode(raw); err != nil {
		failures = append(failures, "STATOCYST_STORAGE_STARTUP_MODE")
		diagnostics = append(diagnostics, launchDiagnostic{
			level:   "ERROR",
			name:    "STATOCYST_STORAGE_STARTUP_MODE",
			value:   raw,
			message: err.Error(),
		})
	}

	stateBackendRaw := strings.ToLower(envValue(lookup, "STATOCYST_STATE_BACKEND"))
	stateBackend := defaultStateBackend
	switch stateBackendRaw {
	case "":
		diagnostics = append(diagnostics, warnUnset(
			"STATOCYST_STATE_BACKEND",
			defaultStateBackend,
			"defaulting to in-memory state; control-plane state will be lost on process restart because S3 is not configured",
		))
	case "memory", "s3":
		stateBackend = stateBackendRaw
	default:
		failures = append(failures, "STATOCYST_STATE_BACKEND")
		diagnostics = append(diagnostics, launchDiagnostic{
			level:   "ERROR",
			name:    "STATOCYST_STATE_BACKEND",
			value:   stateBackendRaw,
			message: `unsupported backend; expected "memory" or "s3"`,
		})
	}

	queueBackendRaw := strings.ToLower(envValue(lookup, "STATOCYST_QUEUE_BACKEND"))
	queueBackend := defaultQueueBackend
	switch queueBackendRaw {
	case "":
		diagnostics = append(diagnostics, warnUnset(
			"STATOCYST_QUEUE_BACKEND",
			defaultQueueBackend,
			"defaulting to in-memory queue; queued messages will be lost on process restart because S3 is not configured",
		))
	case "memory", "s3":
		queueBackend = queueBackendRaw
	default:
		failures = append(failures, "STATOCYST_QUEUE_BACKEND")
		diagnostics = append(diagnostics, launchDiagnostic{
			level:   "ERROR",
			name:    "STATOCYST_QUEUE_BACKEND",
			value:   queueBackendRaw,
			message: `unsupported backend; expected "memory" or "s3"`,
		})
	}

	diagnostics = appendOptionalWarnings(diagnostics,
		warnIfUnset(lookup, "STATOCYST_ADDR", defaultAddr, "server will listen on the default bind address"),
		warnIfUnset(lookup, "STATOCYST_UI_DEV_MODE", "false", "embedded UI assets will be served; local file hot-reload stays disabled"),
		warnIfUnset(lookup, "STATOCYST_ENABLE_LOCAL_CORS", "false", "browser calls from local file:// or alternate localhost origins remain blocked"),
		warnIfUnset(lookup, "STATOCYST_CORS_ALLOWED_ORIGINS", "<unset>", "browser calls from other origins remain blocked unless explicitly allowlisted"),
		warnIfUnset(lookup, "STATOCYST_HEADLESS_MODE", "false", "the built-in UI stays enabled"),
		warnIfUnset(lookup, "STATOCYST_CANONICAL_BASE_URL", "<unset>", "entity uri fields will be omitted from responses and snapshots"),
		warnIfUnset(lookup, "STATOCYST_ADMIN_SNAPSHOT_KEY", "<unset>", "snapshot endpoint access falls back to admin identity checks only"),
		warnIfUnset(lookup, "SUPER_ADMIN_EMAILS", "<unset>", "no explicit super-admin email allowlist is configured"),
		warnIfUnset(lookup, "SUPER_ADMIN_DOMAINS", "<unset>", "no domain-wide super-admin allowlist is configured"),
		warnIfUnset(lookup, "SUPER_ADMIN_REVIEW_MODE", "false", "admin identities behave like normal users instead of read-only reviewers"),
		warnIfUnset(lookup, "BIND_TOKEN_TTL_MINUTES", defaultBindTTLMinutes, "bind tokens will expire after the default lifetime"),
		warnIfUnset(lookup, "STATOCYST_MAX_METADATA_BYTES", defaultMetadataMaxBytes, "metadata writes will use the default size limit"),
		warnIfUnset(lookup, "STATOCYST_APP_NAME", defaultAppName, "the built-in UI will display the default application name"),
		warnIfUnset(lookup, "UI_CONFIG_API_KEY", "<unset>", "privileged /v1/ui/config responses stay disabled"),
		warnIfUnset(lookup, "STATOCYST_ENTITIES_METADATA_KEY", "<unset>", "the entities metadata endpoint cannot be called with a shared system key"),
	)

	headlessMode := strings.EqualFold(envValue(lookup, "STATOCYST_HEADLESS_MODE"), "true")
	if raw := envValue(lookup, "STATOCYST_CORS_ALLOWED_ORIGINS"); raw != "" {
		if _, err := api.ParseCORSAllowedOrigins(raw); err != nil {
			failures = append(failures, "STATOCYST_CORS_ALLOWED_ORIGINS")
			diagnostics = append(diagnostics, launchDiagnostic{
				level:   "ERROR",
				name:    "STATOCYST_CORS_ALLOWED_ORIGINS",
				value:   raw,
				message: err.Error(),
			})
		}
	}
	if headlessMode {
		diagnostics = appendOptionalWarnings(diagnostics, warnIfUnset(
			lookup,
			"STATOCYST_HEADLESS_MODE_URL",
			"<unset>",
			"non-API page requests will return 404 instead of redirecting while headless mode is enabled",
		))
	}

	switch provider {
	case "dev":
		diagnostics = appendOptionalWarnings(diagnostics,
			warnIfUnset(lookup, "DEV_LOGIN_HUMAN_ID", "<unset>", "the built-in dev login page cannot complete a local login automatically"),
			warnIfUnset(lookup, "DEV_LOGIN_HUMAN_EMAIL", "<unset>", "the built-in dev login page cannot prefill a local email address"),
			warnIfUnset(lookup, "DEV_LOGIN_AUTO", "false", "the built-in dev login page will wait for manual action instead of auto-redirecting"),
		)
	case "supabase":
		requireEnv(lookup, &diagnostics, &failures, "SUPABASE_URL", "Supabase auth provider cannot validate bearer tokens without the project URL")
		requireEnv(lookup, &diagnostics, &failures, "SUPABASE_ANON_KEY", "Supabase auth provider cannot validate bearer tokens without the anon key")
		if key := envValue(lookup, "SUPABASE_ANON_KEY"); key != "" && !auth.IsSafeSupabaseBrowserKey(key) {
			failures = append(failures, "SUPABASE_ANON_KEY")
			diagnostics = append(diagnostics, launchDiagnostic{
				level:   "ERROR",
				name:    "SUPABASE_ANON_KEY",
				value:   key,
				message: "must be a browser-safe Supabase anon/publishable key; secret or service-role keys are not allowed",
			})
		}
	}

	if stateBackend == "s3" {
		requireEnv(lookup, &diagnostics, &failures, "STATOCYST_STATE_S3_ENDPOINT", "S3 state backend cannot start without an endpoint URL")
		requireEnv(lookup, &diagnostics, &failures, "STATOCYST_STATE_S3_BUCKET", "S3 state backend cannot start without a bucket name")
		diagnostics = appendOptionalWarnings(diagnostics,
			warnIfUnset(lookup, "STATOCYST_STATE_S3_REGION", defaultStateS3Region, "S3 state requests will use the default signing region"),
			warnIfUnset(lookup, "STATOCYST_STATE_S3_PREFIX", defaultStateS3Prefix, "state objects will be stored under the default prefix"),
			warnIfUnset(lookup, "STATOCYST_STATE_S3_PATH_STYLE", "true", "state S3 requests will default to path-style addressing"),
		)
		requirePairedEnv(
			lookup,
			&diagnostics,
			&failures,
			"STATOCYST_STATE_S3_ACCESS_KEY_ID",
			"STATOCYST_STATE_S3_SECRET_ACCESS_KEY",
			"state S3 request signing requires both access key id and secret access key",
		)
		if envValue(lookup, "STATOCYST_STATE_S3_ACCESS_KEY_ID") == "" && envValue(lookup, "STATOCYST_STATE_S3_SECRET_ACCESS_KEY") == "" {
			diagnostics = appendOptionalWarnings(diagnostics, warnUnset(
				"STATOCYST_STATE_S3_ACCESS_KEY_ID/STATOCYST_STATE_S3_SECRET_ACCESS_KEY",
				"<unset>",
				"state S3 requests will be unsigned; this only works for trusted or publicly accessible S3-compatible endpoints",
			))
		}
	}

	if queueBackend == "s3" {
		requireEnv(lookup, &diagnostics, &failures, "STATOCYST_QUEUE_S3_ENDPOINT", "S3 queue backend cannot start without an endpoint URL")
		requireEnv(lookup, &diagnostics, &failures, "STATOCYST_QUEUE_S3_BUCKET", "S3 queue backend cannot start without a bucket name")
		diagnostics = appendOptionalWarnings(diagnostics,
			warnIfUnset(lookup, "STATOCYST_QUEUE_S3_REGION", defaultQueueS3Region, "S3 queue requests will use the default signing region"),
			warnIfUnset(lookup, "STATOCYST_QUEUE_S3_PREFIX", defaultQueueS3Prefix, "queue objects will be stored under the default prefix"),
			warnIfUnset(lookup, "STATOCYST_QUEUE_S3_PATH_STYLE", "true", "queue S3 requests will default to path-style addressing"),
		)
		requirePairedEnv(
			lookup,
			&diagnostics,
			&failures,
			"STATOCYST_QUEUE_S3_ACCESS_KEY_ID",
			"STATOCYST_QUEUE_S3_SECRET_ACCESS_KEY",
			"queue S3 request signing requires both access key id and secret access key",
		)
		if envValue(lookup, "STATOCYST_QUEUE_S3_ACCESS_KEY_ID") == "" && envValue(lookup, "STATOCYST_QUEUE_S3_SECRET_ACCESS_KEY") == "" {
			diagnostics = appendOptionalWarnings(diagnostics, warnUnset(
				"STATOCYST_QUEUE_S3_ACCESS_KEY_ID/STATOCYST_QUEUE_S3_SECRET_ACCESS_KEY",
				"<unset>",
				"queue S3 requests will be unsigned; this only works for trusted or publicly accessible S3-compatible endpoints",
			))
		}
	}

	if len(failures) == 0 {
		return diagnostics, nil
	}
	sort.Strings(failures)
	return diagnostics, fmt.Errorf("launch configuration invalid: %s", strings.Join(uniqueStrings(failures), ", "))
}

func requireEnv(lookup func(string) (string, bool), diagnostics *[]launchDiagnostic, failures *[]string, name, message string) {
	if envValue(lookup, name) != "" {
		return
	}
	*diagnostics = append(*diagnostics, launchDiagnostic{
		level:   "ERROR",
		name:    name,
		value:   "<unset>",
		message: message,
	})
	*failures = append(*failures, name)
}

func requirePairedEnv(lookup func(string) (string, bool), diagnostics *[]launchDiagnostic, failures *[]string, left, right, message string) {
	leftValue := envValue(lookup, left)
	rightValue := envValue(lookup, right)
	if leftValue == "" && rightValue == "" {
		return
	}
	if leftValue == "" {
		*diagnostics = append(*diagnostics, launchDiagnostic{
			level:   "ERROR",
			name:    left,
			value:   "<unset>",
			message: message,
		})
		*failures = append(*failures, left)
	}
	if rightValue == "" {
		*diagnostics = append(*diagnostics, launchDiagnostic{
			level:   "ERROR",
			name:    right,
			value:   "<unset>",
			message: message,
		})
		*failures = append(*failures, right)
	}
}

func warnIfUnset(lookup func(string) (string, bool), name, fallback, message string) launchDiagnostic {
	if value := envValue(lookup, name); value != "" {
		return launchDiagnostic{}
	}
	return warnUnset(name, fallback, message)
}

func warnUnset(name, fallback, message string) launchDiagnostic {
	prefix := fmt.Sprintf("using %q", fallback)
	if fallback == "<unset>" {
		prefix = "leaving value unset"
	}
	return launchDiagnostic{
		level:   "WARN",
		name:    name,
		value:   "<unset>",
		message: fmt.Sprintf("%s; %s", prefix, message),
	}
}

func envValue(lookup func(string) (string, bool), name string) string {
	value, ok := lookup(name)
	if !ok {
		return ""
	}
	return strings.TrimSpace(value)
}

func uniqueStrings(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	out := values[:0]
	var prev string
	for i, value := range values {
		if i == 0 || value != prev {
			out = append(out, value)
			prev = value
		}
	}
	return out
}

func appendOptionalWarnings(existing []launchDiagnostic, extra ...launchDiagnostic) []launchDiagnostic {
	for _, diagnostic := range extra {
		if diagnostic.level == "" {
			continue
		}
		existing = append(existing, diagnostic)
	}
	return existing
}
