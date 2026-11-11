package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"strings"
	"time"
	"unicode/utf8"

	"github.com/mark3labs/mcp-go/v2/mcp"
	mcpserver "github.com/mark3labs/mcp-go/v2/server"
	"go.uber.org/zap"

	"github.com/slopster67/mcpproxy-go/internal/branding"
	"github.com/slopster67/mcpproxy-go/internal/config"
	"github.com/slopster67/mcpproxy-go/internal/contracts"
	"github.com/slopster67/mcpproxy-go/internal/profile"
	"github.com/slopster67/mcpproxy-go/internal/reqcontext"
	"github.com/slopster67/mcpproxy-go/internal/upstream/managed"
)

const (
	// DirectModeToolSeparator is the separator between server name and tool name in direct mode.
	// Using double underscore to avoid conflicts with single underscores in tool names.
	DirectModeToolSeparator = "__"

	// deferredDirectInputSchema is the minimal permissive input schema every
	// deferred direct entry advertises. The bytes are
	// normative: never literal "{}" (which some strict clients reject as a
	// schema), never absent, and never carrying the upstream properties or
	// required list.
	deferredDirectInputSchema = `{"type":"object"}`
)

// safeTruncateBytes returns the largest cut length <= limit at which s can be
// sliced without splitting a multi-byte UTF-8 rune. Direct-mode truncation uses
// a raw byte budget (ToolResponseLimit); cutting at the raw offset can land in
// the middle of a multi-byte character and emit invalid UTF-8 in the forwarded
// TextContent, which downstream JSON encoders/clients reject or render as a
// replacement char. Callers must ensure limit < len(s) (i.e. truncation is
// actually needed) before calling.
func safeTruncateBytes(s string, limit int) int {
	if limit <= 0 {
		return 0
	}
	if limit >= len(s) {
		return len(s)
	}
	// Back up to the start of the rune that straddles the cut point.
	cut := limit
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return cut
}

// ParseDirectToolName parses a direct mode tool name (serverName__toolName) into server and tool components.
// Splits on the FIRST occurrence of "__" only, so tool names containing "__" are preserved.
// Returns server name, tool name, and whether the parse was successful.
func ParseDirectToolName(directName string) (serverName, toolName string, ok bool) {
	idx := strings.Index(directName, DirectModeToolSeparator)
	if idx <= 0 || idx+len(DirectModeToolSeparator) >= len(directName) {
		return "", "", false
	}
	return directName[:idx], directName[idx+len(DirectModeToolSeparator):], true
}

// FormatDirectToolName formats a server name and tool name into a direct mode tool name.
func FormatDirectToolName(serverName, toolName string) string {
	return serverName + DirectModeToolSeparator + toolName
}

// buildDirectModeTools builds MCP tool definitions for direct mode.
// Each upstream tool is exposed directly with serverName__toolName naming.
// Only approved tools from connected, enabled servers are included.
func (p *MCPProxyServer) buildDirectModeTools() ([]mcpserver.ServerTool, *directCatalog) {
	ctx := context.Background()

	// Resolved ONCE for this rebuild and stamped on whichever catalog we end up
	// publishing, including the failure paths: a mode flip made while
	// discovery is failing must still be recorded, or the guarded reload
	// would see no drift afterwards and the operator's change would be lost
	// until something unrelated happened to rebuild.
	//
	// The consequence, accepted rather than hidden: after such a failure the
	// stamp says "deferred" although nothing was ever RENDERED deferred, so a
	// later config reload sees no drift and will not retry. Recovery does not
	// depend on the reload path — the empty listing is itself the problem, and
	// the servers.changed that fixes discovery rebuilds unconditionally, in
	// whatever mode is then configured. Making the guard retry instead would
	// mean rebuilding on every reload for as long as discovery stayed down.
	mode := p.effectiveDirectToolResponseMode()

	// DiscoverTools already filters to connected, enabled, non-quarantined
	// servers — server-LEVEL filtering only. Tool-level state (pending/changed
	// approval) is applied later by the callability filter.
	// The initial rebuild runs during construction, so this can be reached
	// before the upstream manager is wired. Treat it exactly like a discovery
	// failure rather than panicking: built-ins still register, the catalog is
	// still published, and the next servers.changed fills in the upstreams.
	if p.upstreamManager == nil {
		return p.withDirectBuiltins(nil), emptyDirectCatalog(mode, p.logger)
	}

	tools, err := p.upstreamManager.DiscoverTools(ctx)
	if err != nil {
		p.logger.Error("failed to discover tools for direct mode", zap.Error(err))
		// A NON-NIL empty catalog, not nil. A nil catalog would tell the
		// discovery filters "no catalog yet, do not deny" at exactly the moment
		// upstream discovery is failing, flipping them from deny-on-miss to
		// allow-everything.
		return p.withDirectBuiltins(nil), emptyDirectCatalog(mode, p.logger)
	}

	cat := buildDirectCatalog(tools, p.logger)
	cat.mode = mode
	return p.withDirectBuiltins(p.renderDirectTools(cat)), cat
}

// builtinDirectToolNames is the explicit, POSITIVE set of tool names this
// proxy serves on the direct surface itself, as opposed to an upstream
// projection. Populated directly from the same constructors withDirectBuiltins
// registers (buildDescribeToolTool, …) so the two can never drift apart.
//
// A name is a built-in ONLY when it is in this set. Failing to parse as
// server__tool is not enough: a catalog-admitted upstream tool with an empty
// raw name renders as "server__", which also fails to parse. Only this
// explicit set, and a successful catalog lookup, identify a name.
var builtinDirectToolNames = func() map[string]struct{} {
	names := map[string]struct{}{buildDescribeToolTool().Name: {}, "code_execution": {}}
	for _, tool := range (&MCPProxyServer{}).managementTools() {
		names[FormatDirectToolName(config.ManagementServerName, tool.Tool.Name)] = struct{}{}
	}
	return names
}()

// withDirectBuiltins appends the tools mcpproxy serves itself on the direct
// surface.
//
// It is applied on EVERY return path of buildDirectModeTools, including the
// failure paths, because SetTools REPLACES the whole registry: a rebuild that
// omitted the built-ins would delete describe_tool from the live surface until
// some later successful rebuild happened to restore it. A built-in that
// disappears on the first upstream hiccup is not a built-in.
//
// The DEFINITION is the shared one — the schemas and response shape must not
// drift between surfaces — but the HANDLER is the direct-surface variant, which
// resolves ids through the published catalog rather than the search index.
// Which corpus an id resolves against is a property of the
// registration, not of the request, so it is bound here rather than sniffed
// from the context.
func (p *MCPProxyServer) withDirectBuiltins(tools []mcpserver.ServerTool) []mcpserver.ServerTool {
	tools = append(tools, mcpserver.ServerTool{Tool: buildDescribeToolTool(), Handler: p.describeToolHandler(describeSurfaceDirect)})
	tools = append(tools, p.buildCodeExecutionTool()...)
	return append(tools, p.directManagementTools()...)
}

