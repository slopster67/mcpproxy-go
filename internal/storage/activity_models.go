package storage

import (
	"encoding/json"
	"strings"
	"time"
)

// ActivityRecordsBucket is the encrypted bucket name for activity records
const ActivityRecordsBucket = "activity_records"

// ActivityType represents the type of activity being recorded
type ActivityType string

const (
	// MetadataKeyBlockReason is the event payload key of the typed cause of a
	// profile policy refusal.
	MetadataKeyBlockReason = "block_reason"

	// ActivityTypeToolCall represents a tool execution event
	ActivityTypeToolCall ActivityType = "tool_call"
	// ActivityTypePolicyDecision represents a policy blocking a tool call
	ActivityTypePolicyDecision ActivityType = "policy_decision"
	// ActivityTypeSystemStart represents MCPProxy server startup
	ActivityTypeSystemStart ActivityType = "system_start"
	// ActivityTypeSystemStop represents MCPProxy server shutdown
	ActivityTypeSystemStop ActivityType = "system_stop"
	// ActivityTypeInternalToolCall represents internal MCP tool calls like retrieve_tools, call_tool_*
	ActivityTypeInternalToolCall ActivityType = "internal_tool_call"
	// ActivityTypeConfigChange represents configuration changes like server add/remove/update
	ActivityTypeConfigChange ActivityType = "config_change"
	// ActivityTypeToolQuarantineChange represents a tool-level quarantine state change
	ActivityTypeToolQuarantineChange ActivityType = "tool_quarantine_change"
	// ActivityTypePreflight represents one executed required-tools preflight.
	// The record is written SYNCHRONOUSLY before the
	// preflight is answered — see runtime.ActivityService.RecordPreflight — so a
	// caller that got a verdict can always find the run that produced it.
	//
	// A preflight is set-scoped, not server-scoped: ServerName and ToolName stay
	// empty and the per-tool detail lives in Metadata under the MetadataKeyPreflight*
	// keys. RequestID is the correlation handle (`activity list --request-id`).
	ActivityTypePreflight ActivityType = "preflight"

	// ActivityTypeProfileChange represents a profile change: a client
	// binding, credential rotation or forget. Metadata:
	// actor_kind, actor_name, surface, change, profile, previous_profile,
	// client_id, token_name, diff.
	ActivityTypeProfileChange ActivityType = "profile_change"
)

const (
	// ActivityStatusSuccess is a call the upstream answered normally.
	ActivityStatusSuccess = "success"
	// ActivityStatusError is a call that failed (transport, upstream error, or
	// an isError:true answer).
	ActivityStatusError = "error"
	// ActivityStatusBlocked is a call a policy prevented from running.
	ActivityStatusBlocked = "blocked"
)

// InternalCallToolPrefix marks the internal_tool_call records that MIRROR a
// direct upstream dispatch (call_tool_read / _write / _destructive). Every one
// of them is paired with a tool_call record for the same dispatch, which is why
// both the list filter and the usage aggregate key off this prefix to avoid
// counting one call twice. Internal records WITHOUT it (retrieve_tools,
// describe_tool, code_execution, upstream_servers, quarantine_security …) are
// mcpproxy's own work and have no paired record.
const InternalCallToolPrefix = "call_tool_"

