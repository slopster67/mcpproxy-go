package contracts

import (
	"time"
)

// ActivityType represents the type of activity being recorded
type ActivityType string

// ActivitySource indicates how the activity was triggered
type ActivitySource string

// ActivityRecord represents an activity record in API responses
type ActivityRecord struct {
	ClientHostID      string                 `json:"client_host_id,omitempty"`
	ExecutionHostID   string                 `json:"execution_host_id,omitempty"`
	ID                string                 `json:"id"`                                       // Unique identifier (ULID format)
	Type              ActivityType           `json:"type"`                                     // Type of activity
	Source            ActivitySource         `json:"source,omitempty"`                         // How activity was triggered: "mcp", "cli", "api"
	ServerName        string                 `json:"server_name,omitempty"`                    // Name of upstream MCP server
	ToolName          string                 `json:"tool_name,omitempty"`                      // Name of tool called
	Arguments         map[string]interface{} `json:"arguments,omitempty" swaggertype:"object"` // Tool call arguments
	Response          string                 `json:"response,omitempty"`                       // Tool response (potentially truncated)
	ResponseTruncated bool                   `json:"response_truncated,omitempty"`             // True if response was truncated
	Status            string                 `json:"status"`                                   // Result status: "success", "error", "blocked"
	ErrorMessage      string                 `json:"error_message,omitempty"`                  // Error details if status is "error"
	DurationMs        int64                  `json:"duration_ms,omitempty"`                    // Execution duration in milliseconds
	Timestamp         time.Time              `json:"timestamp"`                                // When activity occurred
	SessionID         string                 `json:"session_id,omitempty"`                     // MCP transport session ID (regenerated on every reconnect)
	WorkSessionID     string                 `json:"work_session_id,omitempty"`                // One client, one project, across reconnects
	RequestID         string                 `json:"request_id,omitempty"`                     // HTTP request ID for correlation
	ParentID          string                 `json:"parent_id,omitempty"`                      // Correlation id of the parent call (the code_execution whose sandbox issued this sub-call)
	Metadata          map[string]interface{} `json:"metadata,omitempty" swaggertype:"object"`  // Additional context-specific data

	// Caller identity, lifted out of the internal `_auth_*` argument
	// keys that the REST boundary strips. Only these two filterable fields are
	// surfaced; the token prefix and user identity stay internal.
	// A scoped (non-admin) caller sees them only on rows it made itself.

	AuthType  string `json:"auth_type,omitempty"`  // "admin", "agent", "user" or "admin_user"; empty without an auth context or on another caller's row for a scoped caller
	AgentName string `json:"agent_name,omitempty"` // Client token name when auth_type is "agent"

	// Scope attribution: the profile, client and token in
	// effect when the call ran. Stamped at write time, never rewritten. For a
	// scoped (non-admin) caller the profile/profile_source/client_id/token_name
	// of a row it did not make are blanked (binding disclosure is admin-only);
	// client_name is self-reported and stays. Absent on records with no
	// MCP/REST request context.
	Profile       string `json:"profile,omitempty"`        // Effective profile name (profile_change: the new profile)
	ProfileSource string `json:"profile_source,omitempty"` // pin|binding|url|session|anonymous|none
	ClientID      string `json:"client_id,omitempty"`      // Client id from the client-credential binding
	ClientName    string `json:"client_name,omitempty"`    // Self-reported clientInfo.name (advisory)
	TokenName     string `json:"token_name,omitempty"`     // Agent/client token name
	BlockReason   string `json:"block_reason,omitempty"`   // Typed cause of a profile policy refusal

	RequestBytes  int `json:"request_bytes,omitempty"`  // JSON-serialized request arguments size in bytes
	ResponseBytes int `json:"response_bytes,omitempty"` // Raw upstream response size in bytes before truncation

}

// ActivityListResponse is the response for GET /api/v1/activity
type ActivityListResponse struct {
	Activities []ActivityRecord `json:"activities"`
	Total      int              `json:"total"`
	Limit      int              `json:"limit"`
	Offset     int              `json:"offset"`
}
