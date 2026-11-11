package httpapi

import (
	"context"
	"net/http"

	"github.com/slopster67/mcpproxy-go/internal/profile"
	internalRuntime "github.com/slopster67/mcpproxy-go/internal/runtime"
)

// The admin read views the REST handlers and the MCP management tools share. Each returns the REST `data` object for an administrator
// caller, so one implementation answers both surfaces and a field added to a
// REST response reaches MCP without a second edit. Errors map through
// ProfilesErrorBody.

// ProfileListData is GET /profiles for an administrator.
func (s *Server) ProfileListData(ctx context.Context) (*internalRuntime.ProfileList, error) {
	list, err := s.profiles().List(ctx)
	if err != nil {
		return nil, err
	}
	return list, nil
}

// ProfileViewData is GET /profiles/{name} for an administrator.
func (s *Server) ProfileViewData(ctx context.Context, name string) (*internalRuntime.ProfileView, error) {
	v, err := s.profiles().Get(ctx, name)
	if err != nil {
		return nil, err
	}
	return v, nil
}

// EffectiveToolsData is GET /profiles/{name}/effective-tools for an
// administrator. client evaluates that client's credential under the profile.
func (s *Server) EffectiveToolsData(ctx context.Context, name, client, server, reason string) (*internalRuntime.EffectiveToolsResult, error) {
	return s.profiles().EffectiveTools(ctx, name, internalRuntime.EffectiveToolsOptions{
		Client: client, Server: server, Reason: reason,
	})
}

// ExplainRequest is the subject and tool of GET /access/explain: exactly one of
// Client, Token, Profile and Anonymous.
type ExplainRequest struct {
	Client  string
	Profile string
	Tool    string
}

// ExplainAccess is GET /access/explain for an administrator.
func (s *Server) ExplainAccess(ctx context.Context, q ExplainRequest) (*internalRuntime.AccessExplanation, error) {
	if (q.Client == "") == (q.Profile == "") {
		return nil, &requestError{http.StatusBadRequest, "exactly one of client or profile is required"}
	}
	if !looksLikeUpstreamTool(q.Tool) {
		return nil, profile.ErrExplainBuiltinTool
	}
	subject := profile.AccessSubject{Kind: profile.AccessSubjectProfile, Profile: q.Profile}
	if q.Client != "" {
		subject = profile.AccessSubject{Kind: profile.AccessSubjectClient, ClientID: q.Client}
	}

	return s.profiles().Explain(ctx, subject, q.Tool)
}