// Metadata keys carried by an ActivityTypePreflight record. The payload is deliberately small and
// enum-only: reason CODES and counts, never tool descriptions or arguments.
const (
	// MetadataKeyPreflightVerdict is the set-level verdict
	// (ready|degraded_retryable|blocked|unknown_ids).
	MetadataKeyPreflightVerdict = "verdict"
	// MetadataKeyPreflightIDsCount is the number of unique tool ids evaluated.
	MetadataKeyPreflightIDsCount = "ids_count"
	// MetadataKeyPreflightReasons is a {reason_code: count} rollup over the
	// unavailable results — the shape a dashboard or CLI summary reads.
	MetadataKeyPreflightReasons = "reasons"
	// MetadataKeyPreflightPerTool is the ordered per-tool detail:
	// [{id, status, reason?}] using the PreflightPerTool* keys below.
	MetadataKeyPreflightPerTool = "per_tool"
	// MetadataKeyPreflightSurface names the surface that ran the preflight when
	// it is not the REST endpoint: only "mcp-check", the in-band describe_tool
	// check mode. It is omitted for the REST surface.
	MetadataKeyPreflightSurface = "surface"

	// MetadataKeyPreflightArguments records the in-band caller's request AS
	// SENT, so the raw requested-id count stays recoverable from the record
	// even though MetadataKeyPreflightIDsCount is the UNIQUE count both
	// surfaces agree on. It carries the PreflightArgumentsKey* members below:
	// still ids and enum-valued filter names, never descriptions or upstream
	// arguments. Omitted for the REST surface.
	MetadataKeyPreflightArguments = "arguments"

	// Keys inside MetadataKeyPreflightArguments.
	//
	// PreflightArgumentsKeyToolIDs is the raw tool_ids array: request order,
	// untrimmed, duplicates intact — len() is the raw requested count.
	PreflightArgumentsKeyToolIDs = "tool_ids"
	// PreflightArgumentsKeyFilters lists the annotation filters that were in
	// effect, in the order describe_tool declares them. Absent when none were.
	PreflightArgumentsKeyFilters = "filters"

	// PreflightSurfaceMCPCheck marks a record written by describe_tool check
	// mode.
	PreflightSurfaceMCPCheck = "mcp-check"

	// Keys inside one MetadataKeyPreflightPerTool entry.
	PreflightPerToolKeyID     = "id"
	PreflightPerToolKeyStatus = "status"
	PreflightPerToolKeyReason = "reason"
)

// ActivitySource indicates how the activity was triggered
type ActivitySource string

const (
	// ActivitySourceMCP indicates the activity was triggered via MCP protocol (AI agent)
	ActivitySourceMCP ActivitySource = "mcp"
	// ActivitySourceAPI indicates the activity was triggered via REST API
	ActivitySourceAPI ActivitySource = "api"
	// ActivitySourceInternal indicates the activity was triggered by an internal subsystem
	ActivitySourceInternal ActivitySource = "internal"
)

// ActivityRecord represents a single activity log entry.
type ActivityRecord struct {
	ClientHostID      string                 `json:"client_host_id,omitempty"`
	ExecutionHostID   string                 `json:"execution_host_id,omitempty"`
	ID                string                 `json:"id"`                           // Unique identifier (ULID format)
	Type              ActivityType           `json:"type"`                         // Type of activity
	Source            ActivitySource         `json:"source,omitempty"`             // How activity was triggered: "mcp", "cli", "api"
	ServerName        string                 `json:"server_name,omitempty"`        // Name of upstream MCP server
	ToolName          string                 `json:"tool_name,omitempty"`          // Name of tool called
	Arguments         map[string]interface{} `json:"arguments,omitempty"`          // Tool call arguments
	Response          string                 `json:"response,omitempty"`           // Tool response (potentially truncated)
	ResponseTruncated bool                   `json:"response_truncated,omitempty"` // True if response was truncated
	Status            string                 `json:"status"`                       // Result status: "success", "error", "blocked"
	ErrorMessage      string                 `json:"error_message,omitempty"`      // Error details if status is "error"
	DurationMs        int64                  `json:"duration_ms,omitempty"`        // Execution duration in milliseconds
	Timestamp         time.Time              `json:"timestamp"`                    // When activity occurred
	SessionID         string                 `json:"session_id,omitempty"`         // MCP transport session ID (regenerated on every reconnect)
	RequestID         string                 `json:"request_id,omitempty"`         // HTTP request ID for correlation
	Metadata          map[string]interface{} `json:"metadata,omitempty"`           // Additional context-specific data

	// ParentID is the correlation id of the record that CAUSED this one: today
	// the code_execution call whose sandbox issued this sub-call. It equals the
	// parent record's RequestID, so the two directions are one query each:
	//   parent → children:  /api/v1/activity?parent_id=<parent request_id>
	//   child  → parent:    /api/v1/activity?request_id=<child parent_id>
	//
	// First-class rather than metadata for the same reason as WorkSessionID:
	// ActivityFilter.Matches compares struct fields, so a value tucked into
	// Metadata would be stored but not filterable. Empty for every top-level
	// call.
	ParentID string `json:"parent_id,omitempty"`

	// WorkSessionID groups records into one unit of USER WORK: one
	// client, in one project, under one principal, across reconnects. Unlike
	// SessionID it survives the client re-initializing, which real clients do
	// every few minutes.
	//
	// First-class rather than metadata on purpose: activity filtering compares
	// struct fields (see ActivityFilter.Matches), so a value tucked into
	// Metadata would be stored but not filterable.
	WorkSessionID string `json:"work_session_id,omitempty"`

	Profile       string `json:"profile,omitempty"`        // Effective profile name (profile_change: the NEW profile)
	ProfileSource string `json:"profile_source,omitempty"` // pin|binding|url|session|anonymous|none
	ClientID      string `json:"client_id,omitempty"`      // Client id from the client-credential binding
	ClientName    string `json:"client_name,omitempty"`    // Self-reported clientInfo.name (advisory, never authoritative)
	TokenName     string `json:"token_name,omitempty"`     // Agent/client token name
	// TokenPrefix is the calling token's 12-char display prefix, persisted as
	// the INTERNAL ownership proof: it is how a scoped caller recognises its
	// own rows. It is never projected: no REST, export or SSE shape maps it.
	TokenPrefix string `json:"token_prefix,omitempty"`
	// BlockReason is the typed cause of a profile policy refusal. Display
	// only: no filter reads it.
	BlockReason string `json:"block_reason,omitempty"`

	RequestBytes  int `json:"request_bytes,omitempty"`  // JSON-serialized request arguments size in bytes
	ResponseBytes int `json:"response_bytes,omitempty"` // Raw upstream response size in bytes before truncation
}

