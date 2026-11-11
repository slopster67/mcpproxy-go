package server

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"time"

	"github.com/mark3labs/mcp-go/v2/mcp"
	mcpserver "github.com/mark3labs/mcp-go/v2/server"
	"github.com/slopster67/mcpproxy-go/internal/httpapi"
)

type managementOperations interface {
	ManagementOperation(context.Context, string, json.RawMessage) (any, error)
}

func isManagementTool(name string) bool {
	if name == profilesToolName {
		return true
	}
	for _, spec := range httpapi.ManagementToolSpecs() {
		if name == spec.Name {
			return true
		}
	}
	return false
}
func (p *MCPProxyServer) buildManagementTools() []mcpserver.ServerTool {
	specs := httpapi.ManagementToolSpecs()
	tools := make([]mcpserver.ServerTool, 0, len(specs))
	for _, spec := range specs {
		schema := jsonSchemaForType(reflect.TypeOf(spec.Input))
		properties := schema["properties"].(map[string]any)
		properties["operation"].(map[string]any)["enum"] = spec.Operations
		tool := newProxyTool(spec.Name, mcp.WithDescription(spec.Description), mcp.WithDestructiveHintAnnotation(true), mcp.WithReadOnlyHintAnnotation(false), mcp.WithOpenWorldHintAnnotation(false), func(t *mcp.Tool) {
			t.InputSchema.Properties = properties
			t.InputSchema.Required = []string{"operation"}
			t.InputSchema.AdditionalProperties = false
		})
		tools = append(tools, mcpserver.ServerTool{Tool: tool, Handler: p.handleManagement})
	}
	return tools
}
func (p *MCPProxyServer) handleManagement(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	ctx, allowed := p.managementContext(ctx)
	if !allowed || !isManagementTool(request.Params.Name) {
		return mcp.NewToolResultError("unknown tool: " + request.Params.Name), nil
	}
	start := time.Now()
	args := request.GetArguments()
	// Arguments and results can contain secrets and newly minted tokens, so the
	// audit entry records them with every credential redacted.
	safeArgs := redactedManagementMap(args)
	var data any
	var err error
	var result *mcp.CallToolResult
	if service, ok := p.adminViews().(managementOperations); ok {
		var raw json.RawMessage
		raw, err = json.Marshal(args)
		if err == nil {
			data, err = service.ManagementOperation(ctx, request.Params.Name, raw)
		}
	} else {
		err = fmt.Errorf("management service unavailable")
	}
	status, errText := "success", ""
	if err == nil && result == nil {
		var raw []byte
		raw, err = json.Marshal(data)
		if err == nil {
			result = mcp.NewToolResultText(string(raw))
		}
	}
	if err != nil {
		status = "error"
		errText = "Management operation failed"
		result = mcp.NewToolResultError(err.Error())
	}
	if result != nil && result.IsError {
		status = "error"
		errText = "Management operation failed"
	}
	if !isSubCallActivity(ctx) {
		var safeResult any
		if err == nil {
			safeResult = redactManagementJSON(data)
		}
		p.emitActivityInternalToolCall(ctx, request.Params.Name, "", "", "", sessionIDFromContext(ctx), mintCorrelationID(request.Params.Name), status, errText, time.Since(start).Milliseconds(), safeArgs, safeResult, nil, "")
	}
	return result, nil
}
