import api from '@/services/api'
import type { SecretEntry } from '@/types'
import { onChange } from '@/services/events'
import { defineCollection } from './collection'

// GET /api/v1/secrets: stored secrets plus every reference a server config
// makes. The stream only invalidates it (a server edit changes it as much as a
// secret write does), so writes refetch too. An environment variable read on a
// peer is available only while that peer is online, so peer changes refetch
// when some entry is read on a peer.
export const useSecretsStore = defineCollection('secrets', {
  list: async () => (await api.secrets.list()).secrets,
  idOf: (entry: SecretEntry) => entry.id,
}, collection => {
  onChange('peers', () => {
    if (collection.loaded.value && collection.items.value.some(entry => entry.host_id)) void collection.refetch()
  })
  async function set(name: string, value: string) {
    const entry = await api.secrets.create({ name, value, type: 'secret' })
    void collection.refetch()
    return entry
  }
  async function remove(name: string, type = 'secret') {
    await api.secrets.delete(name, type)
    void collection.refetch()
  }
  return { set, remove }
})