// MarshalBinary implements encoding.BinaryMarshaler for encrypted storage
func (a *ActivityRecord) MarshalBinary() ([]byte, error) {
	return json.Marshal(a)
}

// UnmarshalBinary implements encoding.BinaryUnmarshaler for encrypted storage
func (a *ActivityRecord) UnmarshalBinary(data []byte) error {
	return json.Unmarshal(data, a)
}

// ScopeFilterUnattributed is the sentinel filter value ("-") that selects
// records or sessions whose profile, client or token is EMPTY. It is never a valid stored value.
const ScopeFilterUnattributed = "-"

// ActivityIdentityOwner is the caller identity of a SCOPED (non-admin) reader.
// When set on an ActivityFilter, Matches evaluates the profile/client/token
// filters against the view that caller is allowed to see: a record the caller
// did not make reads as unattributed. Without it a scoped caller could probe
// "?token=<someone else>" and use total/the row set as an oracle for another
// token's activity.
//
// "Own" needs the stored token prefix AND name to match (names are unique per
// owner only; the 12-char prefix carries few random bits), the same rule as
// httpapi.redactForeignIdentity.
type ActivityIdentityOwner struct {
	TokenName   string
	TokenPrefix string
}

// Owns reports whether the record was made by this caller.
func (o *ActivityIdentityOwner) Owns(r *ActivityRecord) bool {
	if o == nil || o.TokenPrefix == "" {
		return false
	}
	return r.TokenPrefix == o.TokenPrefix && r.TokenName == o.TokenName
}

func (a *ActivityRecord) EffectiveProfile() string {
	return a.Profile
}

// scopeValueMatches applies one profile/client/token filter to an effective
// value: "" is no filter, "-" matches only the empty value, anything else is
// an exact match.
func scopeValueMatches(want, have string) bool {
	if want == "" {
		return true
	}
	if want == ScopeFilterUnattributed {
		return have == ""
	}
	return have == want
}

// matchesScopeAttribution evaluates the profile/client/token filters. Foreign rows read as empty for a scoped IdentityOwner.
func (f *ActivityFilter) matchesScopeAttribution(r *ActivityRecord) bool {
	if f.Profile == "" && f.ClientID == "" && f.TokenName == "" {
		return true
	}
	profile, clientID, tokenName := r.Profile, r.ClientID, r.TokenName
	if f.IdentityOwner != nil && !f.IdentityOwner.Owns(r) {
		profile, clientID, tokenName = "", "", ""
	}
	return scopeValueMatches(f.Profile, profile) &&
		scopeValueMatches(f.ClientID, clientID) &&
		scopeValueMatches(f.TokenName, tokenName)
}