// renderDirectTools turns a catalog into the registrable tool set.
//
// It renders FROM the catalog rather than from the raw projection, so the
// listing and the catalog cannot disagree by construction: a display-name
// collision withheld by the catalog is absent from the listing for free, rather
// than needing the same rule implemented twice.
func (p *MCPProxyServer) renderDirectTools(cat *directCatalog) []mcpserver.ServerTool {
	names := cat.DisplayNames()
	serverTools := make([]mcpserver.ServerTool, 0, len(names))

	// Read from the SNAPSHOT, not from config. Every entry of a published
	// generation must share one serialization — resolving per entry would let a
	// reload landing mid-loop publish a listing that straddles both, which no
	// consumer can describe — and the same stamp is what the
	// reload guard later compares against, so the render and the guard cannot
	// disagree about what was published.
	deferred := cat.Mode() == config.DirectToolResponseModeDeferred

	// Counted and logged because a signature miss is INVISIBLE in the payload —
	// a deferred entry without a suffix looks exactly like a tool whose schema
	// happens to be empty. Without this, "deferral is on but nothing has
	// signatures" (the rebuild ran before the index warmed it) is indistinguishable
	// from "the signatures are all empty", and the first is a real, recoverable
	// operational state.
	signatureMisses := 0

	for _, name := range names {
		entry, ok := cat.Lookup(name)
		if !ok {
			continue
		}

		rendered := fmt.Sprintf("[%s] %s", entry.ServerName, entry.Description)

		var mcpTool mcp.Tool
		if deferred {
			suffix := p.directSignatureSuffix(entry)
			if suffix == "" {
				signatureMisses++
			}
			rendered += suffix
			mcpTool = renderDeferredDirectTool(entry, rendered)
		} else {
			mcpTool = renderFullDirectTool(entry, rendered)
		}

		// Captured at render time and never recomputed: the signature cache
		// mutates independently of rebuilds, so re-rendering later to compare
		// would report a cache warm/evict as a catalog change.
		// In deferred mode this is the description WITH its signature suffix —
		// what was actually registered, which is the only thing a later
		// comparison can honestly be against.
		entry.RenderedDescription = rendered

		// Stamp the identity of THIS entry — the same one the
		// handler below closes over — onto the tool object itself, so the
		// scope and callability filters can authorize a tools/list or
		// call-time re-evaluation against the exact publication that produced
		// this tool, never against whatever catalog happens to be live when
		// the filter runs (see directToolStamp's doc comment). The terminal
		// stripDirectToolStampFilter removes it before any response reaches a
		// client.
		mcpTool.Icons = serverIcons(p.serverIcon(entry.ServerName))
		mcpTool = stampDirectTool(mcpTool, entry)

		serverTools = append(serverTools, mcpserver.ServerTool{
			Tool:    mcpTool,
			Handler: p.makeDirectModeHandler(entry),
		})
	}

	p.logger.Info("built direct mode tools",
		zap.Int("tool_count", len(serverTools)),
		zap.Bool("schema_deferred", deferred),
		zap.Int("signature_misses", signatureMisses))

	return serverTools
}

// emptyDirectCatalog is the non-nil empty snapshot the failure paths publish,
// carrying the mode this rebuild resolved. Non-nil matters: a nil
// catalog tells the discovery filters "not built yet, do not deny" at exactly
// the moment discovery is failing.
func emptyDirectCatalog(mode string, logger *zap.Logger) *directCatalog {
	cat := buildDirectCatalog(nil, logger)
	cat.mode = mode
	return cat
}

// renderFullDirectTool renders a direct-surface tool with its full upstream
// input schema (properties, required, and a top-level
// additionalProperties/$defs when the upstream declares them).
func renderFullDirectTool(entry *directCatalogEntry, description string) mcp.Tool {
	opts := []mcp.ToolOption{mcp.WithDescription(description)}

	if entry.Annotations != nil {
		if entry.Annotations.Title != "" {
			opts = append(opts, mcp.WithTitleAnnotation(entry.Annotations.Title))
			opts = append(opts, mcp.WithToolTitle(entry.Annotations.Title))
		}
		if entry.Annotations.ReadOnlyHint != nil {
			opts = append(opts, mcp.WithReadOnlyHintAnnotation(*entry.Annotations.ReadOnlyHint))
		}
		if entry.Annotations.DestructiveHint != nil {
			opts = append(opts, mcp.WithDestructiveHintAnnotation(*entry.Annotations.DestructiveHint))
		}
		if entry.Annotations.IdempotentHint != nil {
			opts = append(opts, mcp.WithIdempotentHintAnnotation(*entry.Annotations.IdempotentHint))
		}
		if entry.Annotations.OpenWorldHint != nil {
			opts = append(opts, mcp.WithOpenWorldHintAnnotation(*entry.Annotations.OpenWorldHint))
		}
	}

	mcpTool := mcp.NewTool(entry.DisplayName, opts...)

	if entry.ParamsJSON != "" {
		var schema map[string]interface{}
		if err := json.Unmarshal([]byte(entry.ParamsJSON), &schema); err == nil {
			mcpTool.InputSchema = mcp.ToolInputSchema{Type: "object"}
			if props, ok := schema["properties"].(map[string]interface{}); ok {
				mcpTool.InputSchema.Properties = props
			}
			if req, ok := schema["required"].([]interface{}); ok {
				reqStrings := make([]string, 0, len(req))
				for _, r := range req {
					if str, ok := r.(string); ok {
						reqStrings = append(reqStrings, str)
					}
				}
				mcpTool.InputSchema.Required = reqStrings
			}
			// Keep the object open when upstream declared it open: dropping
			// additionalProperties advertises {"properties":{}}, which
			// grammar-constrained clients compile to "no keys allowed".
			// Carried only when present.
			if ap, ok := schema["additionalProperties"]; ok {
				mcpTool.InputSchema.AdditionalProperties = ap
			}
			if defs, ok := schema["$defs"].(map[string]interface{}); ok {
				mcpTool.InputSchema.Defs = defs
			}
		}
	}

	applyToolOutputSchemaJSON(&mcpTool, entry.OutputSchemaJSON)

	return mcpTool
}

