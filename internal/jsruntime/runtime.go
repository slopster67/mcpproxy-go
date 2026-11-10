package jsruntime

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"sync"
	"time"

	"github.com/dop251/goja"
	"github.com/slopster67/mcpproxy-go/internal/profile"
)

// ExecutionOptions contains optional parameters for JavaScript execution
type ExecutionOptions struct {
	RefusalObserver RefusalObserver
	Input           map[string]interface{} // Input data accessible as global `input` variable
	TimeoutMs       int                    // Execution timeout in milliseconds
	MaxToolCalls    int                    // Maximum number of call_tool() invocations (0 = unlimited)
	AllowedServers  []string               // Whitelist of allowed server names (empty = all allowed, unless RestrictToAllowed)
	ExecutionID     string                 // Execution ID the caller logs under; not used by the sandbox
	Language        string                 // Source language: "javascript" (default) or "typescript"
	MaxParallel     int                    // Default concurrency for call_tools() batches (0 = built-in default; per-batch options override this)

	// RestrictToAllowed enforces AllowedServers even when it is empty. Set by the
	// profile path: an active profile with an empty effective server set
	// (deny-all profile, or a non-overlapping token∩profile) must deny ALL
	// call_tool() invocations rather than fall back to the "empty = allow all"
	// default. Leave false for unrestricted code_execution and client token scopes.
	RestrictToAllowed bool

	// ToolGateFunc looks up the permission tier a tool requires AND hands back
	// the opaque per-call gate the host resolved it from, which the dispatch
	// that follows consumes through GatedToolCaller instead of taking a
	// second, independent read. Nil means no per-tool lookup.
	ToolGateFunc ToolGateLookup
}

// PermissionTierUnresolved is the ToolGateLookup outcome for a name the
// populated discovery snapshot of a KNOWN, CONNECTED server does not contain.
// It is not a tier: no permission set — not
// even an administrator's, which passes every tier check — may dispatch a
// tool the proxy cannot identify, so checkDispatchGates refuses the call with
// PERMISSION_DENIED and the upstream is never asked. A lookup that has no
// opinion (server unknown, no runtime, or a snapshot emptied by the server's
// own state — quarantined, disabled, disconnected) keeps answering with a
// tier so the server-level verdicts downstream decide.
const PermissionTierUnresolved = "unresolved"

// ToolGate is the opaque per-call capture a ToolGateLookup hands back beside
// the tier: whatever the host read to authorize the call, so the dispatch
// that follows runs on THAT read rather than on a second one that may
// disagree with it. jsruntime never inspects
// it — it only carries it, unchanged, from the lookup to the dispatch of the
// same call. A nil gate means the lookup captured nothing and the dispatch
// falls back to ToolCaller.CallTool.
type ToolGate interface{}

// ToolGateLookup returns the permission tier required for a tool — one of
// "read", "write", "destructive", or PermissionTierUnresolved when the tool's
// identity cannot be resolved on a server the proxy knows, in which case the
// call is refused — together with the gate it decided the tier from, which is
// handed to GatedToolCaller.CallToolWithGate if the call is authorized.
type ToolGateLookup func(serverName, toolName string) (tier string, gate ToolGate)

// ToolCaller is an interface for calling upstream MCP tools
type ToolCaller interface {
	CallTool(ctx context.Context, serverName, toolName string, args map[string]interface{}) (interface{}, error)
}

// GatedToolCaller is the optional extension of ToolCaller a host implements
// to dispatch on the gate its ToolGateLookup captured for the same call. The
// sandbox uses it only when both are wired and the lookup returned a non-nil
// gate; every other combination dispatches through CallTool, unchanged.
type GatedToolCaller interface {
	ToolCaller
	CallToolWithGate(ctx context.Context, serverName, toolName string, args map[string]interface{}, gate ToolGate) (interface{}, error)
}