// ActivityFilter represents query parameters for filtering activity records
type ActivityFilter struct {
	Types     []string  // Filter by activity types (OR logic)
	Server    string    // Filter by server name
	Tool      string    // Filter by tool name
	SessionID string    // Filter by MCP transport session
	Status    string    // Filter by status (success/error/blocked)
	StartTime time.Time // Activities after this time
	EndTime   time.Time // Activities before this time
	Limit     int       // Max records to return (default 50, max 100)
	Offset    int       // Pagination offset
	RequestID string    // Filter by HTTP request ID for correlation

	// ParentID selects the CHILDREN of one parent call (exact match on
	// ActivityRecord.ParentID) — the sub-calls a code_execution script issued.
	ParentID string

	// WorkSessionID filters by a unit of user work: one client, one
	// project, across reconnects. This is what the UI's "Session" filter means;
	// SessionID above is the raw transport connection.
	WorkSessionID string

	Profile   string // Effective profile name
	ClientID  string // Client id (the binding), NOT the self-reported name
	TokenName string // Token name

	// IdentityOwner is set for scoped (non-admin) callers: a row the owner did
	// not make reads as unattributed to the Profile/ClientID/TokenName filters.
	IdentityOwner *ActivityIdentityOwner

	// AllowedServers is an AUTHORIZATION filter, not a user-facing one: nil means unrestricted, a non-nil slice restricts matches to
	// records attributable to one of these server names ("*" is a wildcard, as
	// in auth.AuthContext.AllowedServers).
	//
	// It lives HERE, in the predicate ListActivities and StreamActivities both
	// run, rather than as a post-filter over a returned page. A post-filter
	// shrinks the page while `total` keeps counting the records it removed —
	// which is its own count oracle for exactly what was hidden, and breaks
	// pagination for the caller besides.
	//
	// An empty non-nil slice matches NOTHING, deliberately: it is the shape a
	// token allowed no servers produces, and treating it as unrestricted would
	// open the door it exists to close. A record with no ServerName (system
	// start/stop, config change) is likewise not matched — those are
	// operator-plane events with no server to be entitled to.
	AllowedServers []string

	ExcludeCallToolSuccess bool
}

// DefaultActivityFilter returns an ActivityFilter with sensible defaults
func DefaultActivityFilter() ActivityFilter {
	return ActivityFilter{
		Limit:                  50,
		Offset:                 0,
		ExcludeCallToolSuccess: true, // Exclude successful call_tool_* to avoid duplicates
	}
}

// Validate validates and normalizes the filter for regular list queries.
func (f *ActivityFilter) Validate() {
	if f.Limit <= 0 {
		f.Limit = 50
	}
	if f.Limit > 100 {
		f.Limit = 100
	}
	if f.Offset < 0 {
		f.Offset = 0
	}
}

// serverAllowed applies the AllowedServers authorization filter. nil means
// unrestricted; see the field comment for why empty-but-non-nil matches nothing
// and why an unattributed record is not matched.
func (f *ActivityFilter) serverAllowed(serverName string) bool {
	if f.AllowedServers == nil {
		return true
	}
	if serverName == "" {
		return false
	}
	for _, allowed := range f.AllowedServers {
		if allowed == "*" || allowed == serverName {
			return true
		}
	}
	return false
}

// Matches checks if an activity record matches the filter criteria.
func (f *ActivityFilter) Matches(record *ActivityRecord) bool {
	// Check types filter (OR logic for multiple types)
	if len(f.Types) > 0 {
		typeMatches := false
		for _, t := range f.Types {
			if string(record.Type) == t {
				typeMatches = true
				break
			}
		}
		if !typeMatches {
			return false
		}
	}

	// Authorization filter first: a caller must never be able to widen its own
	// visibility with any of the query-string filters below it.
	if !f.serverAllowed(record.ServerName) {
		return false
	}

	// Check server filter
	if f.Server != "" && record.ServerName != f.Server {
		return false
	}

	// Check tool filter
	if f.Tool != "" && record.ToolName != f.Tool {
		return false
	}

	// Check session filter
	if f.SessionID != "" && record.SessionID != f.SessionID {
		return false
	}

	// Check work-session filter
	if f.WorkSessionID != "" && record.WorkSessionID != f.WorkSessionID {
		return false
	}

	// Check status filter
	if f.Status != "" && record.Status != f.Status {
		return false
	}

	// Check time range
	if !f.StartTime.IsZero() && record.Timestamp.Before(f.StartTime) {
		return false
	}
	if !f.EndTime.IsZero() && record.Timestamp.After(f.EndTime) {
		return false
	}

	if f.RequestID != "" && record.RequestID != f.RequestID {
		return false
	}

	// Check parent_id filter: the sub-calls one code_execution issued.
	if f.ParentID != "" && record.ParentID != f.ParentID {
		return false
	}

	if f.ExcludeCallToolSuccess {
		if record.Type == ActivityTypeInternalToolCall &&
			strings.HasPrefix(record.ToolName, InternalCallToolPrefix) {
			return false
		}
	}

	// Scope attribution. After the authorization block above,
	// so a scope filter can never widen what the caller may see.
	return f.matchesScopeAttribution(record)
}
