package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/slopster67/mcpproxy-go/internal/mcpauth"
)

// SetOAuthConsent configures the authorization server behind the consent page.
func (s *Server) SetOAuthConsent(consent *mcpauth.Server) { s.oauthConsent = consent }

func (s *Server) registerOAuthConsentRoutes(r chi.Router) {
	r.Get("/oauth/requests/{request}", s.handleGetOAuthRequest)
	r.Post("/oauth/requests/{request}", s.handleDecideOAuthRequest)
}

func (s *Server) oauthConsentError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, mcpauth.ErrRequestNotFound):
		s.writeError(w, r, http.StatusNotFound, "This sign-in request expired or was already used. Start it again from your MCP client or peer.")
	case errors.Is(err, mcpauth.ErrInvalidSubject):
		s.writeError(w, r, http.StatusBadRequest, err.Error())
	default:
		s.writeError(w, r, http.StatusInternalServerError, "Could not load the sign-in request")
	}
}

func (s *Server) handleGetOAuthRequest(w http.ResponseWriter, r *http.Request) {
	if s.oauthConsent == nil {
		s.writeError(w, r, http.StatusServiceUnavailable, "Sign-in is unavailable")
		return
	}
	consent, err := s.oauthConsent.Describe(chi.URLParam(r, "request"))
	if err != nil {
		s.oauthConsentError(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	s.writeSuccess(w, consent)
}

func (s *Server) handleDecideOAuthRequest(w http.ResponseWriter, r *http.Request) {
	if s.oauthConsent == nil {
		s.writeError(w, r, http.StatusServiceUnavailable, "Sign-in is unavailable")
		return
	}
	var body struct {
		Approve bool   `json:"approve"`
		Subject string `json:"subject"`
	}
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&body) != nil {
		s.writeError(w, r, http.StatusBadRequest, "Invalid decision")
		return
	}
	redirect, err := s.oauthConsent.Decide(chi.URLParam(r, "request"), body.Approve, body.Subject)
	if err != nil {
		s.oauthConsentError(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	s.writeSuccess(w, map[string]string{"redirect_url": redirect})
}
