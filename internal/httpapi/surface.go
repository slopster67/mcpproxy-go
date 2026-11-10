package httpapi

import (
	"net/http"
	"strings"

	"github.com/slopster67/mcpproxy-go/internal/profile"
	internalRuntime "github.com/slopster67/mcpproxy-go/internal/runtime"
)

// XMCPProxySurfaceHeader names the surface a profile-mutating request comes
// from (web, macos, cli). Attribution only, never authorization: a caller can
// set it to anything, so it selects a label on the profile_change record and
// nothing else.
const XMCPProxySurfaceHeader = "X-MCPProxy-Surface"

func surfaceFromRequest(r *http.Request) profile.Surface {
	switch profile.Surface(strings.ToLower(strings.TrimSpace(r.Header.Get(XMCPProxySurfaceHeader)))) {
	case profile.SurfaceWeb:
		return profile.SurfaceWeb
	}
	client := strings.ToLower(r.Header.Get(XMCPProxyClientHeader))
	switch {
	case strings.HasPrefix(client, "webui/"), strings.HasPrefix(client, "web/"):
		return profile.SurfaceWeb
	}
	return profile.SurfaceAPI
}

// actorFromRequest is the profile_change actor for a request: the credential
// kind and name from the AuthContext (plan D21) plus the surface above.
func actorFromRequest(r *http.Request) internalRuntime.Actor {
	return internalRuntime.ActorFromContext(r.Context(), surfaceFromRequest(r))
}
