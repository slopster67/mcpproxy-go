// Package management provides server lifecycle and OAuth operations shared by
// the REST API and the MCP management tools.
package management

import (
	"context"
	"fmt"

	"go.uber.org/zap"

	"github.com/slopster67/mcpproxy-go/internal/reqcontext"
	"github.com/slopster67/mcpproxy-go/internal/storage"
	"github.com/slopster67/mcpproxy-go/internal/upstream/core"
)

// Service defines the server lifecycle and OAuth operations.
type Service interface {
	// RestartServer stops and restarts the connection to a specific upstream server.
	// Emits "servers.changed" event on successful restart.
	RestartServer(ctx context.Context, name string) error

	// GetServerTools retrieves all available tools for a specific upstream MCP server
	// from the StateView cache, enriched with approval status.
	GetServerTools(ctx context.Context, name string) ([]map[string]interface{}, error)

	// TriggerOAuthLoginQuick initiates an OAuth flow and returns the browser
	// status immediately.
	TriggerOAuthLoginQuick(ctx context.Context, name string) (*core.OAuthStartResult, error)

	// TriggerOAuthLogout clears the OAuth token and disconnects a server.
	// Emits "servers.changed" event on successful logout.
	TriggerOAuthLogout(ctx context.Context, name string) error
}

// EventEmitter is told which servers an operation changed.
type EventEmitter interface {
	MarkServersChanged(names ...string)
}

// RuntimeOperations defines the runtime operations the service delegates to.
type RuntimeOperations interface {
	RestartServer(serverName string) error
	GetServerTools(serverName string) ([]map[string]interface{}, error)
	GetToolApproval(serverName, toolName string) (*storage.ToolApprovalRecord, error)
	TriggerOAuthLoginQuick(serverName string) (*core.OAuthStartResult, error)
	TriggerOAuthLogout(serverName string) error
}

type service struct {
	runtime      RuntimeOperations
	eventEmitter EventEmitter
	logger       *zap.SugaredLogger
}

// NewService creates a new management service. The runtime parameter is
// typically *runtime.Runtime.
func NewService(runtime RuntimeOperations, eventEmitter EventEmitter, logger *zap.SugaredLogger) Service {
	return &service{runtime: runtime, eventEmitter: eventEmitter, logger: logger}
}

// RestartServer stops and restarts a specific upstream server connection.
func (s *service) RestartServer(ctx context.Context, name string) error {
	if err := s.runtime.RestartServer(name); err != nil {
		s.logger.Errorw("Failed to restart server", "server", name, "error", err)
		return fmt.Errorf("failed to restart server '%s': %w", name, err)
	}
	s.logger.Infow("Successfully restarted server", "server", name)
	return nil
}

// GetServerTools retrieves all tools for a specific upstream server.
func (s *service) GetServerTools(ctx context.Context, name string) ([]map[string]interface{}, error) {
	if name == "" {
		return nil, fmt.Errorf("server name required")
	}
	tools, err := s.runtime.GetServerTools(name)
	if err != nil {
		return nil, fmt.Errorf("failed to get tools: %w", err)
	}
	for i, tool := range tools {
		toolName, _ := tool["name"].(string)
		if toolName == "" {
			continue
		}
		if record, err := s.runtime.GetToolApproval(name, toolName); err == nil && record != nil {
			tools[i]["approval_status"] = string(record.Status)
		}
	}
	return tools, nil
}

// TriggerOAuthLoginQuick initiates OAuth and returns browser status immediately.
func (s *service) TriggerOAuthLoginQuick(ctx context.Context, name string) (*core.OAuthStartResult, error) {
	if name == "" {
		return nil, fmt.Errorf("server name required")
	}
	result, err := s.runtime.TriggerOAuthLoginQuick(name)
	if err != nil {
		return result, fmt.Errorf("failed to start OAuth: %w", err)
	}
	// The upstream manager emits the event on OAuth completion.
	return result, nil
}

// TriggerOAuthLogout clears OAuth token and disconnects a specific server.
func (s *service) TriggerOAuthLogout(ctx context.Context, name string) error {
	correlationID := reqcontext.GetCorrelationID(ctx)
	source := reqcontext.GetRequestSource(ctx)
	if name == "" {
		return fmt.Errorf("server name required")
	}
	s.logger.Infow("OAuth logout initiated", "correlation_id", correlationID, "source", source, "server", name)
	if err := s.runtime.TriggerOAuthLogout(name); err != nil {
		s.logger.Errorw("Failed to trigger OAuth logout",
			"correlation_id", correlationID, "source", source, "server", name, "error", err)
		return fmt.Errorf("failed to logout: %w", err)
	}
	s.logger.Infow("OAuth logout completed successfully", "correlation_id", correlationID, "source", source, "server", name)
	if s.eventEmitter != nil {
		s.eventEmitter.MarkServersChanged(name)
	}
	return nil
}