// renderDeferredDirectTool builds one deferred entry.
//
// mcp.NewTool CANNOT produce this wire shape: its marshaller always emits
// "properties":{} and "required":[], which re-opens the arg-pruning hazard the
// placeholder exists to close. So the schema goes in raw — and because
// NewToolWithRawSchema accepts no ToolOptions and leaves InputSchema zero,
// nothing here may touch mcpTool.InputSchema either: Tool.MarshalJSON returns
// errToolSchemaConflict the moment RawInputSchema and a typed InputSchema.Type
// are both set.
//
// outputSchema is deliberately not applied: a deferred entry
// advertises no schema in either direction.
func renderDeferredDirectTool(entry *directCatalogEntry, description string) mcp.Tool {
	mcpTool := mcp.NewToolWithRawSchema(entry.DisplayName, description, json.RawMessage(deferredDirectInputSchema))
	mcpTool.Annotations = directToolAnnotations(entry.Annotations)
	return mcpTool
}

// directToolAnnotations reproduces, as a struct, exactly what the full-mode
// mcp.NewTool + WithXAnnotation chain in renderFullDirectTool produces.
//
// This exists because NewToolWithRawSchema seeds NOTHING while NewTool seeds
// readOnly=false, destructive=true, idempotent=false, openWorld=true before any
// option runs — and mcp.Tool.MarshalJSON emits "annotations" unconditionally
// (the field carries no omitempty). Copying only the upstream hints would
// therefore marshal a different, usually near-empty, annotations object for the
// same tool in deferred mode, so annotations would differ between modes.
func directToolAnnotations(annotations *config.ToolAnnotations) mcp.ToolAnnotation {
	out := mcp.ToolAnnotation{
		Title:           "",
		ReadOnlyHint:    mcp.ToBoolPtr(false),
		DestructiveHint: mcp.ToBoolPtr(true),
		IdempotentHint:  mcp.ToBoolPtr(false),
		OpenWorldHint:   mcp.ToBoolPtr(true),
	}

	if annotations == nil {
		return out
	}

	// Only a SET upstream hint overrides its default, mirroring the option
	// chain, which appends an option only for a non-nil pointer.
	if annotations.Title != "" {
		out.Title = annotations.Title
	}
	if annotations.ReadOnlyHint != nil {
		out.ReadOnlyHint = mcp.ToBoolPtr(*annotations.ReadOnlyHint)
	}
	if annotations.DestructiveHint != nil {
		out.DestructiveHint = mcp.ToBoolPtr(*annotations.DestructiveHint)
	}
	if annotations.IdempotentHint != nil {
		out.IdempotentHint = mcp.ToBoolPtr(*annotations.IdempotentHint)
	}
	if annotations.OpenWorldHint != nil {
		out.OpenWorldHint = mcp.ToBoolPtr(*annotations.OpenWorldHint)
	}

	return out
}

// directSignatureSuffix returns the newline + bare tool name + compact
// signature appended to a deferred description, or "" when no signature is
// available.
//
// The lookup is Peek, never Get: Get compiles and memoizes on a miss, which
// would put per-request compilation on the listing path, and — worse — would
// hide the miss, since a caller that always gets a
// Signature back cannot tell "warmed at index time" from "compiled just now".
// On a miss the whole suffix is absent and the entry is otherwise unchanged:
// never dropped, never delayed.
//
// The tool-name prefix is this renderer's job. toolsig.Signature.Sig is the
// parenthesized parameter list alone and carries no name, so appending Sig by
// itself would emit a bare "(owner*:str, …)" with nothing to attach it to.
func (p *MCPProxyServer) directSignatureSuffix(entry *directCatalogEntry) string {
	// A hashless entry cannot be looked up: the cache is keyed by the
	// per-tool hash, and "" is not a key any Warm ever wrote.
	if p.sigCache == nil || entry.Hash == "" {
		return ""
	}

	sig, ok := p.sigCache.Peek(entry.Hash)
	if !ok || sig.Sig == "" {
		return ""
	}

	return "\n" + entry.ToolName + sig.Sig
}

// errDirectToolNotFound is mcp-go's own ErrToolNotFound sentinel (the
// error used for an unregistered tool).
// Wrapping it below reproduces the exact text mcp-go emits when its own
// tools/call dispatch cannot find the requested name at all
// (server.go handleToolCall: `fmt.Errorf("tool '%s' not found: %w", name,
// ErrToolNotFound)`).
var errDirectToolNotFound = mcpserver.ErrToolNotFound

// directScopeRefusalError builds the non-disclosing error makeDirectModeHandler's
// OWN profile/server-scope checks return: it
// echoes only the caller-supplied display name, wrapping errDirectToolNotFound
// so the TEXT is byte-identical to what mcp-go's own call-time tool filter
// re-evaluation emits for a name it does not admit at all — never the
// canonical owner the handler's entry closed over.
//
// It is returned as the HANDLER'S OWN error (the function's second return
// value), not built with mcp.NewToolResultError, so the JSON-RPC envelope
// KIND also converges on the filter's: both become a protocol-level error
// response, never a successful call result with isError:true.
//
// mcp-go always maps a handler-returned error to
// mcp.INTERNAL_ERROR (-32603), a code this function cannot override, while
// mcp-go's OWN filter path answers mcp.INVALID_PARAMS (-32602) for the
// identical text. A probe timed inside the narrow profile/config race this
// branch exists for (see the call sites' doc comments) could still tell the
// two apart by that numeric code alone, even though the message and the
// result KIND cannot distinguish "authorized-but-blocked" from
// "genuinely doesn't exist". A ToolHandlerFunc has no way to emit an
// arbitrary top-level JSON-RPC error code — only mcp-go's own dispatch can —
// so full byte-for-byte envelope equality is not achievable from here.
func directScopeRefusalError(displayName string) error {
	return fmt.Errorf("tool '%s' not found: %w", displayName, errDirectToolNotFound)
}

