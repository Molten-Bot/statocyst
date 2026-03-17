package api

import (
	_ "embed"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

//go:embed ui/index.html
var uiIndexHTML []byte

//go:embed ui/app.js
var uiAppJS []byte

//go:embed ui/login.html
var uiLoginHTML []byte

//go:embed ui/login.js
var uiLoginJS []byte

//go:embed ui/common.js
var uiCommonJS []byte

//go:embed ui/profile.html
var uiProfileHTML []byte

//go:embed ui/profile.js
var uiProfileJS []byte

//go:embed ui/organization.html
var uiOrganizationHTML []byte

//go:embed ui/organization.js
var uiOrganizationJS []byte

//go:embed ui/agents.html
var uiAgentsHTML []byte

//go:embed ui/agents.js
var uiAgentsJS []byte

//go:embed ui/docs.html
var uiDocsHTML []byte

//go:embed ui/robots.txt
var uiRobotsTXT []byte

//go:embed ui/humans.txt
var uiHumansTXT []byte

func uiDevModeEnabled() bool {
	return strings.EqualFold(strings.TrimSpace(os.Getenv("STATOCYST_UI_DEV_MODE")), "true")
}

func (h *Handler) handleUI(w http.ResponseWriter, r *http.Request) {
	if strings.HasPrefix(r.URL.Path, "/v1/") || strings.HasPrefix(r.URL.Path, "/health") || strings.HasPrefix(r.URL.Path, "/openapi") || r.URL.Path == "/ping" {
		writeError(w, http.StatusNotFound, "not_found", "route not found")
		return
	}
	if ServeStartupStaticUI(w, r, h.headlessMode) {
		return
	}
	if h.headlessMode {
		if h.redirectHeadlessMode(w, r) {
			return
		}
		writeError(w, http.StatusNotFound, "not_found", "route not found")
		return
	}
	writeError(w, http.StatusNotFound, "not_found", "route not found")
}

func ServeStartupStaticUI(w http.ResponseWriter, r *http.Request, headlessMode bool) bool {
	switch r.URL.Path {
	case "/robots.txt":
		writeUIAsset(w, r, "text/plain; charset=utf-8", uiRobotsTXT, "robots.txt")
		return true
	case "/humans.txt":
		writeUIAsset(w, r, "text/plain; charset=utf-8", uiHumansTXT, "humans.txt")
		return true
	}
	if headlessMode {
		return false
	}

	switch r.URL.Path {
	case "/", "/index.html":
		writeUIAsset(w, r, "text/html; charset=utf-8", uiLoginHTML, "login.html")
		return true
	case "/login.js":
		writeUIAsset(w, r, "application/javascript; charset=utf-8", uiLoginJS, "login.js")
		return true
	case "/common.js":
		writeUIAsset(w, r, "application/javascript; charset=utf-8", uiCommonJS, "common.js")
		return true
	case "/profile", "/profile/", "/profile/index.html":
		writeUIAsset(w, r, "text/html; charset=utf-8", uiProfileHTML, "profile.html")
		return true
	case "/profile.js":
		writeUIAsset(w, r, "application/javascript; charset=utf-8", uiProfileJS, "profile.js")
		return true
	case "/organization", "/organization/", "/organization/index.html":
		writeUIAsset(w, r, "text/html; charset=utf-8", uiOrganizationHTML, "organization.html")
		return true
	case "/organization.js":
		writeUIAsset(w, r, "application/javascript; charset=utf-8", uiOrganizationJS, "organization.js")
		return true
	case "/agents", "/agents/", "/agents/index.html":
		writeUIAsset(w, r, "text/html; charset=utf-8", uiAgentsHTML, "agents.html")
		return true
	case "/agents.js":
		writeUIAsset(w, r, "application/javascript; charset=utf-8", uiAgentsJS, "agents.js")
		return true
	case "/docs", "/docs/", "/docs/index.html":
		writeUIAsset(w, r, "text/html; charset=utf-8", uiDocsHTML, "docs.html")
		return true
	case "/domains", "/domains/", "/domains/index.html":
		writeUIAsset(w, r, "text/html; charset=utf-8", uiIndexHTML, "index.html")
		return true
	case "/app.js", "/domains/app.js":
		writeUIAsset(w, r, "application/javascript; charset=utf-8", uiAppJS, "app.js")
		return true
	default:
		return false
	}
}

func (h *Handler) redirectHeadlessMode(w http.ResponseWriter, r *http.Request) bool {
	target := strings.TrimSpace(h.headlessModeURL)
	if !h.headlessMode || target == "" {
		return false
	}
	http.Redirect(w, r, target, http.StatusFound)
	return true
}

func writeUIAsset(w http.ResponseWriter, r *http.Request, contentType string, embedded []byte, devFileName string) {
	if r.Method != http.MethodGet {
		writeMethodNotAllowed(w)
		return
	}
	body := embedded
	if uiDevModeEnabled() {
		path := filepath.Clean(filepath.Join("internal", "api", "ui", devFileName))
		if fromDisk, err := os.ReadFile(path); err == nil {
			body = fromDisk
		}
		w.Header().Set("Cache-Control", "no-store")
	}
	if strings.HasPrefix(contentType, "text/html") {
		body = []byte(strings.ReplaceAll(string(body), "{{APP_NAME}}", uiAppName()))
	}
	if devFileName == "docs.html" {
		setOpenAPIDocAlternateHeaders(w)
	}
	w.Header().Set("Content-Type", contentType)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}
