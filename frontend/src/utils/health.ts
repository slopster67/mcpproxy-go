import type { Server } from '@/types'
import { HEALTH_STATUS_LABELS, type HealthStatusValue } from '@/types/contracts'

export function healthStatusText(
  health: { summary?: string | null; status?: string | null; usable?: boolean | null },
  connected: boolean,
): string {
  if (health.summary) return health.summary
  if (health.status) return HEALTH_STATUS_LABELS[health.status as HealthStatusValue] ?? health.status
  if (health.usable === false && connected) return 'Unavailable'
  return connected ? 'Connected' : 'Disconnected'
}

export function needsOAuthSignIn(server: Server): boolean {
  return server.health?.action === 'login' || [
    'MCPX_OAUTH_LOGIN_REQUIRED',
    'MCPX_OAUTH_TOKEN_UNAVAILABLE',
    'MCPX_OAUTH_REAUTH_REQUIRED',
    'MCPX_OAUTH_REFRESH_EXPIRED',
    'MCPX_OAUTH_REFRESH_403',
  ].includes(server.diagnostic?.code ?? '')
}