// makeDirectModeHandler creates a handler function for a direct mode tool.
// It handles auth checks, permission enforcement, and upstream calls.
//
// The handler closes over its OWN catalog entry. That is what
// makes "a dispatch can never validate against a definition other than the one
// its own registration advertised" literally true rather than a hope: the entry
// is immutable after publication, and a rebuild produces new entries with new
// handlers, so a request already in flight keeps the definition it was
// dispatched under even as the catalog is swapped underneath it.
func (p *MCPProxyServer) makeDirectModeHandler(entry *directCatalogEntry) mcpserver.ToolHandlerFunc {
	serverName, toolName := entry.ServerName, entry.ToolName
	annotations := entry.Annotations

	return func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		startTime := time.Now()

		// Get session ID for activity logging
		var sessionID string
		if sess := mcpserver.ClientSessionFromContext(ctx); sess != nil {
			sessionID = sess.SessionID()
		}

		// Get request ID from context. Direct-mode calls that did not arrive
		// over an HTTP transport carry none, and every activity this handler
		// emits — including the client token and callability blocks below, which
		// fire before anything else — needs an id a consumer can correlate on.
		// Mint one rather than emit anonymously; a transport-supplied id always
		// wins so the records still line up with the access log.
		//
		// Both ids are resolved BEFORE the client token gates so those denials
		// can emit a correlatable policy decision like every other block.
		requestID := reqcontext.GetRequestID(ctx)
		if requestID == "" {
			requestID = mintActivityRequestID(serverName, toolName)
		}

		args := request.GetArguments()

		profileIndex := p.profileIndexCurrent(ctx)
		ctx, profileResolution := p.resolveForDispatch(ctx, profileIndex)
		profileScope := profileResolution.Scope

		if profileScope != nil && !profileScope.Allows(serverName) {
			refusalErr := directScopeRefusalError(entry.DisplayName)
			p.emitActivityPolicyDecision(ctx, serverName, toolName, sessionID, requestID, "blocked", refusalErr.Error())
			return nil, refusalErr
		}
		if policy := profileResolution.Policy; policy != nil {
			// Direct routing resolves an existing catalog entry above. Nil
			// annotations therefore mean a known but unannotated tool, which
			// must follow the profile's fail-closed unannotated policy.
			intrinsic := profile.IntrinsicTier(annotations, true)
			if admitted, reason, tier := policy.Decide(serverName, toolName, intrinsic); !admitted && reason != profile.ReasonServerNotInProfile {
				errMsg, blockReason := profileToolPolicyRefusal(reason, tier, serverName, toolName, profileRefusalSubject(profileResolution))
				p.emitActivityPolicyDecisionWithBlockReason(ctx, serverName, toolName, sessionID, requestID,
					"blocked", errMsg, string(blockReason))
				return mcp.NewToolResultError(errMsg), nil
			}
		}

		enrichedArgs := injectAuthMetadata(ctx, args)

		if blocked := p.directToolCallabilityBlock(ctx, serverName, toolName, enrichedArgs); blocked != nil {
			p.emitActivityPolicyDecision(ctx, serverName, toolName, sessionID, requestID, "blocked", "direct tool is not callable")
			return blocked, nil
		}

		// Pre-dispatch argument validation, against the
		// schema of THIS handler's own catalog entry.
		//
		// This is what bounds the cost of deferral. A deferred listing
		// advertises `{"type":"object"}`, so an agent guesses its arguments from
		// a compact signature; when the signature was lossy the guess can be
		// wrong, and without this the agent learns that from an opaque upstream
		// error and starts an unbounded debugging loop. Rejecting here, with the
		// FULL stored schema attached, costs exactly one retry.
		//
		// The schema source is entry.ParamsJSON — the STORED upstream schema,
		// never the placeholder the listing advertised, which would accept
		// everything and validate nothing. It is also mode-INDEPENDENT: a
		// full-mode client that guessed wrong gets the same help, because the
		// validator never consults the serialization mode.
		//
		// Validating `args` rather than `enrichedArgs`: injectAuthMetadata adds
		// properties the upstream schema never declared, which a schema with
		// additionalProperties:false would reject — turning our own bookkeeping
		// into the agent's bug. Matches the call_tool_* path (mcp.go).
		//
		// Fail-open is inherited from the validator: an uncompilable
		// or absent schema dispatches exactly as a schemaless proxy would.
		if ok, verr, _ := p.inputValidator.validateArgs(entry.DisplayName, entry.Hash, entry.ParamsJSON, args); !ok {
			detail := oneLineValidationDetail(verr)
			errMsg := fmt.Sprintf("invalid arguments for %s: %s", entry.DisplayName, detail)
			p.logger.Debug("direct mode: pre-dispatch argument validation failed",
				zap.String("server_name", serverName),
				zap.String("tool_name", toolName),
				zap.String("detail", detail))

			// The started/completed-error PAIR, not a bare rejection. A call
			// rejected here never reaches the unconditional
			// emitActivityToolCallStarted below, so without this the activity
			// log would show the call never happening at all. Shapes match the
			// sibling upstream-error emission a few lines down, so the two are
			// one series to a consumer.
			p.emitActivityToolCallStarted(ctx, serverName, toolName, sessionID, requestID, "mcp", enrichedArgs)
			p.emitActivityToolCallCompleted(ctx, serverName, toolName, sessionID, requestID, "mcp", "error", errMsg,
				time.Since(startTime).Milliseconds(), enrichedArgs, "", false, "", nil,
				contracts.ContentTrustForTool(annotations), "", 0, 0, "")

			return invalidParamsErrorResult(entry.DisplayName, entry.ParamsJSON, detail), nil
		}

		// A direct tool call is real work — it earns the session a
		// durable record, and does so BEFORE any activity is emitted so the
		// records carry the right work session.
		//
		// Deliberately AFTER validation: a call rejected for bad arguments did
		// no work upstream, so it does not earn a durable work session, exactly
		// like the policy blocks above it.
		p.markSessionWorked(ctx, sessionID)

		// Emit activity event
		p.emitActivityToolCallStarted(ctx, serverName, toolName, sessionID, requestID, "mcp", enrichedArgs)

		qualifiedName := serverName + ":" + toolName
		var (
			result interface{}
			err    error
		)
		if epoch, ok := p.liveConnectionEpoch(serverName); ok {
			result, err = p.upstreamManager.CallToolOnEpoch(ctx, qualifiedName, args, epoch)
		} else {
			result, err = p.upstreamManager.CallTool(ctx, qualifiedName, args)
		}

		durationMs := time.Since(startTime).Milliseconds()

		// Content trust is derived from openWorldHint.
		directContentTrust := contracts.ContentTrustForTool(annotations)

		if err != nil {

			// The generation moved before the transport: nothing reached
			// the upstream. Same discovery-window body and telemetry bucket
			// as the call_tool_* refusal, with the started record closed.
			if errors.Is(err, managed.ErrConnectionGenerationChanged) {
				errMsg := unresolvedToolIdentityMessage(serverName, toolName, false)

				p.emitActivityToolCallCompleted(ctx, serverName, toolName, sessionID, requestID, "mcp", "error", errMsg, durationMs, enrichedArgs, "", false, "", nil, directContentTrust, "", 0, 0, "")
				p.emitActivityPolicyDecision(ctx, serverName, toolName, sessionID, requestID, "blocked", errMsg)
				return mcp.NewToolResultError(errMsg), nil
			}
			// Emit error activity

			p.emitActivityToolCallCompleted(ctx, serverName, toolName, sessionID, requestID, "mcp", "error", err.Error(), durationMs, enrichedArgs, "", false, "", nil, directContentTrust, "", 0, 0, "")
			return mcp.NewToolResultError(fmt.Sprintf("Error calling %s:%s: %v", serverName, toolName, err)), nil
		}

		// Determine tool variant for activity logging
		toolVariant := contracts.DeriveCallWith(annotations)

		activityStatus, activityErrMsg := activityStatusForResult(result)

		// Forward content blocks (preserving ImageContent, AudioContent, etc.)
		// while applying truncation only to TextContent.
		//
		// Direct mode has a simpler truncator based on ToolResponseLimit; the
		// Truncator type (with caching) is not available here.
		var forwarded *mcp.CallToolResult
		var responseText string
		var truncated bool
		if ctr, ok := result.(*mcp.CallToolResult); ok && ctr != nil {
			newContent := make([]mcp.Content, 0, len(ctr.Content))
			var parts []string
			limit := p.config.ToolResponseLimit
			for _, c := range ctr.Content {
				switch tc := c.(type) {
				case mcp.TextContent:
					txt := tc.Text
					if limit > 0 && len(txt) > limit {
						txt = txt[:safeTruncateBytes(txt, limit)]
						truncated = true
					}
					tc.Text = txt
					newContent = append(newContent, tc)
					parts = append(parts, txt)
				case mcp.ImageContent:
					newContent = append(newContent, tc)
					parts = append(parts, fmt.Sprintf("[image:%s len=%d]", tc.MIMEType, len(tc.Data)))
				case mcp.AudioContent:
					newContent = append(newContent, tc)
					parts = append(parts, fmt.Sprintf("[audio:%s len=%d]", tc.MIMEType, len(tc.Data)))
				default:
					newContent = append(newContent, c)
					if b, err := json.Marshal(c); err == nil {
						parts = append(parts, string(b))
					}
				}
			}
			forwarded = &mcp.CallToolResult{
				Result:            proxyResultEnvelope(ctr),
				Content:           newContent,
				StructuredContent: ctr.StructuredContent,
				IsError:           ctr.IsError,
			}
			responseText = joinTextParts(parts)
		} else {
			// Fallback for non-CallToolResult values (string, struct, etc.)
			switch v := result.(type) {
			case string:
				responseText = v
			default:
				responseBytes, marshalErr := json.Marshal(v)
				if marshalErr != nil {
					responseText = fmt.Sprintf("%v", v)
				} else {
					responseText = string(responseBytes)
				}
			}
			if p.config.ToolResponseLimit > 0 && len(responseText) > p.config.ToolResponseLimit {
				responseText = responseText[:p.config.ToolResponseLimit]
				truncated = true
			}
			forwarded = mcp.NewToolResultText(responseText)
		}

		// Emit completion activity (success, or error when the upstream itself
		// reported one). Sizes are pre-truncation.
		routingResponseBytes := rawByteSize(result)
		routingRequestBytes := rawByteSize(enrichedArgs)
		p.emitActivityToolCallCompleted(ctx, serverName, toolName, sessionID, requestID, "mcp", activityStatus, activityErrMsg, durationMs, enrichedArgs, responseText, truncated, toolVariant, nil, directContentTrust, "", routingRequestBytes, routingResponseBytes, "")

		return forwarded, nil
	}
}

