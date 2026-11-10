import { computed, reactive, type ComputedRef } from 'vue';
import { useRoute, useRouter, type RouteLocationRaw } from 'vue-router';

/**
 * The URL filter contract shared by every scope-aware page. A page reads and
 * writes its filters through this composable, so the same parameter name
 * always means the same thing and maps to the same REST query everywhere.
 * Each parameter is registered once with its pages and an optional `toRest`
 * hook for page-specific REST rules (tool splitting, the session `ws-` prefix,
 * the Usage `window` mapping).
 */

// ---------------------------------------------------------------------------
// Types
// ---------------------------------------------------------------------------

/** Every page whose filters flow through the contract. */
export type PageId =
  | 'activity'
  | 'usage'
  | 'tools'
  | 'servers'
  | 'clients'
  | 'server-detail'
  | 'profiles'
  | 'profile-editor'

/** Context passed to a ParamDef's custom toRest hook. */
interface ScopeRestContext {
  page: PageId
  /** The full raw URL state for this page (every registered param, as strings). */
  state: Readonly<Record<string, string | undefined>>
  /** Resolves a relative or absolute time string to an absolute RFC3339 one. */
  resolveTime: (value: string) => string
  /** Called by a hook when two active parameters can never both be satisfied
   * by one REST request; toRest() then returns null and no request is issued. */
  signalConflict: () => void
}

interface ScopeParamDef {
  /** URL parameter name. */
  name: string
  /**
   * REST query parameter name when it differs from `name`. Ignored when a
   * custom `toRest` hook is supplied (the hook owns the REST shape entirely).
   */
  rest?: string
  /** Carried across scope-aware pages by in-app links built with `linkTo`. */
  sticky?: boolean
  /** Pages that read/write this parameter from the URL at all. */
  pages: PageId[]
  /**
   * Pages on which this parameter is actually sent to REST (as opposed to
   * applied client-side only, or not applicable). Defaults to `pages`. An
   * empty array means "never sent to REST anywhere" (e.g. `q` on Tools).
   */
  restPages?: PageId[]
  /**
   * Custom REST mapping for this one parameter. Returning `undefined` or `{}`
   * omits the parameter from `toRest()`.
   */
  toRest?: (value: string, ctx: ScopeRestContext) => Record<string, string> | undefined
  /** Human label for the chip (defaults to `name`). */
  label?: string
}

interface ScopeChip {
  name: string
  label: string
  value: string
  remove: () => void
}

// ---------------------------------------------------------------------------
// Registry (module-level: one registration set for the whole app)
// ---------------------------------------------------------------------------

const registry = new Map<string, ScopeParamDef>()

function registerScopeParam(def: ScopeParamDef): void {
  registry.set(def.name, def)
}

function defsForPage(page: PageId): ScopeParamDef[] {
  return Array.from(registry.values()).filter(d => d.pages.includes(page))
}

// ---------------------------------------------------------------------------
// Relative time resolution (from/to)
// ---------------------------------------------------------------------------

const RELATIVE_TIME_RE = /^-([0-9]+)(m|h|d)$/

/**
 * Resolves a from/to value: an absolute RFC3339 timestamp passes through
 * unchanged, a relative shorthand ("-24h", "-7d", "-30m") resolves against
 * now. Anything else passes through unchanged.
 */
function resolveScopeTime(value: string, now: Date = new Date()): string {
  if (!value) return value
  const m = RELATIVE_TIME_RE.exec(value)
  if (!m) return value
  const amount = Number(m[1])
  const unit = m[2]
  const ms = unit === 'm' ? amount * 60_000 : unit === 'h' ? amount * 3_600_000 : amount * 86_400_000
  return new Date(now.getTime() - ms).toISOString().replace(/\.\d{3}Z$/, 'Z')
}

/** Maps `from`/`to` to a Usage `window` value. GET /usage accepts only
 * `window=24h|7d|all` and defaults to 24h when it is omitted, so a range that
 * is not one of the presets maps to `all` rather than being dropped. */
export function usageWindowFor(from: string | undefined, to: string | undefined): string {
  if (to) return 'all' // an explicit end time is never one of the presets
  if (!from) return 'all'
  if (from === '-24h') return '24h'
  if (from === '-7d') return '7d'
  return 'all'
}