// dispatchTool performs one upstream call on the gate captured for it when
// the caller can consume one, and through the plain ToolCaller otherwise.
func dispatchTool(ctx context.Context, caller ToolCaller, serverName, toolName string, args map[string]interface{}, gate ToolGate) (interface{}, error) {
	if gate != nil {
		if gated, ok := caller.(GatedToolCaller); ok {
			return gated.CallToolWithGate(ctx, serverName, toolName, args, gate)
		}
	}
	return caller.CallTool(ctx, serverName, toolName, args)
}

// ExecutionContext tracks the state of a single JavaScript execution
type ExecutionContext struct {
	refusalObserver   RefusalObserver
	toolCallCount     int // call_tool()/call_tools() dispatches so far, for MaxToolCalls
	toolCaller        ToolCaller
	maxToolCalls      int
	maxParallel       int // configured default concurrency for call_tools() batches
	allowedServerMap  map[string]bool
	restrictToAllowed bool // enforce allowedServerMap even when empty (deny-all profile)

	// ctx is the execution's timeout context, wired by Execute. Every upstream
	// dispatch — the lone call_tool() and call_tools() batch workers alike —
	// runs under it so an in-flight call is cancelled with the execution
	// instead of outliving it.
	ctx context.Context

	toolGateFunc ToolGateLookup
}

// newExecutionContext builds the per-execution state Execute drives and the
// host functions enforce against.
func newExecutionContext(caller ToolCaller, opts ExecutionOptions) *ExecutionContext {
	execCtx := &ExecutionContext{
		refusalObserver:   opts.RefusalObserver,
		toolCaller:        caller,
		maxToolCalls:      opts.MaxToolCalls,
		maxParallel:       opts.MaxParallel,
		allowedServerMap:  make(map[string]bool),
		restrictToAllowed: opts.RestrictToAllowed,
		toolGateFunc:      opts.ToolGateFunc,
	}

	// Build allowed server map for fast lookup
	for _, serverName := range opts.AllowedServers {
		execCtx.allowedServerMap[serverName] = true
	}

	return execCtx
}

// Execute runs JavaScript or TypeScript code in a sandboxed environment with tool call capabilities.
// When opts.Language is "typescript", the code is transpiled to JavaScript before execution.
func Execute(ctx context.Context, caller ToolCaller, code string, opts ExecutionOptions) *Result {
	execCtx := newExecutionContext(caller, opts)

	// Validate language parameter
	if langErr := ValidateLanguage(opts.Language); langErr != nil {
		return NewErrorResult(langErr)
	}

	// Transpile TypeScript to JavaScript if needed
	if opts.Language == "typescript" {
		transpiled, transpileErr := TranspileTypeScript(code)
		if transpileErr != nil {
			return NewErrorResult(transpileErr)
		}
		code = transpiled
	}

	// Initialize Goja VM
	vm := goja.New()

	// Set up sandbox restrictions
	setupSandbox(vm)

	// Bind input global variable
	if opts.Input == nil {
		opts.Input = make(map[string]interface{})
	}
	if err := vm.Set("input", opts.Input); err != nil {
		return NewErrorResult(NewJsError(ErrorCodeRuntimeError, fmt.Sprintf("failed to set input: %v", err)))
	}

	// Bind call_tool function
	callToolFunc := execCtx.makeCallToolFunction(vm)
	if err := vm.Set("call_tool", callToolFunc); err != nil {
		return NewErrorResult(NewJsError(ErrorCodeRuntimeError, fmt.Sprintf("failed to set call_tool: %v", err)))
	}

	// Bind call_tools function (batched fan-out)
	callToolsFunc := execCtx.makeCallToolsFunction(vm)
	if err := vm.Set("call_tools", callToolsFunc); err != nil {
		return NewErrorResult(NewJsError(ErrorCodeRuntimeError, fmt.Sprintf("failed to set call_tools: %v", err)))
	}

	// Set up timeout enforcement
	timeoutMs := opts.TimeoutMs
	if timeoutMs <= 0 {
		timeoutMs = 120000 // Default 2 minutes
	}

	timeoutCtx, cancel := context.WithTimeout(ctx, time.Duration(timeoutMs)*time.Millisecond)
	defer cancel()

	// Batch workers dispatch under the execution's timeout context. Assigned
	// before the script goroutine starts, so nothing reads it concurrently.
	execCtx.ctx = timeoutCtx

	// Run JavaScript with timeout enforcement
	resultChan := make(chan *Result, 1)
	go func() {
		resultChan <- executeWithVM(vm, code)
	}()

	// Wait for execution or timeout
	select {
	case result := <-resultChan:
		return result
	case <-timeoutCtx.Done():
		// Timeout occurred. Stop the VM: without this the goroutine keeps
		// spinning on whatever the script was doing (a busy loop burns a
		// core for the life of the process) and the pool slot it held is
		// released the moment this returns, so nothing else bounds it. The
		// interrupt surfaces inside RunString as *goja.InterruptedError; an
		// upstream call in flight is cancelled through timeoutCtx, which
		// every dispatch path runs under.
		vm.Interrupt("execution timed out")
		return NewErrorResult(NewJsError(ErrorCodeTimeout, "JavaScript execution timed out"))
	}
}