// buildCodeExecModeTools builds the tool set for code_execution routing mode.
// Includes: code_execution + retrieve_tools (for discovery).
// Does NOT include call_tool_read/write/destructive.
func (p *MCPProxyServer) buildCodeExecModeTools() []mcpserver.ServerTool {
	tools := make([]mcpserver.ServerTool, 0, 4)

	// code_execution tool
	tools = append(tools, p.buildCodeExecutionTool()...)

	// retrieve_tools for discovery — instructs to use code_execution (NOT call_tool_*)
	codeExecRetrieveOpts := []mcp.ToolOption{
		mcp.WithDescription("Search and discover available upstream tools using BM25 full-text search. " +
			"Use this to find tools, then use the `code_execution` tool to call them via `call_tool(serverName, toolName, args)` in JavaScript. " +
			"Do NOT use call_tool_read/write/destructive — they are not available in this mode. " +
			"Use natural language to describe what you want to accomplish. " +
			"Response includes a structured `session_risk` object (level, lethal_trifecta, has_open_world_tools, has_destructive_tools, has_write_tools)." +
			retrieveToolsReachNote + retrieveToolsDiagnosticsNote),
		mcp.WithTitleAnnotation("Retrieve Tools"),
		mcp.WithToolTitle("Retrieve Tools"),
		mcp.WithReadOnlyHintAnnotation(true),
		mcp.WithDestructiveHintAnnotation(false),
		mcp.WithOpenWorldHintAnnotation(false),
		mcp.WithString("query",
			mcp.Required(),
			mcp.Description("Natural language description of what you want to accomplish."),
		),
		mcp.WithNumber("limit",
			mcp.Description("Maximum number of tools to return (default: configured tools_limit, max: 100)"),
		),
		// NO retrieveToolsDetailOption() here. describe_tool is absent from
		// the code-execution surface, so a compact response would reference an
		// unavailable second stage;
		// this mode's retrieve_tools always serializes FULL (enforced in
		// handleRetrieveToolsWithMode) and does not expose the detail param.
	}
	codeExecRetrieveOpts = append(codeExecRetrieveOpts, retrieveToolsAnnotationFilterOptions()...)
	retrieveToolsTool := mcp.NewTool("retrieve_tools", codeExecRetrieveOpts...)
	tools = append(tools, p.setProfileServerTool())
	tools = append(tools, mcpserver.ServerTool{
		Tool:    retrieveToolsTool,
		Handler: p.handleRetrieveToolsForMode(config.RoutingModeCodeExecution),
	})

	p.logger.Info("built code execution mode tools",
		zap.Int("tool_count", len(tools)))

	return tools
}

