// Re-export common types from contracts
export type { HealthStatus, HealthLevel, AdminState, HealthAction, Tier } from './contracts'
export {
  HealthLevelHealthy,
  HealthLevelDegraded,
  HealthLevelUnhealthy,
  AdminStateEnabled,
  AdminStateDisabled,
  HealthActionNone,
  HealthActionLogin,
  HealthActionRestart,
  HealthActionEnable,
  HealthActionApprove,
  HealthActionViewLogs,
  HealthActionSetSecret,
  HealthActionConfigure,
} from './contracts'

// Import HealthStatus/Tier for use in this file
import type {
  CredentialState, HealthStatus, Tier, ProfileSource, ProfileBlockReason,
  ProfileView, ProfileList, ProfileUsedBy, ProfileToolCounts,
  EffectiveTool, EffectiveToolsResult, AccessExplanation, ClientWarning, ClientView,
  FixAction,
} from './contracts'
import type { OAuthOverrides } from './oauth'

// The Profiles v3 wire shapes live in contracts.ts. This file re-exports them
// so the views import from one place, and adds the request bodies and the
// handful of responses that only the Web UI consumes.
export type {
  ProfileView, ProfileList, ProfileUsedBy, ProfileToolCounts,
  EffectiveTool, EffectiveToolsResult, AccessExplanation, ClientWarning, ClientView,
  FixAction, CredentialState,
  ProfileSource, ProfileBlockReason,
}

// Quarantine stats for tool-level quarantine
export interface QuarantineStats {
  pending_count: number
  changed_count: number
  blocked_count: number
}

export interface Server {
 host_id?: string
 host_name?: string
 host_online?: boolean
 available_to_peers?: boolean
  icon_url?: string;
  icon_uploaded?: boolean;
  name: string
  /**
   * Committed config generation and incarnation The
   * incarnation changes only when the name is removed and created again; the
   * approval calls echo it back as `expected_incarnation`.
   */
  generation?: number
  incarnation?: number
  /** Catalog provenance, used only to resolve an already-added catalog card. */
  source_registry_id?: string
  // Human-friendly display label from the source registry. When
  // present it is preferred over `name` for display; `name` stays the stable
  // identifier used for routing and API calls (it may be a reverse-DNS id such
  // as "io.github.owner/repo").
  title?: string
  url?: string
  command?: string
  args?: string[]
  working_dir?: string
  env?: Record<string, string>
  // Configured upstream HTTP headers, as written.
  headers?: Record<string, string>
  protocol: 'http' | 'stdio' | 'streamable-http'
  enabled: boolean

  trust_mode?: string
  connected: boolean
  connecting: boolean
  authenticated?: boolean
  tool_count: number
  last_error?: string
  reconnect_count?: number
  // Stored OAuth block as written (mode, client_secret and overrides).
  oauth?: OAuthOverrides | null
  // True when OAuth is in use: mode "on", or automatic with a stored token or
  // an OAuth-required error.
  oauth_active?: boolean
  oauth_status?: 'authenticated' | 'expired' | 'error' | 'none'
  oauth_mode?: 'auto' | 'on' | 'off' // remote servers only
  token_expires_at?: string
  user_logged_out?: boolean // True if user explicitly logged out (prevents auto-reconnection)
  health?: HealthStatus // Unified health status calculated by the backend
  quarantine?: QuarantineStats // Tool-level quarantine stats
  last_used_at?: string // latest tool call in the retained activity log
  // Structured diagnostic error + stable error code
  error_code?: string
  diagnostic?: Diagnostic | null
}

// Diagnostics & error taxonomy types.
export type DiagnosticSeverity = 'info' | 'warn' | 'error'
export interface Diagnostic {
  code: string
  severity: DiagnosticSeverity
  cause?: string
  detected_at?: string
  user_message?: string
}

// Global Tools response types
export interface GlobalTool {
  classification?: 'read' | 'write' | 'destructive'
  classification_hash?: string
  cached?: boolean
  schema?: unknown
  output_schema?: unknown
  hash?: string
  name: string
  server_name: string
  description: string
  approval_status: string  // "pending" | "changed" | "approved" | ""
  disabled: boolean        // per-tool user toggle
  usage: number
  last_used?: string       // ISO 8601; omitted if never used in window
  annotations?: ToolAnnotation
  // Computed by contracts.AnnotationTier — never derive this
  // from `annotations` on the frontend.
  tier?: Tier
  // Hold evidence present only
  // on tools the trust gate refused to auto-approve. GET /api/v1/tools emits
  // these alongside the approval status (internal/httpapi/server.go); records
  // without them must render unchanged.
  held_reason?: string     // "scan_findings" (threat) | "scan_coverage" (precaution)
  held_verdict?: string    // "dangerous" | "warnings" | "clean"
  held_signals?: string[]  // matched deterministic check ids, producer order, ≤16
  // The incarnation of the server this row was read for; a
  // per-tool decision echoes it as expected_incarnation. Omitted when unknown.
  server_incarnation?: number
  // Present while the tool awaits review (approval_status pending or changed).
  review?: ReviewTool
  // Present only in a view-as listing (`GET /tools?client=|profile=`).
  // `tier` stays the intrinsic tier; `profile_tier` is the tier under the viewed
  // subject's profile and `access` is the subject's verdict for the tool.
  profile_tier?: Tier
  access?: ToolAccess
  // derived locally: enabled = !disabled
}

