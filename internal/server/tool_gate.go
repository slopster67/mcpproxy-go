package server

import (
	"errors"

	"go.uber.org/zap"

	"github.com/slopster67/mcpproxy-go/internal/config"
	"github.com/slopster67/mcpproxy-go/internal/preflight"
	"github.com/slopster67/mcpproxy-go/internal/storage"
)

type toolGate struct {
	serverName string
	toolName   string

	// class is the shared classification — the single authority on callability.
	class preflight.ToolClass
	// serverConfig is the stored upstream record, nil when the server is not
	// configured OR its record could not be read — storageErr tells the two
	// apart, which matters for the paths that deliberately fail OPEN on an
	// unknown server.
	serverConfig *config.ServerConfig
	// approval is the spec 032 record, nil when none exists (implicit-approved).
	approval *storage.ToolApprovalRecord
	// lockStatus is the tool-level quarantine lock ("" | pending | changed) as
	// the DISPATCH paths compute it: it reflects only the quarantine gate, so a
	// tool that is both user-disabled and pending still reports its lock exactly
	// as before this consolidation. class, which follows the spec-098 precedence
	// (user block outranks the quarantine lock), remains the callability truth.
	lockStatus string
	// storageErr is a genuine read failure on either stored input (the upstream
	// record or the approval record) — never the "no such record" sentinels.
	// Dispatch fails CLOSED on it (isToolCallable always has), so it is folded
	// into callable().
	storageErr error
	identity   toolIdentity
	// incarnation is the server's committed incarnation,
	// read BEFORE serverConfig, so a remove and same-name re-add that lands
	// after the record read leaves the gate bound to the removed server: a
	// history write the dispatch makes from serverConfig is then refused
	// instead of recreating the removed server's identity and history under
	// the replacement's incarnation (). 0 when unbound.
	incarnation int64
}

// serverDisabled reports the server-level disabled gate as the classifier
// decides it from the persisted record.
func (g toolGate) serverDisabled() bool {
	return g.class == preflight.ToolClassServerDisabled
}

// callable reports whether dispatch may proceed.
func (g toolGate) callable() bool {
	return g.class.Callable() && g.storageErr == nil
}

// blockedMessage is the agent-actionable refusal text for a non-callable tool
// that is neither quarantined nor approval-locked.
func (g toolGate) blockedMessage() string {
	return blockedToolMessage()
}

// evaluateExactToolGate is evaluateToolGate for a pair that is already split
// into server and RAW tool name. It never re-normalizes: a raw name that
// begins with the server's own prefix ("a:ns:erase" on "a") would otherwise
// be approval- and config-gated as the suffix tool "ns:erase" while dispatch
// targets "a:ns:erase".
func (p *MCPProxyServer) evaluateExactToolGate(serverName, toolName string) toolGate {
	gate := toolGate{serverName: serverName, toolName: toolName}

	if serverName == "" || toolName == "" {
		gate.class = preflight.ToolClassServerNotConfigured
		return gate
	}

	// Before the record read: see incarnation.
	gate.incarnation = p.toolCallIncarnation(serverName)
	serverConfig, err := p.storage.GetUpstreamServer(serverName)
	if err != nil || serverConfig == nil {
		// "No such upstream" is a verdict every dispatch path has always
		// treated as not-callable. A genuine read failure is NOT that verdict:
		// it is recorded so the paths that fail open on an unknown server (the
		// sandbox bridge) can refuse instead of mistaking an unreadable record
		// for an absent one.
		if err != nil && !errors.Is(err, storage.ErrUpstreamNotFound) {
			gate.storageErr = err
		}
		gate.class = preflight.ToolClassServerNotConfigured
		// No record: the identity stands on the StateView's own flags, as
		// it does for a StateView-only fixture.
		gate.identity = p.resolveExactToolIdentityWith(nil, serverName, toolName)
		return gate
	}
	gate.serverConfig = serverConfig

	quarantineGate := !serverConfig.IsQuarantineSkipped()

	// ONE snapshot read serves both the no-record rule below and the
	// classifier's Discovered input, so the two cannot disagree about whether
	// the tool is in the snapshot — and ONE persisted read (serverConfig,
	// above) serves both the classifier's server policy and the identity's
	// hydration, so the identity refusal and the quarantined / disabled
	// verdicts cannot disagree about the server's state either.
	identity := p.resolveExactToolIdentityWith(serverConfig, serverName, toolName)
	gate.identity = identity
	// The no-record rule holds only for a tool the LIVE snapshot lists. A
	// name on a server whose snapshot is empty because of its own state
	// (disconnected, connecting, quarantined, disabled) keeps the server-level
	// verdict that always owned it: an empty
	// snapshot cannot show the name absent, and synthesizing a pending record
	// there would answer "is in the server's tool list" for a list that is
	// empty — and pre-empt the not-connected verdict for a record-less name
	// while the recorded sibling on the same dropped server keeps it. No
	// consumer of this gate can carry such a name to an upstream: every
	// dispatch path refuses a not-connected client before any upstream call
	// (handleCallToolVariant / handleCallTool's IsConnected pre-check; the
	// sandbox bridge dispatches through managed.Client.CallTool, which refuses
	// when not connected and never reconnects), and once the server reconnects
	// the fresh discovery stamp makes a never-listed name Unresolved (identity
	// gate / liveIdentityRefusal / lookupToolPermission).
	// A server the StateView does not hold at all keeps the
	// implicit default (unit fixtures without a runtime; server-existence
	// handling owns the rest).
	recordRequired := identity.Found

	approval, approvalErr := p.lookupToolApproval(serverName, toolName)
	switch {
	case approvalErr == nil:
		gate.approval = approval
	case errors.Is(approvalErr, storage.ErrToolApprovalNotFound):
		// No record: while the tool-level
		// quarantine gate is active for the server, a tool the discovery
		// snapshot contains (recordRequired) is PENDING under its own name,
		// never ready; the implicit-approved default survives only for a
		// name the snapshot does not list (identity resolution's concern for
		// a hydrated snapshot, the server-level verdicts' for an empty one)
		// or while the gate is off for the server.
		gate.approval = implicitPendingApproval(serverName, toolName, recordRequired, identity.Description, quarantineGate)
		if gate.approval != nil {
			p.logImplicitPending("tool_gate", serverName, toolName)
		}
	default:
		// A real encrypted failure must not silently re-enable a tool the user
		// disabled (isToolCallable's long-standing fail-closed rule).
		gate.storageErr = approvalErr
	}

	if quarantineGate && gate.approval != nil {
		switch gate.approval.Status {
		case storage.ToolApprovalStatusPending, storage.ToolApprovalStatusChanged:
			gate.lockStatus = gate.approval.Status
		}
	}

	gate.class = preflight.ClassifyTool(preflight.ClassifyInputs{
		Server: preflight.ServerPolicy{
			Found:                  true,
			Enabled:                serverConfig.Enabled,
			AutoApproveToolChanges: serverConfig.IsQuarantineSkipped(),
		},

		Approval: approvalStateFor(gate.approval),
		// Belt and braces with implicitPendingApproval above: the classifier
		// applies the same no-record rule itself, so the two cannot drift.
		Discovered: recordRequired,
	})
	return gate
}

