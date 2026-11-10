package server

import (
	"context"
	"errors"

	"github.com/mark3labs/mcp-go/v2/mcp"

	"github.com/slopster67/mcpproxy-go/internal/config"
	"github.com/slopster67/mcpproxy-go/internal/preflight"
	"github.com/slopster67/mcpproxy-go/internal/storage"
)

type directCallabilityDecision struct {
	callable       bool
	serverName     string
	toolName       string
	serverConfig   *config.ServerConfig
	approval       *storage.ToolApprovalRecord
	approvalStatus string
	storageErr     error
}

type directCallabilityEvaluator struct {
	proxy         *MCPProxyServer
	serverConfigs map[string]*config.ServerConfig
	serverErrors  map[string]error
	approvals     map[string]*storage.ToolApprovalRecord
	approvalErrs  map[string]error
}

func newDirectCallabilityEvaluator(proxy *MCPProxyServer) *directCallabilityEvaluator {
	return &directCallabilityEvaluator{
		proxy:         proxy,
		serverConfigs: make(map[string]*config.ServerConfig),
		serverErrors:  make(map[string]error),
		approvals:     make(map[string]*storage.ToolApprovalRecord),
		approvalErrs:  make(map[string]error),
	}
}

// filterDirectToolsForAgentCallability hides direct-mode tools that the calling
// client cannot actually invoke because they are disabled, quarantined, pending
// approval, or changed since approval.
func (p *MCPProxyServer) filterDirectToolsForAgentCallability(ctx context.Context, tools []mcp.Tool) []mcp.Tool {
	if len(tools) == 0 {
		return tools
	}

	evaluator := newDirectCallabilityEvaluator(p)
	filtered := make([]mcp.Tool, 0, len(tools))
	for _, tool := range tools {
		var serverName, toolName string

		if stamp, stamped := readDirectToolStamp(tool); stamped {
			//: the identity STAMPED on this exact tool object,
			// never re-derived from a fresh catalog lookup (see
			// directToolStamp's doc comment).
			if stamp.rawName == "" {
				continue
			}
			serverName, toolName = stamp.owner, stamp.rawName
		} else {
			entry, decision := p.resolveDirectTool(tool.Name)

			switch decision {
			case directResolveBuiltin:
				// Built-ins are this proxy's own tools; there is no upstream
				// approval record to evaluate.
				filtered = append(filtered, tool)
				continue
			case directResolveDenied:
				continue
			case directResolveNoCatalog:
				serverName, toolName, _ = ParseDirectToolName(tool.Name)
			case directResolveFound:
				serverName, toolName = entry.ServerName, entry.ToolName
			}
		}

		// D13/gap G6: tier-first precedence. At call time, a
		// tool the caller is over-tier for must reach the handler (which
		// checks tier BEFORE callability) even when it is ALSO
		// disabled/pending/changed — so "over-tier + locked"
		// answers insufficient-permission, not this filter's -32602. The
		// scope+tier filter ahead of this one in the chain already let such a
		// tool through (directCallTimeTierExceeded, mcp_direct_scope.go); this
		// filter must not re-exclude it for callability. list time and an
		// in-scope, within-tier caller are unaffected: the condition is false
		// and evaluate().callable applies exactly as before. authCtx is
		// guaranteed a scoped client token here — the early return above sent
		// every other caller home before this loop started.

		if evaluator.evaluate(serverName, toolName).callable {
			filtered = append(filtered, tool)
		}
	}

	return filtered
}

// directEntryCallable is the callability half of the direct listing gate,
// for callers that already hold a resolved catalog entry.
//
// This MUST stay in parity with filterDirectToolsForAgentCallability above —
// the same invariant: a listed tool is never undescribable.
func (p *MCPProxyServer) directEntryCallable(entry *directCatalogEntry) bool {
	if entry == nil {
		return false
	}
	return newDirectCallabilityEvaluator(p).evaluate(entry.ServerName, entry.ToolName).callable
}

func (p *MCPProxyServer) directToolCallabilityBlock(ctx context.Context, serverName, toolName string, args map[string]interface{}) *mcp.CallToolResult {
	decision := newDirectCallabilityEvaluator(p).evaluate(serverName, toolName)
	if decision.callable {
		return nil
	}

	return p.directToolCallabilityResult(ctx, decision, args)
}

// directRefusalKind keeps telemetry and responses on the same precedence:
// pending or changed approval first, then a generic availability block.
type directRefusalKind int

const (
	directRefusalNone directRefusalKind = iota
	directRefusalPending
	directRefusalChanged
	directRefusalGeneric
)

