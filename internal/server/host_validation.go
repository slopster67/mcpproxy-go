package server

import (
	"fmt"
	"net"
	"net/http"

	"github.com/slopster67/mcpproxy-go/internal/httpx"

	"go.uber.org/zap"
)

// isLoopbackHost delegates to httpx.IsLoopbackHost. The MCP surface and the
// REST/SSE surface must share one origin policy, so the implementation lives in
// the leaf package internal/httpx; these thin wrappers keep this file readable.
func isLoopbackHost(addr string) bool { return httpx.IsLoopbackHost(addr) }

// hostMatchesTrusted delegates to httpx.HostMatchesTrusted.
func hostMatchesTrusted(host string, trusted []string) bool {
	return httpx.HostMatchesTrusted(host, trusted)
}

// originAllowed delegates to httpx.OriginAllowed.
func originAllowed(origin string, trusted []string) bool {
	return httpx.OriginAllowed(origin, trusted)
}

func newHostValidationHandler(next http.Handler, trustedHosts func() []string, logger *zap.Logger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		localAddr, ok := r.Context().Value(http.LocalAddrContextKey).(net.Addr)
		if !ok || localAddr == nil || !isLoopbackHost(localAddr.String()) {
			next.ServeHTTP(w, r)
			return
		}
		var trusted []string
		if trustedHosts != nil {
			trusted = trustedHosts()
		}
		if !isLoopbackHost(r.Host) && !hostMatchesTrusted(r.Host, trusted) {
			logger.Warn("Rejected MCP request with untrusted Host header (DNS-rebinding protection)",
				zap.String("host", r.Host),
				zap.String("remote_addr", r.RemoteAddr),
				zap.String("hint", "if this is a reverse-proxy deployment, add the public domain to MCPPROXY_TRUSTED_HOSTS"))
			http.Error(w, fmt.Sprintf("Forbidden: invalid Host header %q — add this host to MCPPROXY_TRUSTED_HOSTS to allow reverse-proxy access", r.Host), http.StatusForbidden)
			return
		}
		// MCP spec: reject only when Origin is present AND invalid, so
		// header-less non-browser clients and proxied traffic pass untouched.
		if origin := r.Header.Get("Origin"); origin != "" && !originAllowed(origin, trusted) {
			logger.Warn("Rejected MCP request with untrusted Origin header (DNS-rebinding protection)",
				zap.String("origin", origin),
				zap.String("remote_addr", r.RemoteAddr),
				zap.String("hint", "if this browser origin is legitimate, add its host to MCPPROXY_TRUSTED_HOSTS"))
			http.Error(w, fmt.Sprintf("Forbidden: invalid Origin header %q — add this host to MCPPROXY_TRUSTED_HOSTS if the origin is legitimate", origin), http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// hostValidationMiddleware wraps an MCP endpoint handler with
// newHostValidationHandler, sourcing trusted_hosts live from runtime config.
func (s *Server) hostValidationMiddleware(next http.Handler) http.Handler {
	return newHostValidationHandler(next, func() []string {
		if cfg := s.runtime.Config(); cfg != nil {
			return cfg.TrustedHosts
		}
		return nil
	}, s.logger)
}
