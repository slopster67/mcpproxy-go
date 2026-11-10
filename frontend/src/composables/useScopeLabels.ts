import { useClientsStore } from '@/stores/clients'
import { useProfilesStore } from '@/stores/profiles'

// The human words for a profile/client filter value, shared by chips, empty
// states and tooltips. Client labels come from the full saved roster.
export type ScopeName = 'profile' | 'client'

const NAME_LABEL: Record<ScopeName, string> = { profile: 'Profile', client: 'Client' }

export function useScopeLabels() {
  const profiles = useProfilesStore()
  const clients = useClientsStore()

  function valueLabel(name: ScopeName, value: string): string {
    if (value === '-') return name === 'profile' ? 'All servers' : 'unattributed'
    if (name === 'profile') return profiles.labelFor(value)
    return clients.labelFor(value)
  }

  /** "Client: Cursor". */
  function chipLabel(name: ScopeName, value: string): string {
    return `${NAME_LABEL[name]}: ${valueLabel(name, value)}`
  }

  return { valueLabel, chipLabel }
}