// executeWithVM runs the JavaScript code in the given VM and returns the result.
//
// An interrupt (Execute's timeout) that lands while RunString is running comes
// back as its error; one that lands while value.Export() is still running
// script code — a getter on the returned object, a toJSON — is raised by goja
// as a PANIC carrying *goja.InterruptedError. This goroutine is the only thing
// standing between that panic and the process, so it recovers and reports a
// timeout instead. Execute has already answered the caller by then; the
// recovered result only keeps the goroutine's exit orderly.
func executeWithVM(vm *goja.Runtime, code string) (result *Result) {
	defer func() {
		if r := recover(); r != nil {
			if _, ok := r.(*goja.InterruptedError); ok {
				result = NewErrorResult(NewJsError(ErrorCodeTimeout, "JavaScript execution timed out"))
				return
			}
			result = NewErrorResult(NewJsError(ErrorCodeRuntimeError, fmt.Sprintf("script panicked: %v", r)))
		}
	}()
	// Compile the code first to catch syntax errors
	_, err := goja.Compile("", code, false)
	if err != nil {
		// Extract syntax error details
		if exception, ok := err.(*goja.Exception); ok {
			return NewErrorResult(NewJsErrorWithStack(
				ErrorCodeSyntaxError,
				exception.String(),
				exception.String(),
			))
		}
		return NewErrorResult(NewJsError(ErrorCodeSyntaxError, err.Error()))
	}

	// Execute the code
	value, err := vm.RunString(code)
	if err != nil {
		// Extract runtime error details
		if exception, ok := err.(*goja.Exception); ok {
			stack := exception.String() // Stack trace is included in the exception string
			return NewErrorResult(NewJsErrorWithStack(
				ErrorCodeRuntimeError,
				exception.Error(),
				stack,
			))
		}
		return NewErrorResult(NewJsError(ErrorCodeRuntimeError, err.Error()))
	}

	// Export the result to Go value
	exported := value.Export()

	// Validate JSON serializability
	if err := validateSerializable(exported); err != nil {
		return NewErrorResult(NewJsError(ErrorCodeSerializationError, err.Error()))
	}

	return NewSuccessResult(exported)
}

// setupSandbox configures the VM to prevent access to restricted APIs
func setupSandbox(vm *goja.Runtime) {
	// Disable require() - prevent module loading
	vm.Set("require", goja.Undefined())

	// Disable setTimeout/setInterval - prevent async operations
	vm.Set("setTimeout", goja.Undefined())
	vm.Set("setInterval", goja.Undefined())

	// Disable clearTimeout/clearInterval
	vm.Set("clearTimeout", goja.Undefined())
	vm.Set("clearInterval", goja.Undefined())

	// Note: Goja does not provide filesystem, network, or process access by default
	// so we don't need to explicitly block those
}

