import { computed } from 'vue'
import api from '@/services/api'
import type { ProfileConfig, ProfileView } from '@/types/api'
import { defineCollection } from './collection'

// The list behind the Profiles page, the editor, every profile picker and the
// scope labels.
export const useProfilesStore = defineCollection('profiles', {
  list: async () => (await api.profiles.list()).profiles ?? [],
  idOf: (profile: ProfileView) => profile.id,
  sort: (a, b) => a.name.localeCompare(b.name),
}, collection => {
  const hasProfiles = computed(() => collection.items.value.length > 0)
  // An unset profile means access to all servers.
  function labelFor(id: string | undefined): string {
    if (!id) return 'All servers'
    return collection.byId.value.get(id)?.name ?? 'Profile missing'
  }
  async function create(body: Omit<ProfileConfig, 'id'>) {
    const profile = await api.profiles.create(body)
    collection.upsertLocal(profile)
    return profile
  }
  async function patch(id: string, body: Partial<ProfileConfig>) {
    const profile = await api.profiles.patch(id, body)
    collection.upsertLocal(profile)
    return profile
  }
  async function remove(id: string, params: { reassign_to?: string; force?: boolean } = {}) {
    const result = await api.profiles.delete(id, params)
    collection.removeLocal(id)
    return result
  }
  return { hasProfiles, labelFor, create, patch, remove }
})
