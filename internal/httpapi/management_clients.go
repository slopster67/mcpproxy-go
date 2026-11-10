package httpapi

import (
	"context"

	rt "github.com/slopster67/mcpproxy-go/internal/runtime"
)

func (s *Server) createClient(ctx context.Context, actor rt.Actor, req CreateClientRequest) (*ClientCredentialResponse, error) {
	if s.clientsService == nil {
		return nil, rt.ErrEvaluatorUnavailable
	}
	credential, token, err := s.clientsService.Add(ctx, actor, rt.AddRequest{AllowCodeExecution: req.AllowCodeExecution, AllowManaging: req.AllowManaging, DisplayName: req.DisplayName, HostID: req.HostID, Profile: req.Profile, Mode: req.Mode})
	if err != nil {
		return nil, err
	}
	return s.clientWithCredential(ctx, credential, token), nil
}

func (s *Server) rotateClient(ctx context.Context, actor rt.Actor, id string) (*ClientCredentialResponse, error) {
	if s.clientsService == nil {
		return nil, rt.ErrEvaluatorUnavailable
	}
	token, credential, err := s.clientsService.Rotate(ctx, actor, id)
	if err != nil {
		return nil, err
	}
	return s.clientWithCredential(ctx, credential, token), nil
}

func (s *Server) clientWithCredential(ctx context.Context, credential *rt.ClientCredentialView, token string) *ClientCredentialResponse {
	row, _ := s.clientObject(ctx, credential.ID)
	if row == nil {
		row = customRowFromView(*credential)
	}
	return &ClientCredentialResponse{clientPresence: *row, Credential: token, Snippet: s.clientSnippet(token, credential.HostID)}
}

// updateClient applies a partial binding update and returns the client
// object. An omitted profile keeps the current one.
func (s *Server) updateClient(ctx context.Context, actor rt.Actor, id string, req PatchClientRequest) (*clientPresence, error) {
	if s.clientsService == nil {
		return nil, rt.ErrEvaluatorUnavailable
	}
	profileName := ""
	if req.Profile != nil {
		profileName = *req.Profile
	} else {
		current, err := s.clientsService.Get(id)
		if err != nil {
			return nil, err
		}
		if current == nil {
			return nil, &requestError{404, errClientNotFound}
		}
		profileName = current.Profile
	}
	credential, err := s.clientsService.SetBinding(ctx, actor, id, profileName, req.Mode, req.HostID, req.DisplayName, req.AllowManaging, req.AllowCodeExecution)
	if err != nil {
		return nil, err
	}
	row, _ := s.clientObject(ctx, id)
	if row == nil {
		row = customRowFromView(*credential)
	}
	return row, nil
}
