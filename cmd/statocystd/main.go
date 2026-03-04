package main

import (
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"statocyst/internal/api"
	"statocyst/internal/auth"
	"statocyst/internal/longpoll"
	"statocyst/internal/store"
)

func main() {
	addr := os.Getenv("STATOCYST_ADDR")
	if addr == "" {
		addr = ":8080"
	}

	st := store.NewMemoryStore()
	waiters := longpoll.NewWaiters()
	humanAuth := auth.NewHumanAuthProviderFromEnv()
	bindTTL := 15 * time.Minute
	superAdminReviewMode := false
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
	handler := api.NewHandler(
		st,
		waiters,
		humanAuth,
		os.Getenv("SUPABASE_URL"),
		os.Getenv("SUPABASE_ANON_KEY"),
		os.Getenv("SUPER_ADMIN_EMAILS"),
		os.Getenv("SUPER_ADMIN_DOMAINS"),
		superAdminReviewMode,
		bindTTL,
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
