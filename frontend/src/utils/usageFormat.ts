// Shared formatting helpers and colour palette for the Usage graphs.

/** Compact number: 1234 -> "1.2K", 2_500_000 -> "2.5M". */
export function formatNumber(num: number): string {
  if (!Number.isFinite(num)) return '0'
  if (Math.abs(num) >= 1_000_000) return `${(num / 1_000_000).toFixed(1)}M`
  if (Math.abs(num) >= 1_000) return `${(num / 1_000).toFixed(1)}K`
  return String(num)
}

/** Human byte size: 0 -> "0 B", 2048 -> "2.0 KB". */
export function formatBytes(bytes: number | null | undefined): string {
  if (bytes == null || !Number.isFinite(bytes) || bytes <= 0) return '0 B'
  const units = ['B', 'KB', 'MB', 'GB', 'TB']
  let i = 0
  let v = bytes
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024
    i++
  }
  return `${i === 0 ? v : v.toFixed(1)} ${units[i]}`
}

/** Latency in ms: 0 -> "0 ms", 1500 -> "1.5 s". */
function formatLatency(ms: number | null | undefined): string {
  if (ms == null || !Number.isFinite(ms) || ms <= 0) return '0 ms'
  if (ms >= 1000) return `${(ms / 1000).toFixed(1)} s`
  return `${Math.round(ms)} ms`
}

/**
 * A latency percentile. p50/p95 are read off a fixed histogram, so the value is
 * the upper bound of the bucket the percentile fell in and is rendered as a
 * bound. `exceeds` marks the overflow bucket, which has no upper bound: the
 * value is a floor instead ("> 10 s", not "≤ 10 s").
 */
export function formatLatencyBound(
  ms: number | null | undefined,
  exceeds = false
): string {
  if (ms == null || !Number.isFinite(ms) || ms <= 0) return '—'
  return `${exceeds ? '>' : '≤'} ${formatLatency(ms)}`
}

/** The window's headline counts, as the Usage tiles print them. */
export interface UsageHeadline {
  calls: number
  errors: number
  /** Percentage, one decimal, as a string — "0.0" when the window is empty. */
  errorRate: string
}

/**
 * Headline counts for the selected window, from the response's `total_calls`
 * and `total_errors` (the sum of the timeline). The `tools` list is
 * lifetime-cumulative, upstream-only and truncated, so it is not summed.
 */
export function usageHeadline(
  data?: { total_calls?: number; total_errors?: number } | null
): UsageHeadline {
  const calls = data?.total_calls ?? 0
  const errors = data?.total_errors ?? 0
  return {
    calls,
    errors,
    errorRate: calls > 0 ? ((errors / calls) * 100).toFixed(1) : '0.0',
  }
}

// --- unresolved tools ---------------------------------------------------------
//
// A tool that has never completed a call (a typo, or a server that was never
// reachable) is not charted as a used tool; it appears only in the errors
// chart. If it ever succeeds, it reappears on its own.

/** The minimum a tool row needs for the never-completed test. */
export interface UsageToolCompletion {
  calls: number
  errors: number
}

/**
 * True when nothing this tool was asked for ever came back: every recorded call
 * failed, or there were no executed calls at all (only blocked or shed
 * attempts).
 */
export function neverCompleted(tool: UsageToolCompletion): boolean {
  return (tool.calls ?? 0) - (tool.errors ?? 0) <= 0
}

/**
 * Split a tool list into the tools that have completed at least one call and
 * the ones that never have. Charts about USE take the first; the errors chart
 * keeps both, because a tool failing 100% of the time is exactly its subject.
 */
export function partitionUsageTools<T extends UsageToolCompletion>(
  tools: T[]
): { completed: T[]; unresolved: T[] } {
  const completed: T[] = []
  const unresolved: T[] = []
  for (const tool of tools) {
    if (neverCompleted(tool)) unresolved.push(tool)
    else completed.push(tool)
  }
  return { completed, unresolved }
}

/** A short, readable label for a (server, tool) pair. */
export function toolLabel(server: string, tool: string): string {
  return `${server}:${tool}`
}

/** Resolve semantic theme colors for canvas APIs, which cannot read var(). */
export function themeColor(token: string): string {
  return getComputedStyle(document.documentElement).getPropertyValue(`--${token}`).trim();
}
export function paletteColor(index: number): string {
  return themeColor(`chart-${index % 5 + 1}`);
}
