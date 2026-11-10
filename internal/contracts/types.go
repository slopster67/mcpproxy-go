// Package contracts defines typed data transfer objects for API communication
package contracts

import (
	"errors"
	"time"

	"github.com/slopster67/mcpproxy-go/internal/config"
)

// ErrServerNotFound is returned (wrapped) by controllers when a named upstream
// server does not exist, so the REST layer can answer 404 via errors.Is.
var ErrServerNotFound = errors.New("server not found")

// APIResponse is the standard wrapper for all API responses
type APIResponse struct {
	Success   bool        `json:"success"`
	Data      interface{} `json:"data,omitempty" swaggertype:"object"`
	Error     string      `json:"error,omitempty"`
	RequestID string      `json:"request_id,omitempty"`
}

// Server is the one server object: the stored config.ServerConfig fields
// exactly as written (same JSON names PATCH accepts), flattened, plus the
// read-only runtime fields below. runtime.Runtime.Servers builds it for REST,
// the event stream and the MCP management tools (which mask it).
type Server struct {
	config.ServerConfig

	ID           string `json:"id"`
	HostName     string `json:"host_name"`
	HostOnline   bool   `json:"host_online"`
	IconUploaded bool   `json:"icon_uploaded,omitempty"`
	IconURL      string `json:"icon_url,omitempty"`

	Connected      bool   `json:"connected"`
	Connecting     bool   `json:"connecting"`
	Status         string `json:"status"`
	LastError      string `json:"last_error,omitempty"`
	ReconnectCount int    `json:"reconnect_count"`
	ToolCount      int    `json:"tool_count"`
	// OAuthActive reports that OAuth is in use for this server: mode "on", or
	// automatic mode with a stored token or an OAuth-required error.
	OAuthActive bool `json:"oauth_active"`
	// LastUsedAt is the time of the server's latest tool call in the retained
	// activity log; absent when it has none.
	LastUsedAt     *time.Time `json:"last_used_at,omitempty"`
	Authenticated  bool       `json:"authenticated"`              // OAuth token stored
	OAuthStatus    string     `json:"oauth_status,omitempty"`     // "authenticated", "expired", "error", "none"
	OAuthMode      string     `json:"oauth_mode,omitempty"`       // effective mode: "auto", "on" or "off"; remote servers only
	TokenExpiresAt *time.Time `json:"token_expires_at,omitempty"` // OAuth token expiry
	// RetryStopped reports that automatic reconnection has been given up for
	// good because the failure is deterministic and unrecoverable — a missing
	// binary, an image without the interpreter, an unparseable config.
	// RetryStoppedCode is the stable MCPX_* code that proved it and
	// RetryStoppedReason the catalog message explaining how to fix it.
	RetryStopped       bool             `json:"retry_stopped,omitempty"`
	RetryStoppedCode   string           `json:"retry_stopped_code,omitempty"`
	RetryStoppedReason string           `json:"retry_stopped_reason,omitempty"`
	UserLoggedOut      bool             `json:"user_logged_out,omitempty"` // user explicitly logged out (prevents auto-reconnection)
	Health             *HealthStatus    `json:"health,omitempty"`          // unified health status calculated by the backend
	Quarantine         *QuarantineStats `json:"quarantine,omitempty"`      // tool quarantine metrics for this server
	// Generation is the server's committed config generation: it changes whenever the server's configuration changes.
	Generation int64 `json:"generation,omitempty"`
	// Incarnation changes only when the name is removed and created again.
	// Approval endpoints accept it back as
	// expected_incarnation and answer 409 server_replaced on a mismatch.
	Incarnation int64 `json:"incarnation,omitempty"`

	// Structured diagnostic error and stable error code, set when
	// the server is in a failed state and the error has been classified.
	Diagnostic *Diagnostic `json:"diagnostic,omitempty"`
	ErrorCode  string      `json:"error_code,omitempty"`
}

// Diagnostic is the REST-API representation of a classified server failure.
type Diagnostic struct {
	Code        string     `json:"code"`
	Severity    string     `json:"severity"`
	Cause       string     `json:"cause,omitempty"`
	DetectedAt  *time.Time `json:"detected_at,omitempty"`
	UserMessage string     `json:"user_message,omitempty"`
}

// QuarantineStats represents tool quarantine metrics for a server.
type QuarantineStats struct {
	PendingCount int `json:"pending_count"` // Number of newly discovered tools awaiting approval
	ChangedCount int `json:"changed_count"` // Number of tools whose description/schema changed since approval
	BlockedCount int `json:"blocked_count"` // Number of disabled (blocked) tools
}

