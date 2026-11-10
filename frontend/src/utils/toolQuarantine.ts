import {
  TierDestructive,
  TierRead,
  TierUnannotated,
  TierUnknown,
  TierWrite,
  type Tier,
} from '@/types/contracts'

/** Display labels for the raw `approval_status` values; the raw value stays
 * the wire/URL/filter value and is never rendered. */
const TOOL_APPROVAL_LABELS: Record<string, string> = {
  approved: 'Approved',
  pending: 'New',
  changed: 'Changed',
}

/** Label for a raw approval status; an unknown value is shown as-is. */
export function toolApprovalLabel(status: string): string {
  return TOOL_APPROVAL_LABELS[status] ?? status
}

/** Display labels for the Tier enum. */
const TIER_LABELS: Record<Tier, string> = {
  [TierRead]: 'Read',
  [TierWrite]: 'Write',
  [TierDestructive]: 'Destructive',
  [TierUnannotated]: 'Unannotated',
  [TierUnknown]: 'Unknown',
}

/** Label for a raw tier; a missing tier reads Unannotated, an unknown value is shown as-is. */
export function tierLabel(tier?: string): string {
  if (!tier) return TIER_LABELS[TierUnannotated]
  return TIER_LABELS[tier as Tier] ?? tier
}