// A view-as verdict for one tool. `reason` is empty when the
// tool is callable, otherwise one of profile.AccessReasons (see
// utils/profiles.ts reasonText for the words).
export interface ToolAccess {
  visible: boolean
  callable: boolean
  reason?: string
}

export interface GlobalToolsStats {
  total: number
  enabled: number
  disabled: number
  pending_approval: number
}

export interface GlobalToolsResponse {
  tools: GlobalTool[]
  stats: GlobalToolsStats
  partial: boolean
  failed_servers: string[]
}

// Tool Annotation types
export interface ToolAnnotation {
  title?: string
  readOnlyHint?: boolean
  destructiveHint?: boolean
  idempotentHint?: boolean
  openWorldHint?: boolean
}

// MCP Session types
export interface MCPSession {
  id: string
  client_name?: string
  client_version?: string
  status: 'active' | 'closed'
  start_time: string  // ISO 8601
  end_time?: string   // ISO 8601
  last_activity: string  // ISO 8601
  tool_call_count: number
  // MCP Client Capabilities
  has_roots?: boolean
  has_sampling?: boolean
  experimental?: string[]
  // The project the client is working in (basename only — the full
  // local path never leaves the machine), and the work session this connection
  // belongs to.
  workspace_name?: string
  work_session_id?: string
  // The credential the session initialized with and the latest
  // profile resolution.
  client_id?: string
  token_name?: string
  profile?: string
  profile_source?: ProfileSource
}

// Tool approval types

// Search result types
export interface SearchResult {
  tool: {
    name: string
    description: string
    server_name: string
    input_schema?: Record<string, any>
    usage?: number
    last_used?: string
  }
  score: number
  snippet?: string
  matches: number
}

// One frame of GET /api/v1/events (event: change).
export interface ChangeFrame {
  seq: number
  entity: string
  op: 'upsert' | 'delete' | 'append' | 'invalidate'
  id?: string
  data?: unknown
}

export interface ServerLogEntry {
  timestamp: string
  level: string
  message: string
  server: string
  fields?: Record<string, unknown>
}

// One element of GET /api/v1/secrets. Values are never included.
export interface SecretEntry {
  id: string
  name: string
  type: string
  reference: string
  servers: string[]
  host_id?: string
  host_name?: string
  is_set: boolean
  available: boolean
}


// Usage statistics aggregate — GET /api/v1/usage.
// Mirrors contracts.UsageAggregateResponse. Per-tool metrics are
// lifetime-cumulative; `window` scopes the timeline + the tool-list membership.
export interface UsageToolStat {
  server: string
  tool: string
  calls: number
  errors: number
  error_rate: number
  blocked: number
  total_resp_bytes: number
  avg_resp_bytes: number | null   // null when no sized calls exist
  total_req_bytes: number
  avg_req_bytes: number | null
  sized_calls: number
  // Bucket BOUNDS, not measurements: the true percentile is at or below the
  // value, except in the unbounded overflow bucket, where *_exceeds flips it to
  // a floor. Render through formatLatencyBound.
  p50_ms: number
  p50_exceeds: boolean
  p95_ms: number
  p95_exceeds: boolean
  last_used: string
}

export interface UsageOtherBucket {
  tools_folded: number
  calls: number
  total_resp_bytes: number
}

export interface UsageTimeBucket {
  start: string
  calls: number
  errors: number
  total_resp_bytes: number
}

export interface UsageAggregateResponse {
  window: string
  generated_at: string
  freshness_ms: number
  tools: UsageToolStat[]
  other?: UsageOtherBucket | null   // present only when list truncated to top-N
  timeline: UsageTimeBucket[]
  // Headline counts for the window, computed server-side as the sum of the
  // timeline above. NOT a sum of `tools`: that list is lifetime-cumulative,
  // upstream-only and truncated to top-N. Counted with
  // the same CountsAsCall population.
  total_calls: number
  total_errors: number
}

export type UsageWindow = '24h' | '7d' | 'all'
export type UsageSort = 'calls' | 'resp_bytes' | 'error_rate' | 'p95'
export type UsageStatus = 'success' | 'error' | 'blocked'

// Session response types
export interface GetSessionsResponse {
  sessions: MCPSession[]
  total: number
  limit: number
  offset: number
}

// Activity Log types (RFC-003)

