package httpapi

import (
	"encoding/json"
	"net/http"

	"github.com/slopster67/mcpproxy-go/internal/reqcontext"
)

type scopeFilterErrorResponse struct {
	Success   bool   `json:"success"`
	Error     string `json:"error"`
	Code      string `json:"code"`
	Param     string `json:"param"`
	RequestID string `json:"request_id,omitempty"`
}

func scopeFilterListContains(list []string, name string) bool {
	for _, n := range list {
		if n == name {
			return true
		}
	}
	return false
}

// rejectUnsupportedScopeFilters answers 400 unsupported_scope_filter for a
// profile/client/token query parameter the handler does not honour, before
// any storage read. honoured lists the scope parameter names the handler
// supports. It returns true when the caller may proceed; false means it
// already wrote the 400 response and the handler must return immediately.
func rejectUnsupportedScopeFilters(w http.ResponseWriter, r *http.Request, honoured ...string) bool {
	q := r.URL.Query()
	for _, name := range []string{"profile", "client", "token"} {
		if !q.Has(name) {
			continue
		}

		if scopeFilterListContains(honoured, name) {
			continue
		}
		writeScopeFilterRejection(w, r, name)
		return false
	}
	return true
}

func writeScopeFilterRejection(w http.ResponseWriter, r *http.Request, param string) {
	body := scopeFilterErrorResponse{
		Success:   false,
		Error:     "scope filter '" + param + "' is not supported by this server",
		Code:      "unsupported_scope_filter",
		Param:     param,
		RequestID: reqcontext.GetRequestID(r.Context()),
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusBadRequest)
	_ = json.NewEncoder(w).Encode(body)
}