/** Splits a `tool` URL value: "server:tool" becomes {server, tool}; a bare
 * tool has no server. An explicit `server` that disagrees with the tool's
 * server prefix cannot be expressed by one REST request (it has one `server`
 * parameter), so `conflict: true` is returned instead. */
export function splitScopeTool(
  tool: string | undefined,
  explicitServer: string | undefined,
): { server?: string; tool?: string; conflict?: boolean } {
  if (!tool) return { server: explicitServer || undefined }
  const idx = tool.indexOf(':')
  if (idx === -1) return { server: explicitServer || undefined, tool }
  const toolServer = tool.slice(0, idx)
  const toolName = tool.slice(idx + 1)
  if (explicitServer && explicitServer !== toolServer) {
    return { conflict: true }
  }
  return { server: explicitServer || toolServer, tool: toolName }
}

/** Routes a `session` URL value to the REST filter name: a `ws-` prefix is a
 * work session id, anything else a raw MCP transport session id. */
function sessionRestParam(session: string): 'work_session_id' | 'session_id' {
  return session.startsWith('ws-') ? 'work_session_id' : 'session_id'
}

/** The "Other / internal" bucket on Activity `status`: filtered client-side,
 * never sent to REST. */
const OTHER_STATUS = 'other'

// ---------------------------------------------------------------------------
// Default parameter table
// ---------------------------------------------------------------------------

function registerDefaultScopeParams(): void {
  registerScopeParam({
    name: 'server',
    pages: ['tools', 'activity', 'usage'],
    restPages: ['activity', 'usage'], // client-side on Tools
  })
  registerScopeParam({
    name: 'tool',
    pages: ['activity', 'usage'],
    toRest: (value, ctx) => {
      const { server, tool, conflict } = splitScopeTool(value, ctx.state.server)
      if (conflict) {
        ctx.signalConflict()
        return undefined
      }
      const out: Record<string, string> = {}
      if (server) out.server = server
      if (tool) out.tool = tool
      return out
    },
  })
  registerScopeParam({
    name: 'session',
    sticky: false,
    pages: ['activity'],
    toRest: value => ({ [sessionRestParam(value)]: value }),
  })
  registerScopeParam({
    name: 'status',
    pages: ['activity', 'usage', 'tools'],
    restPages: ['activity', 'usage'], // client-side on Tools
    toRest: (value, ctx) => {
      if (ctx.page === 'activity' && value === OTHER_STATUS) return undefined
      return { status: value }
    },
  })
  registerScopeParam({
    name: 'from',
    sticky: true,
    pages: ['activity', 'usage'],
    // On Usage, `from`/`to` map to `window`, computed from both values
    // together in toRest() below.
    restPages: ['activity'],
    toRest: (value, ctx) => ({ start_time: ctx.resolveTime(value) }),
  })
  registerScopeParam({
    name: 'to',
    sticky: true,
    pages: ['activity', 'usage'],
    restPages: ['activity'],
    toRest: (value, ctx) => ({ end_time: ctx.resolveTime(value) }),
  })
  registerScopeParam({ name: 'view', pages: ['activity'], restPages: [] })
  registerScopeParam({ name: 'type', pages: ['activity'] })
  registerScopeParam({ name: 'tab', pages: ['server-detail'], restPages: [] })
  registerScopeParam({ name: 'q', pages: ['tools', 'servers'], restPages: [] })
  registerScopeParam({ name: 'tier', pages: ['tools'], restPages: [], label: 'Tier' })
  registerScopeParam({ name: 'add', pages: ['servers'], restPages: [], sticky: false })
  registerScopeParam({ name: 'focus', pages: ['server-detail', 'clients', 'profile-editor'], restPages: [], sticky: false })

  registerScopeParam({
    name: 'profile',
    sticky: true,
    pages: ['activity', 'usage', 'tools', 'servers', 'clients'],
    restPages: ['activity', 'usage', 'tools', 'servers', 'clients'],
  })
  registerScopeParam({
    name: 'client',
    sticky: true,
    pages: ['activity', 'usage', 'tools', 'clients'],
    restPages: ['activity', 'usage', 'tools', 'clients'],
  })

}

registerDefaultScopeParams()

/** Whether a page registers a parameter (and so applies it). */
export function scopeParamAppliesToPage(name: string, page: PageId | undefined): boolean {
  const def = registry.get(name)
  return Boolean(def && page && def.pages.includes(page))
}

