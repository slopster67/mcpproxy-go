package server

import (
	"context"
	"fmt"

	"github.com/slopster67/mcpproxy-go/internal/auth"
	"github.com/slopster67/mcpproxy-go/internal/preflight"
	"github.com/slopster67/mcpproxy-go/internal/profile"
	"github.com/slopster67/mcpproxy-go/internal/upstream/types"
)

// AccessEvaluator resolves one saved client or profile and evaluates tool access.
// Authentication is enforced at the HTTP boundary before tool dispatch.
type AccessEvaluator struct {
	p *MCPProxyServer

	res      ProfileResolution
	idx      *profileIndex
	authCtx  *auth.AuthContext // the request identity after ScopedView; nil = administrator
	dangling bool
}

func (p *MCPProxyServer) NewAccessEvaluator(subject profile.AccessSubject) (*AccessEvaluator, error) {
	base := auth.WithAuthContext(context.Background(), auth.AdminContext())
	idx := p.profileIndexCurrent(base)
	ev := &AccessEvaluator{p: p, idx: idx}

	switch subject.Kind {
	case profile.AccessSubjectProfile:
		scope := profileScopeFromIndex(idx, subject.Profile)
		if scope == nil {
			return nil, profile.ErrUnknownProfile
		}
		ev.res = ProfileResolution{Name: subject.Profile, Source: string(profile.SourceNone), Scope: scope, Policy: idx.PolicyFor(subject.Profile)}
		ev.authCtx = nil // profile reach is independent of any token
		ev.markDangling()
		return ev, nil

	case profile.AccessSubjectClient:
		record, err := p.clientCredentialRecord(subject.ClientID)
		if err != nil {
			return nil, err
		}
		if record == nil {
			return nil, profile.ErrUnknownClient
		}
		ac := record.AuthContext()
		ev.res = p.resolveProfileV3(auth.WithAuthContext(context.Background(), ac), idx)
		ev.authCtx = ac

		// "What would this client get on profile X": the client's credential
		// (its token servers and permissions) evaluated under another profile.
		// The profile reach comes from the profile itself, like a profile
		// subject; the client's pin or binding would otherwise win.
		if subject.Profile != "" {
			scope := profileScopeFromIndex(idx, subject.Profile)
			if scope == nil {
				return nil, profile.ErrUnknownProfile
			}
			ev.res = ProfileResolution{Name: subject.Profile, Source: string(profile.SourceNone), Scope: scope, Policy: idx.PolicyFor(subject.Profile)}
		}
		ev.markDangling()
		return ev, nil
	}
	return nil, fmt.Errorf("unknown access subject kind %q", subject.Kind)
}

// markDangling records the "profile" step failures: a base that no
// longer resolves, or the anonymous binding guard.
func (ev *AccessEvaluator) markDangling() {
	ev.dangling = (ev.res.Base != "" && ev.idx != nil && ev.idx.position(ev.res.Base) < 0)
}

// clientCredentialRecord returns the kind=client credential record of a
// client, nil when it has none.
func (p *MCPProxyServer) clientCredentialRecord(clientID string) (*auth.ClientCredential, error) {
	if p.storage == nil || clientID == "" {
		return nil, nil
	}
	all, err := p.storage.ListClientCredentials()
	if err != nil {
		return nil, fmt.Errorf("cannot read client credentials: %w", err)
	}
	name := auth.ClientTokenName(clientID)
	for i := range all {
		if all[i].Name == name && all[i].Kind == auth.KindClient {
			rec := all[i]
			return &rec, nil
		}
	}
	return nil, nil
}

// Evaluate applies profile, host, readiness and review rules. It records only
// actual blockers, without manufacturing passing checks or cascading policy errors.
func (ev *AccessEvaluator) Evaluate(server, tool string) profile.AccessVerdict {
	p := ev.p
	annotations, found := p.EffectiveAnnotations(server, tool)
	intrinsic := profile.IntrinsicTier(annotations, found)
	verdict := profile.AccessVerdict{Visible: true, Callable: true, ProfileTier: intrinsic, Issues: []profile.AccessIssue{}}
	block := func(reason profile.AccessReason, code, message string, hidden bool) {
		if verdict.Reason == profile.AccessReasonNone {
			verdict.Reason = reason
		}
		verdict.Callable = false
		if hidden {
			verdict.Visible = false
		}
		verdict.Issues = append(verdict.Issues, profile.AccessIssue{Code: code, Message: message})
	}

	switch {
	case ev.dangling:
		block(profile.AccessReasonProfile, "profile_missing", "Assigned profile was deleted.", true)
	case ev.res.Scope != nil && !ev.res.Scope.Allows(server):
		block(profile.AccessReasonServerNotInProfile, "server_not_in_profile", "Not included in this profile.", true)
	case ev.authCtx != nil && !ev.authCtx.IsAdmin() && (!ev.authCtx.CanAccessServer(server) || !p.hostAllows(ev.authCtx, server)):
		block(profile.AccessReasonServerInScope, "host_unavailable", "Not available on this client's host.", true)
	default:
		if policy := ev.res.Policy; policy != nil {
			admitted, reason, tier := policy.Decide(server, tool, intrinsic)
			verdict.ProfileTier = tier
			if !admitted && reason == profile.ReasonNotSelected {
				block(profile.AccessReasonNotSelected, "tool_not_selected", "Not included in this profile.", true)
			}
		}
	}

	gate := p.evaluateExactToolGate(server, tool)
	if gate.serverConfig == nil {
		if gate.storageErr != nil {
			block(profile.AccessReasonServerState, "server_settings_unavailable", "Could not load server settings.", false)
		} else {
			block(profile.AccessReasonServerState, "server_missing", "Server was removed or does not exist.", false)
		}
		return verdict
	}
	if gate.serverDisabled() {
		block(profile.AccessReasonServerState, "server_disabled", "Server is disabled.", false)
	} else {
		client, exists := p.upstreamManager.GetClient(server)
		switch {
		case !exists:
			block(profile.AccessReasonServerState, "server_offline", "Server is offline.", false)
		case client.IsOAuthCallRequired():
			block(profile.AccessReasonServerState, "sign_in_required", "Server needs sign-in.", false)
		case !client.IsConnected():
			info := client.GetConnectionInfo()
			switch {
			case info.State == types.StatePendingAuth || info.IsOAuthError:
				block(profile.AccessReasonServerState, "sign_in_required", "Server needs sign-in.", false)
			case client.IsConnecting():
				block(profile.AccessReasonServerState, "server_connecting", "Server is connecting.", false)
			case info.LastError != nil:
				block(profile.AccessReasonServerState, "connection_failed", "Server connection failed.", false)
			default:
				block(profile.AccessReasonServerState, "server_offline", "Server is offline.", false)
			}
		}
	}
	switch gate.class {
	case preflight.ToolClassPendingApproval, preflight.ToolClassChanged:
		block(profile.AccessReasonToolApproval, "needs_approval", "Needs approval.", false)
	case preflight.ToolClassBlockedByUser:
		block(profile.AccessReasonToolApproval, "tool_disabled", "Tool is disabled.", false)
	}
	if gate.storageErr != nil {
		block(profile.AccessReasonToolApproval, "review_unavailable", "Could not load tool review status.", false)
	}
	return verdict
}
