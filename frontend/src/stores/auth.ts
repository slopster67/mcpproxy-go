import { defineStore } from 'pinia'
import { computed, ref } from 'vue'
import { setCSRFToken } from '@/services/api'

type User = { subject: string; name: string; email?: string }
type Session = { configured: boolean; authenticated: boolean; user?: User; csrf_token?: string }
export const useAuthStore = defineStore('auth', () => {
  const user = ref<User | null>(null)
  const loading = ref(true)
  const configured = ref(false)
  const bootstrapError = ref('')
  const isAuthenticated = computed(() => user.value !== null)
  const shellPresented = isAuthenticated
  function expire() { user.value = null; setCSRFToken('') }
  async function checkAuth() {
    loading.value = true
    bootstrapError.value = ''
    try {
      const response = await fetch('/auth/session', { credentials: 'same-origin', cache: 'no-store' })
      if (!response.ok) throw new Error('Could not check your session. Reload to try again.')
      const session: Session = await response.json()
      configured.value = session.configured
      user.value = session.authenticated ? session.user ?? null : null
      setCSRFToken(session.csrf_token ?? '')
      if (!session.authenticated) {
        if(session.configured) login()
        else bootstrapError.value='Configure OIDC environment variables to access the dashboard.'
      }
    } catch (error) { expire(); bootstrapError.value = error instanceof Error ? error.message : 'Could not check your session.' }
    finally { loading.value = false }
  }
  function login() {
    const query=new URLSearchParams(window.location.search);query.delete('signin_error')
    const target=window.location.pathname+(query.size?'?'+query.toString():'')
    window.location.replace('/auth/login?return_to=' + encodeURIComponent(target))
  }
  async function logout() {
    const session = await fetch('/auth/session', {credentials:'same-origin',cache:'no-store'}).then(r=>r.json()) as Session
    if(!session.authenticated){expire();window.location.assign('/ui/');return}
    const response = await fetch('/auth/logout', {method:'POST',credentials:'same-origin',headers:{'X-CSRF-Token':session.csrf_token ?? ''}})
    if (response.ok) { expire(); window.location.assign('/ui/') }
  }
  return { user, loading, configured, bootstrapError, isAuthenticated, shellPresented, checkAuth, login, logout, expire }
})