// ToolAnnotation represents MCP tool behavior hints
type ToolAnnotation struct {
	Title           string `json:"title,omitempty"`
	ReadOnlyHint    *bool  `json:"readOnlyHint,omitempty"`
	DestructiveHint *bool  `json:"destructiveHint,omitempty"`
	IdempotentHint  *bool  `json:"idempotentHint,omitempty"`
	OpenWorldHint   *bool  `json:"openWorldHint,omitempty"`
}

// MCPSession represents a client session with MCPProxy
type MCPSession struct {
	ID            string     `json:"id"`
	ClientName    string     `json:"client_name,omitempty"`
	ClientVersion string     `json:"client_version,omitempty"`
	Status        string     `json:"status"`
	StartTime     time.Time  `json:"start_time"`
	EndTime       *time.Time `json:"end_time,omitempty"`
	LastActivity  time.Time  `json:"last_activity"`
	ToolCallCount int        `json:"tool_call_count"`

	// MCP Client Capabilities
	HasRoots     bool     `json:"has_roots,omitempty"`
	HasSampling  bool     `json:"has_sampling,omitempty"`
	Experimental []string `json:"experimental,omitempty"`

	// Workspace / work session. WorkspaceName is the project's
	// basename — the full local path is never exposed. WorkSessionID groups the
	// reconnects that make up one stretch of user work.
	WorkspaceName string `json:"workspace_name,omitempty"`
	WorkSessionID string `json:"work_session_id,omitempty"`

	ClientID      string `json:"client_id,omitempty"`
	TokenName     string `json:"token_name,omitempty"`
	Profile       string `json:"profile,omitempty"`
	ProfileSource string `json:"profile_source,omitempty"`
}

// Tool represents an MCP tool with its metadata
type Tool struct {
	// ID is "server:tool", the id of GET/PATCH /api/v1/tools/{id}.
	ID                 string                 `json:"id"`
	Classification     string                 `json:"classification,omitempty"`
	ClassificationHash string                 `json:"classification_hash,omitempty"`
	Cached             bool                   `json:"cached,omitempty"`
	Name               string                 `json:"name"`
	ServerName         string                 `json:"server_name"`
	Description        string                 `json:"description"`
	Schema             map[string]interface{} `json:"schema,omitempty" swaggertype:"object"`
	OutputSchema       interface{}            `json:"output_schema,omitempty" swaggertype:"object"`
	Usage              int                    `json:"usage"`
	LastUsed           *time.Time             `json:"last_used,omitempty"`
	Annotations        *ToolAnnotation        `json:"annotations,omitempty"`
	// Tier is computed by AnnotationTier from
	// Annotations — read|write|destructive|unannotated. Set by every producer
	// of a Tool (enrichServerTools, the global tools handler); never left for
	// a consuming surface to compute.
	Tier           Tier   `json:"tier,omitempty"`
	ApprovalStatus string `json:"approval_status,omitempty"`
	// ProfileTier and Access are present only in a view-as listing (a tools
	// listing with ?client= or ?profile=). ProfileTier is the tool's tier
	// under the viewed subject's profile (Tier above stays the intrinsic
	// tier); Access is the subject's verdict for the tool. Absent otherwise.
	ProfileTier Tier        `json:"profile_tier,omitempty"`
	Access      *ToolAccess `json:"access,omitempty"`
	// Disabled mirrors ToolApprovalRecord.Disabled so per-tool enable state is
	// available without a second round-trip to the approvals endpoint. Absent
	// in the JSON when false (default) to keep responses compact.
	Disabled bool `json:"disabled,omitempty"`
	// ServerIncarnation is the committed incarnation of the server this row
	// was read for, captured before the tools were fetched.
	// A client echoes it as expected_incarnation on a per-tool decision, so a
	// stale row is refused (409 server_replaced) after a same-name re-add.
	// Omitted when the server has no committed incarnation.
	ServerIncarnation int64 `json:"server_incarnation,omitempty"`
	// Review is the tool's review (runtime.ReviewTool: current and previous
	// definition, diff, the fingerprint approvals bind to) while it awaits
	// approval (approval_status pending or changed); absent otherwise.
	Review any    `json:"review,omitempty" swaggertype:"object"`
	Hash   string `json:"hash,omitempty"`
}

type DisabledToolStatus = string

const (
	DisabledStatusServerDisabled  DisabledToolStatus = "server_disabled"
	DisabledStatusByUser          DisabledToolStatus = "disabled_by_user"
	DisabledStatusPendingApproval DisabledToolStatus = "pending_approval"
	DisabledStatusUnknown         DisabledToolStatus = "disabled_unknown"
)

