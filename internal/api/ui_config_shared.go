package api

import (
	"crypto/subtle"
	"net/http"
	"os"
	"strings"
	"time"

	"moltenhub/internal/auth"
)

type UIConfigPayloadOptions struct {
	HumanAuthName            string
	SupabaseURL              string
	SupabaseAnonKey          string
	SuperAdminEmails         map[string]struct{}
	SuperAdminDomains        map[string]struct{}
	SuperAdminReview         bool
	BindTokenTTL             time.Duration
	HeadlessMode             bool
	IncludePrivilegedEmails  bool
	IncludeStartupBootStatus bool
}

func BuildUIConfigPayload(opts UIConfigPayloadOptions) map[string]any {
	authName := strings.TrimSpace(opts.HumanAuthName)
	authConfig := map[string]any{
		"human": authName,
	}
	if authName == "dev" {
		devConfig := map[string]any{}
		if devHumanID := strings.TrimSpace(os.Getenv("DEV_LOGIN_HUMAN_ID")); devHumanID != "" {
			devConfig["human_id"] = devHumanID
		}
		if devHumanEmail := strings.ToLower(strings.TrimSpace(os.Getenv("DEV_LOGIN_HUMAN_EMAIL"))); devHumanEmail != "" {
			devConfig["human_email"] = devHumanEmail
		}
		if len(devConfig) > 0 {
			authConfig["dev"] = devConfig
		}
	}
	if authName == "supabase" {
		supabaseConfig := map[string]any{}
		if supabaseURL := strings.TrimSpace(opts.SupabaseURL); supabaseURL != "" {
			supabaseConfig["url"] = supabaseURL
		}
		if auth.IsSafeSupabaseBrowserKey(opts.SupabaseAnonKey) {
			supabaseConfig["anon_key"] = opts.SupabaseAnonKey
		}
		if len(supabaseConfig) > 0 {
			authConfig["supabase"] = supabaseConfig
		}
	}

	superAdminEmails := []string{}
	if opts.IncludePrivilegedEmails {
		superAdminEmails = auth.SortedSetValues(opts.SuperAdminEmails)
	}
	adminConfig := map[string]any{
		"review_mode":  opts.SuperAdminReview,
		"write_policy": "global_write",
	}
	if len(superAdminEmails) > 0 {
		adminConfig["emails"] = superAdminEmails
	}
	superAdminDomains := auth.SortedSetValues(opts.SuperAdminDomains)
	if len(superAdminDomains) > 0 {
		adminConfig["domains"] = superAdminDomains
	}

	payload := map[string]any{
		"auth":               authConfig,
		"dev_auto_login":     strings.EqualFold(strings.TrimSpace(os.Getenv("DEV_LOGIN_AUTO")), "true"),
		"admin":              adminConfig,
		"bind_token_ttl_sec": int(opts.BindTokenTTL.Seconds()),
		"headless_mode":      opts.HeadlessMode,
	}
	if opts.IncludeStartupBootStatus {
		payload["boot_status"] = "starting"
	}
	return payload
}

func HasUIConfigPrivilegedAccess(r *http.Request) bool {
	expectedKey := strings.TrimSpace(os.Getenv("UI_CONFIG_API_KEY"))
	if expectedKey == "" {
		return false
	}
	presentedKey := strings.TrimSpace(r.Header.Get("X-UI-Config-Key"))
	if presentedKey == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(presentedKey), []byte(expectedKey)) == 1
}
