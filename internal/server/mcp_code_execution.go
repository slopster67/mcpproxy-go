package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"path/filepath"
	"reflect"
	"sync"
	"time"

	"github.com/slopster67/mcpproxy-go/internal/auth"
	"github.com/slopster67/mcpproxy-go/internal/codescripts"
	"github.com/slopster67/mcpproxy-go/internal/config"
	"github.com/slopster67/mcpproxy-go/internal/contracts"
	"github.com/slopster67/mcpproxy-go/internal/jsruntime"
	"github.com/slopster67/mcpproxy-go/internal/oauth"
	"github.com/slopster67/mcpproxy-go/internal/profile"
	"github.com/slopster67/mcpproxy-go/internal/reqcontext"
	"github.com/slopster67/mcpproxy-go/internal/storage"
	"github.com/slopster67/mcpproxy-go/internal/upstream"
	"github.com/slopster67/mcpproxy-go/internal/upstream/managed"

	"github.com/mark3labs/mcp-go/v2/mcp"
	mcpserver "github.com/mark3labs/mcp-go/v2/server"
	"go.uber.org/zap"
)

// The code_execution tool is registered on two surfaces — the default tool set
// (registerTools) and the routing-mode builder (buildCodeExecutionTool) — which
// must advertise exactly the same contract. They share these strings so the two
// descriptions cannot drift apart.
const (
	codeExecutionToolDescription = "Execute JavaScript or TypeScript code that orchestrates multiple upstream MCP tools in a single request. " +
		"Use this when you need to combine results from 2+ tools, implement conditional logic, loops, or data transformations " +
		"that would require multiple round-trips otherwise.\n\n" +
		"**When to use**: Multi-step workflows with data transformation, conditional logic, error handling, or iterating over results.\n" +
		"**When NOT to use**: Single tool calls (use call_tool directly), long-running operations (>2 minutes).\n\n" +
		"**Available in code**:\n" +
		"- `input` global: Your input data passed via the 'input' parameter\n" +
		"- `call_tool(serverName, toolName, args)`: Call upstream tools (returns {ok, result} or {ok, error})\n" +
		"- `call_tools(requests, options)`: Call INDEPENDENT tools in parallel. `requests` is an array (max 100) of " +
		"{server, tool, args} objects; `options` is optional and accepts `max_parallel` (1-32, defaults to the configured " +
		"code_execution_max_parallel). Returns one {ok, result} / {ok, error} slot per request, in input order, so one " +
		"failing call never fails the others. Malformed arguments return a single {ok:false, error} envelope and dispatch nothing.\n" +
		"- Modern JavaScript (ES2020+): arrow functions, const/let, template literals, destructuring, classes, for-of, " +
		"optional chaining (?.), nullish coalescing (??), spread/rest, Promises, Symbols, Map/Set, Proxy/Reflect " +
		"(no require(), filesystem, or network access)\n\n" +
		"**TypeScript support**: Set `language: \"typescript\"` to write TypeScript code with type annotations, interfaces, enums, and generics. " +
		"Types are automatically stripped before execution.\n\n" +
		"**Stored scripts**: Instead of `code`, pass `script: \"<name>\"` to run a script stored server-side in the `scripts/` directory inside mcpproxy's data directory — " +
		"a long workflow then costs a name per run instead of its full source. Provide exactly one of `code` or `script`. There is no listing: a client must already know the script name, " +
		"and a name that does not exist is refused without naming what is stored.\n\n" +
		"**Important runtime rules**:\n" +
		"- `call_tool` and `call_tools` are strictly SYNCHRONOUS. Do not use `await`.\n" +
		"- Upstream tools usually return an MCP content array. To parse JSON results: `const data = JSON.parse(res.result.content[0].text);`\n" +
		"- The last evaluated expression in your script is automatically returned as the final output.\n\n" +
		"**Security**: Sandboxed execution with timeout enforcement. Respects existing quarantine and server restrictions."

	codeExecutionCodeDescription = "JavaScript or TypeScript source code (ES2020+) to execute. Supports modern syntax: arrow functions, const/let, template literals, destructuring, " +
		"optional chaining, nullish coalescing. Use `input` to access input data, `call_tool(serverName, toolName, args)` to invoke one upstream tool and " +
		"`call_tools([{server, tool, args}, ...], {max_parallel})` to invoke independent tools in parallel. " +
		"Both are SYNCHRONOUS — do not use await. Return value is the last evaluated expression and must be JSON-serializable. " +
		"Example: `const res = call_tool('github', 'get_user', {username: input.username}); const data = JSON.parse(res.result.content[0].text); ({user: data, timestamp: Date.now()})`"

	codeExecutionLanguageDescription = "Source code language. When set to 'typescript', the code is automatically transpiled to JavaScript before execution. " +
		"Type annotations are stripped, enums and namespaces are converted to JavaScript equivalents. Default: 'javascript'."

	codeExecutionScriptDescription = "Name of a STORED script to execute instead of sending `code` inline. Scripts live as `<name>.js` / `<name>.ts` files in the `scripts/` " +
		"directory inside mcpproxy's data directory and are read fresh on every invocation, so an edited script takes effect immediately. " +
		"Provide EXACTLY ONE of `code` or `script`. The name is a bare identifier (letters, digits, '-' and '_'; 1-64 chars) — never a path. " +
		"The language comes from the file extension (.js → javascript, .ts → typescript); an explicit `language` that contradicts it is an error. " +
		"There is no listing: the caller must already know the script name, and a not-found error names neither the stored scripts nor how many there are. Everything else — `input`, options, sandbox limits, results — behaves exactly as for inline code."

	codeExecutionInputDescription = "Input data accessible as global `input` variable in code (default: {})"

	codeExecutionOptionsDescription = "Execution options: timeout_ms (1-600000, default: 120000), max_tool_calls (>= 0, 0=unlimited), " +
		"allowed_servers (array of server names, empty=all allowed). Batch concurrency is not an execution option: " +
		"call_tools() defaults to the configured code_execution_max_parallel and is overridden per batch with call_tools(requests, {max_parallel})."
)

