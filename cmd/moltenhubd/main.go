package main

import (
	"bufio"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"moltenhub/internal/api"
	"moltenhub/internal/auth"
	"moltenhub/internal/longpoll"
	"moltenhub/internal/store"
)

func main() {
	loadDotEnv(".env")
	if err := validateLaunchConfiguration(); err != nil {
		log.Fatalf("%v", err)
	}

	addr := os.Getenv("MOLTENHUB_ADDR")
	if addr == "" {
		addr = ":8080"
	}

	storageStartupMode, err := store.StorageStartupModeFromEnv()
	if err != nil {
		log.Fatalf("storage startup mode configuration error: %v", err)
	}

	waiters := longpoll.NewWaiters()
	humanAuth := auth.NewHumanAuthProviderFromEnv()
	bindTTL := 15 * time.Minute
	superAdminReviewMode := false
	headlessMode := false
	if raw := os.Getenv("BIND_TOKEN_TTL_MINUTES"); raw != "" {
		if mins, err := strconv.Atoi(raw); err == nil && mins > 0 {
			bindTTL = time.Duration(mins) * time.Minute
		}
	}
	if raw := strings.TrimSpace(os.Getenv("SUPER_ADMIN_REVIEW_MODE")); raw != "" {
		if mode, err := strconv.ParseBool(raw); err == nil {
			superAdminReviewMode = mode
		}
	}
	if raw := strings.TrimSpace(os.Getenv("MOLTENHUB_HEADLESS_MODE")); raw != "" {
		if mode, err := strconv.ParseBool(raw); err == nil {
			headlessMode = mode
		}
	}
	enableLocalCORS := envBool("MOLTENHUB_ENABLE_LOCAL_CORS", false)
	allowedCORSOrigins, err := api.ParseCORSAllowedOrigins(os.Getenv("MOLTENHUB_CORS_ALLOWED_ORIGINS"))
	if err != nil {
		log.Fatalf("CORS allowed origins configuration error: %v", err)
	}
	bootstrap := newBootstrapHandler(
		storageStartupMode,
		configuredBackendFromEnv(os.Getenv("MOLTENHUB_STATE_BACKEND"), "memory"),
		configuredBackendFromEnv(os.Getenv("MOLTENHUB_QUEUE_BACKEND"), "memory"),
		bootstrapOptions{
			humanAuth:         humanAuth,
			supabaseURL:       os.Getenv("SUPABASE_URL"),
			supabaseAnonKey:   os.Getenv("SUPABASE_ANON_KEY"),
			superAdminEmails:  os.Getenv("SUPER_ADMIN_EMAILS"),
			superAdminDomains: os.Getenv("SUPER_ADMIN_DOMAINS"),
			superAdminReview:  superAdminReviewMode,
			bindTokenTTL:      bindTTL,
			headlessMode:      headlessMode,
		},
	)
	startupStartedAt := time.Now().UTC()
	currentPhase := "boot"
	phaseStartedAt := startupStartedAt
	phaseDurationsMS := make(map[string]int64)
	bootstrap.SetStartupPhase(currentPhase, phaseStartedAt)
	setStartupPhase := func(name string) {
		now := time.Now().UTC()
		if currentPhase != "" {
			elapsed := now.Sub(phaseStartedAt).Milliseconds()
			if elapsed < 0 {
				elapsed = 0
			}
			phaseDurationsMS[currentPhase] = elapsed
		}
		currentPhase = strings.TrimSpace(name)
		phaseStartedAt = now
		bootstrap.SetStartupPhase(currentPhase, phaseStartedAt)
	}

	server := &http.Server{
		Addr:    addr,
		Handler: bootstrap,
	}

	listener, err := net.Listen("tcp", addr)
	if err != nil {
		log.Fatalf("listen failed: %v", err)
	}

	go func() {
		setStartupPhase("storage_hydrate")
		controlStore, queueStore, storageHealth, storeErr := store.NewStoresFromEnvWithMode(storageStartupMode)
		if storeErr != nil {
			log.Fatalf("storage backend configuration error: %v", storeErr)
		}
		bootstrap.SetStartupStorageHealth(storageHealth)
		if storageHealth.OverallStatus() != "ok" {
			log.Printf(
				"storage backend degraded: mode=%s state_backend=%s state_error=%q queue_backend=%s queue_error=%q",
				storageHealth.StartupMode,
				storageHealth.State.Backend,
				storageHealth.State.Error,
				storageHealth.Queue.Backend,
				storageHealth.Queue.Error,
			)
		}

		handler := api.NewHandler(
			controlStore,
			queueStore,
			waiters,
			humanAuth,
			os.Getenv("MOLTENHUB_CANONICAL_BASE_URL"),
			os.Getenv("SUPABASE_URL"),
			os.Getenv("SUPABASE_ANON_KEY"),
			os.Getenv("MOLTENHUB_ADMIN_SNAPSHOT_KEY"),
			os.Getenv("SUPER_ADMIN_EMAILS"),
			os.Getenv("SUPER_ADMIN_DOMAINS"),
			superAdminReviewMode,
			bindTTL,
			headlessMode,
		)
		handler.SetHeadlessModeRedirectURL(os.Getenv("MOLTENHUB_HEADLESS_MODE_URL"))
		handler.SetSchedulerAPIKeys(
			os.Getenv("MOLTENHUB_SCHEDULER_API_KEYS"),
			os.Getenv("MOLTENHUB_SCHEDULER_API_KEY"),
		)
		handler.SetStorageHealth(storageHealth)
		setStartupPhase("router_ready")
		readyAt := time.Now().UTC()
		phaseElapsed := readyAt.Sub(phaseStartedAt).Milliseconds()
		if phaseElapsed < 0 {
			phaseElapsed = 0
		}
		phaseDurationsMS[currentPhase] = phaseElapsed
		totalMS := readyAt.Sub(startupStartedAt).Milliseconds()
		if totalMS < 0 {
			totalMS = 0
		}
		startupSummary := map[string]any{
			"boot_status": "ready",
			"startup": map[string]any{
				"started_at":         startupStartedAt,
				"ready_at":           readyAt,
				"total_ms":           totalMS,
				"phase_durations_ms": phaseDurationsMS,
				"last_phase":         currentPhase,
				"last_phase_started": phaseStartedAt,
			},
		}
		handler.SetStartupSummary(startupSummary)
		bootstrap.SetReady(api.NewRouterWithOptions(handler, api.RouterOptions{
			EnableLocalCORS:    enableLocalCORS,
			AllowedCORSOrigins: allowedCORSOrigins,
		}))
		log.Printf("moltenhub runtime ready total_ms=%d phase_durations_ms=%v", totalMS, phaseDurationsMS)
	}()

	log.Printf("moltenhub listening on %s", listener.Addr().String())
	log.Printf("local CORS enabled: %t", enableLocalCORS)
	log.Printf("configured CORS origins: %d", len(allowedCORSOrigins))
	if err := server.Serve(listener); err != nil && err != http.ErrServerClosed {
		log.Fatalf("server failed: %v", err)
	}
}

func envBool(name string, fallback bool) bool {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return fallback
	}
	v, err := strconv.ParseBool(raw)
	if err != nil {
		return fallback
	}
	return v
}

func loadDotEnv(path string) {
	f, err := os.Open(filepath.Clean(path))
	if err != nil {
		return
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "export ") {
			line = strings.TrimSpace(strings.TrimPrefix(line, "export "))
		}
		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 {
			continue
		}
		key := strings.TrimSpace(parts[0])
		if key == "" {
			continue
		}
		value := strings.TrimSpace(parts[1])
		if n := len(value); n >= 2 {
			if (value[0] == '"' && value[n-1] == '"') || (value[0] == '\'' && value[n-1] == '\'') {
				value = value[1 : n-1]
			}
		}
		if _, exists := os.LookupEnv(key); exists {
			continue
		}
		_ = os.Setenv(key, value)
	}
}
