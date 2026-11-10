package preflight

import (
	"fmt"

	"github.com/slopster67/mcpproxy-go/internal/health"
)

// NoApprovalRecordDetail / NoApprovalRecordRemediation are the implicit-pending
// bodies shared by dispatch (toolPendingApprovalResult, reason
// no_approval_record), describe_tool and check mode, so the three readers
// cannot drift: a tool the discovery snapshot contains with NO stored approval
// record is pending under its own name while the
// quarantine gate applies, but nothing is listed in the review UI to approve
// yet — the taxonomy's "approve" remediation is a dead end (ApproveTools skips
// a missing record and triggers no rediscovery). The server's next discovery
// pass files the real record, so the caller is pointed at re-discovery.
func NoApprovalRecordDetail(id string) string {
	return fmt.Sprintf("Tool %q is in the server's tool list but has no approval record yet, so it is withheld while tool-level quarantine is active for the server.", id)
}

// NoApprovalRecordRemediation is the re-discovery instruction for a
// record-less tool; see NoApprovalRecordDetail.
func NoApprovalRecordRemediation(serverName string) string {
	return fmt.Sprintf("Restart server %q to discover its tools, then review them on the Tools page and retry.", serverName)
}

// Status is the per-tool outcome. `ready` is a success status, not a failure
// reason — a ready result carries no reason, retryable or action field.
type Status = string

const (
	StatusReady       Status = "ready"
	StatusUnavailable Status = "unavailable"
)

type Reason = string

const (
	ReasonServerInitializing  Reason = "server_initializing"
	ReasonServerUnhealthy     Reason = "server_unhealthy"
	ReasonServerDisabled      Reason = "server_disabled"
	ReasonToolPendingApproval Reason = "tool_pending_approval"
	ReasonToolChanged         Reason = "tool_changed"
	ReasonToolBlockedByUser   Reason = "tool_blocked_by_user"
	ReasonOAuthRequired       Reason = "oauth_required"
	ReasonHashMismatch        Reason = "hash_mismatch"
	ReasonServerNotInScope    Reason = "server_not_in_scope"
	ReasonMissingAnnotation   Reason = "missing_annotation"
	ReasonPolicyFiltered      Reason = "policy_filtered"
	ReasonNotFound            Reason = "not_found"
	ReasonServerNotConfigured Reason = "server_not_configured"
	// ReasonToolBlockedByProfile is the effective profile tool policy refusing
	// a tool that exists. It is the preflight projection of dispatch's
	// "blocked by profile: ..." refusal, so a tool dispatch refuses under a
	// profile can never preflight as ready. Emitted at the OPERATOR tier only;
	// the client token tier answers the scope-silent not_found instead, because
	// discovery hides a profile-excluded tool and the check must not confirm
	// what discovery withholds.
	ReasonToolBlockedByProfile Reason = "tool_blocked_by_profile"
	// ReasonTokenPermissionDenied is the caller's client token permission tier
	// (read / write / destructive) refusing a tool that exists: the preflight
	// projection of dispatch's "Permission denied: token does not have
	// '<tier>' permission required for tool ..." refusal, decided from the
	// same effective annotations. Unlike a
	// profile exclusion it is NOT scope-silenced at the client token tier:
	// discovery (retrieve_tools, routed describe_tool) still lists a tool above
	// the token's tier, and the access explainer reports it visible but
	// blocked, so the check names the refusal rather than calling it absent.
	// Operator callers hold no token permissions and never see it.
	ReasonTokenPermissionDenied Reason = "token_permission_denied"
)

// Class groups reasons by what an operator must do about them. It drives the
// set verdict and therefore the CLI exit code.
type Class = string

const (
	// ClassRetryable: waiting may fix it (the proxy is mid-transition).
	ClassRetryable Class = "retryable"
	// ClassFixState: an operator action on live state fixes it (approve,
	// enable, log in, re-lock a pin).
	ClassFixState Class = "fix_state"
	// ClassPermanentConfig: nothing changes until configuration or the request
	// itself changes.
	ClassPermanentConfig Class = "permanent"
)

// Verdict is the set-level aggregate: the worst class present.
type Verdict = string

const (
	VerdictReady             Verdict = "ready"
	VerdictDegradedRetryable Verdict = "degraded_retryable"
	VerdictBlocked           Verdict = "blocked"
	VerdictUnknownIDs        Verdict = "unknown_ids"
)

// CLI exit codes. Worst class present wins: 12 > 11 > 10 > 0.
const (
	ExitDegradedRetryable = 10
	ExitBlocked           = 11
	ExitUnknownIDs        = 12
)

// reasonSpec is one row of the reason taxonomy. Keeping the columns in one
// literal keeps the table auditable by eye.
type reasonSpec struct {
	class     Class
	retryable bool
	// action uses the existing health-action vocabulary. "No action" is the
	// empty string, which serializers omit — matching the health constants
	// (health.ActionNone), not a literal "none".
	action      string
	verdict     Verdict
	exitCode    int
	remediation string
}