// handleCodeExecution executes JavaScript code that orchestrates multiple upstream tools
func (p *MCPProxyServer) handleCodeExecution(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	p.logger.Debug("code_execution tool called")
	if !p.clientAllowsCodeExecution(ctx) {
		return mcp.NewToolResultError(auth.ErrCodeExecutionDenied.Error()), nil
	}
	profileIdx := p.profileIndexCurrent(ctx)
	ctx, profileResolution := p.resolveForDispatch(ctx, profileIdx)

	// Parse arguments. MaxToolCalls starts at the unset sentinel so an explicit
	// max_tool_calls: 0 — the documented unlimited override — survives default
	// resolution instead of being floored to the configured limit.
	options := jsruntime.ExecutionOptions{MaxToolCalls: codeExecMaxToolCallsUnset}

	// Get all arguments
	args := request.GetArguments()

	// Extract language (optional, default: "javascript")
	explicitLanguage, errMsg := codeExecStringArg(args, "language")
	if errMsg != "" {
		return mcp.NewToolResultError(errMsg), nil
	}
	if explicitLanguage != "" {
		options.Language = explicitLanguage
	}

	// The source is EITHER inline code or a stored script name, never
	// both and never neither. This is resolved before anything else runs — the
	// handler is the only execution-time resolver on every surface (callers
	// send the NAME, never the content).
	code, scriptName, errMsg := p.resolveCodeExecutionSource(ctx, args, &options)
	if errMsg != "" {
		return mcp.NewToolResultError(errMsg), nil
	}

	// Extract input (optional) - this is an object
	input, ok := args["input"].(map[string]interface{})
	if !ok || input == nil {
		input = make(map[string]interface{})
	}
	options.Input = input

	// Extract options object (optional)
	if optionsObj, ok := args["options"].(map[string]interface{}); ok && optionsObj != nil {
		if errMsg := applyCodeExecutionOptions(optionsObj, &options); errMsg != "" {
			return mcp.NewToolResultError(errMsg), nil
		}
	}

	// Read the LIVE snapshot, not the construction-time one: a hot-reloaded
	// code_execution_* value must reach the executions that start after it.
	// Without a config at all, every knob resolves to its built-in default.
	var configTimeoutMs, configMaxToolCalls, configMaxParallel int
	if cfg := p.currentConfig(); cfg != nil {
		configTimeoutMs = cfg.CodeExecutionTimeoutMs
		configMaxToolCalls = cfg.CodeExecutionMaxToolCalls
		configMaxParallel = cfg.CodeExecutionMaxParallel
	}
	resolveCodeExecutionDefaults(&options, configTimeoutMs, configMaxToolCalls, configMaxParallel)

	// Extract session information from context
	var sessionID, clientName, clientVersion string
	if sess := mcpserver.ClientSessionFromContext(ctx); sess != nil {
		sessionID = sess.SessionID()
		if sessInfo := p.sessionStore.GetSession(sessionID); sessInfo != nil {
			clientName = sessInfo.ClientName
			clientVersion = sessInfo.ClientVersion
		}
	}

	// Generate parent call ID before execution
	executionStart := time.Now()
	parentCallID := mintCorrelationIDAt(executionStart, "code_execution")

	// The execution id IS the parent call id. jsruntime.Execute takes options
	// BY VALUE and only fills a missing ExecutionID on its own copy, so it must
	// be set here for toolCaller.executionID and the parent and nested history
	// records' RequestID to correlate.
	options.ExecutionID = parentCallID

	// Create tool caller adapter that wraps the upstream manager
	toolCaller := &upstreamToolCaller{
		upstreamManager: p.upstreamManager,
		logger:          p.logger,
		executionID:     options.ExecutionID,
		storage:         p.storage,

		parentCallID:  parentCallID,
		sessionID:     sessionID,
		clientName:    clientName,
		clientVersion: clientVersion,

		proxy: p,
	}

	// Log pool metrics before acquisition
	if p.jsPool != nil {
		p.logger.Debug("pool metrics before acquisition",
			zap.String("execution_id", options.ExecutionID),
			zap.Int("pool_size", p.jsPool.Size()),
			zap.Int("available", p.jsPool.Available()),
			zap.Int("in_use", p.jsPool.Size()-p.jsPool.Available()),
		)
	}

	// Acquire a runtime instance from the pool (if pool is available)
	// This limits concurrent executions to the configured pool size
	acquireStart := time.Now()
	if p.jsPool != nil {
		vm, err := p.jsPool.Acquire(ctx)
		if err != nil {
			p.logger.Error("failed to acquire JavaScript runtime from pool",
				zap.String("execution_id", options.ExecutionID),
				zap.Error(err),
			)
			return mcp.NewToolResultError(fmt.Sprintf("Failed to acquire JavaScript runtime: %v", err)), nil
		}

		acquireDuration := time.Since(acquireStart)
		p.logger.Debug("acquired JavaScript runtime from pool",
			zap.String("execution_id", options.ExecutionID),
			zap.Duration("acquire_duration", acquireDuration),
			zap.Int("available_after", p.jsPool.Available()),
		)

		// Release the runtime back to the pool when done
		defer func() {
			releaseStart := time.Now()
			if releaseErr := p.jsPool.Release(vm); releaseErr != nil {
				p.logger.Warn("failed to release JavaScript runtime to pool",
					zap.String("execution_id", options.ExecutionID),
					zap.Error(releaseErr),
				)
			} else {
				p.logger.Debug("released JavaScript runtime to pool",
					zap.String("execution_id", options.ExecutionID),
					zap.Duration("release_duration", time.Since(releaseStart)),
					zap.Int("available_after", p.jsPool.Available()),
				)
			}
		}()
	}

	// Determine effective language for logging
	effectiveLanguage := options.Language
	if effectiveLanguage == "" {
		effectiveLanguage = "javascript"
	}

	// Provide the tool annotation lookup for permission-tier resolution to
	// EVERY execution, not only authenticated ones: it is also the sandbox's
	// identity gate (jsruntime PermissionTierUnresolved), which applies to
	// in-process callers that carry no AuthContext as much as to HTTP callers.
	// The gate-capturing form is wired so the lookup's read is the nested
	// call's ONE persisted read: the bridge (CallToolWithGate) dispatches on
	// the gate it captured rather than taking a second one.
	options.ToolGateFunc = func(serverName, toolName string) (string, jsruntime.ToolGate) {
		if serverName == config.ManagementServerName {
			toolCaller.markManagementUsed()
			if p.managementToolDefinition(ctx, serverName+":"+toolName, false) == nil {
				return jsruntime.PermissionTierUnresolved, nil
			}
			return "destructive", nil
		}
		required, rawGate := p.lookupToolGate(serverName, toolName)
		sandbox, ok := rawGate.(*sandboxGate)
		if !ok || sandbox == nil || profileResolution.Policy == nil {
			return required, rawGate
		}
		identity := sandbox.gate.identity
		if !sandbox.gated {
			identity = p.resolveExactToolIdentityWith(nil, serverName, toolName)
		}
		admitted, reason, tier := profileResolution.Policy.Decide(
			serverName, toolName, profile.IntrinsicTier(identity.Annotations, identity.Found),
		)
		if !admitted && reason != profile.ReasonServerNotInProfile {
			sandbox.profileRefusal, sandbox.profileBlockReason = profileToolPolicyRefusal(reason, tier, serverName, toolName, profileRefusalSubject(profileResolution))
		}
		return required, rawGate
	}

	// Intersect profile scope into code_execution.
	managementScopeAllowed := len(options.AllowedServers) == 0
	for _, name := range options.AllowedServers {
		if name == config.ManagementServerName {
			managementScopeAllowed = true
		}
	}
	p.applyResolvedProfileScopeToExecution(&options, profileResolution.Scope)
	if manage, _ := p.clientPermissions(ctx); manage && managementScopeAllowed && (options.RestrictToAllowed || len(options.AllowedServers) > 0) {
		options.AllowedServers = append(options.AllowedServers, config.ManagementServerName)
	}

	options.RefusalObserver = toolCaller

	// Execute code
	p.logger.Info("executing code",
		zap.String("execution_id", options.ExecutionID),
		zap.String("language", effectiveLanguage),
		zap.String("script", scriptName), // empty for an inline call
		zap.Int("code_length", len(code)),
		zap.Int("timeout_ms", options.TimeoutMs),
		zap.Int("max_tool_calls", options.MaxToolCalls),
		zap.Int("allowed_servers_count", len(options.AllowedServers)),
	)

	// Update execution start time to actual execution start
	executionStart = time.Now()

	result := jsruntime.Execute(ctx, toolCaller, code, options)

	executionDuration := time.Since(executionStart)

	// Log execution result with metrics
	if result.Ok {
		p.logger.Info("code execution succeeded",
			zap.String("execution_id", options.ExecutionID),
			zap.Duration("execution_duration", executionDuration),
			zap.Int("tool_calls_made", len(toolCaller.getToolCalls())),
		)
	} else {
		p.logger.Warn("code execution failed",
			zap.String("execution_id", options.ExecutionID),
			zap.Duration("execution_duration", executionDuration),
			zap.String("error_code", string(result.Error.Code)),
			zap.String("error_message", result.Error.Message),
			zap.Int("tool_calls_made", len(toolCaller.getToolCalls())),
		)
	}

	// Log detailed tool call metrics
	if len(toolCaller.getToolCalls()) > 0 {
		p.logger.Debug("tool call summary",
			zap.String("execution_id", options.ExecutionID),
			zap.Int("total_calls", len(toolCaller.getToolCalls())),
			zap.Any("tool_calls", toolCaller.getToolCalls()),
		)
	}

	recordedArgs, recordedResponse := toolCaller.executionRecord(code, scriptName, effectiveLanguage, options.Input, result)
	// Record the parent code_execution call in history
	codeExecRecord := &storage.ToolCallRecord{
		ID:         parentCallID,
		ServerID:   "code_execution", // Special server ID for built-in tool
		ServerName: "mcpproxy",       // Built-in tool
		ToolName:   "code_execution",
		Arguments:  recordedArgs,
		Response:   recordedResponse,
		Duration:   int64(executionDuration),
		Timestamp:  executionStart,

		RequestID:        options.ExecutionID,
		ExecutionType:    "code_execution",
		MCPSessionID:     sessionID,
		MCPClientName:    clientName,
		MCPClientVersion: clientVersion,
	}

	// Store parent call in history
	if err := p.storage.RecordToolCall(codeExecRecord); err != nil {
		p.logger.Warn("failed to record code_execution call in history",
			zap.String("execution_id", options.ExecutionID),
			zap.Error(err),
		)
	}

	// Update session call count.
	if sessionID != "" {
		p.markSessionWorked(ctx, sessionID)
		p.sessionStore.UpdateSessionStats(sessionID)
	}

	// Convert result to MCP response format
	resultJSON, err := json.Marshal(result)
	if err != nil {
		return nil, fmt.Errorf("failed to serialize result: %w", err)
	}

	// Emit internal tool call event for code_execution.
	//
	// This is the WRAPPER's own outcome and stays keyed on the JS runtime's
	// result: a script that called a tool, got an isError answer and handled it
	// has succeeded. The nested dispatches carry their own classification —
	// upstreamToolCaller.CallTool records an isError:true answer as a failed
	// tool call with the upstream's message — so a failure inside
	// the sandbox is visible in the tool-call history rather than being folded
	// into the wrapper.
	var status, errorMsg string
	if result.Ok {
		status = "success"
	} else {
		status = "error"
		if result.Error != nil {
			errorMsg = result.Error.Message
		}
	}
	codeExecArgs := recordedArgs

	// Content trust for code_execution is derived from the tools called.
	// If any tool called within the JS sandbox has openWorldHint=true (or nil, default true),
	// the entire code_execution result is tagged as untrusted.
	codeExecContentTrust := ""
	toolCallRecords := toolCaller.getToolCalls()
	if len(toolCallRecords) > 0 {
		hasOpenWorldTool := false
		for _, tc := range toolCallRecords {
			// tc carries the split pair the script called; read it exactly.
			toolAnnotations, _ := p.lookupExactToolAnnotations(tc.ServerName, tc.ToolName)
			if contracts.IsOpenWorldTool(toolAnnotations) {
				hasOpenWorldTool = true
				break
			}
		}
		if hasOpenWorldTool {
			codeExecContentTrust = contracts.ContentTrustUntrusted
		} else {
			codeExecContentTrust = contracts.ContentTrustTrusted
		}
	}

	p.emitActivityInternalToolCall(ctx, "code_execution", "", "", "", sessionID, parentCallID, status, errorMsg, executionDuration.Milliseconds(), codeExecArgs, recordedResponse, nil, codeExecContentTrust)

	return &mcp.CallToolResult{
		Content: []mcp.Content{
			mcp.NewTextContent(string(resultJSON)),
		},
	}, nil
}

