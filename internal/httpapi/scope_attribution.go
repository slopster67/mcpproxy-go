package httpapi

import (
	"net/http"
	"net/url"

	"github.com/slopster67/mcpproxy-go/internal/storage"
)

const (
	errClientNameUnsupported   = "client_name is not supported on this endpoint; filter by client"
	errClientAndProfile        = "use either client or profile, not both"
	errUnattributedOnlyOnGrids = "'-' (unattributed) is only valid on activity and session filters"
	errProfileNotFound         = "profile not found"
	errClientNotFound          = "client not found"
)

// rejectClientNameParam answers 400 for `client_name` on an endpoint that does
// not filter on the advisory self-reported name (summary, usage, sessions).
// It returns true when the caller may proceed.
func (s *Server) rejectClientNameParam(w http.ResponseWriter, r *http.Request) bool {
	if r.URL.Query().Get("client_name") == "" {
		return true
	}
	s.writeError(w, r, http.StatusBadRequest, errClientNameUnsupported)
	return false
}

// sessionFilterFromQuery builds the storage session filter for GET /sessions.
func sessionFilterFromQuery(q url.Values, limit int, status string) storage.SessionFilter {
	return storage.SessionFilter{
		Limit:     limit,
		Status:    status,
		Profile:   q.Get("profile"),
		ClientID:  q.Get("client"),
		TokenName: q.Get("token"),
	}
}
