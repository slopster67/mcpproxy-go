package server

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/mark3labs/mcp-go/v2/mcp"
	mcpserver "github.com/mark3labs/mcp-go/v2/server"

	"github.com/slopster67/mcpproxy-go/internal/config"
	"github.com/slopster67/mcpproxy-go/internal/contracts"
	"github.com/slopster67/mcpproxy-go/internal/preflight"
)

// maxDescribeToolIDs caps a describe_tool batch. Matches the
// search default k and keeps describe_tool from becoming a bulk-dump loophole.
const maxDescribeToolIDs = 5

// describeErr* are the per-id error codes of the describe_tool contract.
//
// An out-of-scope id reports not_found, byte-indistinguishable from an id that
// does not exist: a distinct code would confirm that a tool the session may
// not see exists.
const (
	describeErrNotFound        = "not_found"
	describeErrPendingApproval = "pending_approval"
	describeErrChanged         = "changed"
	describeErrDisabled        = "disabled"
)

// describeNotFoundRemediation is the standard "gone between listing and
// describe" hint. Deliberately reused for out-of-scope ids so
// the remediation text never confirms that an invisible tool exists.
//
// It is surface-neutral: describe_tool is also registered on the direct
// surface, where retrieve_tools is not exposed at all.
const describeNotFoundRemediation = "Tool not found or no longer available; list tools again."

// describeDiscoveryPendingRemediation is the not-found remediation for a name
// on a server whose tool discovery has not completed for the live connection:
// there is no tool list to refresh from yet,
// so the caller should retry shortly — the same remediation dispatch answers
// (unresolvedToolIdentityMessage).
const describeDiscoveryPendingRemediation = "Tool not found: tool discovery has not completed for this server yet; retry shortly, once its tools have been discovered."

// describeNoApprovalRecordRemediation is the describe_tool answer for a tool
// the server's discovery snapshot contains that has NO approval record yet
// while the quarantine gate is active (implicit pending). It
// carries the same instruction as the dispatch body
// (toolPendingApprovalResult, reason no_approval_record): there is nothing
// in the review UI to approve yet, the server's next discovery pass files the
// record, so the agent is pointed at re-discovery rather than at the approve
// flow.
func describeNoApprovalRecordRemediation(serverName string) string {
	return "In the server's tool list but no approval record exists for it yet, so it is withheld while tool-level quarantine is active for the server. " +
		preflight.NoApprovalRecordRemediation(serverName)
}

// describeMalformedIDRemediation is the answer to an id that does not parse.
//
// It names ONLY the canonical form on purpose, even though the shared tool_ids
// prose advertises both. This string is emitted exclusively by the
// INDEXED surface — the direct surface answers every unresolvable id with the
// plain not-found remediation instead, since a distinct "malformed" code there
// would tell a caller which of its ids were well-formed. So the one surface
// that can produce it accepts colon ids only, and telling its caller otherwise
// would send them round a loop that cannot succeed.
const describeMalformedIDRemediation = "Tool ids must use '<server>:<tool>' format, exactly as listed."

// buildDescribeToolTool constructs the describe_tool definition. ONE builder
// feeds every surface that registers the tool, so the schemas cannot drift.
func buildDescribeToolTool() mcp.Tool {
	return mcp.NewTool("describe_tool",
		mcp.WithDescription("Return full JSON Schema + long description for listed tools. Use when a signature is marked lossy ('~') or you need the exact schema before calling. With check:true it returns one availability verdict per id, not schemas ('ready', or a reason code with retryable/action), to gate a plan before its first call."),
		mcp.WithTitleAnnotation("Describe Tool"),
		mcp.WithReadOnlyHintAnnotation(true),
		mcp.WithDestructiveHintAnnotation(false),
		mcp.WithOpenWorldHintAnnotation(false),
		mcp.WithArray("tool_ids",
			mcp.Required(),
			mcp.Description("Tool ids as listed: '<server>:<tool>' or '<server>__<tool>'. Max 5, or 50 with check:true."),
			mcp.WithStringItems(),
		),
		mcp.WithBoolean("check",
			mcp.Description("Check availability only, no schemas (default false)."),
		),
		// The three annotation filters carry no per-property prose: their names
		// and semantics are the ones the discovery surfaces already teach, and
		// restating them would cost tokens on every session of every surface
		// that registers this tool.
		mcp.WithObject("filters",
			mcp.Description("check:true only. Annotation filters."),
			mcp.Properties(map[string]any{
				"read_only_only":      map[string]any{"type": "boolean"},
				"exclude_destructive": map[string]any{"type": "boolean"},
				"exclude_open_world":  map[string]any{"type": "boolean"},
			}),
		),
	)
}

