import api from '@/services/api'
import type { ClientPresence } from '@/types/api'
import { defineCollection } from './collection'

// GET /api/v1/clients (unscoped), live from `clients` upsert/delete frames:
// presence (active_sessions, last_seen) arrives with them.
export const useClientsStore = defineCollection('clients', {
  list: async () => (await api.clients.list()).clients,
  idOf: (client: ClientPresence) => client.id,
  sort: (a, b) => (a.display_name || a.id).localeCompare(b.display_name || b.id),
}, collection => {
  function labelFor(id: string | undefined, fallback = 'Unknown client'): string {
    return (id && collection.byId.value.get(id)?.display_name) || fallback
  }
  async function create(body: Parameters<typeof api.clients.create>[0]) {
    const { credential, snippet, ...client } = await api.clients.create(body)
    collection.upsertLocal(client)
    return { client: client as ClientPresence, credential, snippet }
  }
  async function patch(id: string, body: Parameters<typeof api.clients.patch>[1]) {
    const client = await api.clients.patch(id, body)
    collection.upsertLocal(client)
    return client
  }
  async function rotate(id: string) {
    const { credential, snippet, ...client } = await api.clients.rotate(id)
    collection.upsertLocal(client)
    return { client: client as ClientPresence, credential, snippet }
  }
  async function remove(id: string) {
    await api.clients.delete(id)
    collection.removeLocal(id)
  }
  return { labelFor, create, patch, rotate, remove }
})
