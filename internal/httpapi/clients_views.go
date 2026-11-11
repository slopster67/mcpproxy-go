package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
)

// ClientsListData is the `data` of GET /clients?profile=&client= as a generic
// JSON object ({clients, routing, warnings}). manage_clients list returns it.
func (s *Server) ClientsListData(ctx context.Context, profileFilter, clientFilter string) (map[string]any, error) {
	resp, err := s.clientsListResponse(ctx, profileFilter, clientFilter)
	if err != nil {
		return nil, &requestError{http.StatusServiceUnavailable, err.Error()}
	}
	return toDataMap(resp)
}

// ClientObjectData is GET /clients/{id} as a generic JSON object, for the MCP
// tools.
func (s *Server) ClientObjectData(ctx context.Context, clientID string) (map[string]any, error) {
	view, err := s.clientObject(ctx, clientID)
	if err != nil {
		return nil, &requestError{http.StatusServiceUnavailable, err.Error()}
	}
	if view == nil {
		return nil, &requestError{http.StatusNotFound, errClientNotFound}
	}
	return toDataMap(view)
}

// toDataMap converts a typed response to the generic JSON object a REST client
// would decode from its `data`.
func toDataMap(v any) (map[string]any, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	out := map[string]any{}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	return out, nil
}
