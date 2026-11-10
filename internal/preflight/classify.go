package preflight

// Tool-approval status values, mirrored from internal/storage
// (ToolApprovalStatus*). They are duplicated rather than imported so this
// package stays a leaf with no storage dependency.
const (
	ApprovalStatusPending = "pending"
	ApprovalStatusChanged = "changed"
)

// ServerPolicy is the config-derived (non-runtime) view of one upstream server.
// Found=false means no server with that name is configured at all.
type ServerPolicy struct {
	Found   bool
	Enabled bool

	// AutoApproveToolChanges mirrors ServerConfig.IsAutoApproveToolChanges()
	// (equivalently IsQuarantineSkipped() / trust_mode auto): the server opted
	// out of tool-level quarantine, so pending/changed approval records do NOT
	// gate its tools.
	AutoApproveToolChanges bool
}

// ApprovalState is the narrow read of a ToolApprovalRecord. A nil
// *ApprovalState means "no record" — NOT an error. It is the implicit-approved
// default EXCEPT for a discovered tool under an active quarantine gate, which
// ClassifyTool reports as pending (see ClassifyInputs.Discovered).
type ApprovalState struct {
	Status            string
	Disabled          bool
	CurrentHash       string
	HashSchemaVersion uint64
}

// ClassifyInputs are the locally-read facts the shared classifier needs. Every
// field is a plain value: the classifier performs no I/O, so it is callable
// from dispatch (latency-sensitive) and from the evaluator alike.
type ClassifyInputs struct {
	Server ServerPolicy

	// Approval is the tool's approval record, or nil when none exists.
	Approval *ApprovalState
	// Discovered reports that the tool is in the server's discovery snapshot.
	Discovered bool
}

// ToolClass is the shared tool classification consumed by the preflight
// evaluator and the dispatch gates. Exactly one class per tool, by fixed
// first-match precedence.
type ToolClass string

const (
	ToolClassReady               ToolClass = "ready"
	ToolClassServerNotConfigured ToolClass = "server_not_configured"
	ToolClassServerDisabled      ToolClass = "server_disabled"
	ToolClassBlockedByUser       ToolClass = "blocked_by_user"
	ToolClassChanged             ToolClass = "changed"
	ToolClassPendingApproval     ToolClass = "pending_approval"
)

// ClassifyTool classifies one tool from its server policy and approval record.
func ClassifyTool(in ClassifyInputs) ToolClass {
	if !in.Server.Found {
		return ToolClassServerNotConfigured
	}

	if !in.Server.Enabled {
		return ToolClassServerDisabled
	}
	if in.Approval == nil {
		if in.Discovered && quarantineGateApplies(in) {
			return ToolClassPendingApproval
		}
		return ToolClassReady
	}
	if in.Approval.Disabled {
		return ToolClassBlockedByUser
	}
	if !quarantineGateApplies(in) {
		return ToolClassReady
	}
	switch in.Approval.Status {
	case ApprovalStatusChanged:
		return ToolClassChanged
	case ApprovalStatusPending:
		return ToolClassPendingApproval
	default:
		return ToolClassReady
	}
}

// quarantineGateApplies reports whether pending/changed approval records gate
// this server's tools — the exact condition the dispatch path and
// describeGateReason use.
func quarantineGateApplies(in ClassifyInputs) bool {
	return !in.Server.AutoApproveToolChanges
}

// Reason maps a class to its preflight reason code. ToolClassReady maps to the
// empty string (no failure).
func (c ToolClass) Reason() Reason {
	switch c {
	case ToolClassServerNotConfigured:
		return ReasonServerNotConfigured

	case ToolClassServerDisabled:
		return ReasonServerDisabled
	case ToolClassBlockedByUser:
		return ReasonToolBlockedByUser
	case ToolClassChanged:
		return ReasonToolChanged
	case ToolClassPendingApproval:
		return ReasonToolPendingApproval
	case ToolClassReady:
		return ""
	default:
		return ""
	}
}

// Callable reports whether the class permits dispatch. It is the one-line form
// the dispatch paths consume.
func (c ToolClass) Callable() bool {
	return c == ToolClassReady
}
