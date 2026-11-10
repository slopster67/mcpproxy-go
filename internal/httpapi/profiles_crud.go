package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/slopster67/mcpproxy-go/internal/config"
	"github.com/slopster67/mcpproxy-go/internal/contracts"
	internalRuntime "github.com/slopster67/mcpproxy-go/internal/runtime"
)

// decodeProfileBody decodes a request body into v, refusing unknown fields so a
// typo in a profile field is a 400, never a silently ignored setting.
func decodeProfileBody(r *http.Request, v interface{}) error {
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		if errors.Is(err, io.EOF) {
			return errors.New("request body is required")
		}
		return err
	}
	return nil
}

func (s *Server) badProfileBody(w http.ResponseWriter, r *http.Request, err error) {
	s.writeClientBindingError(w, r, http.StatusBadRequest, "", "", "invalid request body: "+err.Error())
}

func (s *Server) handleCreateProfile(w http.ResponseWriter, r *http.Request) {
	var p config.ProfileConfig
	if err := decodeProfileBody(r, &p); err != nil {
		s.badProfileBody(w, r, err)
		return
	}
	res, err := s.profiles().Create(r.Context(), actorFromRequest(r), p)
	if err != nil {
		s.writeProfileServiceError(w, r, err)
		return
	}
	s.writeJSON(w, http.StatusCreated, contracts.NewSuccessResponse(res.Profile))
}

// patchableProfileFields are the keys PATCH /profiles/{id} accepts. "id" is
// accepted only when it repeats the path id.
var patchableProfileFields = map[string]bool{"id": true, "name": true, "servers": true, "selected_tools": true, "switchable_to": true}

// handlePatchProfile applies a partial update to a profile: the stored profile
// with every given field replaced (null clears selected_tools/switchable_to).
// A patch that only changes the name is recorded as a rename.
func (s *Server) handlePatchProfile(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var patch map[string]json.RawMessage
	if err := decodeProfileBody(r, &patch); err != nil {
		s.badProfileBody(w, r, err)
		return
	}
	for key := range patch {
		if !patchableProfileFields[key] {
			s.badProfileBody(w, r, fmt.Errorf("unknown field %q", key))
			return
		}
	}
	cfg, err := s.controller.GetConfig()
	if err != nil || cfg == nil {
		s.writeProfileServiceError(w, r, internalRuntime.ErrConfigUnavailable)
		return
	}
	var current *config.ProfileConfig
	for i := range cfg.Profiles {
		if cfg.Profiles[i].ID == id {
			current = &cfg.Profiles[i]
			break
		}
	}
	if current == nil {
		s.writeProfileServiceError(w, r, &internalRuntime.ProfileNotFoundError{Name: id})
		return
	}
	merged, err := mergeProfilePatch(*current, patch)
	if err != nil {
		s.badProfileBody(w, r, err)
		return
	}
	if merged.ID != id {
		s.writeProfileServiceError(w, r, &internalRuntime.IDMismatchError{Path: id, Body: merged.ID})
		return
	}
	var view internalRuntime.ProfileView
	if onlyName := len(patch) == 1 && patch["name"] != nil || len(patch) == 2 && patch["name"] != nil && patch["id"] != nil; onlyName {
		res, rerr := s.profiles().Rename(r.Context(), actorFromRequest(r), id, merged.Name)
		if rerr != nil {
			s.writeProfileServiceError(w, r, rerr)
			return
		}
		view = res.Profile
	} else {
		res, uerr := s.profiles().Update(r.Context(), actorFromRequest(r), id, merged)
		if uerr != nil {
			s.writeProfileServiceError(w, r, uerr)
			return
		}
		view = res.Profile
	}
	s.writeSuccess(w, view)
}

// mergeProfilePatch returns cur with every patched field decoded over it.
func mergeProfilePatch(cur config.ProfileConfig, patch map[string]json.RawMessage) (config.ProfileConfig, error) {
	out := cur
	out.Servers = append([]string(nil), cur.Servers...)
	fields := map[string]any{"id": &out.ID, "name": &out.Name, "servers": &out.Servers, "selected_tools": &out.SelectedTools, "switchable_to": &out.SwitchableTo}
	for key, raw := range patch {
		target := fields[key]
		switch t := target.(type) {
		case *map[string][]string:
			*t = nil
		case **[]string:
			*t = nil
		}
		if err := json.Unmarshal(raw, target); err != nil {
			return out, fmt.Errorf("%s: %w", key, err)
		}
	}
	return out, nil
}

func (s *Server) handleDeleteProfile(w http.ResponseWriter, r *http.Request) {
	force := false
	if raw := r.URL.Query().Get("force"); raw != "" {
		v, err := strconv.ParseBool(raw)
		if err != nil {
			s.writeClientBindingError(w, r, http.StatusBadRequest, "", "force", `"force" must be true or false`)
			return
		}
		force = v
	}
	res, err := s.profiles().Delete(r.Context(), actorFromRequest(r), chi.URLParam(r, "id"), r.URL.Query().Get("reassign_to"), force)
	if err != nil {
		s.writeProfileServiceError(w, r, err)
		return
	}
	s.writeSuccess(w, res)
}

func (s *Server) handleProfileEffectiveTools(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	name := chi.URLParam(r, "id")
	q := r.URL.Query()
	opt := internalRuntime.EffectiveToolsOptions{
		Client: q.Get("client"), Server: q.Get("server"), Reason: q.Get("reason"),
	}
	res, err := s.profiles().EffectiveTools(ctx, name, opt)
	if err != nil {
		s.writeProfileServiceError(w, r, err)
		return
	}
	s.writeSuccess(w, res)
}