// Every value ACTIVITY_TYPE_LABELS (utils/activity.ts) knows a label for —
// that map's own comment flags the drift risk of hand-copying this list, so
// keep the two in sync.
export type ActivityType =
  | 'tool_call'
  | 'internal_tool_call'
  | 'system_start'
  | 'system_stop'
  | 'config_change'
  | 'policy_decision'
  | 'server_change'
  /**
   * One executed required-tools preflight. Set-scoped, not
   * server-scoped — server_name/tool_name are empty and the verdict,
   * requested-id count and per-tool reason codes live in `metadata`
   * ({verdict, ids_count, reasons{code:count}, per_tool[{id,status,reason?}]}).
   */
  | 'preflight'
  /** Tool-level quarantine state change. */
  | 'tool_quarantine_change'
  /** Server edition only. */
  | 'credential_broker'
  /** A client binding, credential rotation or forget. */
  | 'profile_change'

export type ActivitySource = 'mcp' | 'api'

/** Activity call outcomes. */
export type ActivityStatus = 'success' | 'error' | 'blocked'

export interface ActivityRecord {
 client_host_id?: string
 execution_host_id?: string
  id: string
  type: ActivityType
  source?: ActivitySource
  server_name?: string
  tool_name?: string
  arguments?: Record<string, any>
  response?: string
  response_truncated?: boolean
  status: ActivityStatus
  error_message?: string
  duration_ms?: number
  timestamp: string
  session_id?: string
  /** One client, one project, across reconnects. */
  work_session_id?: string
  request_id?: string
  /**
   * Correlation id of the parent `code_execution` call this record was
   * dispatched from — equal to the parent record's `request_id`. Present only on
   * sandboxed sub-calls; a top-level call omits it. Navigate parent → children
   * with `?parent_id=<parent request_id>`, child → parent with
   * `?request_id=<child parent_id>`.
   */
  parent_id?: string
  metadata?: Record<string, any>
  /**
   * Caller identity — 'admin' | 'agent', plus 'user' | 'admin_user' on
   * the server edition. Absent on records written without an auth context, and
   * blanked for a scoped caller on rows it did not make.
   */
  auth_type?: 'admin' | 'agent' | 'user' | 'admin_user'
  /** Agent token name when auth_type is "agent". */
  agent_name?: string
  // The profile, client and token IN EFFECT when the call ran,
  // stamped at emit time.
  profile?: string
  profile_source?: ProfileSource
  client_id?: string
  /** The self-reported clientInfo.name: advisory, never authoritative. */
  client_name?: string
  token_name?: string
  /** Why a profile refused the call (activity of status "blocked"). */
  block_reason?: ProfileBlockReason
  // Sensitive data detection fields
}

export interface ActivityListResponse {
  activities: ActivityRecord[]
  total: number
  limit: number
  offset: number
}

// A Clients row: presence fields plus the credential and binding fields.
export type ClientPresence = Omit<ClientView, 'credential_state' | 'blocked_24h' | 'last_seen'> & {
  last_seen?: string | null
  credential_state?: CredentialState
  blocked_24h?: number
  // Lifecycle fields of the client's credential record.
  issuer?: CredentialIssuer | null
  profile_state?: 'ok' | 'dangling' | 'none'
}
export interface ClientsResponse { clients: ClientPresence[]; warnings?: ClientWarning[] }


export interface ReviewToolPrevious {
  description: string
  input_schema?: unknown
  output_schema?: unknown
  annotations?: Record<string, unknown> | null
}

export interface ReviewToolDiff {
  description?: string
  input_schema?: string
  output_schema?: string
  annotations?: string
}

export interface ReviewTool {
  classification?: 'read' | 'write' | 'destructive'
  classification_hash?: string
  name: string
  description: string
  input_schema?: unknown
  output_schema?: unknown
  annotations?: Record<string, unknown> | null
  tier: 'read' | 'write' | 'destructive' | 'unannotated' | 'unknown' | string
  approval_status: 'approved' | 'pending' | 'changed' | string
  disabled: boolean
  held_reason?: string
  held_signals?: string[]
  /** Fail-closed default selection computed by the core (D43); computed by the core. */
  default_allowed?: boolean
  previous?: ReviewToolPrevious | null
  diff?: ReviewToolDiff | null
  /** Review fingerprint sent back in expected_hashes to bind an approval to this review. */
  current_hash?: string
}

// ---------------------------------------------------------------------------
// Profiles v3 Response shapes come from contracts.ts.
// ---------------------------------------------------------------------------

/** The editable profile document (PUT /profiles/{id}, POST /profiles, try). */
export interface ProfileConfig {
  id: string
  selected_tools?: Record<string,string[]>
  name: string
  servers: string[]
  switchable_to?: string[]
}

export type ProfileMoved = { clients: string[] }
export interface ProfileDeleteResult { deleted: string; moved: ProfileMoved }


export interface ClientCredential {
  credential: string
  snippet: { generic_http: string }
}

export interface ExplainSubjectQuery {
  tool: string
  client?: string
  profile?: string

}

export interface CredentialIssuer { actor_kind: string; actor_name?: string; surface: string }

export interface Peer { id:string; name:string; online:boolean; created_at:string; last_seen?:string; address?:string }
