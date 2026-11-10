import type { OAuthDiscovered } from '@/types/oauth'
import { whenStreamReady } from '@/services/events'
import type {
  Server, SearchResult, SecretEntry, GetSessionsResponse, MCPSession, ActivityListResponse, ActivityRecord,
  GlobalTool, GlobalToolsResponse, UsageAggregateResponse, UsageWindow, UsageSort, UsageStatus,
  ServerLogEntry, Peer, Tier,
} from '@/types'
import type {
  ProfileConfig, ProfileList, ProfileView, ProfileDeleteResult, EffectiveToolsResult, AccessExplanation,
  ExplainSubjectQuery, ClientPresence, ClientsResponse, ClientCredential,
} from '@/types/api'

// A failed request: the HTTP status (0 for a network failure), the backend's
// `error` text and the whole error body (guard refusals carry `field`,
// `used_by`, `bindings`, …).
export class ApiError extends Error {
  readonly status: number
  readonly body: Record<string, unknown>
  constructor(status: number, message: string, body: Record<string, unknown> = {}) {
    super(message)
    this.name = 'ApiError'
    this.status = status
    this.body = body
  }
  get code(): string | undefined { return typeof this.body.code === 'string' ? this.body.code : undefined }
  get field(): string | undefined { return typeof this.body.field === 'string' ? this.body.field : undefined }
}

type Query = Record<string, string | number | boolean | undefined | null>

function qs(params: Query = {}): string {
  const q = new URLSearchParams()
  for (const [k, v] of Object.entries(params)) {
    if (v === undefined || v === null || v === '') continue
    q.set(k, String(v))
  }
  const text = q.toString()
  return text ? `?${text}` : ''
}

const seg = encodeURIComponent

let csrfToken = ''
let onUnauthorized: (() => void) | null = null

export function setCSRFToken(token: string): void { csrfToken = token }
// Called on every 401 (an expired dashboard session). A 403 is a refusal the
// caller shows inline.
export function setUnauthorizedHandler(handler: (() => void) | null): void { onUnauthorized = handler }

// The one request helper: answers the envelope's `data`, throws an ApiError
// for every failure.
async function request<T>(path: string, init: RequestInit & { json?: unknown } = {}): Promise<T> {
  const { json, ...options } = init
  const headers: Record<string, string> = { 'X-MCPProxy-Client': 'webui/web' }
  if (json !== undefined) headers['Content-Type'] = 'application/json'
  if (csrfToken) headers['X-CSRF-Token'] = csrfToken
  let response: Response
  try {
    response = await fetch(path, {
      ...options,
      body: json !== undefined ? JSON.stringify(json) : options.body,
      credentials: 'same-origin',
      headers,
    })
  } catch (err) {
    if (err instanceof DOMException && err.name === 'AbortError') throw err
    throw new ApiError(0, err instanceof Error ? err.message : 'Network error')
  }
  const body = await response.json().catch(() => ({})) as { data?: T; error?: unknown } & Record<string, unknown>
  if (!response.ok) {
    if (response.status === 401) onUnauthorized?.()
    const message = typeof body.error === 'string' && body.error ? body.error : `HTTP ${response.status}: ${response.statusText}`
    throw new ApiError(response.status, message, body)
  }
  return body.data as T
}

// Reads wait for the change stream (see whenStreamReady) so the data they
// return is covered by its subscription.
const get = async <T>(path: string, signal?: AbortSignal) => {
  await whenStreamReady()
  return request<T>(path, { signal })
}
const post = <T>(path: string, json: unknown = {}) => request<T>(path, { method: 'POST', json })
const patch = <T>(path: string, json: unknown) => request<T>(path, { method: 'PATCH', json })
const del = <T = { deleted: string }>(path: string) => request<T>(path, { method: 'DELETE' })

// The URL id of /api/v1/tools/{id}: "server:tool" (split at the first colon).
export function toolId(server: string, tool: string): string {
  return `${server}:${tool}`
}

export interface OAuthStartResponse {
  success: boolean
  server_name: string
  correlation_id?: string
  auth_url?: string
  browser_opened: boolean
  browser_error?: string
  message?: string
}

export interface ApproveToolsResult { approved: number; not_approved?: string[] }
export interface BlockToolsResult { blocked: number }

type ScopeQuery = { profile?: string; client?: string; token?: string }

export interface ActivityQuery extends ScopeQuery {
  type?: string
  server?: string
  tool?: string
  session_id?: string
  work_session_id?: string
  status?: string
  parent_id?: string
  request_id?: string
  start_time?: string
  end_time?: string
  limit?: number
  offset?: number
}

export interface UsageQuery extends ScopeQuery {
  window?: UsageWindow
  server?: string
  tool?: string
  status?: UsageStatus
  top?: number
  sort?: UsageSort
}