// approvalStateFor narrows a storage record to the classifier's read-only view.
func approvalStateFor(record *storage.ToolApprovalRecord) *preflight.ApprovalState {
	if record == nil {
		return nil
	}
	return &preflight.ApprovalState{
		Status:            record.Status,
		Disabled:          record.Disabled,
		CurrentHash:       record.CurrentHash,
		HashSchemaVersion: record.HashSchemaVersion,
	}
}

// lookupToolApproval reads the approval record for one (server, RAW
// tool) pair, already split by normalizeServerTool or held split by the
// caller. It is the storage half of the reader; the snapshot
// half — "no record for a tool the discovery snapshot contains is pending
// while the quarantine gate is active" — needs the live StateView and the
// gate flag, so it lives with the callers that hold both
// (evaluateExactToolGate, directCallabilityEvaluator.evaluate) and, for the
// preflight evaluator, in preflight.ClassifyTool's Discovered input.
//
// The absence of a usable record is reported as
// storage.ErrToolApprovalNotFound, the same contract GetToolApproval keeps.
func (p *MCPProxyServer) lookupToolApproval(serverName, toolName string) (*storage.ToolApprovalRecord, error) {
	return readToolApprovalRecord(p.storage, serverName, toolName)
}

// readToolApprovalRecord reads the exact upstream tool identity.
func readToolApprovalRecord(st *storage.Manager, serverName, toolName string) (*storage.ToolApprovalRecord, error) {
	return st.GetToolApproval(serverName, toolName)
}

// isImplicitPendingApproval reports whether a record is the synthesized
// no-record placeholder rather than a stored pending record.
func isImplicitPendingApproval(record *storage.ToolApprovalRecord) bool {
	return record != nil && record.TransientImplicit
}

func implicitPendingApproval(serverName, toolName string, discovered bool, description string, quarantineGate bool) *storage.ToolApprovalRecord {
	if !quarantineGate || !discovered {
		return nil
	}
	return &storage.ToolApprovalRecord{
		ServerName:         serverName,
		ToolName:           toolName,
		Status:             storage.ToolApprovalStatusPending,
		TransientImplicit:  true,
		CurrentDescription: description,
	}
}

// logImplicitPending records, at Warn, that a dispatch gate refused a
// snapshot tool because no approval record exists for it — the discovery
// pass that should have filed one either has not run yet or failed open.
func (p *MCPProxyServer) logImplicitPending(site, serverName, toolName string) {
	if p.logger == nil {
		return
	}
	p.logger.Warn("Snapshot tool has no approval record; treating as pending under the active quarantine gate",
		zap.String("site", site),
		zap.String("server_name", serverName),
		zap.String("tool_name", toolName))
}
