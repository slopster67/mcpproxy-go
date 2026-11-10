package server

import (
	"net/http"

	"github.com/slopster67/mcpproxy-go/internal/httpx"
)

// publicOriginHandler remembers the host and scheme of each request so a
// sign-in started by that request can build its callback from them.
func publicOriginHandler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if origin := httpx.PublicOrigin(r); origin != "" {
			r = r.WithContext(httpx.WithPublicOrigin(r.Context(), origin))
		}
		next.ServeHTTP(w, r)
	})
}
