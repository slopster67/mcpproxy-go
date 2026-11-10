// TypeScript mirrors of the Go contracts: health, activity and profile vocabularies and REST row shapes.

// Health constants
// Health levels
export const HealthLevelHealthy = 'healthy' as const;
export const HealthLevelDegraded = 'degraded' as const;
export const HealthLevelUnhealthy = 'unhealthy' as const;
export type HealthLevel = typeof HealthLevelHealthy | typeof HealthLevelDegraded | typeof HealthLevelUnhealthy;

// Admin states
export const AdminStateEnabled = 'enabled' as const;
export const AdminStateDisabled = 'disabled' as const;
export type AdminState = typeof AdminStateEnabled | typeof AdminStateDisabled;

// Health actions - suggested remediation for health issues
export const HealthActionNone = '' as const;
export const HealthActionLogin = 'login' as const;
export const HealthActionRestart = 'restart' as const;
export const HealthActionEnable = 'enable' as const;
export const HealthActionApprove = 'approve' as const;
export const HealthActionViewLogs = 'view_logs' as const;
export const HealthActionSetSecret = 'set_secret' as const;
export const HealthActionConfigure = 'configure' as const;
export const HealthActionEditURL = 'edit_url' as const;
export type HealthAction =
  | typeof HealthActionNone
  | typeof HealthActionLogin
  | typeof HealthActionRestart
  | typeof HealthActionEnable
  | typeof HealthActionApprove
  | typeof HealthActionViewLogs
  | typeof HealthActionSetSecret
  | typeof HealthActionConfigure
  | typeof HealthActionEditURL;

// Health status vocabulary. The ONE vocabulary every
// surface (Web UI, macOS, tray, CLI) renders as text — HealthLevel stays a
// severity signal for badge/tray coloring only; no renderer may print it as
// text.
export const HealthStatusReady = 'ready' as const;
export const HealthStatusConnecting = 'connecting' as const;
export const HealthStatusSignInRequired = 'sign_in_required' as const;
export const HealthStatusNeedsReview = 'needs_review' as const;
export const HealthStatusNeedsSecret = 'needs_secret' as const;
export const HealthStatusNeedsConfig = 'needs_config' as const;
export const HealthStatusError = 'error' as const;
export const HealthStatusDisabled = 'disabled' as const;
export type HealthStatusValue =
  | typeof HealthStatusReady
  | typeof HealthStatusConnecting
  | typeof HealthStatusSignInRequired
  | typeof HealthStatusNeedsReview
  | typeof HealthStatusNeedsSecret
  | typeof HealthStatusNeedsConfig
  | typeof HealthStatusError
  | typeof HealthStatusDisabled;

// Label tables — binding for the Web UI, macOS window/tray and CLI table
// (StatusLabels / ActionLabels in internal/health/constants.go).
export const HEALTH_STATUS_LABELS: Record<HealthStatusValue, string> = {
  [HealthStatusReady]: 'Online',
  [HealthStatusConnecting]: 'Connecting',
  [HealthStatusSignInRequired]: 'Sign-in required',
  [HealthStatusNeedsReview]: 'Needs review',
  [HealthStatusNeedsSecret]: 'Secret required',
  [HealthStatusNeedsConfig]: 'Needs configuration',
  [HealthStatusError]: 'Error',
  [HealthStatusDisabled]: 'Disabled',
};

export interface HealthStatus {
  level: HealthLevel;
  admin_state: AdminState;
  summary: string;
  detail?: string;
  action?: HealthAction;
  /** The ONE status vocabulary rendered as text on every surface. */
  status: HealthStatusValue;
  /** True only when status === 'ready'. */
  usable: boolean;
  /** Every applicable next step, in priority order. Action always equals actions[0] (or action is '' when empty). */
  actions: HealthAction[];
}

export const TierRead = 'read' as const;
export const TierWrite = 'write' as const;
export const TierDestructive = 'destructive' as const;
export const TierUnannotated = 'unannotated' as const;
export const TierUnknown = 'unknown' as const;
export type Tier =
  | typeof TierRead
  | typeof TierWrite
  | typeof TierDestructive
  | typeof TierUnannotated
  | typeof TierUnknown;

// Activity views (the `view` URL parameter; the CLI has no `sessions`)
export const ActivityViewCalls = 'calls' as const;
export const ActivityViewSessions = 'sessions' as const;
export const ActivityViewSystem = 'system' as const;
export const ActivityViewAll = 'all' as const;
export type ActivityView =
  | typeof ActivityViewCalls
  | typeof ActivityViewSessions
  | typeof ActivityViewSystem
  | typeof ActivityViewAll;

// Profiles v3 enums





export const ProfileReasonNone = '' as const;
export const ProfileReasonServerNotInProfile = 'server_not_in_profile' as const;
export const ProfileReasonNotSelected = 'not_selected' as const;



