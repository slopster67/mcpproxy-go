package server

import (
	"context"

	"github.com/mark3labs/mcp-go/v2/mcp"

	"github.com/slopster67/mcpproxy-go/internal/auth"
	"github.com/slopster67/mcpproxy-go/internal/runtime"
)

func principalFromContext(ctx context.Context) string {
	ac := auth.AuthContextFromContext(ctx)
	if ac == nil {
		return ""
	}
	switch {
	case ac.AgentName != "":
		// Client token names are unique per owner, not globally: two tenants can
		// each hold a token called "ci". Qualify the principal with the owning
		// user so their work sessions (and the work_session_id stamped onto
		// their activity records) cannot collapse into one. Personal-edition
		// tokens have no owner and keep the historical bare form.
		if ac.UserID != "" {
			return "agent:" + ac.UserID + ":" + ac.AgentName
		}
		return "agent:" + ac.AgentName
	case ac.UserID != "":
		return "user:" + ac.UserID
	default:
		// The local API key is a single principal. Naming it keeps admin work
		// from being lumped in with client token work.
		return ac.Type
	}
}

// markSessionWorked records that this session has done something real, and so has
// earned a durable record. Returns the work session it belongs to.
//
// Sessions are NOT persisted at the handshake: on a real machine 99 of 100
// session records were background agents that connected, did nothing, and left —
// every ~15 minutes, around the clock — burying the user's real sessions and
// evicting them from the retention cap within a day.
//
// MUST be called before UpdateSessionStats, which requires the row to exist and
// would otherwise drop that call's counts.
func (p *MCPProxyServer) markSessionWorked(ctx context.Context, sessionID string) string {
	if sessionID == "" || p.sessionStore == nil {
		return ""
	}

	principal := principalFromContext(ctx)

	return p.sessionStore.EnsurePersisted(sessionID, func(info *SessionInfo) string {
		resolve := p.resolveWorkSession()
		if resolve == nil {
			return ""
		}
		return resolve(runtime.WorkSessionIdentity{
			Principal:     principal,
			ClientName:    info.ClientName,
			ClientVersion: info.ClientVersion,
			WorkspaceRoot: info.Workspace,
		})
	})
}

// resolveWorkSession is the runtime's work-session tracker, or nil when the
// proxy has no runtime.
func (p *MCPProxyServer) resolveWorkSession() func(runtime.WorkSessionIdentity) string {
	if p.mainServer == nil || p.mainServer.runtime == nil {
		return nil
	}
	return p.mainServer.runtime.ResolveWorkSession
}

// markWorkIfToolCall attributes a request to a work session when — and only when
// — it is real work.
//
// This runs from the beforeAny hook, which every MCP method passes through on
// every server instance (direct / code-exec / call-tool all share these hooks).
// That placement is the point: work is not marked inside individual tool
// handlers, and the built-ins that did not bother — list_registries,
// upstream_servers, quarantine_security — wrote activity with no work session on
// it. The Web UI grouped those orphaned rows under the raw transport session id
// while the rest of the same connection grouped under its work-session id, so a
// single client appeared as two sessions in the picker. Marking here means a new
// built-in cannot reintroduce that by forgetting a call.
//
// A tools/call is the boundary of "work", deliberately: a connection that only
// initializes and lists tools has done nothing, and persisting it would bury the
// user's real sessions under background agents that connect every few minutes,
// do nothing, and leave.
func (p *MCPProxyServer) markWorkIfToolCall(ctx context.Context, method mcp.MCPMethod) string {
	if method != mcp.MethodToolsCall {
		return ""
	}
	return p.markSessionWorked(ctx, sessionIDFromContext(ctx))
}
