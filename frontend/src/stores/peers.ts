import { computed } from 'vue'
import api from '@/services/api'
import type { Peer } from '@/types/api'
import { defineCollection } from './collection'

export const usePeersStore = defineCollection('peers', {
  list: async () => (await api.peers.list()).peers,
  idOf: (peer: Peer) => peer.id,
  sort: (a, b) => a.name.localeCompare(b.name),
}, collection => {
  const hasPeers = computed(() => collection.items.value.length > 0)
  function label(id?: string) {
    return id ? collection.byId.value.get(id)?.name || 'Unknown peer' : 'Primary'
  }
  function online(id?: string) {
    return !id || !!collection.byId.value.get(id)?.online
  }
  async function create(name: string) {
    const { token, ...peer } = await api.peers.create({ name })
    collection.upsertLocal(peer)
    return { peer: peer as Peer, token }
  }
  async function rotate(id: string) {
    const { token, ...peer } = await api.peers.rotate(id)
    collection.upsertLocal(peer)
    return { peer: peer as Peer, token }
  }
  async function remove(id: string) {
    await api.peers.delete(id)
    collection.removeLocal(id)
  }
  return { hasPeers, label, online, create, rotate, remove }
})
