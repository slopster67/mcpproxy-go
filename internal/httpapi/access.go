package httpapi

import (
	"net/http"
	"strings"
)

func (s *Server) handleAccessExplain(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	res, err := s.ExplainAccess(r.Context(), ExplainRequest{
		Client: q.Get("client"), Profile: q.Get("profile"), Tool: q.Get("tool"),
	})
	if err != nil {
		s.writeProfileServiceError(w, r, err)
		return
	}
	s.writeSuccess(w, res)
}

// looksLikeUpstreamTool reports whether tool is "server:tool" with both parts
// non-empty. A built-in management tool (upstream_servers, retrieve_tools, ...)
// has no server segment.
func looksLikeUpstreamTool(tool string) bool {
	i := strings.Index(tool, ":")
	return i > 0 && i < len(tool)-1
}
