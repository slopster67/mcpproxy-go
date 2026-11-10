package server

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/mark3labs/mcp-go/v2/mcp"
	mcpserver "github.com/mark3labs/mcp-go/v2/server"
	"github.com/slopster67/mcpproxy-go/internal/config"
	"github.com/slopster67/mcpproxy-go/internal/contracts"
	"github.com/slopster67/mcpproxy-go/internal/index"
	"github.com/slopster67/mcpproxy-go/internal/toolsig"
)

// Management tools share discovery and dispatch contracts with upstream tools,
// but authorization comes from the live client capability, never its profile.
func (p *MCPProxyServer) managementTools() []mcpserver.ServerTool {
	return append(p.buildManagementTools(), p.buildProfilesServerTool())
}

func isDirectManagementTool(name string) bool {
	raw, ok := strings.CutPrefix(name, config.ManagementServerName+"__")
	return ok && isManagementTool(raw)
}

func managementMetadata(tool mcp.Tool) *config.ToolMetadata {
	schema, _ := json.Marshal(tool.InputSchema)
	yes, no := true, false
	return &config.ToolMetadata{
		Name: config.ManagementServerName + ":" + tool.Name, RawName: tool.Name,
		ServerName: config.ManagementServerName, Description: tool.Description,
		ParamsJSON:  string(schema),
		Annotations: &config.ToolAnnotations{ReadOnlyHint: &no, DestructiveHint: &yes, OpenWorldHint: &no},
	}
}

func (p *MCPProxyServer) managementToolDefinition(ctx context.Context, id string, direct bool) *config.ToolMetadata {
	if _, allowed := p.managementContext(ctx); !allowed {
		return nil
	}
	separator := ":"
	if direct {
		separator = "__"
	}
	raw, ok := strings.CutPrefix(id, config.ManagementServerName+separator)
	if !ok && direct {
		raw, ok = strings.CutPrefix(id, config.ManagementServerName+":")
	}
	if !ok {
		return nil
	}
	for _, tool := range p.managementTools() {
		if tool.Tool.Name == raw {
			return managementMetadata(tool.Tool)
		}
	}
	return nil
}

func (p *MCPProxyServer) searchManagementTools(ctx context.Context, query string, limit int) ([]*config.SearchResult, error) {
	if _, allowed := p.managementContext(ctx); !allowed {
		return nil, nil
	}
	p.managementIndexOnce.Do(func() {
		p.managementIndex, p.managementIndexErr = index.NewMemoryManager(p.logger)
		if p.managementIndexErr != nil {
			return
		}
		var tools []*config.ToolMetadata
		for _, tool := range p.managementTools() {
			tools = append(tools, managementMetadata(tool.Tool))
		}
		p.managementIndexErr = p.managementIndex.BatchIndexTools(tools)
	})
	if p.managementIndexErr != nil {
		return nil, p.managementIndexErr
	}
	return p.managementIndex.SearchTools(query, limit)
}

func (p *MCPProxyServer) directManagementTools() []mcpserver.ServerTool {
	tools := p.managementTools()
	for i := range tools {
		name := tools[i].Tool.Name
		meta := managementMetadata(tools[i].Tool)
		entry := &directCatalogEntry{DisplayName: FormatDirectToolName(config.ManagementServerName, name), ServerName: config.ManagementServerName, ToolName: name, Description: meta.Description, ParamsJSON: meta.ParamsJSON, Annotations: meta.Annotations}
		description := "[mcpproxy] " + meta.Description
		if p.effectiveDirectToolResponseMode() == config.DirectToolResponseModeDeferred {
			sig, _ := toolsig.Render(meta.ParamsJSON, meta.Description)
			tools[i].Tool = renderDeferredDirectTool(entry, description+" "+sig.Sig)
		} else {
			tools[i].Tool = renderFullDirectTool(entry, description)
		}
		tools[i].Handler = func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return p.callManagementTool(ctx, name, request.GetArguments(), contracts.ToolVariantDestructive)
		}
	}
	return tools
}

// parseToolArguments keeps the ordinary call_tool_* envelope for built-ins too.
func parseToolArguments(arguments map[string]interface{}) (map[string]interface{}, error) {
	for key := range arguments {
		switch key {
		case "name", "args", "intent_data_sensitivity", "intent_reason":
		default:
			return nil, fmt.Errorf("unknown parameter %q; put tool arguments inside args", key)
		}
	}
	if arguments["args"] == nil {
		return map[string]interface{}{}, nil
	}
	args, ok := arguments["args"].(map[string]interface{})
	if !ok {
		return nil, fmt.Errorf("args must be a JSON object")
	}
	return args, nil
}

func (p *MCPProxyServer) callManagementTool(ctx context.Context, name string, args map[string]interface{}, variant string) (*mcp.CallToolResult, error) {
	id := config.ManagementServerName + ":" + name
	meta := p.managementToolDefinition(ctx, id, false)
	if meta == nil {
		return mcp.NewToolResultError("unknown tool: " + id), nil
	}
	if variant != contracts.ToolVariantDestructive {
		return mcp.NewToolResultError("Use call_tool_destructive for " + id + "; management tools can change or delete configuration."), nil
	}
	if p.inputValidator != nil {
		if ok, err, _ := p.inputValidator.validateArgs(id, "", meta.ParamsJSON, args); !ok {
			return invalidParamsErrorResult(id, meta.ParamsJSON, err.Error()), nil
		}
	}
	request := mcp.CallToolRequest{}
	request.Params.Name, request.Params.Arguments = name, args
	if name == profilesToolName {
		return p.handleProfiles(ctx, request)
	}
	return p.handleManagement(ctx, request)
}