// errorEnvelope builds the {ok:false, error:{code, message}} value both
// call_tool() and call_tools() hand back to scripts.
func errorEnvelope(code ErrorCode, message string) map[string]interface{} {
	return map[string]interface{}{
		"ok": false,
		"error": map[string]interface{}{
			"code":    string(code),
			"message": message,
		},
	}
}

// successEnvelope builds the {ok:true, result} value both host functions hand
// back to scripts.
func successEnvelope(result interface{}) map[string]interface{} {
	return map[string]interface{}{
		"ok":     true,
		"result": result,
	}
}

// authInjectedArgPrefix mirrors security.StripInternalArgs' prefix (the
// `_auth_*` activity-metadata keys) without importing
// internal/security into the sandbox package.
const authInjectedArgPrefix = "_auth_"

// stripAuthInjectedArgs returns args without any `_auth_`-prefixed key. It
// never mutates the input; when nothing is stripped it returns a shallow
// copy so the report cannot alias the script's live map either.
func stripAuthInjectedArgs(args map[string]interface{}) map[string]interface{} {
	if args == nil {
		return nil
	}
	out := make(map[string]interface{}, len(args))
	for k, v := range args {
		if strings.HasPrefix(k, authInjectedArgPrefix) {
			continue
		}
		out[k] = v
	}
	return out
}

// resolveDispatchGates applies the server-scope and per-tool gates to one
// call. It returns an error envelope for a refused call, or the ToolGate the
// tier lookup captured (nil when none): the dispatch that follows consumes it
// so the call runs on the read that authorized it.
//
// Refused calls are reported to the activity observer with internal auth metadata removed.
func (ec *ExecutionContext) resolveDispatchGates(serverName, toolName string, args map[string]interface{}) (gateErr map[string]interface{}, gate ToolGate) {
	profileDenies := (ec.restrictToAllowed || len(ec.allowedServerMap) > 0) && !ec.allowedServerMap[serverName]
	if profileDenies {
		message := fmt.Sprintf("server not allowed: %s", serverName)

		reason := ""
		if ec.restrictToAllowed {
			reason = string(profile.BlockReasonServerScope)
		}
		ec.reportRefusal(serverName, toolName, message, args, reason)
		return errorEnvelope(ErrorCodeServerNotAllowed, message), nil
	}

	if ec.toolGateFunc == nil {
		return nil, nil
	}
	var requiredPerm string
	requiredPerm, gate = ec.toolGateFunc(serverName, toolName)
	// An identity the lookup could not resolve on a known server has no tier
	// to hold, so the call is refused here — before any permission check an
	// administrator would pass, and regardless of whether the caller carries
	// auth info at all. It answers with the permission envelope, never with an
	// upstream's own "tool not found".
	if requiredPerm == PermissionTierUnresolved {
		message := fmt.Sprintf("permission denied: tool '%s:%s' cannot be resolved against the current tool list of server '%s' (undiscovered or stale name), so no permission tier applies to it",
			serverName, toolName, serverName)

		ec.reportRefusal(serverName, toolName, message, args, "")
		return errorEnvelope(ErrorCodePermissionDenied, message), nil
	}
	if refusal, ok := gate.(interface{ ProfilePolicyRefusal() string }); ok {
		if message := refusal.ProfilePolicyRefusal(); message != "" {
			// The typed reason comes from a SECOND optional method so the
			// refusal never depends on it: a gate that only implements
			// ProfilePolicyRefusal still refuses.
			reason := ""
			if typed, ok := gate.(interface{ ProfilePolicyBlockReason() string }); ok {
				reason = typed.ProfilePolicyBlockReason()
			}
			ec.reportRefusal(serverName, toolName, message, args, reason)
			return errorEnvelope(ErrorCodeAccessDenied, message), nil
		}
	}

	return nil, gate
}

