package server

import (
	"encoding/json"

	"go.uber.org/zap"

	"github.com/slopster67/mcpproxy-go/internal/config"
	"github.com/slopster67/mcpproxy-go/internal/contracts"
	"github.com/slopster67/mcpproxy-go/internal/toolsig"
)

// compactModeHint is the single deterministic hint line attached (top-level
// "hint") to every compact-mode retrieve_tools response.
// It explains the lossy marker and the describe_tool second stage; being part
// of the serialized payload it counts toward every measured response size.
const compactModeHint = "sig legend: name*:type = required param, ~ = collapsed/lossy (details omitted), (~) = schema unavailable. Before calling a tool whose entry is lossy:true, call describe_tool with its id to get the full input schema."

// buildToolEntry renders ONE search result as a response entry for the given
// tool_response_mode. It is the full/compact serialization seam:
//
//   - mode full (or anything else): the exact per-entry map the handler
//     built inline before the extraction — byte-identical under
//     json.Marshal, guarded by the T011 golden test.
//   - mode compact (US1,): {id, score, sig, desc, lossy} rendered
//     from the shared p.sigCache — no inputSchema, no full description, no
//     annotations (those move to describe_tool / self-healing errors).
//
// Cross-cutting response sections (usage_instructions, disabled/remediation,
// notice, session_risk, debug, usage_summary) intentionally stay in
// handleRetrieveToolsWithMode — this builds entries only.
func (p *MCPProxyServer) buildToolEntry(result *config.SearchResult, mode string, includeStats bool) map[string]interface{} {
	if mode == config.ToolResponseModeCompact {
		return p.buildCompactToolEntry(result)
	}
	return p.buildFullToolEntry(result, includeStats)
}

// Compact entries use the same canonical tool ID as full entries.
func (p *MCPProxyServer) buildCompactToolEntry(result *config.SearchResult) map[string]interface{} {
	tool := result.Tool

	var sig toolsig.Signature
	if tool.Hash != "" {
		// The shared Runtime-owned cache, keyed by the tool
		// hash and warmed at index time — a pure read on this path.
		sig = p.sigCache.Get(tool.Hash, tool.ParamsJSON, tool.Description)
	} else {
		// Defensive: indexed tools always carry a hash, but a hashless tool
		// must not memoize under a shared "" key (distinct schemas would
		// collide on first-write-wins). Render directly instead; Render is
		// fail-soft, so the error is log-only (mirrors the tolerant
		// inputSchema fallback in the full path).
		var err error
		sig, err = toolsig.Render(tool.ParamsJSON, tool.Description)
		if err != nil {
			p.logger.Debug("compact signature rendered via unparseable-schema fallback",
				zap.String("tool", tool.Name),
				zap.Error(err))
		}
	}

	return map[string]interface{}{
		"id":    config.CanonicalToolName(tool.ServerName, config.RawToolName(tool)),
		"score": result.Score,
		"sig":   sig.Sig,
		"desc":  sig.Desc,
		"lossy": sig.Lossy,
	}
}

// buildFullToolEntry reproduces today's full-mode entry exactly
// (extracted verbatim from handleRetrieveToolsWithMode).
func (p *MCPProxyServer) buildFullToolEntry(result *config.SearchResult, includeStats bool) map[string]interface{} {
	// Parse the input schema from ParamsJSON
	var inputSchema map[string]interface{}
	if result.Tool.ParamsJSON != "" {
		if err := json.Unmarshal([]byte(result.Tool.ParamsJSON), &inputSchema); err != nil {
			p.logger.Warn("Failed to parse tool params JSON",
				zap.String("tool_name", result.Tool.Name),
				zap.Error(err))
			inputSchema = map[string]interface{}{
				"type":       "object",
				"properties": map[string]interface{}{},
			}
		}
	} else {
		inputSchema = map[string]interface{}{
			"type":       "object",
			"properties": map[string]interface{}{},
		}
	}

	name := config.CanonicalToolName(result.Tool.ServerName, config.RawToolName(result.Tool))
	// Create MCP-compatible tool representation
	mcpTool := map[string]interface{}{
		"name":        name,
		"description": result.Tool.Description,
		"inputSchema": inputSchema,
		"score":       result.Score,
		"server":      result.Tool.ServerName,
	}

	if annotations := result.Tool.Annotations; annotations != nil {
		mcpTool["annotations"] = annotations
	}
	mcpTool["call_with"] = contracts.DeriveCallWith(result.Tool.Annotations)

	// Add usage statistics if requested
	if includeStats {
		if stats, err := p.storage.GetToolUsage(name); err == nil {
			mcpTool["usage_count"] = stats.Count
			mcpTool["last_used"] = stats.LastUsed
		}
	}

	return mcpTool
}
