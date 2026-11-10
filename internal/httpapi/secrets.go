package httpapi

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/slopster67/mcpproxy-go/internal/secret"
)

// SecretEntry is one element of GET /api/v1/secrets: a stored secret or a
// referenced secret / environment variable. Values are never included.
//
// id is "type:name" ("env:name@host" for an environment variable read on a
// peer); the REST routes address a stored secret by name (+ ?type=).
type SecretEntry struct {
	ID        string   `json:"id"`
	Name      string   `json:"name"`
	Type      string   `json:"type"`
	Reference string   `json:"reference"`
	Servers   []string `json:"servers"`
	HostID    string   `json:"host_id,omitempty"`
	HostName  string   `json:"host_name,omitempty"`
	// IsSet: the value exists (in the store, or in the environment of the
	// process that reads it).
	IsSet bool `json:"is_set"`
	// Available is false when the state is unknown: an environment variable on
	// a peer that is offline (or never reported it).
	Available bool `json:"available"`
}

// secretEntries is the merged secrets list: every stored secret plus every
// reference found in server configs, with the servers that use it.
func (s *Server) secretEntries(ctx context.Context) ([]SecretEntry, error) {
	if s.controller.GetSecretResolver() == nil {
		return nil, &requestError{http.StatusServiceUnavailable, "Secret resolver not available"}
	}
	cfg := s.controller.GetCurrentConfig()
	if cfg == nil {
		return nil, &requestError{http.StatusServiceUnavailable, "Configuration not available"}
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	inspected, err := s.inspectGlobalSecrets(ctx, cfg)
	if err != nil {
		return nil, err
	}
	out := make([]SecretEntry, 0, len(inspected.Secrets)+len(inspected.EnvironmentVars))
	for _, st := range inspected.Secrets {
		out = append(out, secretEntry(st.Ref, st.Servers, "", "", st.IsSet, true))
	}
	for _, ev := range inspected.EnvironmentVars {
		available := ev.HostID == "" || ev.Available
		out = append(out, secretEntry(ev.Ref, ev.Servers, ev.HostID, ev.HostName, ev.IsSet, available))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func secretEntry(ref secret.Ref, servers []string, hostID, hostName string, isSet, available bool) SecretEntry {
	id := ref.Type + ":" + ref.Name
	if hostID != "" {
		id += "@" + hostID
	}
	if servers == nil {
		servers = []string{}
	}
	return SecretEntry{
		ID: id, Name: ref.Name, Type: ref.Type, Reference: fmt.Sprintf("${%s:%s}", ref.Type, ref.Name),
		Servers: servers, HostID: hostID, HostName: hostName, IsSet: isSet, Available: available,
	}
}

func (s *Server) secretEntry(ctx context.Context, name, typ string) (*SecretEntry, error) {
	entries, err := s.secretEntries(ctx)
	if err != nil {
		return nil, err
	}
	for i := range entries {
		if entries[i].Name == name && entries[i].Type == typ && entries[i].HostID == "" {
			return &entries[i], nil
		}
	}
	return nil, &requestError{http.StatusNotFound, "secret not found"}
}

func (s *Server) handleListSecrets(w http.ResponseWriter, r *http.Request) {
	entries, err := s.secretEntries(r.Context())
	if err != nil {
		s.writeManagementError(w, r, err)
		return
	}
	s.writeSuccess(w, map[string]any{"secrets": entries})
}

// handleSetSecret stores a secret (POST /secrets {name, value, type}).
func (s *Server) handleSetSecret(w http.ResponseWriter, r *http.Request) {
	var req SetSecretRequest
	if err := decodeManagementBody(r, &req); err != nil {
		s.writeError(w, r, 400, err.Error())
		return
	}
	s.writeStoredSecret(w, r, req)
}

// handlePatchSecret replaces a stored secret's value (PATCH /secrets/{name}
// {value}; ?type= defaults to secret).
func (s *Server) handlePatchSecret(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Value string `json:"value"`
	}
	if err := decodeManagementBody(r, &body); err != nil {
		s.writeError(w, r, 400, err.Error())
		return
	}
	s.writeStoredSecret(w, r, SetSecretRequest{Name: decodePathParam(chi.URLParam(r, "name")), Value: body.Value, Type: r.URL.Query().Get("type")})
}

func (s *Server) writeStoredSecret(w http.ResponseWriter, r *http.Request, req SetSecretRequest) {
	if _, err := s.setSecret(r.Context(), req); err != nil {
		s.writeManagementError(w, r, err)
		return
	}
	if req.Type == "" {
		req.Type = secretTypeStored
	}
	entry, err := s.secretEntry(r.Context(), req.Name, req.Type)
	if err != nil {
		s.writeManagementError(w, r, err)
		return
	}
	s.writeSuccess(w, entry)
}

func (s *Server) handleDeleteSecret(w http.ResponseWriter, r *http.Request) {
	name := decodePathParam(chi.URLParam(r, "name"))
	if _, err := s.deleteSecret(r.Context(), name, r.URL.Query().Get("type")); err != nil {
		s.writeManagementError(w, r, err)
		return
	}
	s.writeSuccess(w, map[string]string{"deleted": name})
}
