package httpapi

import (
	"context"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/slopster67/mcpproxy-go/internal/contracts"
	"github.com/slopster67/mcpproxy-go/internal/profile"
	internalRuntime "github.com/slopster67/mcpproxy-go/internal/runtime"
)

// PatchClientRequest is the body of PATCH /api/v1/clients/{client}. Every
// field is optional (display_name renames the client); an omitted profile keeps the current one ("" is the
// built-in All servers scope). An omitted mode keeps the credential's current
// mode, except that profile "" means switchable.
type PatchClientRequest struct {
	AllowCodeExecution *bool   `json:"allow_code_execution,omitempty"`
	AllowManaging      *bool   `json:"allow_managing,omitempty"`
	Profile            *string `json:"profile"`
	HostID             *string `json:"host_id,omitempty"`
	Mode               *string `json:"mode,omitempty"`
	DisplayName        *string `json:"display_name,omitempty"`
}

// clientObject returns the object GET /clients/{id} serves (sessions
// included), or nil when the client does not exist.
func (s *Server) clientObject(ctx context.Context, clientID string) (*clientPresence, error) {
	rows, _, err := s.clientRows(ctx, true, clientID)
	if err != nil {
		return nil, err
	}
	for i := range rows {
		if rows[i].ID == clientID {
			return &rows[i], nil
		}
	}
	return nil, nil
}

func (s *Server) handlePatchClient(w http.ResponseWriter, r *http.Request) {
	var req PatchClientRequest
	if err := decodeManagementBody(r, &req); err != nil {
		s.writeClientBindingError(w, r, 400, "", "", err.Error())
		return
	}
	data, err := s.updateClient(r.Context(), actorFromRequest(r), chi.URLParam(r, "client"), req)
	if err != nil {
		s.writeManagementError(w, r, err)
		return
	}
	s.writeSuccess(w, data)
}

// writeClientBindingFailure maps a clients-service error to its wire shape.
func (s *Server) writeClientBindingFailure(w http.ResponseWriter, r *http.Request, err error) {
	if s.writeProfilesError(w, r, err) {
		return
	}
	s.logger.Errorw("client operation failed", "error", err)
	s.writeError(w, r, http.StatusInternalServerError, "the client operation failed")
}

// --- custom client add -----------------------------------------------------------

// CreateClientRequest is the body of POST /api/v1/clients.
type CreateClientRequest struct {
	AllowCodeExecution bool    `json:"allow_code_execution"`
	AllowManaging      bool    `json:"allow_managing"`
	DisplayName        string  `json:"display_name"`
	HostID             string  `json:"host_id,omitempty"`
	Profile            string  `json:"profile,omitempty"`
	Mode               *string `json:"mode,omitempty"`
}

// ClientCredentialResponse is the data of POST /clients and POST
// /clients/{id}/rotate: the client object plus its new credential, shown
// ONCE (the response is no-store).
type ClientCredentialResponse struct {
	clientPresence
	Credential string        `json:"credential"`
	Snippet    ClientSnippet `json:"snippet"`
}

func (s *Server) handleCreateClient(w http.ResponseWriter, r *http.Request) {
	var req CreateClientRequest
	if derr := decodeIssuanceBody(r, &req, true); derr != nil {
		s.writeClientBindingError(w, r, 400, "", derr.Field, derr.Error())
		return
	}
	data, err := s.createClient(r.Context(), actorFromRequest(r), req)
	if err != nil {
		s.writeManagementError(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	s.writeJSON(w, http.StatusCreated, contracts.NewSuccessResponse(data))
}

// customRowFromView is the minimal custom-client row of a just-issued
// credential, used only when the decorated row cannot be read back.
func customRowFromView(v internalRuntime.ClientCredentialView) *clientPresence {
	row := &clientPresence{
		AllowCodeExecution: v.AllowCodeExecution, AllowManaging: v.AllowManaging,
		HostID: v.HostID, ID: v.ID, DisplayName: v.DisplayName, Kind: "custom", CredentialState: profile.CredentialStateClient,
		TokenName: v.TokenName, Profile: v.Profile, ProfileMode: v.Mode,
		Connected: true, State: contracts.ClientPresenceConnectedNeverSeen,
	}
	if row.DisplayName == "" {
		row.DisplayName = v.ID
	}
	return row
}

func (s *Server) handleRotateClient(w http.ResponseWriter, r *http.Request) {

	data, err := s.rotateClient(r.Context(), actorFromRequest(r), chi.URLParam(r, "client"))
	if err != nil {
		s.writeManagementError(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	s.writeJSON(w, http.StatusOK, contracts.NewSuccessResponse(data))
}

// handleDeleteClient removes the saved client and all of its credential secrets.
type DeleteClientResponse struct {
	Deleted string `json:"deleted"`
}

func (s *Server) handleDeleteClient(w http.ResponseWriter, r *http.Request) {
	if s.clientsService == nil {
		s.writeError(w, r, http.StatusServiceUnavailable, "client service not available")
		return
	}
	clientID := chi.URLParam(r, "client")
	if err := s.clientsService.Delete(r.Context(), actorFromRequest(r), clientID); err != nil {
		s.writeClientBindingFailure(w, r, err)
		return
	}
	s.writeSuccess(w, DeleteClientResponse{Deleted: clientID})
}

// --- admin-key upgrade ---------------------------------------------------------------------