func (p *MCPProxyServer) buildCallToolModeTools() []mcpserver.ServerTool {
	tools := make([]mcpserver.ServerTool, 0, 8)

	// retrieve_tools — instructs to use call_tool_read/write/destructive
	callToolRetrieveOpts := []mcp.ToolOption{
		mcp.WithDescription("Search and discover available upstream tools using BM25 full-text search. " +
			"WORKFLOW: 1) Call this tool first to find relevant tools, 2) Check the 'call_with' field in results " +
			"to determine which variant to use, 3) Call the tool using call_tool_read, call_tool_write, or call_tool_destructive. " +
			"Results include 'annotations' (tool behavior hints like destructiveHint), 'call_with' recommendation, " +
			"and a structured `session_risk` object (level, lethal_trifecta, has_open_world_tools, has_destructive_tools, has_write_tools). " +
			"Compact mode returns one-line signatures ('sig': '*'=required, '~'=lossy) with first-sentence 'desc'; call describe_tool for full schemas. " +
			"Use natural language to describe what you want to accomplish." +
			retrieveToolsReachNote + retrieveToolsDiagnosticsNote),
		mcp.WithTitleAnnotation("Retrieve Tools"),
		mcp.WithToolTitle("Retrieve Tools"),
		mcp.WithReadOnlyHintAnnotation(true),
		mcp.WithDestructiveHintAnnotation(false),
		mcp.WithOpenWorldHintAnnotation(false),
		mcp.WithString("query",
			mcp.Required(),
			mcp.Description("Natural language description of what you want to accomplish. Be specific (e.g., 'create a new GitHub repository', 'get weather for London')."),
		),
		mcp.WithNumber("limit",
			mcp.Description("Maximum number of tools to return (default: configured tools_limit, max: 100)"),
		),
		mcp.WithBoolean("include_stats",
			mcp.Description("Include usage statistics for returned tools (default: false)"),
		),
		retrieveToolsDetailOption(),
	}
	callToolRetrieveOpts = append(callToolRetrieveOpts, retrieveToolsAnnotationFilterOptions()...)
	tools = append(tools, mcpserver.ServerTool{
		Tool:    mcp.NewTool("retrieve_tools", callToolRetrieveOpts...),
		Handler: p.handleRetrieveToolsForMode(config.RoutingModeRetrieveTools),
	})

	// describe_tool: second-stage full definitions, beside retrieve_tools.
	// Direct mode registers its own variant; code-execution mode has none.
	tools = append(tools, mcpserver.ServerTool{
		Tool:    buildDescribeToolTool(),
		Handler: p.handleDescribeTool,
	})

	tools = append(tools, p.setProfileServerTool())

	// call_tool_read / call_tool_write / call_tool_destructive — all three
	// built from the shared helper in mcp.go so schema stays in sync across
	// the default and retrieve_tools routing modes.
	tools = append(tools, mcpserver.ServerTool{
		Tool:    buildCallToolVariantTool(contracts.ToolVariantRead),
		Handler: p.handleCallToolRead,
	})
	tools = append(tools, mcpserver.ServerTool{
		Tool:    buildCallToolVariantTool(contracts.ToolVariantWrite),
		Handler: p.handleCallToolWrite,
	})
	tools = append(tools, mcpserver.ServerTool{
		Tool:    buildCallToolVariantTool(contracts.ToolVariantDestructive),
		Handler: p.handleCallToolDestructive,
	})

	// read_cache for paginated responses
	readCacheTool := mcp.NewTool("read_cache",
		mcp.WithDescription("Retrieve paginated data when mcpproxy indicates a tool response was truncated. Use the cache key provided in truncation messages."),
		mcp.WithTitleAnnotation("Read Cache"),
		mcp.WithToolTitle("Read Cache"),
		mcp.WithReadOnlyHintAnnotation(true),
		mcp.WithDestructiveHintAnnotation(false),
		mcp.WithOpenWorldHintAnnotation(false),
		mcp.WithString("key",
			mcp.Required(),
			mcp.Description("Cache key provided by mcpproxy when a response was truncated."),
		),
		mcp.WithNumber("offset",
			mcp.Description("Starting record offset for pagination (default: 0)"),
		),
		mcp.WithNumber("limit",
			mcp.Description("Maximum number of records to return per page (default: 50, max: 1000)"),
		),
	)
	tools = append(tools, mcpserver.ServerTool{
		Tool:    readCacheTool,
		Handler: p.handleReadCache,
	})

	// code_execution tool (available but not the primary workflow)
	tools = append(tools, p.buildCodeExecutionTool()...)

	p.logger.Info("built call tool mode tools",
		zap.Int("tool_count", len(tools)))

	return tools
}

// buildCodeExecutionTool defines the tool; client permission controls access.
func (p *MCPProxyServer) buildCodeExecutionTool() []mcpserver.ServerTool {

	codeExecutionTool := mcp.NewTool("code_execution",
		mcp.WithDescription(codeExecutionToolDescription),
		mcp.WithTitleAnnotation("Code Execution"),
		mcp.WithToolTitle("Code Execution"),
		mcp.WithDestructiveHintAnnotation(true),
		mcp.WithReadOnlyHintAnnotation(false),
		mcp.WithOpenWorldHintAnnotation(true),
		// Optional `code` + optional `script`; the handler enforces
		// the exactly-one-of rule.
		mcp.WithString("code",
			mcp.Description(codeExecutionCodeDescription),
		),
		mcp.WithString("script",
			mcp.Description(codeExecutionScriptDescription),
		),
		mcp.WithString("language",
			mcp.Description(codeExecutionLanguageDescription),
			mcp.Enum("javascript", "typescript"),
		),
		mcp.WithObject("input",
			openObject(),
			mcp.Description(codeExecutionInputDescription),
		),
		mcp.WithObject("options",
			openObject(),
			mcp.Description(codeExecutionOptionsDescription),
		),
	)
	return []mcpserver.ServerTool{{
		Tool:    codeExecutionTool,
		Handler: p.handleCodeExecution,
	}}
}

// filterProfileV3Tools applies client-controlled built-in visibility to
// every request-scoped tools/list response. Handler gates remain mandatory:
// tool filters are discovery controls, not an execution boundary.
func (p *MCPProxyServer) filterProfileV3Tools(ctx context.Context, tools []mcp.Tool) []mcp.Tool {
	canManage, canExecute := p.clientPermissions(ctx)
	visible := make([]mcp.Tool, 0, len(tools))
	for _, tool := range tools {
		if isDirectManagementTool(tool.Name) && !canManage {
			continue
		}
		if tool.Name == "code_execution" && !canExecute {
			continue
		}
		visible = append(visible, tool)
	}
	return visible
}

// defaultDirectInstructions is the direct surface's own initialize
// instructions, used when the operator configured none.
//
// It is deliberately NOT resolveInstructions' defaultInstructions. That text
// tells agents to "Use 'retrieve_tools'", to call 'call_tool_read/write/
// destructive', and to reach for 'upstream_servers' — none of which
// buildDirectModeTools registers. Advertising a workflow this surface cannot
// serve is worse than saying nothing: the agent's first move fails with an
// unknown tool. So this names only what is actually here: 'server__tool'
// calling and 'describe_tool'.
//
// Naming 'describe_tool' is safe only because it IS registered on this surface
// (withDirectBuiltins, on every rebuild path).
const defaultDirectInstructions = "This is mcpproxy-go, an MCP aggregator proxy that connects multiple upstream MCP servers. " +
	"This endpoint lists every tool of every connected server directly. " +
	"CALLING: each upstream tool appears under its own 'server__tool' name — call it by that name. " +
	"SCHEMAS: call 'describe_tool' with a listed tool name to get its full input schema. " +
	// Carry the project links at the protocol level, matching
	// the default server's instructions.
	"ABOUT: MCPProxy homepage " + branding.Homepage + ", source " + branding.Repo + ", docs " + branding.Docs + "."