// codeExecutionSourceXORMessage explains the exactly-one-of rule.
// JSON Schema cannot express XOR, so the schema marks both parameters optional
// and the handler is the one place that enforces the rule — on every surface.
const codeExecutionSourceXORMessage = "Provide exactly one of 'code' (inline source) or 'script' (the name of a script stored in the 'scripts' directory inside mcpproxy's data directory) — not both, not neither."

// codeExecStringArg reads an optional string argument, returning a user-facing
// message when the value is present but is not a string.
func codeExecStringArg(args map[string]interface{}, key string) (value, errMsg string) {
	raw, present := args[key]
	if !present || raw == nil {
		return "", ""
	}
	str, ok := raw.(string)
	if !ok {
		return "", fmt.Sprintf("Parameter '%s' must be a string", key)
	}
	return str, ""
}

// resolveCodeExecutionSource applies the exactly-one-of rule and returns the
// source to execute together with the stored-script name it came from (empty
// for an inline call). For a stored script the language is derived from the
// file extension and written back into options, so everything downstream —
// transpilation, logging, records — sees what actually ran.
func (p *MCPProxyServer) resolveCodeExecutionSource(ctx context.Context, args map[string]interface{}, options *jsruntime.ExecutionOptions) (code, scriptName, errMsg string) {
	code, errMsg = codeExecStringArg(args, "code")
	if errMsg != "" {
		return "", "", errMsg
	}
	scriptName, errMsg = codeExecStringArg(args, "script")
	if errMsg != "" {
		return "", "", errMsg
	}

	if (code == "") == (scriptName == "") {
		return "", "", codeExecutionSourceXORMessage
	}
	if scriptName == "" {
		return code, "", ""
	}

	source, language, err := p.resolveStoredScript(scriptName, options.Language)
	if err != nil {
		return "", "", fmt.Sprintf("Cannot execute stored script: %v", err)
	}
	options.Language = language
	return string(source), scriptName, ""
}

