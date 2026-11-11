<template>
  <div class="flex min-w-0 flex-col gap-4">
    <div class="flex flex-wrap items-center gap-3">
      <Input v-model="search" type="search" placeholder="Search logs…" aria-label="Search logs" class="w-full sm:w-56" />
      <NativeSelect v-model="tail" aria-label="Number of log entries" class="w-auto">
        <option :value="100">Last 100</option>
        <option :value="200">Last 200</option>
        <option :value="500">Last 500</option>
      </NativeSelect>
      <span class="ml-auto flex items-center gap-2 text-sm" role="status"><span class="size-2 rounded-full bg-[var(--server-status-green)]" aria-hidden="true" />Live</span>
    </div>
    <Alert v-if="error" variant="destructive">{{ error }}</Alert>
    <Card class="relative h-[min(60dvh,40rem)] min-h-64 gap-0 overflow-hidden bg-black py-0">
      <div v-if="loading" v-show="showSkeleton" class="flex h-full flex-col gap-4 p-4" role="status" aria-label="Loading logs">
        <div v-for="row in 8" :key="row" class="flex gap-3"><Skeleton class="h-3 w-20" /><Skeleton class="h-3 w-10" /><Skeleton class="h-3 flex-1" /></div>
      </div>
      <div v-else ref="viewport" class="h-full overflow-auto overscroll-contain p-2" role="log" aria-label="Server log entries" tabindex="0" @scroll="trackScroll">
        <div v-if="!visibleRows.length" class="flex h-full items-center justify-center text-sm text-muted-foreground">{{ entries.length ? 'No matching logs' : 'No log entries' }}</div>
        <div v-for="row in visibleRows" :key="row.key" class="rounded-sm hover:bg-muted/30" :class="row.hasFields && 'cursor-pointer'" @pointerdown="startRowClick" @click="row.hasFields && clickRow($event, row.key)">
          <div class="grid grid-cols-[0.875rem_5.5rem_3rem_minmax(0,1fr)] items-start gap-x-2 px-1 py-1.5 font-mono text-[11px] leading-5 sm:gap-x-3 sm:text-xs">
            <button v-if="row.hasFields" type="button" class="mt-0.5 cursor-pointer text-muted-foreground hover:text-foreground" :aria-expanded="expanded.has(row.key)" :aria-label="`Details: ${row.entry.message}`" @click.stop="toggle(row.key)"><ChevronRight class="size-3.5 transition-transform" :class="expanded.has(row.key) && 'rotate-90'" /></button>
            <span v-else />
            <time :datetime="row.entry.timestamp" :title="row.entry.timestamp" class="cursor-text tabular-nums text-muted-foreground">{{ row.time }}</time>
            <span class="cursor-text" :class="levelColor(row.entry.level)">{{ row.entry.level }}</span>
            <span class="min-w-0 cursor-text whitespace-pre-wrap break-words [overflow-wrap:anywhere]">{{ row.text }}<template v-if="row.error">: {{ row.error }}</template></span>
          </div>
          <JsonCode v-if="expanded.has(row.key)" :data="row.details" class="mx-5 mb-2 cursor-text border border-border/50" />
        </div>
      </div>
      <Button v-if="!atTop && entries.length" size="sm" variant="outline" class="absolute right-4 bottom-4 shadow-md" @click="jumpToLatest"><ArrowUp data-icon="inline-start" />Jump to latest</Button>
    </Card>
  </div>
</template>

<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { ArrowUp, ChevronRight } from '@lucide/vue'
import { Alert } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import { Card } from '@/components/ui/card'
import { Input } from '@/components/ui/input'
import { NativeSelect } from '@/components/ui/native-select'
import { Skeleton } from '@/components/ui/skeleton'
import JsonCode from '@/components/JsonCode.vue'
import { useDelayedLoading } from '@/composables/useDelayedLoading'
import api from '@/services/api'
import type { ServerLogEntry } from '@/types/api'

const props = defineProps<{ name: string }>()
const entries = ref<ServerLogEntry[]>([])
const loading = ref(true), error = ref('')
const search = ref(''), tail = ref(200)
const viewport = ref<HTMLElement | null>(null), atTop = ref(true)
const expanded = ref(new Set<string>())
const showSkeleton = useDelayedLoading(() => loading.value)
let timer: ReturnType<typeof setTimeout> | undefined
let request: AbortController | undefined
let disposed = false
let signature = ''
let pending: ServerLogEntry[] | undefined
let lastRowClick: { key: string; wasExpanded: boolean } | undefined
let pointerStart = { x: 0, y: 0, selecting: false }
const timeFormat = new Intl.DateTimeFormat(undefined, { hour: '2-digit', minute: '2-digit', second: '2-digit', fractionalSecondDigits: 3, hour12: false })

