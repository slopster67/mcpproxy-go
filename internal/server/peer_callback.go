package server

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/slopster67/mcpproxy-go/internal/config"
	"github.com/slopster67/mcpproxy-go/internal/oauth"
)

// Both the primary and execution host require an outstanding single-use state.
// Authorization codes are relayed transiently, never persisted on the primary.
func (s *Server) oauthCallback(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	query, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil || len(query["state"]) != 1 || len(query.Get("state")) > 2048 || query.Get("state") == "" || len(query["code"]) > 1 || len(query["error"]) > 1 || (query.Get("code") == "") == (query.Get("error") == "") {
		http.NotFound(w, r)
		return
	}
	encodedName := strings.TrimPrefix(r.URL.EscapedPath(), config.OAuthRemoteCallbackPrefix)
	name, err := url.PathUnescape(encodedName)
	if err != nil || name == "" || strings.Contains(encodedName, "/") {
		http.NotFound(w, r)
		return
	}
	if host, ok := s.runtime.ConsumePeerOAuthCallback(name, query.Get("state")); ok {
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()
		var result struct {
			Status      int    `json:"status"`
			Body        string `json:"body"`
			ContentType string `json:"content_type"`
		}
		if err := s.runtime.Peers().Call(ctx, host, "oauth/callback", name, r.URL.RawQuery, &result); err != nil {
			http.Error(w, "Execution host unavailable", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", result.ContentType)
		w.WriteHeader(result.Status)
		_, _ = w.Write([]byte(result.Body))
		return
	}
	oauth.GetGlobalCallbackManager().RemoteCallbackHandler(w, r)
}
