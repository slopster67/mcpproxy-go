import { defineStore } from 'pinia'
import { computed, ref, shallowRef } from 'vue'
import { ApiError } from '@/services/api'
import { onChange, onHello } from '@/services/events'
import type { ChangeFrame } from '@/types'

export interface CollectionOptions<T> {
  /** Fetches the whole collection (GET /api/v1/{entity}). */
  list: () => Promise<T[]>
  idOf: (item: T) => string
  sort?: (a: T, b: T) => number
  /** Debounce of `invalidate` frames before the refetch (ms). */
  invalidateDelay?: number
}

type Frame = Pick<ChangeFrame, 'op' | 'id' | 'data'>

function toApiError(err: unknown): ApiError {
  if (err instanceof ApiError) return err
  return new ApiError(0, err instanceof Error ? err.message : String(err))
}

// Replaces target's fields with source's, keeping target's identity so a
// component rendering it is updated rather than remounted.
function mergeInto(target: Record<string, unknown>, source: Record<string, unknown>) {
  for (const key of Object.keys(target)) if (!(key in source)) delete target[key]
  Object.assign(target, source)
}

/**
 * A live collection of one stream entity: loads on first use, then keeps
 * itself current from `change` frames (upsert/delete in place, invalidate →
 * debounced refetch) and refetches on every `hello` once loaded.
 *
 * Ordering: a list response is a snapshot of some moment during its request,
 * so frames that arrive while a list is in flight are applied immediately and
 * also replayed on top of the list when it lands. A frame that predates the
 * snapshot is followed by the frame of any later change, so the replay
 * converges. Only the newest list request may write.
 *
 * `extend` adds entity-specific getters and actions to the same store.
 */
export function defineCollection<T extends object, E extends object = Record<never, never>>(
  entity: string,
  options: CollectionOptions<T>,
  extend?: (collection: CollectionCore<T>) => E,
) {
  return defineStore(entity, () => {
    const list = ref<T[]>([]) as { value: T[] }
    const loaded = ref(false)
    const loading = ref(false)
    const error = shallowRef<ApiError | null>(null)

    const items = computed<T[]>(() => options.sort ? [...list.value].sort(options.sort) : list.value)
    const byId = computed(() => new Map(list.value.map(item => [options.idOf(item), item])))

    let ticket = 0
    let inflight: Promise<void> | null = null
    // Frames received while a list request is in flight, replayed when it lands.
    let replay: Frame[] | null = null
    let invalidateTimer: ReturnType<typeof setTimeout> | null = null

    function indexOf(id: string): number {
      return list.value.findIndex(item => options.idOf(item) === id)
    }

    function applyOne(frame: Frame) {
      if (frame.op === 'upsert' && frame.data) {
        const next = frame.data as T
        const index = indexOf(frame.id ?? options.idOf(next))
        if (index >= 0) mergeInto(list.value[index] as Record<string, unknown>, next as Record<string, unknown>)
        else list.value.push(next)
      } else if (frame.op === 'delete' && frame.id !== undefined) {
        const index = indexOf(frame.id)
        if (index >= 0) list.value.splice(index, 1)
      }
    }

    function replaceAll(incoming: T[]) {
      const existing = byId.value
      list.value = incoming.map(item => {
        const current = existing.get(options.idOf(item))
        if (!current) return item
        mergeInto(current as Record<string, unknown>, item as Record<string, unknown>)
        return current
      })
    }

    function refetch(): Promise<void> {
      const mine = ++ticket
      replay ??= []
      loading.value = true
      const run: Promise<void> = (async () => {
        let incoming: T[] | null = null
        let failure: unknown = null
        try {
          incoming = await options.list()
        } catch (err) {
          failure = err
        }
        // Superseded: settle with the newer request, so a caller awaiting
        // this one sees its outcome.
        if (mine !== ticket) return inflight ?? undefined
        const pending = replay ?? []
        replay = null
        if (incoming) {
          replaceAll(incoming)
          for (const frame of pending) applyOne(frame)
          loaded.value = true
          error.value = null
        } else {
          error.value = toApiError(failure)
        }
        loading.value = false
        inflight = null
      })()
      inflight = run
      return run
    }

    /** Loads once; concurrent callers share the request. Retries after a failed first load. */
    function ensureLoaded(): Promise<void> {
      if (loaded.value) return Promise.resolve()
      return inflight ?? refetch()
    }

    const active = () => loaded.value || inflight !== null

    // Coalesces invalidations: the first starts the delay, later ones join it.
    function scheduleRefetch() {
      invalidateTimer ??= setTimeout(() => {
        invalidateTimer = null
        if (active()) void refetch()
      }, options.invalidateDelay ?? 250)
    }

    function apply(frame: Frame) {
      if (frame.op === 'invalidate') {
        if (active()) scheduleRefetch()
        return
      }
      if (!active()) return
      replay?.push(frame)
      applyOne(frame)
    }

    /** Applies an object a write answered with, ordered like a frame. */
    function upsertLocal(item: T) { apply({ op: 'upsert', id: options.idOf(item), data: item }) }
    function removeLocal(id: string) { apply({ op: 'delete', id }) }

    onChange(entity, apply)
    onHello(initial => { if (!initial && active()) void refetch() })

    const core = { items, byId, loaded, loading, error, ensureLoaded, refetch, upsertLocal, removeLocal }
    return { ...core, ...(extend ? extend(core as unknown as CollectionCore<T>) : ({} as E)) }
  })
}

/** The collection as `extend` sees it (refs unwrapped by the caller via .value). */
export interface CollectionCore<T> {
  items: { readonly value: T[] }
  byId: { readonly value: Map<string, T> }
  loaded: { readonly value: boolean }
  loading: { readonly value: boolean }
  error: { readonly value: ApiError | null }
  ensureLoaded: () => Promise<void>
  refetch: () => Promise<void>
  upsertLocal: (item: T) => void
  removeLocal: (id: string) => void
}
