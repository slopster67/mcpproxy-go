package profile

import "errors"

// ErrUnknownProfile is returned when an access subject names a profile that
// does not exist.
var ErrUnknownProfile = errors.New("profile not found")

// ErrUnknownToken is returned when an access subject names a token that does
// not exist.
var ErrUnknownToken = errors.New("token not found")

// ErrClientCredentialToken is returned when an access subject names a client
// credential as a token: it is explained as a client instead.
var ErrClientCredentialToken = errors.New("use client=<id> for a client credential")

// ErrExplainBuiltinTool is returned by the explainer for a tool name that is
// not an upstream server:tool: built-in management tools do not walk the
// upstream chain and are not explained.
var ErrExplainBuiltinTool = errors.New("access/explain covers upstream tools (server:tool) only")

// ErrUnknownClient is returned when an access subject names a client that is
// neither a known client nor holds a credential record.
var ErrUnknownClient = errors.New("client not found")

// The access chain vocabulary shared by the view-as rows of GET /tools and
// the access explainer. The REST rows, the CLI columns, the MCP
// effective_tools operation and the generated Web/Swift types all spell these
// values the same way.

// AccessReason is why an effective-tool row is not visible or not callable.
// The zero value ("") means callable. The profile decision reasons are
// reported by the step that carries them; every other value is the failing
// step's own name.
type AccessReason string

const (
	AccessReasonNone AccessReason = ""
	// The profile decision reasons (Reason): server_not_in_profile is
	// reported by server_in_scope, not_selected by the tool selection.
	AccessReasonServerNotInProfile AccessReason = "server_not_in_profile"
	AccessReasonNotSelected        AccessReason = "not_selected"
	// The step names.
	AccessReasonProfile       AccessReason = "profile"
	AccessReasonServerInScope AccessReason = "server_in_scope"

	AccessReasonServerState  AccessReason = "server_state"
	AccessReasonToolApproval AccessReason = "tool_approval"
)

// AccessSubjectKind selects whose access is evaluated.
type AccessSubjectKind string

const (
	// AccessSubjectClient is a client as its connection resolves today: its
	// saved profile and host binding.
	AccessSubjectClient AccessSubjectKind = "client"
	// AccessSubjectProfile is a profile's own reach, independent of any
	// client or token.
	AccessSubjectProfile AccessSubjectKind = "profile"
)

// AccessSubject names the subject of an access evaluation.
type AccessSubject struct {
	Kind AccessSubjectKind
	// ClientID names the client for AccessSubjectClient.
	ClientID string
	// Profile names the profile for AccessSubjectProfile. For
	// AccessSubjectClient it is an optional OVERRIDE: the client's credential
	// evaluated under this profile ("what would Cursor get on that profile",
	// GET /profiles/{name}/effective-tools?client=).
	Profile string
}

// AccessIssue explains one concrete reason a tool cannot be used.
type AccessIssue struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// AccessVerdict is the result for one tool. Only problems produce issues.
type AccessVerdict struct {
	Visible     bool
	Callable    bool
	Reason      AccessReason
	ProfileTier Tier
	Issues      []AccessIssue
}