// resolveStoredScript resolves a stored script for the calling client. Every
// refusal is the non-disclosing form: the caller's own name and the reason,
// independent of the directory's contents and location, so a failed call is
// not an oracle for what is stored or where.
func (p *MCPProxyServer) resolveStoredScript(scriptName, explicitLanguage string) ([]byte, string, error) {
	source, language, err := codescripts.Resolve(p.scriptsDir(), scriptName, explicitLanguage)
	if err != nil {
		p.logger.Debug("Stored-script refusal",
			zap.String("script", scriptName),
			zap.String("refusal", fmt.Sprintf("%T", err)))
	}
	return source, language, err
}

// scriptsDir stores scripts under the primary data directory.
func (p *MCPProxyServer) scriptsDir() string {
	if p.config == nil || p.config.DataDir == "" {
		return ""
	}
	return filepath.Join(p.config.DataDir, codescripts.DirName)
}

func (p *MCPProxyServer) warmStoredScripts(dir string) {
	if err := codescripts.Warm(dir); err != nil {
		p.logger.Debug("Stored-script index not built; scoped callers are refused until the directory changes",
			zap.String("dir", dir), zap.Error(err))
	}
}

// codeExecRecordArguments builds the argument payload recorded for a
// code_execution call. History and the activity event share it so they cannot
// disagree: both keep the EXECUTED SOURCE under "code" and,
// for a stored script, additionally name it.
func codeExecRecordArguments(code, scriptName, language string, input map[string]interface{}) map[string]interface{} {
	args := map[string]interface{}{
		"code":     code,
		"input":    input,
		"language": language,
	}
	if scriptName != "" {
		args["script"] = scriptName
	}
	return args
}

// codeExecMaxToolCallsUnset marks max_tool_calls as not supplied by the
// caller. Zero cannot be the sentinel: an explicit 0 is the documented
// unlimited override, distinct from "use the configured limit". Negative
// caller values are rejected during parsing, so the sentinel can never arrive
// from outside.
const codeExecMaxToolCallsUnset = -1

// resolveCodeExecutionDefaults fills config defaults for the options the
// caller left unset. timeout_ms uses zero as its unset marker (0 is out of
// range and rejected during parsing); max_tool_calls uses the sentinel so an
// explicit zero survives. max_parallel has no request-level option — it is
// the configured default for call_tools() batches, which a script overrides
// per batch inside the sandbox.
func resolveCodeExecutionDefaults(opts *jsruntime.ExecutionOptions, configTimeoutMs, configMaxToolCalls, configMaxParallel int) {
	if opts.TimeoutMs == 0 {
		opts.TimeoutMs = configTimeoutMs
	}
	if opts.MaxToolCalls == codeExecMaxToolCallsUnset {
		opts.MaxToolCalls = configMaxToolCalls
	}
	if opts.MaxParallel == 0 {
		opts.MaxParallel = configMaxParallel
	}
}

// applyCodeExecutionOptions parses the `options` object of a code_execution
// call into opts, returning a user-facing message when a value is out of range
// or the wrong type (empty string means the options were applied).
//
// Values arrive in two shapes: JSON-decoded over the MCP transport (float64,
// []interface{}) and Go-typed from an in-process caller that builds the
// arguments map directly (the REST handler behind POST /api/v1/code/exec).
// Both must be accepted, or a REST caller's server restriction would be
// silently dropped.
func applyCodeExecutionOptions(optionsObj map[string]interface{}, opts *jsruntime.ExecutionOptions) string {
	// Parse timeout_ms
	if raw, present := optionsObj["timeout_ms"]; present && raw != nil {
		timeoutMs, ok := codeExecOptionInt(raw)
		if !ok {
			// A fractional 1.9 silently becoming a 1ms budget is worse than an
			// error; same for non-numeric values.
			return "timeout_ms must be an integer"
		}
		opts.TimeoutMs = timeoutMs
		// Validate timeout range
		if opts.TimeoutMs < 1 || opts.TimeoutMs > 600000 {
			return "timeout_ms must be between 1 and 600000 milliseconds"
		}
	}

	// Parse max_tool_calls
	if raw, present := optionsObj["max_tool_calls"]; present && raw != nil {
		maxToolCalls, ok := codeExecOptionInt(raw)
		if !ok {
			// 0.5 truncated to 0 would flip a caller's limit into the
			// unlimited override.
			return "max_tool_calls must be an integer"
		}
		opts.MaxToolCalls = maxToolCalls
		// Validate max_tool_calls
		if opts.MaxToolCalls < 0 {
			return "max_tool_calls cannot be negative"
		}
	}

	// Parse allowed_servers
	switch allowedServers := optionsObj["allowed_servers"].(type) {
	case []string:
		opts.AllowedServers = append(make([]string, 0, len(allowedServers)), allowedServers...)
	case []interface{}:
		serverNames := make([]string, 0, len(allowedServers))
		for _, serverVal := range allowedServers {
			serverName, ok := serverVal.(string)
			if !ok {
				return "allowed_servers must be an array of strings"
			}
			serverNames = append(serverNames, serverName)
		}
		opts.AllowedServers = serverNames
	}

	return ""
}

// codeExecOptionInt normalises the numeric shapes a code_execution option can
// arrive in: float64 from a JSON decode, json.Number from a decoder using
// UseNumber, and the integer types an in-process caller passes through.
func codeExecOptionInt(value interface{}) (int, bool) {
	switch v := value.(type) {
	case float64:
		if v != math.Trunc(v) {
			return 0, false
		}
		return int(v), true
	case float32:
		f := float64(v)
		if f != math.Trunc(f) {
			return 0, false
		}
		return int(f), true
	case int:
		return v, true
	case int32:
		return int(v), true
	case int64:
		return int(v), true
	case json.Number:
		parsed, err := v.Int64()
		if err != nil {
			return 0, false
		}
		return int(parsed), true
	default:
		return 0, false
	}
}

// toolCallRecord tracks information about a single tool call for observability
type toolCallRecord struct {
	ServerName string        `json:"server_name"`
	ToolName   string        `json:"tool_name"`
	StartTime  time.Time     `json:"start_time"`
	Duration   time.Duration `json:"duration"`
	Success    bool          `json:"success"`
	Error      string        `json:"error,omitempty"`
}

