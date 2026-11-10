import api from '@/services/api'
import type { MCPSession } from '@/types/api'
import { defineCollection } from './collection'

// The most recent MCP sessions (the API caps a page at 100), newest activity
// first; `sessions` upserts add and update rows live.
export const useSessionsStore = defineCollection('sessions', {
  list: async () => (await api.sessions.list({ limit: 100 })).sessions,
  idOf: (session: MCPSession) => session.id,
  sort: (a, b) => b.last_activity.localeCompare(a.last_activity),
})