// LockedToolEntry is the lean discovery shape for a non-callable tool returned
// by retrieve_tools when include_disabled=true. No input schema —
// the agent only needs enough to tell the user the capability exists and why.
type LockedToolEntry struct {
	Name        string             `json:"name"`
	Server      string             `json:"server"`
	Description string             `json:"description"`
	Status      DisabledToolStatus `json:"status"`
}

// SearchResult represents a search result for tools
type SearchResult struct {
	Tool    Tool    `json:"tool"`
	Score   float64 `json:"score"`
	Snippet string  `json:"snippet,omitempty"`
	Matches int     `json:"matches"`
}

// ServerStats represents aggregated statistics about servers
type ServerStats struct {
	TotalServers     int `json:"total_servers"`
	ConnectedServers int `json:"connected_servers"`
	TotalTools       int `json:"total_tools"`
}

// UsageAggregateResponse is the GET /api/v1/activity/usage payload.
// It is served from the actor-owned in-memory usage aggregate snapshot (never a
// per-request full-log scan).
//
// Windowing semantics:
//   - The per-tool rollup (Tools) carries lifetime-cumulative metrics. `window`
//     scopes the tool LIST to tools last used within the span (by LastUsed); the
//     counts/bytes/latency themselves are lifetime totals over the aggregate's
//     retention horizon. Exact per-tool windowed counts would require per-tool
//     time buckets in the aggregate.
//   - The Timeline buckets are global (not per-tool); `window` trims them to the
//     requested span. Timeline is therefore not filtered by tool/server/status.
//   - tool/server/status act as membership filters on the per-tool rollup.
type UsageAggregateResponse struct {
	Window      string    `json:"window"`
	GeneratedAt time.Time `json:"generated_at"`
	FreshnessMs int64     `json:"freshness_ms"` // age of the underlying snapshot in ms

	Tools    []UsageToolStat   `json:"tools"`
	Other    *UsageOtherBucket `json:"other,omitempty"` // present only when the list was truncated to top-N
	Timeline []UsageTimeBucket `json:"timeline"`
	// TotalCalls and TotalErrors are the headline counts for the window: the sum
	// of the timeline this same response carries, so the tiles and the histogram
	// under them cannot disagree. They are NOT the sum of Tools — that list is
	// lifetime-cumulative, upstream-only and truncated to top-N, so it must not
	// be summed client-side. The population is storage.CountsAsCall.
	//
	// Two bounds on how exactly this matches the Activity Log's own count:
	//
	//   - Window granularity is the timeline's: whole hour buckets, so the span
	//     is the requested window rounded up to a bucket edge.
	//   - This response is served from a snapshot behind a short read cache
	//     (observability.usage_cache_ttl, 5s by default) so the endpoint never
	//     scans the activity log per request, while the summary endpoint counts
	//     live. Calls that land inside that window appear on the Activity Log
	//     first. FreshnessMs and GeneratedAt say how old the figures are, and
	//     the Usage tab prints it ("Updated 3s ago").
	TotalCalls  int64 `json:"total_calls"`
	TotalErrors int64 `json:"total_errors"`
}

// UsageToolStat is the per-(server,tool) rollup row in UsageAggregateResponse.
type UsageToolStat struct {
	Server    string  `json:"server"`
	Tool      string  `json:"tool"`
	Calls     int64   `json:"calls"`
	Errors    int64   `json:"errors"`
	ErrorRate float64 `json:"error_rate"`
	Blocked   int64   `json:"blocked"`

	TotalRespBytes int64  `json:"total_resp_bytes"`
	AvgRespBytes   *int64 `json:"avg_resp_bytes"` // null when sized_calls == 0
	TotalReqBytes  int64  `json:"total_req_bytes"`
	AvgReqBytes    *int64 `json:"avg_req_bytes"` // null when no sized request calls
	SizedCalls     int64  `json:"sized_calls"`   // calls with known response size (basis for avg_resp_bytes)
	// P50Ms and P95Ms are read off a fixed latency histogram, so they are BUCKET
	// BOUNDS, not measured durations: the true percentile is at or below the
	// value, and a client must render it as a bound ("≤ 5 ms"). P50Exceeds /
	// P95Exceeds flip that reading for the unbounded overflow bucket, where the
	// value is the last bound and the truth is above it ("> 10 s").
	P50Ms      int64     `json:"p50_ms"`
	P50Exceeds bool      `json:"p50_exceeds"`
	P95Ms      int64     `json:"p95_ms"`
	P95Exceeds bool      `json:"p95_exceeds"`
	LastUsed   time.Time `json:"last_used"`
}

