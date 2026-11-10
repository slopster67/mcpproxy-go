package server

import (
	"context"

	"github.com/slopster67/mcpproxy-go/internal/auth"
)

// Client permissions are read from storage for every list/call. A session's
// authentication snapshot cannot preserve a permission after revocation.
func (p *MCPProxyServer) clientPermissions(ctx context.Context) (manage, execute bool) {
	ac := auth.AuthContextFromContext(ctx)
	if ac == nil {
		return false, false
	}
	if ac.IsAdmin() {
		return true, true
	}
	if !ac.IsClientCredential() {
		return false, false
	}
	rec, err := p.clientCredentialRecord(ac.ClientID)
	if err != nil || rec == nil {
		return false, false
	}
	return rec.AllowManaging, rec.AllowCodeExecution
}
func (p *MCPProxyServer) managementContext(ctx context.Context) (context.Context, bool) {
	manage, execute := p.clientPermissions(ctx)
	if !manage {
		return ctx, false
	}
	current := *auth.AuthContextFromContext(ctx)
	current.AllowManaging, current.AllowCodeExecution = manage, execute
	return auth.WithAuthContext(ctx, &current), true
}
func (p *MCPProxyServer) adminToolAccess(ctx context.Context) (admin, visible bool) {
	_, allowed := p.managementContext(ctx)
	return allowed, allowed
}
func (p *MCPProxyServer) clientAllowsCodeExecution(ctx context.Context) bool {
	_, execute := p.clientPermissions(ctx)
	return execute
}
