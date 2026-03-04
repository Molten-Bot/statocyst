package main

import (
	"bufio"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"statocyst/internal/api"
	"statocyst/internal/auth"
	"statocyst/internal/longpoll"
	"statocyst/internal/store"
)

func main() {
	loadDotEnv(".env")

	addr := os.Getenv("STATOCYST_ADDR")
	if addr == "" {
		addr = ":8080"
	}

	controlStore, queueStore, err := store.NewStoresFromEnv()
	if err != nil {
		log.Fatalf("storage backend configuration error: %v", err)
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
	if raw := strings.TrimSpace(os.Getenv("STATOCYST_HEADLESS_MODE")); raw != "" {
		if mode, err := strconv.ParseBool(raw); err == nil {
			headlessMode = mode
		}
	}
	handler := api.NewHandler(
		controlStore,
		queueStore,
		waiters,
		humanAuth,
		os.Getenv("SUPABASE_URL"),
		os.Getenv("SUPABASE_ANON_KEY"),
		os.Getenv("SUPER_ADMIN_EMAILS"),
		os.Getenv("SUPER_ADMIN_DOMAINS"),
		superAdminReviewMode,
		bindTTL,
		headlessMode,
	)
	router := api.NewRouter(handler)

	server := &http.Server{
		Addr:    addr,
		Handler: router,
	}

	log.Printf("statocyst listening on %s", addr)
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("server failed: %v", err)
	}
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
