package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/slopster67/mcpproxy-go/internal/config"
	"github.com/slopster67/mcpproxy-go/internal/contracts"
	"github.com/slopster67/mcpproxy-go/internal/oauth"
	"github.com/slopster67/mcpproxy-go/internal/servericon"
)

// prepareServerCreate validates a create request. masked is true for a caller
// that reads servers masked (the MCP management tools); a mask echoed into the
// new config is then refused.
func (s *Server) prepareServerCreate(req AddServerRequest, masked bool) (*config.ServerConfig, error) {
	if req.Name == config.ManagementServerName {
		return nil, &requestError{http.StatusBadRequest, "Server name mcpproxy is reserved for built-in management tools"}
	}
	if req.Name == "" {
		return nil, &requestError{http.StatusBadRequest, "Server name is required"}
	}

	if req.URL == "" && req.Command == "" {
		return nil, &requestError{http.StatusBadRequest, "Either 'url' or 'command' is required"}
	}

	if !config.IsValidTrustMode(req.TrustMode) {
		return nil, &requestError{http.StatusBadRequest, invalidTrustModeMessage(req.TrustMode)}
	}

	protocol := req.Protocol
	if protocol == "" {
		if req.Command != "" {
			protocol = "stdio"
		} else if req.URL != "" {
			protocol = "streamable-http"
		}
	}

	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}

	if req.Icon != nil {
		normalized, err := servericon.Normalize(*req.Icon)
		if err != nil {
			return nil, &requestError{http.StatusBadRequest, err.Error()}
		}
		req.Icon = &normalized
	}
	if req.HostID != nil && *req.HostID != "" && !s.controller.GetPeerHub().Exists(*req.HostID) {
		return nil, &requestError{400, "Unknown execution host"}
	}
	serverConfig := &config.ServerConfig{
		HostID: req.HostID, AvailableToPeers: req.AvailableToPeers,
		Name:       req.Name,
		Icon:       req.Icon,
		URL:        req.URL,
		Command:    req.Command,
		Args:       req.Args,
		Env:        flattenNullableMap(req.Env),
		Headers:    flattenNullableMap(req.Headers),
		WorkingDir: req.WorkingDir,
		Protocol:   protocol,
		Enabled:    enabled,
	}

	if req.TrustMode != "" {
		serverConfig.TrustMode = req.TrustMode
	}

	if req.OAuth != nil {
		configured, err := config.PatchOAuth(nil, req.OAuth)
		if err != nil {
			return nil, &requestError{400, err.Error()}
		}
		serverConfig.OAuth = configured
	}

	if masked {
		if err := oauth.CheckArgvMaskEcho("args", req.Args, nil); err != nil {
			return nil, &requestError{http.StatusBadRequest, err.Error()}
		}
		if err := oauth.CheckServerWriteMasks("server.", serverConfig); err != nil {
			return nil, &requestError{http.StatusBadRequest, err.Error()}
		}
	}

	return serverConfig, nil
}

// createServer adds a server and returns its server object; masked callers
// get it masked.
func (s *Server) createServer(ctx context.Context, req AddServerRequest, masked bool) (*contracts.Server, error) {
	cfg, err := s.prepareServerCreate(req, masked)
	if err != nil {
		return nil, err
	}
	if err = s.controller.AddServer(ctx, cfg); err != nil {
		return nil, err
	}
	return s.serverObject(req.Name, masked)
}

// serverObject returns the server object for name, masked for MCP callers.
func (s *Server) serverObject(name string, masked bool) (*contracts.Server, error) {
	server, err := s.controller.GetServer(name)
	if err != nil {
		if errors.Is(err, contracts.ErrServerNotFound) {
			return nil, &requestError{http.StatusNotFound, "Server not found"}
		}
		return nil, err
	}
	if masked {
		oauth.RedactServerSecretFields(server)
	}
	return server, nil
}

// patchServer applies a partial update and returns the updated server object.
func (s *Server) patchServer(ctx context.Context, name string, req AddServerRequest, masked bool) (*contracts.Server, error) {
	if name == "" {
		return nil, &requestError{400, "Server name is required"}
	}
	if !config.IsValidTrustMode(req.TrustMode) {
		return nil, &requestError{400, invalidTrustModeMessage(req.TrustMode)}
	}
	resolve := func(existing *config.ServerConfig) (*config.ServerConfig, error) {
		if req.HostID != nil && *req.HostID != "" && !s.controller.GetPeerHub().Exists(*req.HostID) {
			return nil, &patchError{status: 400, msg: "Unknown execution host"}
		}
		return resolveServerPatch(name, &req, existing, masked)
	}
	if err := s.controller.PatchServer(ctx, name, resolve); err != nil {
		return nil, err
	}
	return s.serverObject(name, masked)
}

func decodeManagementBody(r *http.Request, target any) error {
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	return decoder.Decode(target)
}

func (s *Server) writeManagementError(w http.ResponseWriter, r *http.Request, err error) {
	if s.writeProfilesError(w, r, err) || s.writeStaleReview(w, r, err) {
		return
	}
	var pe *patchError
	if errors.As(err, &pe) {
		s.writeError(w, r, pe.status, pe.msg)
		return
	}

	status := 500
	if strings.Contains(err.Error(), "not found") {
		status = 404
	} else if strings.Contains(err.Error(), "already exists") {
		status = 409
	}
	s.writeError(w, r, status, err.Error())
}
