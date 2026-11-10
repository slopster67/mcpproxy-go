package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/slopster67/mcpproxy-go/internal/contracts"
	"github.com/slopster67/mcpproxy-go/internal/storage"
)

// splitToolID splits a tools id "server:tool" at its first colon. Server
// names never contain one; raw tool names may ("ns:erase").
func splitToolID(id string) (server, tool string, ok bool) {
	server, tool, ok = strings.Cut(id, ":")
	return server, tool, ok && server != "" && tool != ""
}

// toolObject returns the tools list element for server:tool, or nil.
func (s *Server) toolObject(ctx context.Context, server, tool string) (*contracts.Tool, error) {
	data, err := s.globalToolsData(ctx, server)
	if err != nil {
		return nil, err
	}
	for i := range data.Tools {
		if data.Tools[i].Name == tool {
			return &data.Tools[i], nil
		}
	}
	return nil, nil
}

func (s *Server) handleGetTool(w http.ResponseWriter, r *http.Request) {
	server, tool, ok := splitToolID(decodePathParam(chi.URLParam(r, "id")))
	if !ok {
		s.writeError(w, r, http.StatusBadRequest, "tool id must be server:tool")
		return
	}
	obj, err := s.toolObject(r.Context(), server, tool)
	if err != nil {
		s.writeError(w, r, http.StatusInternalServerError, "Failed to read tool")
		return
	}
	if obj == nil {
		s.writeError(w, r, http.StatusNotFound, "tool not found")
		return
	}
	s.writeSuccess(w, obj)
}

// PatchToolRequest is the body of PATCH /api/v1/tools/{id}. classification ""
// clears a manual classification; expected_hash is required with it (the
// definition the operator classified). expected_incarnation binds the write
// to the server the client read.
type PatchToolRequest struct {
	Enabled             *bool   `json:"enabled,omitempty"`
	Classification      *string `json:"classification,omitempty"`
	ExpectedHash        string  `json:"expected_hash,omitempty"`
	ExpectedIncarnation int64   `json:"expected_incarnation,omitempty"`
}

func (s *Server) handlePatchTool(w http.ResponseWriter, r *http.Request) {
	server, tool, ok := splitToolID(decodePathParam(chi.URLParam(r, "id")))
	if !ok {
		s.writeError(w, r, http.StatusBadRequest, "tool id must be server:tool")
		return
	}
	var req PatchToolRequest
	if err := decodeManagementBody(r, &req); err != nil {
		s.writeError(w, r, http.StatusBadRequest, err.Error())
		return
	}
	if req.Enabled == nil && req.Classification == nil {
		s.writeError(w, r, http.StatusBadRequest, "No fields to update")
		return
	}
	if req.Classification != nil {
		tier := *req.Classification
		if tier != "" && tier != "read" && tier != "write" && tier != "destructive" {
			s.writeError(w, r, http.StatusBadRequest, "choose read, write or destructive")
			return
		}
		if err := s.controller.SetToolClassificationAt(server, tool, tier, req.ExpectedHash, req.ExpectedIncarnation); err != nil {
			switch {
			case errors.Is(err, storage.ErrToolClassificationStale), errors.Is(err, storage.ErrToolAlreadyAnnotated):
				s.writeError(w, r, http.StatusConflict, err.Error())
			case errors.Is(err, storage.ErrToolApprovalNotFound):
				s.writeError(w, r, http.StatusNotFound, "tool not found")
			default:
				s.writeApprovalWriteError(w, r, err, "Could not classify tool")
			}
			return
		}
	}
	if req.Enabled != nil {
		if err := s.controller.SetToolEnabledAt(server, tool, *req.Enabled, "api", req.ExpectedIncarnation); err != nil {
			s.writeApprovalWriteError(w, r, err, "Failed to update tool enabled state")
			return
		}
	}
	obj, err := s.toolObject(r.Context(), server, tool)
	if err != nil || obj == nil {
		s.writeError(w, r, http.StatusInternalServerError, "Failed to read tool after the change")
		return
	}
	s.writeSuccess(w, obj)
}