// resolveDescribeDefinition resolves one id to its definition inputs on the
// given surface, or returns the per-id error entry to report instead.
//
// The two surfaces answer from different authorities and with different id
// grammars; everything downstream — the shared builder, the score deletion, the
// additive output_schema — is common.
//
// The fourth return is the DIRECT surface's registered display name
// ("<server>__<tool>"), empty on the indexed surface. It is what the direct
// surface can actually be asked to call, so the assembly seam swaps it over the
// canonical "<server>:<tool>" the shared builder emits.
func (p *MCPProxyServer) resolveDescribeDefinition(
	ctx context.Context,
	surface describeSurface,
	id string,
) (*config.ToolMetadata, toolEntryOpts, string, map[string]interface{}) {
	if meta := p.managementToolDefinition(ctx, id, surface == describeSurfaceDirect); meta != nil {
		display := ""
		if surface == describeSurfaceDirect {
			display = FormatDirectToolName(config.ManagementServerName, meta.RawName)
		}
		return meta, toolEntryOpts{annotationsOverride: meta.Annotations}, display, nil
	}
	if surface == describeSurfaceDirect {
		entry, ok := p.resolveDirectDescribeID(ctx, id)
		if !ok {
			// One answer for malformed, unknown, withheld and invisible: on this
			// surface a distinct code would confirm the tool exists.
			remediation := describeNotFoundRemediation
			if corrected, ok := p.suggestDirectToolID(ctx, id); ok {
				remediation = fmt.Sprintf("Tool not found. Tool ids are case-sensitive — did you mean '%s'?", corrected)
			}
			return nil, toolEntryOpts{}, "", describeToolIDError(id, describeErrNotFound, remediation)
		}
		// The catalog entry carries the UPSTREAM annotations. Letting the
		// builder fall back to its StateView lookup would silently downgrade a
		// listed-but-pending destructive tool to call_with "read", because the
		// StateView does not carry it.
		return entry.toolMetadata(), toolEntryOpts{annotationsOverride: entry.Annotations}, entry.DisplayName, nil
	}

	serverName, toolName, ok := splitServerTool(id)
	if !ok {
		return nil, toolEntryOpts{}, "", describeToolIDError(id, describeErrNotFound, describeMalformedIDRemediation)
	}

	visible, reason := p.toolVisibleToSession(ctx, serverName, toolName)
	if !visible {
		code, remediation := p.describeVisibilityError(reason, serverName, toolName)
		// The case-correction suggestion is attempted for
		// visReasonNotIndexed AND visReasonServerNotInScope alike.
		// suggestCanonicalToolID only ever offers a correction that is VISIBLE
		// to this session (over the authorized corpus), so this cannot
		// disclose anything; it ensures an id whose literal (server, tool)
		// pair happens to be a HIDDEN document gets the same suggestion a
		// nonexistent id would, so the hidden document's existence does not
		// swallow a did-you-mean. visReasonToolUnresolved is left out: it
		// fires only for a server already inside scope, and its remediation
		// is chosen by resolution state.
		if reason == visReasonNotIndexed || reason == visReasonServerNotInScope {
			if canonical, ok := p.suggestCanonicalToolID(ctx, serverName, toolName); ok {
				remediation = fmt.Sprintf("Tool not found. Tool ids are case-sensitive — did you mean '%s'?", canonical)
			}
		}
		return nil, toolEntryOpts{}, "", describeToolIDError(id, code, remediation)
	}

	meta := p.lookupIndexedTool(serverName, toolName)
	if meta == nil {
		// Disappeared between the visibility check and the lookup.
		return nil, toolEntryOpts{}, "", describeToolIDError(id, describeErrNotFound, describeNotFoundRemediation)
	}
	return meta, toolEntryOpts{}, "", nil
}