// upstreamToolCaller adapts the upstream.Manager to implement jsruntime.ToolCaller
type upstreamToolCaller struct {
	upstreamManager *upstream.Manager
	logger          *zap.Logger
	executionID     string
	storage         *storage.Manager

	managementUsed bool
	toolCalls      []toolCallRecord
	mu             sync.Mutex
	parentCallID   string // ID of the parent code_execution call
	sessionID      string // MCP session ID
	clientName     string // MCP client name
	clientVersion  string // MCP client version

	proxy *MCPProxyServer
}

// sandboxGate is the jsruntime.ToolGate the bridge hands from the sandbox's
// pre-authorization lookup (lookupToolGate) to CallToolWithGate: the ONE
// persisted read of a nested call, captured before the
// JavaScript authorization and consumed by the dispatch, so the identity,
// tier, policy verdict and persisted record all derive from it. gated is
// dispatchGate's second result carried along: false means no gate was
// evaluated (a proxy without storage).
type sandboxGate struct {
	gate               toolGate
	gated              bool
	profileRefusal     string
	profileBlockReason profile.BlockReason
}

// The sandbox asserts these two methods separately (jsruntime
// resolveDispatchGates). Changing ProfilePolicyRefusal's signature would
// silently stop the assertion matching and DROP nested profile refusals, so
// both are pinned at compile time.
var _ interface {
	ProfilePolicyRefusal() string
	ProfilePolicyBlockReason() string
} = (*sandboxGate)(nil)

func (g *sandboxGate) ProfilePolicyRefusal() string {
	if g == nil {
		return ""
	}
	return g.profileRefusal
}

// ProfilePolicyBlockReason is the typed cause (profile_tier, profile_rule,
// profile_unannotated) of the refusal ProfilePolicyRefusal reports, "" when the
// gate is not refused.
func (g *sandboxGate) ProfilePolicyBlockReason() string {
	if g == nil {
		return ""
	}
	return string(g.profileBlockReason)
}

// CallTool implements jsruntime.ToolCaller. It takes the gate read itself,
// for a caller that captured none.
func (u *upstreamToolCaller) CallTool(ctx context.Context, serverName, toolName string, args map[string]interface{}) (interface{}, error) {
	if serverName == config.ManagementServerName {
		return u.callManagement(ctx, toolName, args)
	}
	gate, gated := u.dispatchGate(serverName, toolName)
	return u.callTool(ctx, serverName, toolName, args, gate, gated)
}

// CallToolWithGate implements jsruntime.GatedToolCaller: the dispatch runs
// on the gate lookupToolGate captured for this very call, never on a read
// of its own. A gate that is not the bridge's, that was
// captured for another pair, or that is record-less while this bridge has
// storage to read, is not trusted — the call falls back to CallTool's own
// read, never a bypass of the policy gate.
func (u *upstreamToolCaller) CallToolWithGate(ctx context.Context, serverName, toolName string, args map[string]interface{}, gate jsruntime.ToolGate) (interface{}, error) {
	if serverName == config.ManagementServerName {
		return u.callManagement(ctx, toolName, args)
	}
	captured, ok := gate.(*sandboxGate)
	switch {
	case !ok || captured == nil:
		return u.CallTool(ctx, serverName, toolName, args)
	case captured.gated && (captured.gate.serverName != serverName || captured.gate.toolName != toolName):
		return u.CallTool(ctx, serverName, toolName, args)
	case !captured.gated && u.proxy.storage != nil:
		return u.CallTool(ctx, serverName, toolName, args)
	}
	return u.callTool(ctx, serverName, toolName, args, captured.gate, captured.gated)
}