// reasonTable is the single source of truth for the reason taxonomy.
var reasonTable = map[Reason]reasonSpec{
	ReasonServerInitializing: {
		class: ClassRetryable, retryable: true, action: health.ActionNone,
		verdict: VerdictDegradedRetryable, exitCode: ExitDegradedRetryable,
		remediation: "Server is still starting up; retry shortly.",
	},
	ReasonServerUnhealthy: {
		class: ClassRetryable, retryable: true, action: health.ActionViewLogs,
		verdict: VerdictDegradedRetryable, exitCode: ExitDegradedRetryable,
		remediation: "Check the server's Logs tab and retry once it recovers.",
	},
	ReasonServerDisabled: {
		class: ClassFixState, retryable: false, action: health.ActionEnable,
		verdict: VerdictBlocked, exitCode: ExitBlocked,
		remediation: "Enable the server in its Configuration tab.",
	},
	ReasonToolPendingApproval: {
		class: ClassFixState, retryable: false, action: health.ActionApprove,
		verdict: VerdictBlocked, exitCode: ExitBlocked,
		remediation: "Review and approve the tool (Web UI, Server detail -> Tools).",
	},
	ReasonToolChanged: {
		class: ClassFixState, retryable: false, action: health.ActionApprove,
		verdict: VerdictBlocked, exitCode: ExitBlocked,
		remediation: "The tool definition changed after approval; review the diff and re-approve it (Web UI, Server detail -> Tools).",
	},
	ReasonToolBlockedByUser: {
		class: ClassFixState, retryable: false, action: health.ActionEnable,
		verdict: VerdictBlocked, exitCode: ExitBlocked,
		remediation: "Re-enable the tool (Web UI, Server detail -> Tools).",
	},
	ReasonOAuthRequired: {
		class: ClassFixState, retryable: false, action: health.ActionLogin,
		verdict: VerdictBlocked, exitCode: ExitBlocked,
		remediation: "Sign in from the server page; waiting will not help.",
	},
	ReasonHashMismatch: {
		class: ClassFixState, retryable: false, action: health.ActionConfigure,
		verdict: VerdictBlocked, exitCode: ExitBlocked,
		remediation: "Review the tool's current definition and re-pin it with the current hash.",
	},
	ReasonServerNotInScope: {
		class: ClassPermanentConfig, retryable: false, action: health.ActionConfigure,
		verdict: VerdictBlocked, exitCode: ExitBlocked,
		remediation: "Add the server to the profile, or run the preflight without the profile.",
	},
	ReasonMissingAnnotation: {
		class: ClassPermanentConfig, retryable: false, action: health.ActionConfigure,
		verdict: VerdictBlocked, exitCode: ExitBlocked,
		remediation: "The upstream tool lacks the annotation this filter requires; publish annotations upstream or drop the filter.",
	},
	ReasonPolicyFiltered: {
		class: ClassPermanentConfig, retryable: false, action: health.ActionNone,
		verdict: VerdictBlocked, exitCode: ExitBlocked,
		remediation: "The tool is explicitly marked unsafe for this filter; drop the filter to use it.",
	},
	ReasonToolBlockedByProfile: {
		class: ClassPermanentConfig, retryable: false, action: health.ActionConfigure,
		verdict: VerdictBlocked, exitCode: ExitBlocked,
		remediation: "This tool is not selected in the profile; check under a profile that allows it, or have an operator adjust the profile (mcpproxy access explain --profile <name> --tool <server:tool>).",
	},
	ReasonTokenPermissionDenied: {
		class: ClassPermanentConfig, retryable: false, action: health.ActionConfigure,
		verdict: VerdictBlocked, exitCode: ExitBlocked,
		remediation: "The credential's permissions (read/write/destructive) do not include this tool's tier; use a token minted with that permission (mcpproxy token create --permissions ...), or check a tool within the token's tier.",
	},
	ReasonNotFound: {
		class: ClassPermanentConfig, retryable: false, action: health.ActionConfigure,
		verdict: VerdictUnknownIDs, exitCode: ExitUnknownIDs,
		remediation: "Check the tool ID (format <server>:<tool>) on the Tools page.",
	},
	ReasonServerNotConfigured: {
		class: ClassPermanentConfig, retryable: false, action: health.ActionConfigure,
		verdict: VerdictUnknownIDs, exitCode: ExitUnknownIDs,
		remediation: "Add the server on the Servers page, or fix the ID.",
	},
}

// Retryable reports whether waiting can plausibly clear the reason. Unknown
// codes are non-retryable.
func Retryable(code Reason) bool {
	spec, ok := reasonTable[code]
	return ok && spec.retryable
}

// DefaultAction returns the health-vocabulary action for a reason, or "" when
// the reason has no action (the field is then omitted from the response).
// server_unhealthy's action is a best-effort default that the evaluator may
// override from upstream diagnostics.
func DefaultAction(code Reason) string {
	return reasonTable[code].action
}

// DefaultRemediation returns the one actionable instruction for a reason.
func DefaultRemediation(code Reason) string {
	return reasonTable[code].remediation
}

// ReasonVerdict maps one reason to the set verdict it forces on its own.
func ReasonVerdict(code Reason) Verdict {
	if spec, ok := reasonTable[code]; ok {
		return spec.verdict
	}
	// An unknown code must never silently downgrade the verdict: treat it as
	// blocked (non-retryable, operator action needed).
	return VerdictBlocked
}

// verdictRank orders verdicts from best to worst; the aggregate is the max.
var verdictRank = map[Verdict]int{
	VerdictReady:             0,
	VerdictDegradedRetryable: 1,
	VerdictBlocked:           2,
	VerdictUnknownIDs:        3,
}

// VerdictForResults is VerdictForReasons over evaluator results, ignoring ready
// entries.
func VerdictForResults(results []Result) Verdict {
	worst := VerdictReady
	for i := range results {
		if results[i].Status == StatusReady {
			continue
		}
		if v := ReasonVerdict(results[i].Reason); verdictRank[v] > verdictRank[worst] {
			worst = v
		}
	}
	return worst
}
