package profile

// Enumerations shared by every profile surface — REST, CLI, MCP, Web UI,
// macOS. Every value below is the exact wire spelling used in JSON, CLI
// flags/output and the generated frontend/src/types/contracts.ts
// (cmd/generate-types) and Swift models, so no surface can drift from another.

// Reason is why the profile decision excluded a tool. The zero value
// ("") means admitted — no exclusion reason.
type Reason string

const (
	ReasonNone               Reason = ""
	ReasonServerNotInProfile Reason = "server_not_in_profile"
	ReasonNotSelected        Reason = "not_selected"
)

// Source is where an effective profile resolution came from.
type Source string

const (
	SourcePin     Source = "pin"
	SourceBinding Source = "binding"
	SourceSession Source = "session"
	SourceNone    Source = "none"
)

// BlockReason is the activity-record cause of a profile-driven call refusal.
type BlockReason string

const (
	BlockReasonRule BlockReason = "profile_rule"
	// BlockReasonServerScope marks a nested code_execution call to a server
	// outside the caller's profile scope. Top-level out-of-scope refusals are
	// the non-disclosing row with no block_reason.
	BlockReasonServerScope BlockReason = "profile_server_scope"
)

// FixAction identifies the action attached to a client warning.
type FixAction string

const FixMoveClient FixAction = "move_client"

// CredentialState reports what a client's connection carries.
type CredentialState string

const (
	CredentialStateClient CredentialState = "client"
	CredentialStateNone   CredentialState = "none"
)

// Error codes carried in the `code` field of a profile refusal body.
const (
	ErrorCodeNoClientCredential = "no_client_credential"
	// ErrorCodeConnectInProgress answers a second connect, rotate, finalize or
	// binding change of a client whose connect holds the in-flight claim.
	ErrorCodeConnectInProgress = "connect_in_progress"
	// ErrorCodeCredentialSuperseded answers a connect whose credential was
	// replaced or revoked before it could be finalized.
	ErrorCodeCredentialSuperseded = "credential_superseded"

	// Profile CRUD refusals.
	ErrorCodeProfileInUse = "profile_in_use"

	ErrorCodeIDMismatch         = "id_mismatch"
	ErrorCodePreconditionFailed = "precondition_failed"
)

// ChangeKind is the `change` of a `profile_change` activity record. The clients service writes assign/lock/unlock/forget/
// rotate; the profiles service writes the rest.
type ChangeKind string

const (
	ChangeCreate   ChangeKind = "create"
	ChangeUpdate   ChangeKind = "update"
	ChangeDelete   ChangeKind = "delete"
	ChangeRename   ChangeKind = "rename"
	ChangeAssign   ChangeKind = "assign"
	ChangeLock     ChangeKind = "lock"
	ChangeUnlock   ChangeKind = "unlock"
	ChangeMoveHost ChangeKind = "move_host"

	ChangeRotate ChangeKind = "rotate"
	// ChangeIssue records a credential issued through the credentials
	// service, on any surface (MCP, REST, CLI).
	ChangeIssue ChangeKind = "issue"
)

// Credential error codes of the `credentials` admin MCP tool. Each is the
// `code` of a refusal body {code, error, field?}.
const (
	CredentialErrorCodeSecretInArgument = "secret_in_argument"
)

// Surface names the caller of a profile-mutating operation, recorded on
// `profile_change` activity (the `X-MCPProxy-Surface` header).
type Surface string

const (
	SurfaceWeb Surface = "web"

	SurfaceMCP Surface = "mcp"
	SurfaceAPI Surface = "api"
)

// WarningCode is a Clients-surface warning code.
type WarningCode string

const (
	WarningProfileMissing WarningCode = "profile_missing"
)

// WarningSeverity grades a client warning.
type WarningSeverity string

const (
	WarningSeverityWarn WarningSeverity = "warn"
)
