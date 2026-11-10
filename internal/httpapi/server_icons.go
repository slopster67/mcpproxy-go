package httpapi

import (
	"net/http"

	"github.com/slopster67/mcpproxy-go/internal/servericon"
)

// Icons carry no credentials. Their content-addressed URLs can be loaded by
// MCP clients without attaching an API key or a client token to an image URL.
func (s *Server) handleServerIcon(w http.ResponseWriter, r *http.Request) {
	cfg := s.controller.GetCurrentConfig()
	if cfg != nil {
		for _, sc := range cfg.Servers {
			if sc == nil {
				continue
			}
			icon := s.controller.GetServerIcon(sc.Name)
			if icon == "" || servericon.Path(icon) != r.URL.Path {
				continue
			}
			if err := servericon.Validate(icon); err != nil {
				break
			}
			data, err := servericon.Bytes(icon)
			if err != nil {
				break
			}
			etag := `"` + r.URL.Path + `"`
			w.Header().Set("Content-Type", servericon.MIMEType(icon))
			w.Header().Set("X-Content-Type-Options", "nosniff")
			w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'none'; sandbox")
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
			w.Header().Set("ETag", etag)
			if r.Header.Get("If-None-Match") == etag {
				w.WriteHeader(http.StatusNotModified)
				return
			}
			_, _ = w.Write(data)
			return
		}
	}
	http.NotFound(w, r)
}