export type ProfileReason = typeof ProfileReasonNone | typeof ProfileReasonServerNotInProfile | typeof ProfileReasonNotSelected;

export const ProfileSourcePin = 'pin' as const;
export const ProfileSourceBinding = 'binding' as const;
export const ProfileSourceSession = 'session' as const;

export const ProfileSourceNone = 'none' as const;
export type ProfileSource = typeof ProfileSourcePin | typeof ProfileSourceBinding | typeof ProfileSourceSession | typeof ProfileSourceNone;


export const BlockReasonProfileRule = 'profile_rule' as const;

export const BlockReasonProfileServerScope = 'profile_server_scope' as const;
export type ProfileBlockReason = typeof BlockReasonProfileRule | typeof BlockReasonProfileServerScope;

export const FixActionMoveClient = 'move_client' as const;
export type FixAction = typeof FixActionMoveClient;

export const CredentialStateClient = 'client' as const;

export const CredentialStateNone = 'none' as const;


export type CredentialState = typeof CredentialStateClient | typeof CredentialStateNone;




export const WarningProfileMissing = 'profile_missing' as const;

export type ClientWarningCode = typeof WarningProfileMissing;

// Warning severity and action, the access explainer and the REST
// view shapes of GET /profiles, GET /clients and GET /access/explain.
export const WarningSeverityWarn = 'warn' as const;
export const WarningSeverityInfo = 'info' as const;
export type WarningSeverity = typeof WarningSeverityWarn | typeof WarningSeverityInfo;

// A warning's action.kind is a FixAction spelling or this one (the bulk admin-key upgrade).


export const AccessSubjectClient = 'client' as const;
export const AccessSubjectToken = 'token' as const;
export const AccessSubjectProfile = 'profile' as const;
export const AccessSubjectAnonymous = 'anonymous' as const;
export type AccessSubjectKind =
  | typeof AccessSubjectClient
  | typeof AccessSubjectToken
  | typeof AccessSubjectProfile
  | typeof AccessSubjectAnonymous;


export interface ProfileToolCounts {
 unannotated: number;
  read: number;
  write: number;
  destructive: number;
}

// used_by is administrator-only: it is omitted, never emptied, for every other caller.
export interface ProfileUsedBy {
  clients: { id: string; mode: string }[];
}

// GET /api/v1/profiles row and GET /api/v1/profiles/{id}.
export interface ProfileView {
  id: string;
  selected_tools?: Record<string,string[]>;
  name: string;
  servers: string[];
  switchable_to?: string[];
  effective_servers: string[];
  tool_counts: ProfileToolCounts;
  calls_24h: number;
  blocked_24h: number;
  used_by?: ProfileUsedBy;
}

export interface ProfileList {
  profiles: ProfileView[];
}

// GET /api/v1/profiles/{id}/effective-tools.
export interface EffectiveTool {
  server: string;
  tool: string;
  intrinsic_tier: string;
  profile_tier: string;
  access: { visible: boolean; callable: boolean; reason: ProfileReason | string };
}

export interface EffectiveToolsResult {
  profile: string;
  tools: EffectiveTool[];
  counts: { visible: number; hidden: number; callable?: number; by_reason?: Record<string, number> };
  // Administrators only: classify entries for annotated or missing tools.
  // Administrators only: why each stale entry no longer applies ("annotated" or "missing").
}

// GET /api/v1/access/explain: availability and concrete blockers only.
export interface AccessExplanation {
 subject: { kind: AccessSubjectKind; name?: string };
 tool: string;
 available: boolean;
 issues: { code: string; message: string }[];
}

// One Clients-surface warning (GET /api/v1/clients warnings[]).
export interface ClientWarning {
  code: ClientWarningCode;
  severity: WarningSeverity;
  client_id?: string;
  message: string;
  action?: { kind: FixAction; target?: string };
  bindings?: { client_id: string; token_name: string; profile: string; mode: string }[];
  fixes?: { kind: string; target?: string }[];
}

// GET /api/v1/clients row: presence fields plus the credential and binding fields.
export interface ClientView {
  allow_code_execution: boolean;
  allow_managing: boolean;
 host_id?: string
  id: string;
  display_name: string;
  kind: 'supported' | 'other' | 'custom';
  icon?: string;
  state: string;
  connected: boolean;
  last_seen: string | null;
  active_sessions: number;
  calls_24h: number;
  sessions?: { id: string; work_session_id?: string; started_at: string; last_activity: string; profile?: string; profile_source?: string }[];
  credential_state: CredentialState;
  token_name?: string;
  profile?: string;
  profile_mode?: 'locked' | 'switchable';
  profile_source?: 'pin' | 'binding';
  profile_missing?: boolean;
  blocked_24h: number;
}
