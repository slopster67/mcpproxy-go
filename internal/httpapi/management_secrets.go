package httpapi

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/slopster67/mcpproxy-go/internal/config"
	"github.com/slopster67/mcpproxy-go/internal/secret"
)

type ConvertSecretRequest struct {
	Scope      string `json:"scope"`
	Key        string `json:"key"`
	SecretName string `json:"secret_name"`
}

func (s *Server) convertConfigToSecret(ctx context.Context, serverName string, req ConvertSecretRequest) (any, error) {
	if req.Scope != "header" && req.Scope != "env" {
		return nil, &requestError{http.StatusBadRequest, `"scope" must be "header" or "env"`}
	}
	if req.Key == "" {
		return nil, &requestError{http.StatusBadRequest, `"key" is required`}
	}
	if req.SecretName == "" {
		return nil, &requestError{http.StatusBadRequest, `"secret_name" is required`}
	}

	// Look up the real value from the current loaded config — the
	// API-redacted view that the client just saw is not the source of
	// truth.
	cfg, err := s.controller.GetConfig()
	if err != nil || cfg == nil {
		return nil, &requestError{http.StatusInternalServerError, "Configuration not available"}
	}
	var sc *config.ServerConfig
	for _, c := range cfg.Servers {
		if c != nil && c.Name == serverName {
			sc = c
			break
		}
	}
	if sc == nil {
		return nil, &requestError{http.StatusNotFound, fmt.Sprintf("server %q not found", serverName)}
	}

	var value string
	var present bool
	if req.Scope == "header" {
		value, present = sc.Headers[req.Key]
	} else {
		value, present = sc.Env[req.Key]
	}
	if !present {
		return nil, &requestError{http.StatusNotFound, fmt.Sprintf("%s %q not found on server %q", req.Scope, req.Key, serverName)}
	}
	if value == "" {
		return nil, &requestError{http.StatusBadRequest, fmt.Sprintf("%s %q has no value to store", req.Scope, req.Key)}
	}
	if strings.HasPrefix(value, "${secret:") || strings.HasPrefix(value, "${env:") {
		return nil, &requestError{http.StatusBadRequest, fmt.Sprintf("%s %q is already a reference (%s); nothing to convert", req.Scope, req.Key, value)}
	}

	resolver := s.controller.GetSecretResolver()
	if resolver == nil {
		return nil, &requestError{http.StatusInternalServerError, "Secret resolver not available"}
	}

	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	// Store first, then patch. If the secret store write fails, the config is
	// unchanged. If the secret store write succeeds and the patch fails, the
	// secret is in the secret store under a name that's not yet referenced —
	// the operator can either delete it or retry. We log the partial
	// state so it's traceable.
	ref := secret.Ref{Type: secretTypeStored, Name: req.SecretName}
	storeErr := resolver.Store(ctx, ref, value)
	if err := storeErr; err != nil {
		s.logger.Errorw("config-to-secret: secret store store failed",
			"server", serverName, "scope", req.Scope, "key", req.Key, "error", err)
		return nil, &requestError{http.StatusInternalServerError, fmt.Sprintf("failed to store secret: %v", err)}
	}
	referenceStr := fmt.Sprintf("${%s:%s}", secretTypeStored, req.SecretName)

	// Apply the swap as a deep-merge PATCH so we don't touch any other
	// keys on the server. Build the updates map with just the one field
	// we care about, from the entry the update is applied to.
	buildUpdates := func(cur *config.ServerConfig) *config.ServerConfig {
		updates := &config.ServerConfig{Name: serverName}
		if req.Scope == "header" {
			merged := map[string]string{}
			for k, v := range cur.Headers {
				merged[k] = v
			}
			merged[req.Key] = referenceStr
			updates.Headers = merged
		} else {
			merged := map[string]string{}
			for k, v := range cur.Env {
				merged[k] = v
			}
			merged[req.Key] = referenceStr
			updates.Env = merged
		}
		// Preserve existing bool fields so the partial config doesn't reset them.
		updates.Enabled = cur.Enabled
		updates.OAuth = cur.OAuth

		return updates
	}

	updateErr := s.controller.PatchServer(ctx, serverName, func(cur *config.ServerConfig) (*config.ServerConfig, error) { return buildUpdates(cur), nil })

	if err := updateErr; err != nil {
		s.logger.Errorw("config-to-secret: secret store write succeeded but config update failed; secret is stored but not referenced",
			"server", serverName, "scope", req.Scope, "key", req.Key, "secret_name", req.SecretName, "error", err)
		return nil, &requestError{http.StatusInternalServerError, fmt.Sprintf("secret stored as %q but config update failed: %v", req.SecretName, err)}
	}

	// Wake any servers that depend on the new secret. Best-effort.
	if err := s.controller.NotifySecretsChanged(ctx, "store", req.SecretName); err != nil {
		s.logger.Warnw("config-to-secret: failed to notify runtime of secret change",
			"name", req.SecretName, "error", err)
	}

	return map[string]interface{}{
		"message":   fmt.Sprintf("%s %q on %q now references secret %q", req.Scope, req.Key, serverName, req.SecretName),
		"reference": referenceStr,
	}, nil
}