// makeCallToolFunction creates the call_tool() function bound to this execution context
func (ec *ExecutionContext) makeCallToolFunction(vm *goja.Runtime) func(goja.FunctionCall) goja.Value {
	return func(call goja.FunctionCall) goja.Value {
		// Extract arguments: call_tool(serverName, toolName, args)
		if len(call.Arguments) < 3 {
			return vm.ToValue(errorEnvelope(ErrorCodeInvalidArgs, "call_tool requires 3 arguments: serverName, toolName, args"))
		}

		serverName := call.Arguments[0].String()
		toolName := call.Arguments[1].String()

		// Parse args (must be an object)
		argsValue := call.Arguments[2].Export()
		args, ok := argsValue.(map[string]interface{})
		if !ok {
			return vm.ToValue(errorEnvelope(ErrorCodeInvalidArgs, "args must be an object"))
		}

		// Check max_tool_calls limit
		if ec.maxToolCalls > 0 && ec.toolCallCount >= ec.maxToolCalls {
			return vm.ToValue(errorEnvelope(ErrorCodeMaxToolCallsExceeded,
				fmt.Sprintf("exceeded max tool calls limit: %d", ec.maxToolCalls)))
		}

		gateErr, gate := ec.resolveDispatchGates(serverName, toolName, args)
		if gateErr != nil {
			return vm.ToValue(gateErr)
		}

		// Call the upstream tool under the execution's timeout context so a
		// call still in flight when the script is cut off is cancelled with
		// it, and on the gate the checks above were decided from, so the
		// dispatch never takes a second read of its own.
		ec.toolCallCount++
		return vm.ToValue(dispatchToolEnvelope(ec.ctx, ec.toolCaller, serverName, toolName, args, gate))
	}
}

// Limits for call_tools() batches.
const (
	// batchMaxRequests caps a single batch so one script cannot fan out
	// without bound.
	batchMaxRequests = 100
	// batchMinParallel / batchMaxParallel bound the effective worker count,
	// whatever the config or the script asks for.
	batchMinParallel = 1
	batchMaxParallel = 32
	// batchDefaultParallel applies when neither the config nor the batch
	// specifies a concurrency.
	batchDefaultParallel = 8
)

// batchRequest is one validated element of a call_tools() batch.
type batchRequest struct {
	server string
	tool   string
	args   map[string]interface{}
	// gate is the ToolGate the pre-dispatch pass captured for this element
	// (nil when none); the worker dispatches on it.
	gate ToolGate
}

// makeCallToolsFunction creates the call_tools() function bound to this
// execution context. The closure runs on the script goroutine: it parses and
// gates every element there, dispatches the survivors through a bounded worker
// pool, and converts the assembled slots back into VM values only after the
// workers have joined — the VM is owned by this goroutine alone.
func (ec *ExecutionContext) makeCallToolsFunction(vm *goja.Runtime) func(goja.FunctionCall) goja.Value {
	return func(call goja.FunctionCall) goja.Value {
		requests, maxParallel, err := parseBatchCall(call)
		if err != nil {
			// A malformed batch is a single envelope, never a throw: scripts
			// handle call_tools failures the same way they handle call_tool
			// failures.
			return vm.ToValue(errorEnvelope(ErrorCodeInvalidArgs, err.Error()))
		}

		return vm.ToValue(ec.runBatch(requests, maxParallel))
	}
}