// callTool is the dispatch proper, over an already-evaluated gate.
func (u *upstreamToolCaller) callTool(ctx context.Context, serverName, toolName string, args map[string]interface{}, gate toolGate, gated bool) (interface{}, error) {
	startTime := time.Now()
	// Bind the nested history write to the incarnation the sub-call started
	// on. The gate's incarnation was read before its record; a bridge without
	// a gate reads it here.
	callInc := gate.incarnation
	if !gated {
		callInc = u.proxy.toolCallIncarnation(serverName)
	}
	// One correlation id per sub-call, shared by every activity record it
	// emits; mintCorrelationIDAt bumps a sequence on every call, so minting
	// twice would never match.
	requestID := mintCorrelationIDAt(startTime, serverName, toolName)

	ctx = reqcontext.WithRequestSource(ctx, reqcontext.SourceInternal)

	u.logger.Debug("calling upstream tool from JavaScript",
		zap.String("execution_id", u.executionID),
		zap.String("server", serverName),
		zap.String("tool", toolName),
	)

	// The sandbox is a dispatch path like any other, so it
	// consumes the same shared gate primitive as call_tool_* and direct mode —
	// a script must not reach a quarantined, disabled or
	// approval-locked tool that every other surface refuses. This covers both
	// ad-hoc code_execution and stored scripts.
	//
	// It is FAIL-OPEN for an unknown server (no stored record): the
	// "server not found" path below answers that case.
	//
	// The gate is this call's ONE persisted read: the policy
	// verdict, the identity hydration and the live certification below all
	// derive from the record it captured, exactly as handleCallToolVariant's
	// do. On the sandbox path it was captured BEFORE the JavaScript
	// authorization (lookupToolGate → CallToolWithGate), so the
	// tier the script was authorized against and the verdict answered here
	// are one read. gated is false only for a proxy without storage,
	// where no persisted record exists to read.
	if refusal := policyRefusalFor(gate, gated); refusal != nil {
		duration := time.Since(startTime)
		u.recordToolCall(serverName, toolName, startTime, duration, false, refusal.Error())
		u.storeToolCallInHistory(serverName, toolName, args, nil, refusal, startTime, duration, callInc)

		u.emitSubCallRefused(ctx, serverName, toolName, requestID, args, refusal, startTime, duration, "")
		return nil, refusal
	}

	// Get the managed client for the server
	client, exists := u.upstreamManager.GetClient(serverName)
	if !exists {
		err := fmt.Errorf("server not found: %s", serverName)
		duration := time.Since(startTime)
		u.recordToolCall(serverName, toolName, startTime, duration, false, err.Error())
		u.storeToolCallInHistory(serverName, toolName, args, nil, err, startTime, duration, callInc)

		u.emitSubCallActivity(ctx, serverName, toolName, requestID, args, nil, err, startTime, duration)
		return nil, err
	}

	// The sandbox's identity
	// read (lookupToolPermission) defers to the server-level verdicts when
	// the StateView reads the server as not connected — but this path has no
	// not-connected check of its own before client.CallTool, and the live
	// client may be connected while the connected event is still in flight.
	// Same closure as handleCallToolVariant: once the live client is found
	// connected, only a CERTIFIED name dispatches (an unlisted name, or one
	// the not-hydrated snapshot retains from a previous generation, is the
	// discovery window). The deferred identity is the GATE's
	// (hydrated from the record it captured) and the live certification is
	// hydrated from that same record; only the StateView and
	// the live client are re-read here.
	deferred := gate.identity
	if !gated {
		// No storage, so no persisted record: the snapshot's own flags
		// stand, as they do for the shared gate on a record-less server.
		deferred = u.proxy.resolveExactToolIdentityWith(nil, serverName, toolName)
	}
	certified, msg, refuse := u.proxy.liveIdentityRefusal(gate.serverConfig, serverName, toolName, deferred, client)
	if refuse {
		refusal := errors.New(msg)
		duration := time.Since(startTime)
		u.recordToolCall(serverName, toolName, startTime, duration, false, refusal.Error())
		u.storeToolCallInHistory(serverName, toolName, args, nil, refusal, startTime, duration, callInc)
		u.emitSubCallRefused(ctx, serverName, toolName, requestID, args, refusal, startTime, duration, "")
		return nil, refusal
	}

	// Call the tool — pinned to the generation the identity check above
	// certified: the
	// managed client re-checks the connection generation after the
	// admission queue and immediately before the transport, so a name
	// certified on connection A can never execute on connection B. The
	// refusal is answered exactly as the pre-dispatch check answers a name
	// whose generation is not certified: the discovery-window body, recorded
	// as a refusal, zero upstream calls.
	var (
		result *mcp.CallToolResult
		err    error
	)
	if certified.certified() {
		result, err = client.CallToolOnEpoch(ctx, toolName, args, certified.DiscoveryEpoch)
	} else {
		result, err = client.CallTool(ctx, toolName, args)
	}

	// This path calls the managed client directly, bypassing
	// upstream.Manager.callTool, so it feeds the failure-rate window itself.
	u.upstreamManager.RecordClientCallOutcome(ctx, client, result, err)
	if errors.Is(err, managed.ErrConnectionGenerationChanged) {
		refusal := errors.New(unresolvedToolIdentityMessage(serverName, toolName, false))
		duration := time.Since(startTime)
		u.recordToolCall(serverName, toolName, startTime, duration, false, refusal.Error())
		u.storeToolCallInHistory(serverName, toolName, args, nil, refusal, startTime, duration, callInc)
		// The managed client refused to send: an error nothing reached the
		// upstream for.
		u.emitSubCallRefused(ctx, serverName, toolName, requestID, args, refusal, startTime, duration, "")
		return nil, refusal
	}
	duration := time.Since(startTime)

	u.recordUpstreamCall(serverName, toolName, startTime, duration, result, err)
	u.storeToolCallInHistory(serverName, toolName, args, result, err, startTime, duration, callInc)

	u.emitSubCallActivity(ctx, serverName, toolName, requestID, args, result, err, startTime, duration)

	u.logger.Debug("upstream tool call completed",
		zap.String("execution_id", u.executionID),
		zap.String("server", serverName),
		zap.String("tool", toolName),
		zap.Duration("duration", duration),
		zap.Bool("success", err == nil && !upstreamAnsweredWithError(result)),
	)

	if err != nil {
		return nil, err
	}

	return result, nil
}

// subCallActivityResponseLimit caps the response text recorded for ONE
// sandboxed sub-call. One execution can issue up to max_tool_calls of them, so
// the per-record budget is deliberately far smaller than the 64KB a single
// direct dispatch is allowed.
const subCallActivityResponseLimit = 8 * 1024

func (u *upstreamToolCaller) emitSubCallActivity(ctx context.Context, serverName, toolName, requestID string, args map[string]interface{}, result interface{}, callErr error, startTime time.Time, duration time.Duration) {
	status, errMsg, responseText, truncated := subCallActivityOutcome(result, callErr)
	requestBytes, responseBytes := subCallByteSizes(args, result)
	u.proxy.emitActivityToolCallCompleted(ctx,
		serverName, toolName, u.sessionID, requestID, string(storage.ActivitySourceInternal),
		status, errMsg, duration.Milliseconds(), args, responseText, truncated,
		"", nil, "", "", requestBytes, responseBytes, u.parentCallID)
}

// subCallByteSizes returns the pre-truncation JSON byte lengths of a sandbox
// sub-call's arguments and result, the same way the top-level dispatch computes
// them. Throughout the activity log 0 bytes means UNKNOWN, not free.
//
// A nil result yields 0 response bytes. That is a TRUE zero rather than an
// unknown: the caller only reaches this with a nil result when the upstream
// never answered, and a call that produced no response contributed no response
// tokens.
// result is the untyped dispatch result, so a typed-nil pointer can arrive
// inside a non-nil interface; that marshals to "null" rather than nothing, and
// reflect is what tells the two apart.
func subCallByteSizes(args map[string]interface{}, result interface{}) (requestBytes, responseBytes int) {
	if result != nil {
		if rv := reflect.ValueOf(result); rv.Kind() == reflect.Pointer && rv.IsNil() {
			result = nil
		}
	}
	return rawByteSize(args), rawByteSize(result)
}

func (u *upstreamToolCaller) emitSubCallRefused(ctx context.Context, serverName, toolName, requestID string, args map[string]interface{}, refusal error, startTime time.Time, duration time.Duration, blockReason string) {
	u.proxy.emitActivityToolCallCompletedWithBlockReason(ctx,
		serverName, toolName, u.sessionID, requestID, string(storage.ActivitySourceInternal),
		storage.ActivityStatusBlocked, refusal.Error(), duration.Milliseconds(), args, "", false,
		// The policy gate refused this before dispatch, so there IS no response
		// and 0 response bytes is a true zero, not an unmeasured one. The
		// request was still formed and is measured like any other.
		"", nil, "", "", rawByteSize(args), 0, u.parentCallID, blockReason)
}

func subCallActivityOutcome(result interface{}, callErr error) (status, errMsg, response string, truncated bool) {
	status, errMsg = activityStatusForResult(result)
	if callErr != nil {
		return storage.ActivityStatusError, callErr.Error(), "", false
	}
	if result == nil {
		return status, errMsg, "", false
	}
	encoded, mErr := json.Marshal(result)
	if mErr != nil {
		return status, errMsg, "", false
	}
	response = string(encoded)
	if len(response) > subCallActivityResponseLimit {
		// safeTruncateBytes backs the cut up to a rune boundary, so the stored
		// text is always valid UTF-8.
		response = response[:safeTruncateBytes(response, subCallActivityResponseLimit)]
		truncated = true
	}
	return status, errMsg, response, truncated
}