const rows = computed(() => {
  const occurrences = new Map<string, number>()
  return entries.value.map(entry => {
    const record = JSON.stringify(entry)
    const occurrence = occurrences.get(record) ?? 0
    occurrences.set(record, occurrence + 1)
    const error = typeof entry.fields?.error === 'string' && !entry.message.includes(entry.fields.error) ? entry.fields.error : ''
    // A line a child process printed (stderr, launcher output) carries its text
    // in a field: show it inline after the label instead of behind a click.
    const childLine = entry.fields?.child_output === true && typeof entry.fields.message === 'string' ? entry.fields.message : ''
    const text = childLine ? `${entry.message}: ${childLine}` : entry.message
    const details = Object.fromEntries(Object.entries(entry.fields ?? {}).filter(([key]) =>
      (key !== 'error' || !error) && !(childLine && (key === 'message' || key === 'child_output'))))
    return { entry, error, text, details, key: `${record}:${occurrence}`, hasFields: Object.keys(details).length > 0, time: timeFormat.format(new Date(entry.timestamp)) }
  }).reverse().sort((a, b) => Date.parse(b.entry.timestamp) - Date.parse(a.entry.timestamp))
})
const visibleRows = computed(() => {
  const query = search.value.toLowerCase()
  return rows.value.filter(row => !query || row.key.toLowerCase().includes(query))
})
function levelColor(value: string) {
  if (['ERROR', 'FATAL', 'PANIC', 'DPANIC'].includes(value)) return 'text-destructive'
  if (value === 'WARN') return 'text-warning'
  return value === 'DEBUG' ? 'text-muted-foreground' : 'text-foreground'
}
function toggle(key: string) {
  lastRowClick = undefined
  const next = new Set(expanded.value)
  if (next.has(key)) next.delete(key)
  else next.add(key)
  expanded.value = next
  void nextTick(trackScroll)
}
function startRowClick(event: PointerEvent) {
  pointerStart = { x: event.clientX, y: event.clientY, selecting: window.getSelection()?.isCollapsed === false }
}
function clickRow(event: MouseEvent, key: string) {
  if ((event.target as Element).closest('button, a, input, pre') || event.defaultPrevented || event.button !== 0 || event.ctrlKey || event.metaKey || event.shiftKey || event.altKey) return
  if (event.detail > 1) {
    // A word-selection double click restores the state before its first click.
    if (lastRowClick?.key === key && expanded.value.has(key) !== lastRowClick.wasExpanded) toggle(key)
    lastRowClick = undefined
    return
  }
  if (pointerStart.selecting || window.getSelection()?.isCollapsed === false) return
  if (Math.hypot(event.clientX - pointerStart.x, event.clientY - pointerStart.y) > 4) return
  const wasExpanded = expanded.value.has(key)
  toggle(key)
  lastRowClick = { key, wasExpanded }
}
function trackScroll() {
  const el = viewport.value
  if (el) {
    const wasAtTop = atTop.value
    atTop.value = el.scrollTop < 40
    if (!wasAtTop && atTop.value && pending) void jumpToLatest()
  }
}
function applyEntries(next: ServerLogEntry[]) {
  entries.value = next
  signature = JSON.stringify(next)
  expanded.value = new Set([...expanded.value].filter(key => rows.value.some(row => row.key === key)))
}
async function jumpToLatest() {
  if (pending) { applyEntries(pending); pending = undefined }
  await nextTick()
  if (viewport.value) viewport.value.scrollTop = 0
  atTop.value = true
}
async function load() {
  clearTimeout(timer)
  request?.abort()
  const current = new AbortController()
  request = current
  try {
    const { logs: next } = await api.servers.logs(props.name, { tail: Number(tail.value) }, current.signal)
    if (current.signal.aborted || disposed) return
    const serialized = JSON.stringify(next)
    if (signature !== serialized) {
      if (atTop.value || loading.value) {
        applyEntries(next)
        pending = undefined
        loading.value = false
        await jumpToLatest()
      } else {
        // Keep the rows being read stable; continue receiving the latest tail.
        pending = next
      }
    } else {
      pending = undefined
    }
    error.value = ''
  } catch (err) {
    if (!current.signal.aborted && !disposed) error.value = err instanceof Error ? err.message : 'Could not load logs'
  } finally {
    if (request === current && !disposed) {
      loading.value = false
      if (!document.hidden) timer = setTimeout(() => void load(), 1500)
    }
  }
}
function visibilityChanged() {
  clearTimeout(timer)
  if (document.hidden) request?.abort()
  else void load()
}
watch([() => props.name, tail], ([name], previous) => {
  atTop.value = true
  if (!previous || previous[0] !== name) {
    entries.value = []
    signature = ''
    pending = undefined
    expanded.value = new Set()
    loading.value = true
    atTop.value = true
  }
  void load()
}, { immediate: true })
onMounted(() => document.addEventListener('visibilitychange', visibilityChanged))
onBeforeUnmount(() => {
  disposed = true
  clearTimeout(timer)
  request?.abort()
  document.removeEventListener('visibilitychange', visibilityChanged)
})
</script>