// parseBatchCall validates the call_tools(requests, options) arguments,
// returning the requests in input order and the per-batch max_parallel
// override (0 when absent). Any problem invalidates the WHOLE call — nothing
// is dispatched and no budget is consumed — and names the first offending
// element.
func parseBatchCall(call goja.FunctionCall) (requests []batchRequest, maxParallel int, err error) {
	if len(call.Arguments) < 1 {
		return nil, 0, fmt.Errorf("call_tools requires 1 argument: requests (an array of {server, tool, args})")
	}

	rawRequests, ok := call.Arguments[0].Export().([]interface{})
	if !ok {
		return nil, 0, fmt.Errorf("call_tools: requests must be an array of {server, tool, args}")
	}
	if len(rawRequests) > batchMaxRequests {
		return nil, 0, fmt.Errorf("call_tools: batch of %d exceeds the maximum of %d requests", len(rawRequests), batchMaxRequests)
	}

	maxParallel, err = parseBatchOptions(call)
	if err != nil {
		return nil, 0, err
	}

	requests = make([]batchRequest, 0, len(rawRequests))
	for i, raw := range rawRequests {
		// A sparse array hole exports as nil, so holes fail this check too.
		element, ok := raw.(map[string]interface{})
		if !ok {
			return nil, 0, fmt.Errorf("call_tools: element %d: must be an object with server and tool", i)
		}

		server, ok := element["server"].(string)
		if !ok || server == "" {
			return nil, 0, fmt.Errorf("call_tools: element %d: server must be a non-empty string", i)
		}
		tool, ok := element["tool"].(string)
		if !ok || tool == "" {
			return nil, 0, fmt.Errorf("call_tools: element %d: tool must be a non-empty string", i)
		}

		args := map[string]interface{}{}
		if rawArgs, present := element["args"]; present {
			args, ok = rawArgs.(map[string]interface{})
			if !ok {
				return nil, 0, fmt.Errorf("call_tools: element %d: args must be an object", i)
			}
		}

		requests = append(requests, batchRequest{server: server, tool: tool, args: args})
	}

	return requests, maxParallel, nil
}

// parseBatchOptions reads the optional second argument of call_tools().
// Returns 0 when no override was supplied. Unknown keys are ignored.
func parseBatchOptions(call goja.FunctionCall) (int, error) {
	if len(call.Arguments) < 2 {
		return 0, nil
	}

	// Passing undefined/null for a trailing optional argument means "no
	// options", the way JavaScript callers expect.
	raw := call.Arguments[1].Export()
	if raw == nil {
		return 0, nil
	}

	options, ok := raw.(map[string]interface{})
	if !ok {
		return 0, fmt.Errorf("call_tools: options must be an object")
	}

	value, present := options["max_parallel"]
	if !present {
		return 0, nil
	}

	maxParallel, ok := batchOptionInt(value)
	if !ok {
		return 0, fmt.Errorf("call_tools: options.max_parallel must be an integer")
	}
	if maxParallel < batchMinParallel || maxParallel > batchMaxParallel {
		return 0, fmt.Errorf("call_tools: options.max_parallel must be between %d and %d, got %d",
			batchMinParallel, batchMaxParallel, maxParallel)
	}
	return maxParallel, nil
}

// batchOptionInt accepts the numeric shapes goja exports: int64 for integral
// numbers, float64 otherwise. A fractional value is rejected rather than
// truncated, so 2.5 never silently becomes 2.
func batchOptionInt(value interface{}) (int, bool) {
	switch v := value.(type) {
	case int64:
		return int(v), true
	case int:
		return v, true
	case float64:
		if v != math.Trunc(v) {
			return 0, false
		}
		return int(v), true
	default:
		return 0, false
	}
}

// effectiveMaxParallel resolves the worker bound for one batch:
// per-batch override > ExecutionOptions.MaxParallel (the configured default) >
// built-in default, always clamped to the supported range.
func (ec *ExecutionContext) effectiveMaxParallel(override int) int {
	value := batchDefaultParallel
	switch {
	case override > 0:
		value = override
	case ec.maxParallel > 0:
		value = ec.maxParallel
	}

	if value < batchMinParallel {
		return batchMinParallel
	}
	if value > batchMaxParallel {
		return batchMaxParallel
	}
	return value
}

// runBatch enforces, dispatches and assembles one call_tools() batch. It runs
// on the script goroutine; only the dispatch itself is concurrent.
func (ec *ExecutionContext) runBatch(requests []batchRequest, maxParallelOverride int) []interface{} {
	slots := make([]interface{}, len(requests))
	dispatch := make([]int, 0, len(requests))

	// Pre-dispatch pass, in input order, with the same check order a lone
	// call_tool() uses: budget first, then the scope gates. The budget of
	// element k counts the calls already made plus the elements this batch
	// has accepted so far.
	for i, req := range requests {
		if ec.maxToolCalls > 0 && ec.toolCallCount+len(dispatch) >= ec.maxToolCalls {
			slots[i] = errorEnvelope(ErrorCodeMaxToolCallsExceeded,
				fmt.Sprintf("exceeded max tool calls limit: %d", ec.maxToolCalls))
			continue
		}

		gateErr, gate := ec.resolveDispatchGates(req.server, req.tool, req.args)
		if gateErr != nil {
			slots[i] = gateErr
			continue
		}

		requests[i].gate = gate
		dispatch = append(dispatch, i)
	}

	if len(dispatch) == 0 {
		return slots
	}

	ec.toolCallCount += len(dispatch)
	ec.dispatchBatch(requests, dispatch, slots, ec.effectiveMaxParallel(maxParallelOverride))
	return slots
}