// dispatchGate is the sandbox bridge's ONE evaluation of the shared per-tool
// policy gates for a call — the single persisted read of the dispatch,
// which policyRefusalFor, the identity hydration and the live
// certification in CallTool all consume. It reports false, with the zero
// gate, for a proxy without storage, where there is no persisted record to
// read and the gate is skipped.
func (u *upstreamToolCaller) dispatchGate(serverName, toolName string) (toolGate, bool) {
	if u.proxy.storage == nil {
		return toolGate{}, false
	}
	// The script named the server and the raw tool separately, so the pair
	// is already split and is gated exactly (never re-normalized).
	return u.proxy.evaluateExactToolGate(serverName, toolName), true
}

func policyRefusalFor(gate toolGate, gated bool) error {
	if !gated {
		return nil
	}
	serverName, toolName := gate.serverName, gate.toolName
	if gate.serverConfig == nil {
		if gate.storageErr != nil {
			// The record exists as far as anyone knows — it just could not be
			// read. Fail-open is licensed for a server that is genuinely
			// UNKNOWN, not for one whose policy the proxy failed to load;
			// treating a read failure as "unknown server" would let a script
			// through a quarantine gate by breaking the database.
			return fmt.Errorf("cannot verify policy for server %q: %w", serverName, gate.storageErr)
		}
		// Unknown server: leave the existing "server not found" path to answer.
		return nil
	}
	if gate.callable() {
		return nil
	}

	switch gate.lockStatus {
	case storage.ToolApprovalStatusPending:
		if isImplicitPendingApproval(gate.approval) {
			// No stored record yet, so there is nothing to
			// approve — the server's next discovery pass files it.
			return fmt.Errorf("tool %s:%s has no approval record yet and cannot be called while tool-level quarantine is active; restart server %q from its settings and retry", serverName, toolName, serverName)
		}
		return fmt.Errorf("tool %s:%s is pending security approval and cannot be called", serverName, toolName)
	case storage.ToolApprovalStatusChanged:
		return fmt.Errorf("tool %s:%s changed since approval and is locked pending review", serverName, toolName)
	}
	return fmt.Errorf("%s", gate.blockedMessage())
}

// upstreamAnsweredWithError reports whether a dispatched result is an MCP
// answer flagged isError:true — a failure the upstream reported over a
// successful transport hop.
func upstreamAnsweredWithError(result interface{}) bool {
	status, _ := activityStatusForResult(result)
	return status != "success"
}

// nestedCallFailure classifies one nested dispatch from the JS sandbox,
// returning (success, message). A Go error wins over the upstream's own text:
// when the call never completed, that is the useful explanation.
func nestedCallFailure(result interface{}, err error) (bool, string) {
	if err != nil {
		return false, err.Error()
	}
	status, msg := activityStatusForResult(result)
	if status == "success" {
		return true, ""
	}
	return false, msg
}

// recordUpstreamCall records a nested tool call with timing and its classified
// outcome (thread-safe).
func (u *upstreamToolCaller) recordUpstreamCall(serverName, toolName string, startTime time.Time, duration time.Duration, result interface{}, err error) {
	success, errMsg := nestedCallFailure(result, err)
	u.recordToolCall(serverName, toolName, startTime, duration, success, errMsg)
}

// recordToolCall records a tool call with timing and result information (thread-safe)
func (u *upstreamToolCaller) recordToolCall(serverName, toolName string, startTime time.Time, duration time.Duration, success bool, errMsg string) {
	u.mu.Lock()
	defer u.mu.Unlock()

	u.toolCalls = append(u.toolCalls, toolCallRecord{
		ServerName: serverName,
		ToolName:   toolName,
		StartTime:  startTime,
		Duration:   duration,
		Success:    success,
		Error:      errMsg,
	})
}

// getToolCalls returns all recorded tool calls (thread-safe)
func (u *upstreamToolCaller) getToolCalls() []toolCallRecord {
	u.mu.Lock()
	defer u.mu.Unlock()

	// Return a copy to prevent external modification
	calls := make([]toolCallRecord, len(u.toolCalls))
	copy(calls, u.toolCalls)
	return calls
}

// storeToolCallInHistory stores a nested tool call in the database for history tracking
func (u *upstreamToolCaller) storeToolCallInHistory(serverName, toolName string, args map[string]interface{}, result interface{}, callErr error, startTime time.Time, duration time.Duration, callInc int64) {
	// Skip if storage is not available
	if u.storage == nil {
		return
	}

	// Get server config to generate server ID
	serverConfig, err := u.storage.GetUpstreamServer(serverName)
	if err != nil {
		u.logger.Warn("failed to get server config for history recording",
			zap.String("server", serverName),
			zap.String("execution_id", u.executionID),
			zap.Error(err),
		)
		return
	}

	// Create tool call record for history
	record := &storage.ToolCallRecord{
		ID:         mintCorrelationIDAt(startTime, toolName),
		ServerID:   storage.GenerateServerID(serverConfig),
		ServerName: serverName,
		ToolName:   toolName,
		Arguments:  args,
		Response:   result,
		Duration:   int64(duration),
		Timestamp:  startTime,

		RequestID:        u.executionID, // Use execution ID as request ID to link nested calls
		ParentCallID:     u.parentCallID,
		ExecutionType:    "code_execution",
		MCPSessionID:     u.sessionID,
		MCPClientName:    u.clientName,
		MCPClientVersion: u.clientVersion,
	}

	// An upstream that answered isError:true failed, even though the
	// transport hop did not; record the upstream's message, as call_tool_* does.
	if _, errMsg := nestedCallFailure(result, callErr); errMsg != "" {
		record.Error = errMsg
	}

	// Store in database
	if _, err := u.storage.RecordToolCallIfIncarnation(record, callInc); err != nil {
		u.logger.Warn("failed to store nested tool call in history",
			zap.String("server", serverName),
			zap.String("tool", toolName),
			zap.String("execution_id", u.executionID),
			zap.Error(err),
		)
	} else {
		u.logger.Debug("stored nested tool call in history",
			zap.String("server", serverName),
			zap.String("tool", toolName),
			zap.String("execution_id", u.executionID),
			zap.String("record_id", record.ID),
		)
	}
}

func (p *MCPProxyServer) applyResolvedProfileScopeToExecution(options *jsruntime.ExecutionOptions, profileScope *profile.ProfileScope) {
	if options == nil {
		return
	}
	if profileScope == nil {
		return
	}

	options.RestrictToAllowed = true
	profileServers := profileScope.AllowedServerNames()
	if len(options.AllowedServers) == 0 {
		// No caller-supplied restriction: the profile is the restriction.
		// AllowedServerNames returns a non-nil empty slice for a deny-all
		// scope, which RestrictToAllowed then enforces as "nothing".
		options.AllowedServers = profileServers
		return
	}

	// Intersect the caller-supplied list with the profile's servers.
	profileSet := make(map[string]struct{}, len(profileServers))
	for _, s := range profileServers {
		profileSet[s] = struct{}{}
	}
	intersected := make([]string, 0, len(options.AllowedServers))
	for _, s := range options.AllowedServers {
		if _, ok := profileSet[s]; ok {
			intersected = append(intersected, s)
		}
	}
	options.AllowedServers = intersected
}

