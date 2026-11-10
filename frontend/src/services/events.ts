import { getCurrentScope, onScopeDispose } from 'vue'
import type { ChangeFrame } from '@/types'

// In-process fan-out of the GET /api/v1/events stream. The connection itself
// lives in stores/system.ts; it hands every parsed frame to dispatchChange and
// every (re)connect to dispatchHello. Collections and the pages that show
// non-collection data (activity, usage, tools, review) subscribe here.

type ChangeHandler = (frame: ChangeFrame) => void
// `initial` marks the first hello of a connection that reads were waiting
// for (whenStreamReady): nothing was loaded before it, so nothing refetches.
type HelloHandler = (initial: boolean) => void

// Reads wait for the stream before their first request, so they are covered
// by its subscription and the first hello needs no refetch. The wait is
// bounded: a stream that does not answer in READY_TIMEOUT_MS lets reads go,
// and its eventual hello then refetches as a reconnect would.
const READY_TIMEOUT_MS = 3000
let waiting = false
let ready = false
let readyTimer: ReturnType<typeof setTimeout> | null = null
let waiters: Array<() => void> = []

function markReady() {
  if (readyTimer) clearTimeout(readyTimer)
  readyTimer = null
  waiting = false
  ready = true
  const run = waiters
  waiters = []
  for (const resolve of run) resolve()
}

/** Resolves once the stream is connected (or gave up waiting); immediately when no stream is starting. */
export function whenStreamReady(): Promise<void> {
  if (!waiting) return Promise.resolve()
  return new Promise(resolve => { waiters.push(resolve) })
}

/** The stream is connecting for the first time since sign-in: hold reads until its hello. */
export function streamStarting(): void {
  if (ready || waiting) return
  waiting = true
  readyTimer = setTimeout(markReady, READY_TIMEOUT_MS)
}

/** The stream was closed for good (sign-out): the next connect waits again. */
export function streamStopped(): void {
  if (waiting) markReady()
  ready = false
}

const changeHandlers = new Map<string, Set<ChangeHandler>>()
const helloHandlers = new Set<HelloHandler>()

function disposeWithScope(off: () => void): () => void {
  if (getCurrentScope()) onScopeDispose(off)
  return off
}

/** Subscribes to `change` frames of one entity; unsubscribes with the current effect scope. */
export function onChange(entity: string, handler: ChangeHandler): () => void {
  let set = changeHandlers.get(entity)
  if (!set) changeHandlers.set(entity, (set = new Set()))
  set.add(handler)
  return disposeWithScope(() => { set.delete(handler) })
}

/** Subscribes to `hello` (every connect and reconnect). */
export function onHello(handler: HelloHandler): () => void {
  helloHandlers.add(handler)
  return disposeWithScope(() => { helloHandlers.delete(handler) })
}

function safely(run: () => void) {
  try { run() } catch (error) { console.error('Change handler failed:', error) }
}

export function dispatchChange(frame: ChangeFrame): void {
  for (const handler of [...(changeHandlers.get(frame.entity) ?? [])]) safely(() => handler(frame))
}

export function dispatchHello(): void {
  const initial = waiting
  if (initial) markReady()
  for (const handler of [...helloHandlers]) safely(() => handler(initial))
}

/**
 * Coalesced reaction to any change of the given entities and to every hello:
 * the pattern of every page that shows data the stream only invalidates. The
 * first frame starts the delay and later ones join it, so a steady stream
 * still runs once per delay. `filter` narrows which frames count.
 */
export function onLive(entities: string[], run: () => void, options: { delay?: number; filter?: (frame: ChangeFrame) => boolean } = {}): () => void {
  const delay = options.delay ?? 250
  let timer: ReturnType<typeof setTimeout> | null = null
  const schedule = () => {
    timer ??= setTimeout(() => { timer = null; run() }, delay)
  }
  const offs = entities.map(entity => onChange(entity, frame => {
    if (!options.filter || options.filter(frame)) schedule()
  }))
  offs.push(onHello(initial => { if (!initial) schedule() }))
  return disposeWithScope(() => {
    if (timer) clearTimeout(timer)
    for (const off of offs) off()
  })
}