// directDeferralLegend explains the compact-signature convention in-band.
// It is static across both serialization modes and phrased
// conditionally ("Some tool descriptions…") so it stays true in full mode,
// where no entry carries a signature — the alternative, emitting it only under
// deferral, would make the initialize response depend on a serialization
// setting and give clients two shapes to cache instead of one.
const directDeferralLegend = "Some tool descriptions end with a compact signature `(param*:type, ...)`: " +
	"`*` marks a required parameter and `~` marks collapsed/lossy details. " +
	"When a signature is present the listed inputSchema is a placeholder, not the real schema — " +
	"flat signatures are directly callable, and for `~`-marked tools call 'describe_tool' " +
	"with the listed tool name to get the full schema."

// resolveDirectInstructions composes the direct server's initialize
// instructions: the operator's configured value when non-empty, otherwise the
// direct-specific default, then a blank line and the deferral legend.
//
// Without the custom branch, the operator-configurable `instructions` key
// would be silently unreachable on the direct surface.
//
// Resolved at server-CONSTRUCTION time, not per request: mcp-go fixes
// WithInstructions on the server instance. That matches the default server
// (mcp.go's NewMCPProxyServer), so editing `instructions` needs a restart on
// both surfaces — unlike the serialization mode, which is read
// live per rebuild so it is hot-reloadable.
func resolveDirectInstructions(custom string) string {
	base := custom
	if base == "" {
		base = defaultDirectInstructions
	}
	return base + "\n\n" + directDeferralLegend
}

// directCustomInstructions reads the operator's configured instructions,
// tolerating a nil config.
func directCustomInstructions(cfg *config.Config) string {
	if cfg == nil {
		return ""
	}
	return cfg.Instructions
}

// initRoutingModeServers creates separate MCP server instances for each routing mode.
// Each server instance has its own set of tools registered appropriate for that mode.
// The main "server" field remains the retrieve_tools mode server (default).
func (p *MCPProxyServer) initRoutingModeServers() {
	// All routing mode servers share the same hooks for session tracking
	opts := []mcpserver.ServerOption{
		mcpserver.WithToolCapabilities(true),
		mcpserver.WithRecovery(),
	}
	// Private short-lived cache hints on every routing-mode
	// server (shared opts, so directOpts inherits them too).
	opts = append(opts, cacheHintServerOptions()...)
	if p.hooks != nil {
		// Mark which real JSON-RPC method
		// produced this request — mcp-go calls these with the SAME ctx it
		// then hands to handleListTools/handleToolCall, synchronously, so the
		// direct-mode discovery filters (mcp_direct_scope.go,
		// mcp_direct_callability.go) can tell a tools/list enumeration from
		// the call-time re-evaluation of one tool, which the filter API
		// itself does not distinguish. See
		// directRequestKindFromContext's doc comment for the full mechanism
		// and why every routing-mode server (not just directServer) safely
		// shares this hook.
		p.hooks.AddBeforeListTools(func(ctx context.Context, _ any, _ *mcp.ListToolsRequest) {
			setDirectRequestKind(ctx, directRequestKindList)
		})
		p.hooks.AddBeforeCallTool(func(ctx context.Context, _ any, _ *mcp.CallToolRequest) {
			setDirectRequestKind(ctx, directRequestKindCall)
		})
		opts = append(opts, mcpserver.WithHooks(p.hooks))
	}
	opts = append(opts, mcpserver.WithToolFilter(p.filterProfileV3Tools))
	// Name the caller's reachable servers in retrieve_tools' description
	// (no-op on the direct surface, which has no retrieve_tools).
	opts = append(opts, mcpserver.WithToolFilter(p.filterAdvertiseServersInRetrieveTools))

	// Create direct mode server. Both direct-mode tool filters are agent-scoped
	// discovery filters and belong only on the direct server (not the shared
	// code-exec / call-tool servers): filterDirectModeToolsForAuth enforces
	// client token server/permission scope, filterDirectToolsForAgentCallability
	// hides tools the agent could not actually invoke.
	directOpts := append([]mcpserver.ServerOption{}, opts...)
	directOpts = append(directOpts,
		mcpserver.WithToolFilter(p.filterDirectModeToolsForAuth),
		mcpserver.WithToolFilter(p.filterDirectToolsForAgentCallability),
		// TERMINAL filter, registered last so it runs after
		// the two above — mcp-go feeds each filter's output to the next, both
		// for tools/list and for the call-time re-evaluation of one tool — and
		// removes the internal identity stamp those two authorize against, for
		// EVERY caller including administrators, before any tool reaches the
		// wire.
		mcpserver.WithToolFilter(stripDirectToolStampFilter),
		// The in-band convention channel. The direct server gets its
		// own text (plus the deferral legend); the code-execution and
		// call-tool servers below share p.server's.
		mcpserver.WithInstructions(resolveDirectInstructions(directCustomInstructions(p.config))),
	)
	p.directServer = mcpserver.NewMCPServer(
		"mcpproxy-go",
		mcpServerVersion(),
		directOpts...,
	)

	// The code-execution and call-tool servers carry the same initialize
	// instructions as p.server. /mcp is served through GetMCPServerForMode,
	// which returns callToolServer (retrieve_tools mode) or codeExecServer —
	// almost never p.server — so without this a client on the default
	// endpoint would get no instructions and would not learn that upstream
	// tools sit behind retrieve_tools. defaultInstructions is routing-mode-aware.
	sharedInstructions := mcpserver.WithInstructions(resolveInstructions(directCustomInstructions(p.config)))

	// Create code execution mode server
	p.codeExecServer = mcpserver.NewMCPServer(
		"mcpproxy-go",
		mcpServerVersion(),
		append(append([]mcpserver.ServerOption{}, opts...), sharedInstructions)...,
	)

	p.callToolServer = mcpserver.NewMCPServer(
		"mcpproxy-go",
		mcpServerVersion(),
		append(append([]mcpserver.ServerOption{}, opts...), sharedInstructions)...,
	)

	// Register tools for code execution mode (static tools that don't change)
	codeExecTools := p.buildCodeExecModeTools()
	for _, st := range codeExecTools {
		p.codeExecServer.AddTool(st.Tool, st.Handler)
	}
	// Seed the content guard with what was just registered, so the first
	// config.reloaded does not re-register (and notify every client about) an
	// identical surface merely because the fingerprint started out empty.
	p.codeExecSurfaceFP = toolSetFingerprint(codeExecTools)

	// Register tools for call tool mode
	callToolModeTools := p.buildCallToolModeTools()
	for _, st := range callToolModeTools {
		p.callToolServer.AddTool(st.Tool, st.Handler)
	}
	p.callToolSurfaceFP = toolSetFingerprint(callToolModeTools)

	// Initial direct rebuild. Done by CALLING RefreshDirectModeTools so
	// there is exactly ONE publisher and one copy of the SetTools-then-publish
	// ordering — a second inline copy here would be the obvious way to introduce
	// the mismatch that ordering exists to prevent.
	//
	// Upstreams are typically not connected yet, so this registers the built-ins
	// and publishes an EMPTY catalog. Both matter: describe_tool must be on
	// the surface from the first request rather than from the first upstream
	// reconcile, and a published (non-nil) catalog puts the discovery filters in
	// deny-on-miss immediately instead of leaving them permissive until then.
	p.RefreshDirectModeTools()

	p.logger.Info("routing mode servers initialized",
		zap.String("default_mode", p.config.RoutingMode))
}