type SetSecretRequest struct {
	Name  string `json:"name"`
	Value string `json:"value"`
	Type  string `json:"type"`
}

func (s *Server) setSecret(ctx context.Context, request SetSecretRequest) (any, error) {
	resolver := s.controller.GetSecretResolver()
	if resolver == nil {
		return nil, &requestError{503, "Secret resolver not available"}
	}
	if request.Name == "" {
		return nil, &requestError{http.StatusBadRequest, "Secret name is required"}
	}

	if request.Value == "" {
		return nil, &requestError{http.StatusBadRequest, "Secret value is required"}
	}

	// Default to secret store if type not specified
	if request.Type == "" {
		request.Type = secretTypeStored
	}

	// Only allow secret store type for security
	if request.Type != secretTypeStored {
		return nil, &requestError{http.StatusBadRequest, "Only secret store type is supported"}
	}

	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	ref := secret.Ref{
		Type: request.Type,
		Name: request.Name,
	}

	err := resolver.Store(ctx, ref, request.Value)
	if err != nil {
		s.logger.Errorw("Failed to store secret", "name", request.Name, "error", err)
		return nil, &requestError{http.StatusInternalServerError, fmt.Sprintf("Failed to store secret: %v", err)}
	}

	// Notify runtime that secrets changed (this will restart affected servers)
	if runtime := s.controller; runtime != nil {
		if err := runtime.NotifySecretsChanged(ctx, "store", request.Name); err != nil {
			s.logger.Warnw("Failed to notify runtime of secret change",
				"name", request.Name,
				"error", err)
		}
	}

	return map[string]interface{}{
		"message":   fmt.Sprintf("Secret '%s' stored successfully in %s", request.Name, request.Type),
		"name":      request.Name,
		"type":      request.Type,
		"reference": fmt.Sprintf("${%s:%s}", request.Type, request.Name),
	}, nil
}

func (s *Server) deleteSecret(ctx context.Context, name, secretType string) (any, error) {
	resolver := s.controller.GetSecretResolver()
	if resolver == nil {
		return nil, &requestError{503, "Secret resolver not available"}
	}
	if name == "" {
		return nil, &requestError{http.StatusBadRequest, "Secret name is required"}
	}

	if secretType == "" {
		secretType = secretTypeStored
	}

	// Only allow secret store type for security
	if secretType != secretTypeStored {
		return nil, &requestError{http.StatusBadRequest, "Only secret store type is supported"}
	}

	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	ref := secret.Ref{
		Type: secretType,
		Name: name,
	}

	err := resolver.Delete(ctx, ref)
	if err != nil {
		s.logger.Errorw("Failed to delete secret", "name", name, "error", err)
		return nil, &requestError{http.StatusInternalServerError, fmt.Sprintf("Failed to delete secret: %v", err)}
	}

	// Notify runtime that secrets changed (this will restart affected servers)
	if runtime := s.controller; runtime != nil {
		if err := runtime.NotifySecretsChanged(ctx, "delete", name); err != nil {
			s.logger.Warnw("Failed to notify runtime of secret deletion",
				"name", name,
				"error", err)
		}
	}

	return map[string]interface{}{
		"message": fmt.Sprintf("Secret '%s' deleted successfully from %s", name, secretType),
		"name":    name,
		"type":    secretType,
	}, nil
}