// ---------------------------------------------------------------------------
// useScopeQuery
// ---------------------------------------------------------------------------

export interface UseScopeQueryResult {
  state: Record<string, string | undefined>
  set: (patch: Record<string, string | undefined>) => void
  clear: (names?: string[]) => void
  /** Null when the active parameters are contradictory (an explicit `server`
   * that disagrees with `tool`'s server prefix): the caller must issue no
   * request and render the conflict empty state instead. */
  toRest: () => Record<string, string> | null
  linkTo: (page: PageId, patch?: Record<string, string>) => RouteLocationRaw
  chips: ComputedRef<ScopeChip[]>
}

export function useScopeQuery(page: PageId): UseScopeQueryResult {
  const route = useRoute()
  const router = useRouter()

  function rawQueryValue(name: string): string | undefined {
    const v = route.query[name]
    if (Array.isArray(v)) return v[0] ?? undefined
    return v ?? undefined
  }

  // `state` is always in sync with route.query, so a caller can read it (or
  // call toRest()) synchronously during setup, before the first fetch.
  const state = reactive<Record<string, string | undefined>>({})
  for (const def of defsForPage(page)) {
    Object.defineProperty(state, def.name, {
      enumerable: true,
      get: () => rawQueryValue(def.name),
    })
  }

  function set(patch: Record<string, string | undefined>): void {
    // Unknown parameters, including valueless ones ("?foo", parsed as null),
    // are preserved untouched.
    const query: Record<string, string | string[] | null> = {}
    for (const [k, v] of Object.entries(route.query)) {
      if (v === undefined) continue
      query[k] = v as string | string[] | null
    }
    for (const [k, v] of Object.entries(patch)) {
      if (v === undefined || v === '') delete query[k]
      else query[k] = v
    }
    router.replace({ query })
  }

  function clear(names?: string[]): void {
    const target = names ?? defsForPage(page).map(d => d.name)
    const patch: Record<string, string | undefined> = {}
    for (const n of target) patch[n] = undefined
    set(patch)
  }

  function toRest(): Record<string, string> | null {
    const out: Record<string, string> = {}
    const snapshot: Record<string, string | undefined> = {}
    for (const def of defsForPage(page)) snapshot[def.name] = state[def.name]

    let conflicted = false
    const ctx: ScopeRestContext = {
      page,
      state: snapshot,
      resolveTime: v => resolveScopeTime(v),
      signalConflict: () => {
        conflicted = true
      },
    }

    for (const def of defsForPage(page)) {
      const value = snapshot[def.name]
      if (value === undefined || value === '') continue

      const restPages = def.restPages ?? def.pages
      if (!restPages.includes(page)) continue // client-side only / not applicable here

      if (def.toRest) {
        const mapped = def.toRest(value, ctx)
        if (mapped) Object.assign(out, mapped)
        continue
      }
      out[def.rest ?? def.name] = value
    }

    // No REST query can express both conflicting values, so no request is
    // issued at all, never a partial one.
    if (conflicted) return null

    // Usage `window` is computed from both `from` and `to`, including when
    // neither is present (window=all).
    if (page === 'usage') {
      const w = usageWindowFor(snapshot.from, snapshot.to)
      if (w) out.window = w
    }

    return out
  }

  function linkTo(target: PageId, patch: Record<string, string> = {}): RouteLocationRaw {
    const query: Record<string, string> = {}
    for (const def of registry.values()) {
      if (!def.sticky) continue
      if (!def.pages.includes(target)) continue
      const v = rawQueryValue(def.name)
      if (v) query[def.name] = v
    }
    Object.assign(query, patch)
    // An empty patch value clears a sticky param, so the caller can name the
    // subject for the target page without inheriting a competing one.
    for (const key of Object.keys(query)) if (query[key] === '') delete query[key]
    return { name: target, query }
  }

  const chips = computed<ScopeChip[]>(() => {
    const out: ScopeChip[] = []
    for (const def of defsForPage(page)) {
      const value = state[def.name]
      if (value === undefined || value === '') continue
      out.push({
        name: def.name,
        label: def.label ?? def.name,
        value,
        remove: () => clear([def.name]),
      })
    }
    return out
  })

  return { state, set, clear, toRest, linkTo, chips }
}
