

import { ActivityViewAll, ActivityViewCalls, ActivityViewSessions, ActivityViewSystem, type ActivityView } from '@/types/contracts';

// Activity type labels, keyed by every backend activity type. Declaration
// order is the filter-dropdown order.
export const ACTIVITY_TYPE_LABELS: Record<string, string> = {
  'tool_call': 'Tool Call',
  'system_start': 'System Start',
  'system_stop': 'System Stop',
  'internal_tool_call': 'Internal Tool Call',
  'config_change': 'Config Change',
  'policy_decision': 'Policy Decision',
  'server_change': 'Server Change',
  'preflight': 'Preflight',
  'tool_quarantine_change': 'Tool Quarantine Change',
  'credential_broker': 'Credential Broker',
  'profile_change': 'Profile Change'
}

const typeLabels = ACTIVITY_TYPE_LABELS

// Activity view tab labels, keyed by the ActivityView enum.
export const ACTIVITY_VIEW_LABELS: Record<ActivityView, string> = {
  [ActivityViewCalls]: 'Tool calls',
  [ActivityViewSessions]: 'Sessions',
  [ActivityViewSystem]: 'System events',
  [ActivityViewAll]: 'All',
}

// The activity types the `view=calls` filter selects.
const ACTIVITY_CALL_TYPES = ['tool_call', 'internal_tool_call']

/**
 * Resolves an Activity `view` query value to the `type` filter it applies:
 * `calls` is the two call types, `system` is every other known type. Any
 * other view applies no type filter and returns `undefined`, so the caller can
 * tell "no override" from "override to zero types".
 */
export function activityViewTypes(view: string): string[] | undefined {
  if (view === 'calls') return [...ACTIVITY_CALL_TYPES]
  if (view === 'system') {
    return Object.keys(ACTIVITY_TYPE_LABELS).filter(t => !ACTIVITY_CALL_TYPES.includes(t))
  }
  return undefined
}

// Status labels
const statusLabels: Record<string, string> = {
  'success': 'Success',
  'error': 'Error',
  'blocked': 'Blocked',
}

/** snake_case -> Title Case, for a type without a label. */
const humaniseType = (type: string): string => {
  return type
    .split('_')
    .filter(Boolean)
    .map(word => word.charAt(0).toUpperCase() + word.slice(1))
    .join(' ')
}

/**
 * Format activity type for display
 */
export const formatType = (type: string): string => {
  return typeLabels[type] ?? humaniseType(type)
}

/**
 * Format status for display
 */
export const formatStatus = (status: string): string => {
  return statusLabels[status] || status
}