// lookupToolGate is lookupToolPermission with the gate it decided the tier
// from — the jsruntime.ToolGateLookup the sandbox is wired with. The gate
// it returns is the nested call's ONE persisted read: the
// tier the sandbox authorizes against is derived from it here, and the
// bridge's CallToolWithGate answers the policy verdict, hydrates the
// identity and certifies the live client from the same capture, so an
// operator's write that lands between the sandbox's authorization and its
// dispatch owns every LATER call and never splits one call's verdict.
// For a proxy without storage the gate is the record-less marker
// (sandboxGate.gated=false), which the bridge treats exactly as its own
// record-less read.
func (p *MCPProxyServer) lookupToolGate(serverName, toolName string) (string, jsruntime.ToolGate) {
	var (
		identity toolIdentity
		record   *config.ServerConfig
		captured = &sandboxGate{}
	)
	if p.storage != nil {
		captured.gate, captured.gated = p.evaluateExactToolGate(serverName, toolName), true
		gate := captured.gate
		if gate.serverDisabled() {
			return tierForAnnotations(gate.identity.Annotations, gate.identity.Found), captured
		}
		identity, record = gate.identity, gate.serverConfig
	} else {
		// A proxy without storage: no persisted
		// record exists to read, so the snapshot's own flags stand.
		identity = p.resolveExactToolIdentityWith(nil, serverName, toolName)
	}
	if identity.Unresolved() {
		return jsruntime.PermissionTierUnresolved, captured
	}
	// The identity read defers
	// when the StateView reads the server as not connected, relying on the
	// server-level verdicts — but this path has no not-connected check of
	// its own before client.CallTool, and the live client may be connected
	// while the server_connected event is still in flight. Same closure as
	// handleCallToolVariant (liveIdentityRefusal): once the live client is
	// found connected, any name short of certified — unlisted, or retained
	// from a previous generation on a not-hydrated snapshot — is unresolved,
	// and the sandbox answers with the permission envelope before any
	// AuthInfo check. The closure is hydrated from the gate's record above:
	// this lookup, like a dispatch, reads the persisted record once.

	if identity.ServerKnown && !identity.SnapshotHydrated && p.upstreamManager != nil {
		if client, ok := p.upstreamManager.GetClient(serverName); ok {
			if _, _, refuse := p.liveIdentityRefusal(record, serverName, toolName, identity, client); refuse {
				return jsruntime.PermissionTierUnresolved, captured
			}
		}
	}
	return tierForAnnotations(identity.Annotations, identity.Found), captured
}

// tierForAnnotations maps one lookupToolAnnotationsFound result to the
// permission tier it requires. It is split from lookupToolPermission so a
// caller that already holds the StateView read (handleCallToolVariant, which
// needs the same annotations for intent validation) classifies the tier from
// THAT read rather than taking a second, independent snapshot. found=false
// reaches it only when the proxy holds no snapshot for the server at all, or
// when the snapshot is not authoritative because of the server's own state
// (quarantined, disabled, not connected) and a server-level verdict owns the
// name — an undiscovered name on a known, connected server is refused before
// any tier is derived (toolIdentity.Unresolved).
func tierForAnnotations(annotations *config.ToolAnnotations, found bool) string {
	if !found {
		return contracts.OperationTypeDestructive
	}
	if perm := contracts.ToolVariantToOperationType[contracts.DeriveCallWith(annotations)]; perm != "" {
		return perm
	}
	return contracts.OperationTypeRead
}

// Administration sub-calls use the same handlers and live permission checks.
// The handler records only the operation, never secret/token payloads.
func (u *upstreamToolCaller) callManagement(ctx context.Context, name string, args map[string]interface{}) (interface{}, error) {
	u.markManagementUsed()
	if !u.proxy.clientAllowsCodeExecution(ctx) {
		return nil, fmt.Errorf("code execution is not allowed")
	}
	start := time.Now()
	result, err := u.proxy.callManagementTool(withSubCallActivity(ctx), name, args, contracts.ToolVariantDestructive)
	if err == nil && result.IsError {
		err = fmt.Errorf("management operation failed")
		for _, content := range result.Content {
			if text, ok := content.(mcp.TextContent); ok {
				err = fmt.Errorf("%s", text.Text)
				break
			}
		}
	}
	// Recorded as a sub-call of the script, with credentials redacted.
	var recorded any
	if err == nil {
		recorded = redactManagementJSON(result.Content)
	}
	duration := time.Since(start)
	u.emitSubCallActivity(ctx, config.ManagementServerName, name, mintCorrelationID(config.ManagementServerName, name), redactedManagementMap(args), recorded, err, start, duration)
	u.recordToolCall(config.ManagementServerName, name, start, duration, err == nil, errorText(err))
	if err != nil {
		return nil, err
	}
	return result, nil
}

func errorText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// A script that used administration tools can pass a secret in its input or
// return a newly minted client token, so its history entry is recorded with
// credentials redacted. The caller still gets the actual result.
func (u *upstreamToolCaller) executionRecord(code, script, language string, input map[string]interface{}, response interface{}) (map[string]interface{}, interface{}) {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.managementUsed {
		return codeExecRecordArguments(oauth.MaskDetectedSecrets(code), script, language, redactedManagementMap(input)), redactManagementJSON(response)
	}
	return codeExecRecordArguments(code, script, language, input), response
}

func (u *upstreamToolCaller) markManagementUsed() {
	u.mu.Lock()
	u.managementUsed = true
	u.mu.Unlock()
}

// ObserveRefusal records sandbox refusals that never reach the upstream bridge.
func (u *upstreamToolCaller) ObserveRefusal(report jsruntime.CallRefusal) {
	if report.ServerName == config.ManagementServerName {
		u.markManagementUsed()
		report.Arguments = redactedManagementMap(report.Arguments)
		report.Message = "Management call denied"
	}
	startedAt := time.Now()
	refusal := errors.New(report.Message)
	incarnation := u.proxy.toolCallIncarnation(report.ServerName)
	u.storeToolCallInHistory(report.ServerName, report.ToolName, report.Arguments, nil, refusal, startedAt, 0, incarnation)
	u.emitSubCallRefused(report.Ctx, report.ServerName, report.ToolName, mintCorrelationID(report.ServerName, report.ToolName), report.Arguments, refusal, startedAt, 0, report.BlockReason)
}
