import { ref, computed } from 'vue'

// Singleton state shared across all useAuth() calls
const authenticated = ref(false)
const loading = ref(true)

export function useAuth() {
  async function checkAuth(): Promise<void> {
    loading.value = true
    try {
      const res = await fetch('/api/auth/me')
      authenticated.value = res.ok
    } catch {
      authenticated.value = false
    } finally {
      loading.value = false
    }
  }

  async function login(token: string): Promise<boolean> {
    const res = await fetch('/api/auth/login', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ token }),
    })
    authenticated.value = res.ok
    return res.ok
  }

  async function logout(): Promise<void> {
    try {
      await fetch('/api/auth/logout', { method: 'POST' })
    } finally {
      authenticated.value = false
    }
  }

  return {
    authenticated: computed(() => authenticated.value),
    loading: computed(() => loading.value),
    checkAuth,
    login,
    logout,
  }
}