// UsageOtherBucket folds the tail of the per-tool list beyond top-N (FR: charts
// stay readable on high-cardinality logs).
type UsageOtherBucket struct {
	ToolsFolded    int   `json:"tools_folded"`
	Calls          int64 `json:"calls"`
	TotalRespBytes int64 `json:"total_resp_bytes"`
}

// UsageTimeBucket is one timeline bar (executed calls only; blocked attempts and
// non-tool records are excluded by the aggregate).
type UsageTimeBucket struct {
	Start          time.Time `json:"start"`
	Calls          int64     `json:"calls"`
	Errors         int64     `json:"errors"`
	TotalRespBytes int64     `json:"total_resp_bytes"`
}

// LogEntry represents a single log entry
type LogEntry struct {
	Timestamp time.Time              `json:"timestamp"`
	Level     string                 `json:"level"`
	Message   string                 `json:"message"`
	Server    string                 `json:"server,omitempty"`
	Fields    map[string]interface{} `json:"fields,omitempty" swaggertype:"object"`
}

// API Request/Response DTOs

// GetServersResponse is the response for GET /api/v1/servers
type GetServersResponse struct {
	Servers []Server    `json:"servers"`
	Stats   ServerStats `json:"stats"`
}

// GlobalToolsStats is the aggregate rollup shown on the global tools page.
// Disabled counts a tool that is user-disabled;
// Enabled = Total - Disabled; PendingApproval counts pending/changed approval.
type GlobalToolsStats struct {
	Total           int `json:"total"`
	Enabled         int `json:"enabled"`
	Disabled        int `json:"disabled"`
	PendingApproval int `json:"pending_approval"`
}

// GlobalToolsResponse is the response for GET /api/v1/tools — every tool from
// every configured server (including disabled servers and individually
// disabled tools), enriched with approval state and usage. Partial is set
// when one or more servers could not be read; the list still contains every
// tool that could be gathered.
type GlobalToolsResponse struct {
	Tools         []Tool           `json:"tools"`
	Stats         GlobalToolsStats `json:"stats"`
	Partial       bool             `json:"partial,omitempty"`
	FailedServers []string         `json:"failed_servers,omitempty"`
}

// ToolAccess is a view-as verdict for one tool. Reason is
// empty when the tool is callable; otherwise it is one of profile.AccessReasons
// (server_not_in_profile, denied_by_rule, unannotated_hidden, above_tier_cap,
// credential, profile, server_in_scope, token_permission, global_gate,
// server_state, tool_approval). Visible means the subject's discovery would
// list the tool; Callable means a real call would succeed.
type ToolAccess struct {
	Visible  bool   `json:"visible"`
	Callable bool   `json:"callable"`
	Reason   string `json:"reason,omitempty"`
}

// SearchToolsResponse is the response for GET /api/v1/index/search
type SearchToolsResponse struct {
	Query   string         `json:"query"`
	Results []SearchResult `json:"results"`
	Total   int            `json:"total"`
	Took    string         `json:"took"`
}

// GetServerLogsResponse is the response for GET /api/v1/servers/{id}/logs
type GetServerLogsResponse struct {
	ServerName string     `json:"server_name"`
	Logs       []LogEntry `json:"logs"`
	Count      int        `json:"count"`
}

// Secret management DTOs

// OAuthStartResponse is returned by POST /api/v1/servers/{id}/login when OAuth flow starts successfully.
type OAuthStartResponse struct {
	Success       bool   `json:"success"`                 // Always true for successful start
	ServerName    string `json:"server_name"`             // Name of the server being authenticated
	CorrelationID string `json:"correlation_id"`          // UUID for tracking this flow
	AuthURL       string `json:"auth_url,omitempty"`      // Authorization URL (always included for manual use)
	BrowserOpened bool   `json:"browser_opened"`          // Whether browser launch succeeded
	BrowserError  string `json:"browser_error,omitempty"` // Error message if browser launch failed
	Message       string `json:"message"`                 // Human-readable status message
}