const api = {
  servers: {
    list: (params: { profile?: string } = {}) => get<{ servers: Server[] }>(`/api/v1/servers${qs(params)}`),
    get: (id: string) => get<Server>(`/api/v1/servers/${seg(id)}`),
    create: (body: Record<string, unknown>) => post<Server>('/api/v1/servers', body),
    patch: (id: string, body: Record<string, unknown>) => patch<Server>(`/api/v1/servers/${seg(id)}`, body),
    delete: (id: string) => del(`/api/v1/servers/${seg(id)}`),
    restart: (id: string) => post<Server>(`/api/v1/servers/${seg(id)}/restart`),
    login: (id: string) => post<OAuthStartResponse>(`/api/v1/servers/${seg(id)}/login`),
    logout: (id: string) => post<Server>(`/api/v1/servers/${seg(id)}/logout`),
    configToSecret: (id: string, body: { scope: 'header' | 'env'; key: string; secret_name: string }) =>
      post<{ message: string; reference: string }>(`/api/v1/servers/${seg(id)}/config-to-secret`, body),
    logs: (id: string, params: { tail?: number } = {}, signal?: AbortSignal) =>
      get<{ logs: ServerLogEntry[] }>(`/api/v1/servers/${seg(id)}/logs${qs(params)}`, signal),
    oauthDiscovered: (id: string) => get<OAuthDiscovered>(`/api/v1/servers/${seg(id)}/oauth/discovered`),
    approveTools: (id: string, body: { tools?: string[]; approve_all?: boolean; expected_hashes?: Record<string, string>; expected_incarnation?: number }) =>
      post<ApproveToolsResult>(`/api/v1/servers/${seg(id)}/tools/approve`, body),
    blockTools: (id: string, body: { tools?: string[]; block_all?: boolean; expected_incarnation?: number }) =>
      post<BlockToolsResult>(`/api/v1/servers/${seg(id)}/tools/block`, body),
  },
  tools: {
    list: (params: { client?: string; profile?: string; server?: string } = {}) => get<GlobalToolsResponse>(`/api/v1/tools${qs(params)}`),
    get: (id: string) => get<GlobalTool>(`/api/v1/tools/${seg(id)}`),
    patch: (id: string, body: { enabled?: boolean; classification?: string; expected_hash?: string; expected_incarnation?: number }) =>
      patch<GlobalTool & { tier: Tier }>(`/api/v1/tools/${seg(id)}`, body),
  },
  index: {
    search: (q: string, limit = 10) => get<{ results: SearchResult[] }>(`/api/v1/index/search${qs({ q, limit })}`),
  },
  clients: {
    list: (params: { profile?: string; client?: string } = {}) => get<ClientsResponse>(`/api/v1/clients${qs(params)}`),
    get: (id: string) => get<ClientPresence>(`/api/v1/clients/${seg(id)}`),
    create: (body: { display_name: string; host_id?: string; profile?: string; mode?: 'locked' | 'switchable'; allow_managing?: boolean; allow_code_execution?: boolean }) =>
      post<ClientPresence & ClientCredential>('/api/v1/clients', body),
    patch: (id: string, body: { display_name?: string; profile?: string; mode?: 'locked' | 'switchable'; host_id?: string; allow_managing?: boolean; allow_code_execution?: boolean }) =>
      patch<ClientPresence>(`/api/v1/clients/${seg(id)}`, body),
    rotate: (id: string) => post<ClientPresence & ClientCredential>(`/api/v1/clients/${seg(id)}/rotate`),
    delete: (id: string) => del(`/api/v1/clients/${seg(id)}`),
  },
  peers: {
    list: () => get<{ peers: Peer[] }>('/api/v1/peers'),
    get: (id: string) => get<Peer>(`/api/v1/peers/${seg(id)}`),
    create: (body: { name: string }) => post<Peer & { token: string }>('/api/v1/peers', body),
    rotate: (id: string) => post<Peer & { token: string }>(`/api/v1/peers/${seg(id)}/rotate`),
    delete: (id: string) => del(`/api/v1/peers/${seg(id)}`),
  },
  profiles: {
    list: () => get<ProfileList>('/api/v1/profiles'),
    get: (id: string) => get<ProfileView>(`/api/v1/profiles/${seg(id)}`),
    create: (body: Omit<ProfileConfig, 'id'>) => post<ProfileView>('/api/v1/profiles', body),
    patch: (id: string, body: Partial<ProfileConfig>) => patch<ProfileView>(`/api/v1/profiles/${seg(id)}`, body),
    delete: (id: string, params: { reassign_to?: string; force?: boolean } = {}) =>
      del<ProfileDeleteResult>(`/api/v1/profiles/${seg(id)}${qs({ reassign_to: params.reassign_to, force: params.force || undefined })}`),
    effectiveTools: (id: string, params: { client?: string; server?: string; reason?: string } = {}) =>
      get<EffectiveToolsResult>(`/api/v1/profiles/${seg(id)}/effective-tools${qs(params)}`),
  },
  access: {
    explain: (params: ExplainSubjectQuery) => get<AccessExplanation>(`/api/v1/access/explain${qs({ ...params })}`),
  },
  secrets: {
    list: () => get<{ secrets: SecretEntry[] }>('/api/v1/secrets'),
    create: (body: { name: string; value: string; type?: string }) => post<SecretEntry>('/api/v1/secrets', body),
    patch: (name: string, body: { value: string }, type = 'secret') => patch<SecretEntry>(`/api/v1/secrets/${seg(name)}${qs({ type })}`, body),
    delete: (name: string, type = 'secret') => del(`/api/v1/secrets/${seg(name)}${qs({ type })}`),
  },
  sessions: {
    list: (params: ScopeQuery & { limit?: number; offset?: number; status?: 'active' | 'closed' } = {}) =>
      get<GetSessionsResponse>(`/api/v1/sessions${qs(params)}`),
    get: (id: string) => get<MCPSession>(`/api/v1/sessions/${seg(id)}`),
  },
  activity: {
    list: (params: ActivityQuery = {}) => get<ActivityListResponse>(`/api/v1/activity${qs({ ...params })}`),
    get: (id: string) => get<ActivityRecord>(`/api/v1/activity/${seg(id)}`),
  },
  usage: {
    get: (params: UsageQuery = {}) => get<UsageAggregateResponse>(`/api/v1/usage${qs({ ...params })}`),
  },
  events: {
    url: '/api/v1/events',
  },
}

export default api
