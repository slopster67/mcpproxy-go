import api from '@/services/api'
import type { Server } from '@/types'
import { openOAuthLogin } from '@/utils/oauthLogin'
import { defineCollection } from './collection'

// GET /api/v1/servers, live from `servers` upsert/delete frames. Every write
// answers the server object, which is applied at once; the frame that follows
// carries the same or a newer state.
export const useServersStore = defineCollection<Server, ReturnType<typeof serverActions>>('servers', {
  list: async () => (await api.servers.list()).servers,
  idOf: server => server.name,
  sort: (a, b) => a.name.localeCompare(b.name),
}, collection => serverActions(collection.upsertLocal, collection.removeLocal))

function serverActions(upsertLocal: (server: Server) => void, removeLocal: (id: string) => void) {
  async function create(body: Record<string, unknown>): Promise<Server> {
    const server = await api.servers.create(body)
    upsertLocal(server)
    return server
  }
  async function patch(name: string, body: Record<string, unknown>): Promise<Server> {
    const server = await api.servers.patch(name, body)
    upsertLocal(server)
    return server
  }
  async function restart(name: string): Promise<Server> {
    const server = await api.servers.restart(name)
    upsertLocal(server)
    return server
  }
  async function logout(name: string): Promise<Server> {
    const server = await api.servers.logout(name)
    upsertLocal(server)
    return server
  }
  async function remove(name: string): Promise<void> {
    await api.servers.delete(name)
    removeLocal(name)
  }
  function login(name: string) {
    return openOAuthLogin(() => api.servers.login(name))
  }
  return { create, patch, restart, logout, remove, login }
}
