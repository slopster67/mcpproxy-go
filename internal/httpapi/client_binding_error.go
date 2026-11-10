package httpapi

import (
	"net/http"

	"github.com/slopster67/mcpproxy-go/internal/reqcontext"
)

// ClientBindingErrorResponse is the 409/400 body of a refused binding write:
// `code` is no_client_credential (409), `field` names the offending input
// (400).
type ClientBindingErrorResponse struct {
	Success   bool   `json:"success"` // Always false
	Error     string `json:"error"`
	Code      string `json:"code,omitempty"`
	Field     string `json:"field,omitempty"`
	RequestID string `json:"request_id,omitempty"`
}

func (s *Server) writeClientBindingError(w http.ResponseWriter, r *http.Request, status int, code, field, msg string) {
	s.writeJSON(w, status, ClientBindingErrorResponse{
		Success: false, Error: msg, Code: code, Field: field,
		RequestID: reqcontext.GetRequestID(r.Context()),
	})
}