func classifyDirectRefusal(decision directCallabilityDecision) directRefusalKind {
	switch {

	case decision.approvalStatus == storage.ToolApprovalStatusPending:
		return directRefusalPending
	case decision.approvalStatus == storage.ToolApprovalStatusChanged:
		return directRefusalChanged
	default:
		return directRefusalGeneric
	}
}

// evaluate classifies one direct-mode tool through the SHARED gate primitive
// so direct mode, the call_tool_* variants, code_execution and
// stored scripts cannot disagree about what is callable. The memoized storage
// reads below still exist because this evaluator runs over a whole tool list;
// only the classification moved.
func (e *directCallabilityEvaluator) evaluate(serverName, toolName string) directCallabilityDecision {
	decision := directCallabilityDecision{
		serverName: serverName,
		toolName:   toolName,
	}

	if e.proxy.storage == nil {
		return decision
	}

	serverConfig, serverErr := e.getServerConfig(serverName)
	if serverErr != nil || serverConfig == nil {
		decision.storageErr = serverErr
		return decision
	}
	decision.serverConfig = serverConfig
	// Disabled servers report their own gate before tool approval messages.
	serverGatesPassed := serverConfig.Enabled

	quarantineGate := !serverConfig.IsQuarantineSkipped()

	// The direct catalog IS the direct surface's discovery snapshot: every
	// caller of this evaluator holds a pair resolved through a catalog or
	// registry entry (the registered handler closes over its own entry, the
	// listing filter and describe resolve theirs), and that catalog is built
	// from a live tools/list of the server. So the tool is discovered by
	// construction here, and the classifier's Discovered input is true
	// regardless of what the StateView holds at this instant. The StateView is
	// consulted only for the description the pending body carries. Reading
	// Discovered from the StateView instead re-opened on this
	// surface: the catalog is rebuilt on servers.changed, which on (re)connect
	// races the runtime's own discovery + checkToolApprovalsHeld pass, so a newly
	// added tool sat in the catalog with no record and no StateView entry —
	// and "not discovered, no record" classified as ready.
	const discovered = true
	identity := e.proxy.resolveExactToolIdentity(serverName, toolName)

	approval, approvalErr := e.getToolApproval(serverName, toolName)
	switch {
	case approvalErr == nil:
	case errors.Is(approvalErr, storage.ErrToolApprovalNotFound):
		approval = implicitPendingApproval(serverName, toolName, discovered, identity.Description, quarantineGate)
		if approval != nil {
			e.proxy.logImplicitPending("direct", serverName, toolName)
		}
	default:
		decision.storageErr = approvalErr
		return decision
	}
	decision.approval = approval

	if serverGatesPassed && quarantineGate && approval != nil {
		switch approval.Status {
		case storage.ToolApprovalStatusPending, storage.ToolApprovalStatusChanged:
			decision.approvalStatus = approval.Status
		}
	}

	class := preflight.ClassifyTool(preflight.ClassifyInputs{
		Server: preflight.ServerPolicy{
			Found:                  true,
			Enabled:                serverConfig.Enabled,
			AutoApproveToolChanges: serverConfig.IsQuarantineSkipped(),
		},

		Approval:   approvalStateFor(approval),
		Discovered: discovered,
	})
	decision.callable = class.Callable()
	return decision
}

func (e *directCallabilityEvaluator) getServerConfig(serverName string) (*config.ServerConfig, error) {
	if serverConfig, ok := e.serverConfigs[serverName]; ok {
		return serverConfig, e.serverErrors[serverName]
	}

	serverConfig, err := e.proxy.storage.GetUpstreamServer(serverName)
	e.serverConfigs[serverName] = serverConfig
	e.serverErrors[serverName] = err
	return serverConfig, err
}

func (e *directCallabilityEvaluator) getToolApproval(serverName, toolName string) (*storage.ToolApprovalRecord, error) {
	key := serverName + "\x00" + toolName
	if approval, ok := e.approvals[key]; ok {
		return approval, e.approvalErrs[key]
	}
	if err, ok := e.approvalErrs[key]; ok {
		return nil, err
	}

	approval, err := e.proxy.lookupToolApproval(serverName, toolName)
	if approval != nil {
		e.approvals[key] = approval
	}
	e.approvalErrs[key] = err
	return approval, err
}

func (p *MCPProxyServer) directToolCallabilityResult(ctx context.Context, decision directCallabilityDecision, args map[string]interface{}) *mcp.CallToolResult {
	switch classifyDirectRefusal(decision) {
	case directRefusalPending:
		return toolPendingApprovalResult(decision.serverName, decision.toolName, decision.approval)
	case directRefusalChanged:
		return toolChangedApprovalResult(decision.serverName, decision.toolName, decision.approval)
	default:
		return mcp.NewToolResultError(blockedToolMessage())
	}
}
