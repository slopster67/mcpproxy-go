package contracts

import (
	"encoding/json"
	"time"

	"github.com/slopster67/mcpproxy-go/internal/config"
)

// ConvertGenericToolsToTyped converts []map[string]interface{} to []Tool
func ConvertGenericToolsToTyped(genericTools []map[string]interface{}) []Tool {
	tools := make([]Tool, 0, len(genericTools))

	for _, generic := range genericTools {
		tool := Tool{
			Schema: make(map[string]interface{}),
		}

		// Extract basic fields
		if name, ok := generic["name"].(string); ok {
			tool.Name = name
		}
		if serverName, ok := generic["server_name"].(string); ok {
			tool.ServerName = serverName
		}
		if description, ok := generic["description"].(string); ok {
			tool.Description = description
		}
		if usage, ok := generic["usage"].(int); ok {
			tool.Usage = usage
		}

		if schema, ok := generic["inputSchema"].(map[string]interface{}); ok {
			tool.Schema = schema
		}

		if output, ok := generic["outputSchema"]; ok {
			if encoded, ok := output.(string); ok {
				_ = json.Unmarshal([]byte(encoded), &tool.OutputSchema)
			} else {
				tool.OutputSchema = output
			}
		}

		// Extract timestamps
		if lastUsed, ok := generic["last_used"].(time.Time); ok {
			tool.LastUsed = &lastUsed
		}

		// Extract annotations — may be a struct pointer or a map depending on source
		if annotations, ok := generic["annotations"].(map[string]interface{}); ok {
			tool.Annotations = convertMapToToolAnnotation(annotations)
		} else if annotationsPtr, ok := generic["annotations"].(*config.ToolAnnotations); ok && annotationsPtr != nil {
			tool.Annotations = &ToolAnnotation{
				Title:           annotationsPtr.Title,
				ReadOnlyHint:    annotationsPtr.ReadOnlyHint,
				DestructiveHint: annotationsPtr.DestructiveHint,
				IdempotentHint:  annotationsPtr.IdempotentHint,
				OpenWorldHint:   annotationsPtr.OpenWorldHint,
			}
		}
		// Computed here, once, for every producer of a Tool; never left for a
		// consuming surface to derive locally.
		tool.Tier = AnnotationTier(toolAnnotationToConfig(tool.Annotations))

		tools = append(tools, tool)
	}

	return tools
}

// ConvertGenericSearchResultsToTyped converts []map[string]interface{} to []SearchResult
func ConvertGenericSearchResultsToTyped(genericResults []map[string]interface{}) []SearchResult {
	results := make([]SearchResult, 0, len(genericResults))

	for _, generic := range genericResults {
		result := SearchResult{}

		// Extract basic fields
		if score, ok := generic["score"].(float64); ok {
			result.Score = score
		}
		if snippet, ok := generic["snippet"].(string); ok {
			result.Snippet = snippet
		}
		if matches, ok := generic["matches"].(int); ok {
			result.Matches = matches
		}

		// Extract embedded tool
		if toolData, ok := generic["tool"].(map[string]interface{}); ok {
			tools := ConvertGenericToolsToTyped([]map[string]interface{}{toolData})
			if len(tools) > 0 {
				result.Tool = tools[0]
			}
		}

		results = append(results, result)
	}

	return results
}

// Helper function to create typed API responses
func NewSuccessResponse(data interface{}) APIResponse {
	return APIResponse{
		Success: true,
		Data:    data,
	}
}

func NewErrorResponseWithRequestID(errorMsg, requestID string) APIResponse {
	return APIResponse{
		Success:   false,
		Error:     errorMsg,
		RequestID: requestID,
	}
}

// Config validation converters

// convertMapToToolAnnotation converts a map to ToolAnnotation
func convertMapToToolAnnotation(m map[string]interface{}) *ToolAnnotation {
	if m == nil {
		return nil
	}

	annotation := &ToolAnnotation{}

	if title, ok := m["title"].(string); ok {
		annotation.Title = title
	}
	if readOnly, ok := m["readOnlyHint"].(bool); ok {
		annotation.ReadOnlyHint = &readOnly
	}
	if destructive, ok := m["destructiveHint"].(bool); ok {
		annotation.DestructiveHint = &destructive
	}
	if idempotent, ok := m["idempotentHint"].(bool); ok {
		annotation.IdempotentHint = &idempotent
	}
	if openWorld, ok := m["openWorldHint"].(bool); ok {
		annotation.OpenWorldHint = &openWorld
	}

	return annotation
}

// toolAnnotationToConfig adapts the wire-shaped ToolAnnotation to
// config.ToolAnnotations — the type AnnotationTier takes — so the tier
// computation stays the single pure function in tier.go rather than being
// duplicated against this package's own annotation type.
func toolAnnotationToConfig(a *ToolAnnotation) *config.ToolAnnotations {
	if a == nil {
		return nil
	}
	return &config.ToolAnnotations{
		Title:           a.Title,
		ReadOnlyHint:    a.ReadOnlyHint,
		DestructiveHint: a.DestructiveHint,
		IdempotentHint:  a.IdempotentHint,
		OpenWorldHint:   a.OpenWorldHint,
	}
}
