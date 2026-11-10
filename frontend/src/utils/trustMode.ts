// Per-server policy for approving new and changed tools.

export type TrustMode = 'auto' | 'manual'

const RECOGNIZED: readonly TrustMode[] = ['auto', 'manual'] as const

function isTrustMode(raw: string): raw is TrustMode {
  return (RECOGNIZED as readonly string[]).includes(raw)
}

/**
 * Fail-closed resolution of a raw configured trust-mode value.
 * Unset, empty, mis-cased ("Scan"), padded (" scan ") and unknown values all
 * resolve to `manual`.
 */
export function effectiveTrustMode(raw?: string): TrustMode {
  if (raw && isTrustMode(raw)) return raw
  return 'manual'
}
