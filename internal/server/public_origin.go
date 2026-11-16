package server

import (
	"net/http"

	"github.com/slopster67/mcpproxy-go/internal/httpx"
)

// publicOriginHandler remembers the host and scheme of each request so a
// sign-in started by that request can build its callback from them.
func publicOriginHandler(next http.Handler, allowedOrigins []string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if origins, present := r.Header["Origin"]; present {
			valid := len(origins) == 1 && httpx.OriginAllowed(origins[0], nil)
			if len(origins) == 1 {
				for _, allowed := range allowedOrigins {
					if origins[0] == allowed {
						valid = true
						break
					}
				}
			}
			if !valid {
				http.Error(w, "invalid origin", http.StatusForbidden)
				return
			}
		}
		if origin := httpx.PublicOrigin(r); origin != "" {
			r = r.WithContext(httpx.WithPublicOrigin(r.Context(), origin))
		}
		next.ServeHTTP(w, r)
	})
}