// applyDirectSurfaceIdentity rewrites a definition so every identifier in it is
// one the DIRECT surface can actually be asked for.
//
// The shared builder emits the canonical "<server>:<tool>" name and a `call_with`
// naming one of the call_tool_read/write/destructive intent variants. Both are
// right on the indexed surface and wrong on this one: the direct surface
// registers "<server>__<tool>" and exposes no intent variants at all, so an
// agent that followed either field verbatim would get "tool not found".
// Deferred mode tells agents to recover schemas through describe_tool, so
// this matters.
//
// `call_with` is dropped rather than retargeted: it answers "which variant do I
// dispatch through", a question this surface does not have. The permission
// signal it carried is not lost — `annotations` travels on the same definition
// and is the source `call_with` was derived from.
func applyDirectSurfaceIdentity(entry map[string]interface{}, displayName string) {
	if displayName == "" {
		return
	}
	entry["name"] = displayName
	delete(entry, "call_with")
}

// applyDescribeOutputSchema attaches the tool's declared output schema to a
// definition. Absent — not empty — when the tool declares
// none, and absent when what it declares is not valid JSON, since emitting a
// broken schema is worse than emitting none.
func applyDescribeOutputSchema(entry map[string]interface{}, outputSchemaJSON string) {
	if outputSchemaJSON == "" {
		return
	}
	raw := json.RawMessage(outputSchemaJSON)
	if !json.Valid(raw) {
		return
	}
	entry["output_schema"] = raw
}

// describeToolIDError renders one per-id error entry.
func describeToolIDError(id, code, remediation string) map[string]interface{} {
	return map[string]interface{}{
		"id":          id,
		"error":       code,
		"remediation": remediation,
	}
}

// describeVisibilityError maps a toolVisibleToSession reason to the contract's
// per-id error code + remediation, reusing the shared disabled-tool remediation
// text where applicable. Scope failures produce the plain not-found answer —
// code AND remediation — so the response never confirms that an out-of-scope
// tool exists.
func (p *MCPProxyServer) describeVisibilityError(reason, serverName, toolName string) (code, remediation string) {
	switch reason {
	case visReasonServerNotInScope:
		return describeErrNotFound, describeNotFoundRemediation

	case visReasonToolPendingApproval:
		return describeErrPendingApproval, disabledToolRemediation(contracts.DisabledStatusPendingApproval)
	case visReasonToolNoApprovalRecord:
		// Implicit pending: nothing is listed to approve yet,
		// so the approve-flow remediation would be a dead end. Same
		// remediation dispatch answers for this case (toolPendingApprovalResult).
		return describeErrPendingApproval, describeNoApprovalRecordRemediation(serverName)
	case visReasonToolChangedApproval:
		return describeErrChanged, disabledToolRemediation(contracts.DisabledStatusPendingApproval)
	case visReasonToolNotCallable:
		return describeErrDisabled, disabledToolRemediation(p.classifyDisabledTool(serverName, toolName))
	case visReasonToolUnresolved:
		// The not-found shape dispatch's refusal maps to. The remediation
		// depends on WHY the
		// name is unresolved, exactly as unresolvedToolIdentityMessage's does:
		// no completed discovery for the live connection → retry shortly;
		// a completed discovery that does not list the name → list again.
		if !p.resolveExactToolIdentity(serverName, toolName).DiscoveryDone {
			return describeErrNotFound, describeDiscoveryPendingRemediation
		}
		return describeErrNotFound, describeNotFoundRemediation
	default: // visReasonNotIndexed and anything future
		return describeErrNotFound, describeNotFoundRemediation
	}
}

type describeSurface int

const (
	describeSurfaceIndexed describeSurface = iota
	// describeSurfaceDirect is the direct enumeration surface: ids resolve
	// through the published catalog, in either accepted form.
	describeSurfaceDirect
)

// describeToolHandler builds the describe_tool handler for one surface.
func (p *MCPProxyServer) describeToolHandler(surface describeSurface) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return p.handleDescribeToolOnSurface(ctx, request, surface)
	}
}

func (p *MCPProxyServer) handleDescribeTool(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	return p.handleDescribeToolOnSurface(ctx, request, describeSurfaceIndexed)
}

