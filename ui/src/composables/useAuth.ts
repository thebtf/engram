import { ref, computed } from 'vue'

// Singleton state shared across all useAuth() calls
const authenticated = ref(false)
const loading = ref(true)
const authDisabled = ref(false)

export function useAuth() {
  async function checkAuth(): Promise<void> {
    loading.value = true
    try {
      const res = await fetch('/api/auth/me', { credentials: 'include' })
      authenticated.value = res.ok
      if (res.ok) {
        const data = await res.json()
        authDisabled.value = data?.auth_disabled === true
      }
    } catch {
      authenticated.value = false
    } finally {
      loading.value = false
    }
  }

  async function login(token: string): Promise<boolean> {
    try {
      const res = await fetch('/api/auth/login', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ token }),
        credentials: 'include',
      })
      authenticated.value = res.ok
      return res.ok
    } catch {
      authenticated.value = false
      return false
    }
  }

  async function logout(): Promise<void> {
    try {
      await fetch('/api/auth/logout', { method: 'POST', credentials: 'include' })
    } finally {
      authenticated.value = false
    }
  }

  return {
    authenticated: computed(() => authenticated.value),
    loading: computed(() => loading.value),
    authDisabled: computed(() => authDisabled.value),
    checkAuth,
    login,
    logout,
  }
}