// dispatchBatch runs the accepted elements through a bounded worker pool and
// returns once every worker has finished. Each worker writes only into the
// slot its own index owns, so no locking is needed and the WaitGroup
// publishes the writes to the script goroutine.
func (ec *ExecutionContext) dispatchBatch(requests []batchRequest, dispatch []int, slots []interface{}, workers int) {
	if workers > len(dispatch) {
		workers = len(dispatch)
	}

	// The queue is prefilled and closed before any worker starts: there is no
	// producer to outlive the pool and nothing to deadlock on.
	indices := make(chan int, len(dispatch))
	for _, i := range dispatch {
		indices <- i
	}
	close(indices)

	ctx := ec.ctx
	caller := ec.toolCaller

	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range indices {
				req := requests[i]
				slots[i] = dispatchToolEnvelope(ctx, caller, req.server, req.tool, req.args, req.gate)
			}
		}()
	}

	// Unconditional join: the script goroutine never touches the slots while a
	// worker is alive, cancellation included.
	wg.Wait()
}

// dispatchToolEnvelope performs one upstream call and returns the envelope the
// script sees. It touches no execution state, so it is safe to run on a batch
// worker goroutine.
func dispatchToolEnvelope(ctx context.Context, caller ToolCaller, serverName, toolName string, args map[string]interface{}, gate ToolGate) map[string]interface{} {
	result, err := dispatchTool(ctx, caller, serverName, toolName, args, gate)
	if err != nil {
		return errorEnvelope(ErrorCodeUpstreamError, err.Error())
	}

	// Expose the result under its wire (JSON) shape rather than the live Go
	// value: goja would otherwise surface Go field names and exported methods.
	plain, nerr := normalizeToolResult(result)
	if nerr != nil {
		return errorEnvelope(ErrorCodeSerializationError,
			"tool result is not JSON-serializable: "+nerr.Error())
	}
	return successEnvelope(plain)
}

// normalizeToolResult converts an upstream tool result into its JSON wire shape
// so scripts see the documented field names (content[0].text) instead of Go
// struct fields, and custom MarshalJSON semantics are preserved.
func normalizeToolResult(v interface{}) (interface{}, error) {
	if v == nil {
		return nil, nil
	}

	data, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}

	var plain interface{}
	if err := json.Unmarshal(data, &plain); err != nil {
		return nil, err
	}
	return plain, nil
}

// validateSerializable checks if a value can be JSON-serialized
func validateSerializable(value interface{}) error {
	// Attempt JSON marshaling
	_, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("result must be JSON-serializable: %w", err)
	}
	return nil
}

// CallRefusal describes a nested call the sandbox refused before dispatch.
type CallRefusal struct {
	Ctx                                        context.Context
	ServerName, ToolName, Message, BlockReason string
	Arguments                                  map[string]interface{}
}

// RefusalObserver receives every refused nested call, for activity logging.
type RefusalObserver interface{ ObserveRefusal(CallRefusal) }

func (ec *ExecutionContext) reportRefusal(serverName, toolName, message string, args map[string]interface{}, blockReason string) {
	if ec.refusalObserver != nil {
		ec.refusalObserver.ObserveRefusal(CallRefusal{Ctx: ec.ctx, ServerName: serverName, ToolName: toolName, Message: message, Arguments: stripAuthInjectedArgs(args), BlockReason: blockReason})
	}
}