// handleDescribeToolOnSurface implements the describe_tool built-in: a batch
// of 1–5 "<server>:<tool>" ids resolves to full
// definitions — field-equal to the full-mode retrieve_tools rendering over
// {name, description, inputSchema, server, annotations, call_with}, with the
// ranked-only score absent — plus per-id errors for ids that don't resolve.
//
// Every id runs through p.toolVisibleToSession — retrieve_tools' search gates
// (scope → callability) plus the STRICTER describe-only contract gates
// (index presence, server quarantine, pending/changed approval). Because it
// only adds gates on top of search's, describe_tool can never return a
// definition the same session's search would not.
// The handler never consults the response mode: output is identical under
// full and compact.
//
// The `check: true` branch (mcp_describe_check.go) answers verdict-only
// availability from the shared preflight evaluator instead of definitions.
func (p *MCPProxyServer) handleDescribeToolOnSurface(ctx context.Context, request mcp.CallToolRequest, surface describeSurface) (*mcp.CallToolResult, error) {
	startTime := time.Now()
	var sessionID string
	if sess := mcpserver.ClientSessionFromContext(ctx); sess != nil {
		sessionID = sess.SessionID()
	}
	requestID := mintCorrelationID("describe_tool")

	// Mode selection and its strict validation run
	// before tool_ids is touched, so a misused parameter is reported as
	// what it is. A rejected request executes nothing and therefore records
	// nothing — no verdict, and no internal_tool_call either, matching the REST
	// surface's 400 class.
	mode, err := parseDescribeToolMode(request)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	if surface == describeSurfaceDirect {
		ctx = p.cacheDirectProfileView(ctx)
	}
	if mode.check {
		return p.handleDescribeToolCheck(ctx, request, mode, surface, sessionID, requestID)
	}

	emitError := func(errMsg string, args map[string]interface{}) {
		p.emitActivityInternalToolCall(ctx, "describe_tool", "", "", "", sessionID, requestID,
			"error", errMsg, time.Since(startTime).Milliseconds(), args, nil, nil, "")
	}

	ids, err := request.RequireStringSlice("tool_ids")
	if err != nil {
		emitError(err.Error(), nil)
		return mcp.NewToolResultError(fmt.Sprintf("Missing required parameter 'tool_ids': %v", err)), nil
	}
	args := map[string]interface{}{"tool_ids": ids}
	if len(ids) == 0 {
		errMsg := "Missing required parameter 'tool_ids': provide 1-5 tool ids in '<server>:<tool>' format"
		emitError(errMsg, args)
		return mcp.NewToolResultError(errMsg), nil
	}
	// Anti-bulk-loophole: >5 ids fails the whole batch — no
	// partial dump.
	if len(ids) > maxDescribeToolIDs {
		errMsg := fmt.Sprintf("too many tool_ids: %d (max %d). Narrow your selection.", len(ids), maxDescribeToolIDs)
		emitError(errMsg, args)
		return mcp.NewToolResultError(errMsg), nil
	}

	definitions := make([]map[string]interface{}, 0, len(ids))
	idErrors := make([]map[string]interface{}, 0)
	for _, id := range ids {
		meta, opts, directDisplayName, idErr := p.resolveDescribeDefinition(ctx, surface, id)
		if idErr != nil {
			idErrors = append(idErrors, idErr)
			continue
		}

		// The definition IS the full-mode entry (same builder, so the
		// shared fields cannot drift) minus the ranked-only score — a lookup
		// is not a ranked search.
		entry := p.buildToolEntry(&config.SearchResult{Tool: meta}, config.ToolResponseModeFull, opts)
		delete(entry, "score")

		// `output_schema` is ADDITIVE and lands at the
		// definition-assembly seam, not inside buildFullToolEntry — putting it
		// in the shared builder would add it to full-mode retrieve_tools
		// entries too.
		applyDescribeOutputSchema(entry, meta.OutputSchemaJSON)

		// On the direct surface the shared builder's canonical name and
		// intent-variant `call_with` are both uncallable. Applied at the same
		// assembly seam as output_schema, for the same reason.

		applyDirectSurfaceIdentity(entry, directDisplayName)

		definitions = append(definitions, entry)
	}

	response := map[string]interface{}{
		"definitions": definitions,
		"errors":      idErrors,
	}

	jsonResult, err := json.Marshal(response)
	if err != nil {
		emitError(err.Error(), args)
		return mcp.NewToolResultError(fmt.Sprintf("Failed to serialize definitions: %v", err)), nil
	}

	activityArgs := injectAuthMetadata(ctx, args)
	p.emitActivityInternalToolCall(ctx, "describe_tool", "", "", "", sessionID, requestID,
		"success", "", time.Since(startTime).Milliseconds(), activityArgs, response, nil, "")

	return mcp.NewToolResultText(string(jsonResult)), nil
}
