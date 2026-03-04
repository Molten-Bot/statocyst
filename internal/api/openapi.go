package api

import (
	_ "embed"
	"net/http"
)

//go:embed openapi.yaml
var openapiYAML []byte

func (h *Handler) handleOpenAPIYAML(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeMethodNotAllowed(w)
		return
	}
	// Use a broadly browser-renderable type so the spec opens inline.
	w.Header().Set("Content-Type", "text/yaml; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(openapiYAML)
}