// OAuthFlowError is returned for OAuth runtime failures (after validation passes but before browser opens).
// Examples: metadata discovery failure, DCR failure, authorization URL construction failure.
// Implements the error interface so it can be returned as an error while carrying structured data.
type OAuthFlowError struct {
	Success       bool               `json:"success"`           // Always false
	ErrorType     string             `json:"error_type"`        // Category of OAuth runtime failure
	ErrorCode     string             `json:"error_code"`        // Machine-readable error code (e.g., OAUTH_NO_METADATA)
	ServerName    string             `json:"server_name"`       // Server that failed OAuth
	CorrelationID string             `json:"correlation_id"`    // Flow tracking ID for log correlation
	RequestID     string             `json:"request_id"`        // HTTP request ID
	Message       string             `json:"message"`           // Human-readable error description
	Details       *OAuthErrorDetails `json:"details,omitempty"` // Structured discovery/failure details
	Suggestion    string             `json:"suggestion"`        // Actionable remediation hint
	DebugHint     string             `json:"debug_hint"`        // CLI command for log lookup
}

// Error implements the error interface for OAuthFlowError.
func (e *OAuthFlowError) Error() string {
	return e.Message
}

// OAuthErrorDetails contains structured discovery/failure details for OAuth errors.
type OAuthErrorDetails struct {
	ServerURL                   string          `json:"server_url"`
	ProtectedResourceMetadata   *MetadataStatus `json:"protected_resource_metadata,omitempty"`
	AuthorizationServerMetadata *MetadataStatus `json:"authorization_server_metadata,omitempty"`
	DCRStatus                   *DCRStatus      `json:"dcr_status,omitempty"`
}

// MetadataStatus represents the status of OAuth metadata discovery.
type MetadataStatus struct {
	Found                bool     `json:"found"`
	URLChecked           string   `json:"url_checked"`
	Error                string   `json:"error,omitempty"`
	AuthorizationServers []string `json:"authorization_servers,omitempty"`
}

// DCRStatus represents the status of Dynamic Client Registration.
type DCRStatus struct {
	Attempted  bool   `json:"attempted"`
	Success    bool   `json:"success"`
	StatusCode int    `json:"status_code,omitempty"`
	Error      string `json:"error,omitempty"`
}

// OAuth flow error type constants
const (
	OAuthErrorMetadataMissing = "oauth_metadata_missing"

	OAuthErrorClientIDRequired = "oauth_client_id_required"
	OAuthErrorDCRFailed        = "oauth_dcr_failed"
	OAuthErrorFlowFailed       = "oauth_flow_failed"
)

// OAuth flow error code constants (machine-readable)
const (
	OAuthCodeNoMetadata = "OAUTH_NO_METADATA"

	OAuthCodeNoClientID = "OAUTH_NO_CLIENT_ID"
	OAuthCodeDCRFailed  = "OAUTH_DCR_FAILED"
	OAuthCodeFlowFailed = "OAUTH_FLOW_FAILED"
)

// Tool Call History types

// GetSessionsResponse is the response for GET /api/v1/sessions
type GetSessionsResponse struct {
	Sessions []MCPSession `json:"sessions"`
	Total    int          `json:"total"`
	Limit    int          `json:"limit"`
	Offset   int          `json:"offset"`
}

// Configuration management types

// Tool call replay types

// HealthStatus represents the unified health status of an upstream MCP server.
// Calculated once in the backend and rendered identically by all interfaces.
type HealthStatus struct {
	// Level indicates the health level: "healthy", "degraded", or "unhealthy"
	Level string `json:"level"`

	// AdminState indicates the admin state: "enabled" or "disabled"
	AdminState string `json:"admin_state"`

	// Summary is a human-readable status message (e.g., "Connected (5 tools)")
	Summary string `json:"summary"`

	// Detail is an optional longer explanation of the status
	Detail string `json:"detail,omitempty"`

	// Action is the suggested fix action: "login", "restart", "enable", "approve", "view_logs", "set_secret", "configure", "edit_url", or "" (none)
	// Invariant: Action always equals Actions[0], or "" when Actions is empty.
	Action string `json:"action,omitempty"`

	Status string `json:"status"`

	// Usable reports whether the server can currently serve tool calls. True
	// only when Status == "ready".
	Usable bool `json:"usable"`

	// Actions lists every applicable next step in priority order:
	// login > set_secret > configure > edit_url > approve > restart >
	// view_logs > enable. Always non-nil (empty slice, never null).
	Actions []string `json:"actions"`
}

// ---------------------------------------------------------------------------
// Required-tools preflight
//
// The wire mirror of internal/preflight. The evaluator package owns the
// semantics (classes, retryability, precedence); these constants exist so the
// REST DTOs, the OpenAPI spec and the generated TypeScript all name the same
// values. An anti-drift unit test in internal/preflight asserts the two sets
// are identical, so a new code cannot land on one side only.
// ---------------------------------------------------------------------------
