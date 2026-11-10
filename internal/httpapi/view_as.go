package httpapi

import (
	"context"
	"errors"
	"net/http"

	"github.com/slopster67/mcpproxy-go/internal/config"
	"github.com/slopster67/mcpproxy-go/internal/contracts"
	"github.com/slopster67/mcpproxy-go/internal/profile"
	"github.com/slopster67/mcpproxy-go/internal/storage"
)

// View-as: GET /api/v1/tools?client=<id> and ?profile=<name>
// answer "what would this client / this profile see, and what could it call",
// row by row, from the SAME access chain a real call walks. This file owns the
// request handling; the verdicts come from internal/server.AccessEvaluator.

// ViewAsEvaluator answers the access chain for one resolved subject.
type ViewAsEvaluator interface {
	Evaluate(server, tool string) profile.AccessVerdict
}

// viewAsController is the optional controller capability behind view-as, kept
// separate from ServerController (like profileToolVisibilityController) so
// controllers that do not evaluate access need no stub.
type viewAsController interface {
	// ResolveViewAs resolves the subject once; it returns profile.ErrUnknownProfile
	// or profile.ErrUnknownClient when the subject does not exist.
	ResolveViewAs(subject profile.AccessSubject) (ViewAsEvaluator, error)
}

// viewAsRequest is a parsed view-as request.
type viewAsRequest struct {
	subject profile.AccessSubject
}

// parseViewAs reads ?client= / ?profile=. It returns (nil, true) when neither
// is present, and (nil, false) after writing the error response when the
// request is not answerable:
//
//	400 use either client or profile, not both
//	400 '-' (unattributed) is only valid on activity and session filters
//	404 profile not found
func (s *Server) parseViewAs(w http.ResponseWriter, r *http.Request) (*viewAsRequest, bool) {
	q := r.URL.Query()
	client, prof := q.Get("client"), q.Get("profile")
	if client == "" && prof == "" {
		return nil, true
	}
	if client != "" && prof != "" {
		s.writeError(w, r, http.StatusBadRequest, errClientAndProfile)
		return nil, false
	}
	if client == storage.ScopeFilterUnattributed || prof == storage.ScopeFilterUnattributed {
		s.writeError(w, r, http.StatusBadRequest, errUnattributedOnlyOnGrids)
		return nil, false
	}

	if client != "" {
		return &viewAsRequest{subject: profile.AccessSubject{Kind: profile.AccessSubjectClient, ClientID: client}}, true
	}

	cfg, err := s.controller.GetConfig()
	if err != nil || cfg == nil || !profileConfigured(cfg, prof) {
		s.writeError(w, r, http.StatusNotFound, errProfileNotFound)
		return nil, false
	}
	return &viewAsRequest{subject: profile.AccessSubject{Kind: profile.AccessSubjectProfile, Profile: prof}}, true
}

// profileConfigured reports whether a profile with this id is configured.
func profileConfigured(cfg *config.Config, id string) bool {
	for i := range cfg.Profiles {
		if cfg.Profiles[i].ID == id {
			return true
		}
	}
	return false
}

// resolveViewAs turns a parsed request into an evaluator, writing the error
// response (404 / 501) when it cannot.
func (s *Server) resolveViewAs(w http.ResponseWriter, r *http.Request, req *viewAsRequest) (ViewAsEvaluator, bool) {
	vc, ok := s.controller.(viewAsController)
	if !ok {
		s.writeError(w, r, http.StatusNotImplemented, "view-as is not available on this server")
		return nil, false
	}
	ev, err := vc.ResolveViewAs(req.subject)
	switch {
	case err == nil:
		return ev, true
	case errors.Is(err, profile.ErrUnknownProfile):
		s.writeError(w, r, http.StatusNotFound, errProfileNotFound)
	case errors.Is(err, profile.ErrUnknownClient):
		s.writeError(w, r, http.StatusNotFound, errClientNotFound)
	default:
		s.logger.Errorw("view-as resolution failed", "error", err)
		s.writeError(w, r, http.StatusInternalServerError, "Failed to evaluate access")
	}
	return nil, false
}

// applyViewAs stamps the subject's verdict on every row.
func applyViewAs(ev ViewAsEvaluator, tools []contracts.Tool) {
	for i := range tools {
		v := ev.Evaluate(tools[i].ServerName, tools[i].Name)
		tools[i].ProfileTier = contracts.Tier(v.ProfileTier.String())
		tools[i].Access = &contracts.ToolAccess{Visible: v.Visible, Callable: v.Callable, Reason: string(v.Reason)}
	}
}

// limitServersToProfile restricts a server listing to the viewed profile's
// effective servers and rewrites each row's tool_count to the number of that
// server's tools visible under the profile.
func (s *Server) limitServersToProfile(ctx context.Context, servers []contracts.Server, req *viewAsRequest, ev ViewAsEvaluator) []contracts.Server {
	cfg, err := s.controller.GetConfig()
	if err != nil || cfg == nil {
		return servers[:0:0]
	}
	effective := map[string]struct{}{}
	for i := range cfg.Profiles {
		if cfg.Profiles[i].ID != req.subject.Profile {
			continue
		}
		for _, name := range cfg.Profiles[i].EffectiveServers(cfg) {
			effective[name] = struct{}{}
		}
	}
	out := make([]contracts.Server, 0, len(servers))
	for i := range servers {
		if _, ok := effective[servers[i].Name]; !ok {
			continue
		}
		row := servers[i]
		row.ToolCount = 0
		for _, tool := range s.serverToolNames(ctx, row.Name) {
			if ev.Evaluate(row.Name, tool).Visible {
				row.ToolCount++
			}
		}
		out = append(out, row)
	}
	return out
}

// serverToolNames lists the raw tool names of one server the same way the
// global tools listing does (management service first, controller fallback).
func (s *Server) serverToolNames(ctx context.Context, serverName string) []string {
	var rows []map[string]interface{}
	var err error
	if svc := s.controller.GetManagementService(); svc != nil {
		rows, err = svc.GetServerTools(ctx, serverName)
	} else {
		rows, err = s.controller.GetServerTools(serverName)
	}
	if err != nil {
		s.logger.Debugw("view-as: server tools unavailable", "server", serverName, "error", err)
		return nil
	}
	names := make([]string, 0, len(rows))
	for _, row := range rows {
		if name, _ := row["name"].(string); name != "" {
			names = append(names, name)
		}
	}
	return names
}
