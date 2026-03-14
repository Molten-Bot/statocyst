package api

import (
	_ "embed"
	"net/http"
)

//go:embed openapi.yaml
var openapiYAML []byte

//go:embed openapi.md
var openapiMarkdown []byte

func setOpenAPIDocAlternateHeaders(w http.ResponseWriter) {
	w.Header().Set("Link", `</openapi.yaml>; rel="alternate"; type="text/yaml", </openapi.md>; rel="alternate"; type="text/markdown"`)
}

func (h *Handler) handleOpenAPIYAML(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeMethodNotAllowed(w)
		return
	}
	// Use a broadly browser-renderable type so the spec opens inline.
	setOpenAPIDocAlternateHeaders(w)
	w.Header().Set("Content-Type", "text/yaml; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(openapiYAML)
}

func (h *Handler) handleOpenAPIMarkdown(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeMethodNotAllowed(w)
		return
	}
	setOpenAPIDocAlternateHeaders(w)
	w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(openapiMarkdown)
}
