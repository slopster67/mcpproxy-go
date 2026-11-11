package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/slopster67/mcpproxy-go/internal/config"
	rt "github.com/slopster67/mcpproxy-go/internal/runtime"
)

// ProfileManagementRequest is the input of the manage_profiles tool. The
// profile object is the REST profile body; the other fields are the REST path
// and query arguments of the matching route.
type ProfileManagementRequest struct {
	Operation string `json:"operation"`
	// ID is the profile of get, update, delete, rename, effective_tools and
	// explain (as the subject when client is empty).
	ID string `json:"id,omitempty"`
	// Profile is the full profile for create and update (update replaces it).
	Profile    *config.ProfileConfig `json:"profile,omitempty"`
	NewName    string                `json:"new_name,omitempty"`
	ReassignTo string                `json:"reassign_to,omitempty"`
	Force      bool                  `json:"force,omitempty"`
	// Client narrows effective_tools to one client, or is the subject of explain.
	Client string `json:"client,omitempty"`
	Server string `json:"server,omitempty"`
	Reason string `json:"reason,omitempty"`
	Tool   string `json:"tool,omitempty"`
	// From and To are the profiles of move_clients; "" is All servers.
	From *string `json:"from,omitempty"`
	To   *string `json:"to,omitempty"`
	Mode *string `json:"mode,omitempty"`
}

// profileManagementError keeps the REST error body (code, field, used_by, …)
// as the tool's error text, so agents see the same refusal details as REST.
type profileManagementError struct{ body string }

func (e *profileManagementError) Error() string { return e.body }

func wrapProfileError(err error) error {
	if err == nil {
		return nil
	}
	var req *requestError
	if errors.As(err, &req) {
		return err
	}
	_, body := ProfilesErrorBody(err)
	raw, marshalErr := json.Marshal(body)
	if marshalErr != nil {
		return err
	}
	return &profileManagementError{body: string(raw)}
}

func (s *Server) manageProfiles(ctx context.Context, q ProfileManagementRequest) (any, error) {
	if s.profilesService == nil {
		return nil, rt.ErrEvaluatorUnavailable
	}
	data, err := s.runProfileOperation(ctx, q)
	return data, wrapProfileError(err)
}

func (s *Server) runProfileOperation(ctx context.Context, q ProfileManagementRequest) (any, error) {
	actor := managementActor(ctx)
	switch q.Operation {
	case "list":
		return s.ProfileListData(ctx)
	case "get":
		if err := managementRequired(q.ID, "id"); err != nil {
			return nil, err
		}
		return s.ProfileViewData(ctx, q.ID)
	case "create":
		if q.Profile == nil {
			return nil, &requestError{400, "profile is required"}
		}
		if err := managementRequired(q.Profile.Name, "profile.name"); err != nil {
			return nil, err
		}
		return s.profiles().Create(ctx, actor, *q.Profile)
	case "update":
		if err := managementRequired(q.ID, "id"); err != nil {
			return nil, err
		}
		if q.Profile == nil {
			return nil, &requestError{400, "profile is required"}
		}
		profile := *q.Profile
		if profile.ID == "" {
			profile.ID = q.ID
		}
		return s.profiles().Update(ctx, actor, q.ID, profile)
	case "delete":
		if err := managementRequired(q.ID, "id"); err != nil {
			return nil, err
		}
		return s.profiles().Delete(ctx, actor, q.ID, q.ReassignTo, q.Force)
	case "rename":
		if err := managementRequired(q.ID, "id"); err != nil {
			return nil, err
		}
		if err := managementRequired(q.NewName, "new_name"); err != nil {
			return nil, err
		}
		return s.profiles().Rename(ctx, actor, q.ID, q.NewName)
	case "effective_tools":
		if err := managementRequired(q.ID, "id"); err != nil {
			return nil, err
		}
		return s.EffectiveToolsData(ctx, q.ID, q.Client, q.Server, q.Reason)
	case "explain":
		return s.ExplainAccess(ctx, ExplainRequest{Client: q.Client, Profile: q.ID, Tool: q.Tool})
	case "move_clients":
		return s.moveProfileClients(ctx, actor, q)
	}
	return nil, &requestError{400, fmt.Sprintf("unknown operation %q", q.Operation)}
}

// moveProfileClients rebinds every client of one profile to another.
func (s *Server) moveProfileClients(ctx context.Context, actor rt.Actor, q ProfileManagementRequest) (any, error) {
	if q.From == nil {
		return nil, &requestError{400, `from is required ("" for All servers)`}
	}
	if q.To == nil {
		return nil, &requestError{400, `to is required ("" for All servers)`}
	}
	if s.clientsService == nil {
		return nil, rt.ErrEvaluatorUnavailable
	}
	if *q.To != "" {
		known := false
		if cfg := s.controller.GetCurrentConfig(); cfg != nil {
			for i := range cfg.Profiles {
				known = known || cfg.Profiles[i].ID == *q.To
			}
		}
		if !known {
			return nil, &rt.ValidationError{Field: "to", Message: fmt.Sprintf("unknown profile %q", *q.To)}
		}
	}
	moved, skipped, err := s.clientsService.BulkAssign(ctx, actor, *q.From, *q.To, q.Mode)
	if err != nil {
		return nil, err
	}
	if moved == nil {
		moved = []string{}
	}
	if skipped == nil {
		skipped = []rt.Skipped{}
	}
	return map[string]any{"moved": moved, "skipped": skipped}, nil
}
