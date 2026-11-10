package httpapi

import (
	"net/http"

	"github.com/go-chi/chi/v5"
)

// handleGetServerOAuthDiscovered returns the OAuth values discovery resolves
// for the server; the configured overrides are part of its stored config.
func (s *Server) handleGetServerOAuthDiscovered(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	settings, err := s.controller.GetServerOAuthSettings(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		s.writeManagementError(w, r, err)
		return
	}
	s.writeSuccess(w, settings)
}