// RefreshDirectModeTools rebuilds the direct mode server's tool set.
// Should be called when upstream servers change (connect/disconnect/tool updates).
func (p *MCPProxyServer) RefreshDirectModeTools() {
	if p.directServer == nil {
		return
	}

	// Serialize rebuilds. Callers are the routing-mode refresh listener, the
	// initial rebuild in initRoutingModeServers (on the constructor goroutine)
	// and the config.reloaded guard. Without the lock, two
	// concurrent rebuilds can interleave as SetTools(A), SetTools(B),
	// publish(A) — leaving catalog A paired with tool map B, which is exactly
	// the mismatch the SetTools-then-publish ordering exists to prevent.
	p.directRefreshMu.Lock()
	defer p.directRefreshMu.Unlock()

	p.refreshDirectModeToolsLocked()
}

// refreshDirectModeToolsLocked is the rebuild body. The caller MUST hold
// directRefreshMu — split out so the reload guard can hold the lock across its
// drift check and the rebuild that check authorizes.
func (p *MCPProxyServer) refreshDirectModeToolsLocked() {
	directTools, cat := p.buildDirectModeTools()

	serverTools := make([]mcpserver.ServerTool, len(directTools))
	copy(serverTools, directTools)

	// ORDER IS LOAD-BEARING. SetTools lands the registry first, the
	// catalog is published immediately after. The two are separate publications
	// and cannot be made one transaction — mcp-go owns its registry read — so the
	// guarantee is directional rather than atomic.
	//
	// What the window exposes:
	//
	//   - renderDirectTools stamps each rendered tool with the
	//     identity of the SAME build that produced its handler, and the
	//     LISTING filters read that stamp first, never a fresh
	//     catalog lookup — so an ADDED, REMOVED, reverse-flipped or tier-changed
	//     name is authorized correctly for every caller, scoped or not, as soon
	//     as SetTools lands it, with no window at all.
	//   - describe_tool resolves against
	//     whichever catalog generation `p.loadDirectCatalog()` currently
	//     returns, so it can lag the listing for the width of this window —
	//     two OPPOSITE, both accepted, transient cross-generation residuals
	//     that close at the publish: an added name is listed but not yet
	//     describable, and a
	//     removed name is still describable from the previous snapshot after
	//     it drops off the listing (stale, not a disclosure — the same
	//     session could have described it one request earlier).
	//
	// Both close at the publish. Schema- and annotations-only changes are
	// invisible in the listing by construction.
	//
	// Skip a rebuild that would change nothing. SetTools notifies every client
	// unconditionally (see mcp_surface_fingerprint.go), and servers.changed fires
	// on ordinary connection churn, so an unguarded rebuild would re-notify
	// everyone on every reconnect attempt.
	//
	// BOTH fingerprints must match. The listing alone is not enough: registry and
	// catalog are published as a pair because a handler closes over its catalog
	// entry, and routing can move — a collision resolving to a different upstream,
	// a changed RequiredPermission — while the listing stays byte-identical.
	toolsFP := toolSetFingerprint(serverTools)
	routingFP := cat.routingFingerprint()
	if directSurfaceUnchanged(p.directSurfaceToolsFP, p.directSurfaceRoutingFP, toolsFP, routingFP) {
		p.logger.Debug("direct mode tools unchanged; skipping rebuild",
			zap.Int("tool_count", len(directTools)))
		return
	}

	p.directServer.SetTools(serverTools...)
	p.publishDirectCatalog(cat)
	p.directSurfaceToolsFP = toolsFP
	p.directSurfaceRoutingFP = routingFP

	p.logger.Info("refreshed direct mode tools",
		zap.Int("tool_count", len(directTools)),
		zap.Uint64("catalog_generation", cat.Generation()))
}

// RefreshCodeExecModeTools rebuilds the code execution mode server's tool catalog description.
// Should be called when upstream servers change to update the available tools listing.
func (p *MCPProxyServer) RefreshCodeExecModeTools() {
	if p.codeExecServer == nil {
		return
	}

	codeExecTools := p.buildCodeExecModeTools()
	serverTools := make([]mcpserver.ServerTool, len(codeExecTools))
	copy(serverTools, codeExecTools)

	// This surface carries BUILT-INS ONLY — buildCodeExecModeTools never
	// iterates upstreams — so an unguarded rebuild on every servers.changed
	// would tell every client its tool list changed on each reconnect.
	// Guarded on content rather than by dropping the servers.changed call, so
	// this stays correct if the surface gains fleet-dependent content.
	fp := toolSetFingerprint(serverTools)
	p.codeExecRefreshMu.Lock()
	defer p.codeExecRefreshMu.Unlock()
	if p.codeExecSurfaceFP == fp {
		p.logger.Debug("code execution mode tools unchanged; skipping rebuild",
			zap.Int("tool_count", len(codeExecTools)))
		return
	}

	p.codeExecServer.SetTools(serverTools...)
	p.codeExecSurfaceFP = fp

	p.logger.Info("refreshed code execution mode tools",
		zap.Int("tool_count", len(codeExecTools)))
}

// GetMCPServerForMode returns the MCP server instance for the given routing mode.
// Falls back to the default retrieve_tools server for unknown modes.
func (p *MCPProxyServer) GetMCPServerForMode(mode string) *mcpserver.MCPServer {
	switch mode {
	case config.RoutingModeDirect:
		if p.directServer != nil {
			return p.directServer
		}
	case config.RoutingModeCodeExecution:
		if p.codeExecServer != nil {
			return p.codeExecServer
		}
	case config.RoutingModeRetrieveTools:
		if p.callToolServer != nil {
			return p.callToolServer
		}
	}
	// Default: retrieve_tools mode

	return p.server
}
