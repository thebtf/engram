import { ref, computed } from 'vue'

interface User {
  id: string
  email: string
  role: string
}

// Singleton state shared across all useAuth() calls
const authenticated = ref(false)
const loading = ref(true)
const authDisabled = ref(false)
const user = ref<User | null>(null)

export function useAuth() {
  const isAdmin = computed(() => user.value?.role === 'admin')

  async function checkAuth(): Promise<void> {
    loading.value = true
    try {
      const res = await fetch('/api/auth/me', { credentials: 'include' })
      authenticated.value = res.ok
      if (res.ok) {
        const data = await res.json()
        authDisabled.value = data?.auth_disabled === true
        user.value = data?.user ?? null
      } else {
        user.value = null
      }
    } catch {
      authenticated.value = false
      user.value = null
    } finally {
      loading.value = false
    }
  }

  async function fetchMe(): Promise<void> {
    try {
      const res = await fetch('/api/auth/me', { credentials: 'include' })
      if (res.ok) {
        const data = await res.json()
        user.value = data?.user ?? null
        authenticated.value = true
      } else {
        user.value = null
        authenticated.value = false
      }
    } catch {
      user.value = null
      authenticated.value = false
    }
  }

  async function checkSetupNeeded(): Promise<boolean> {
    try {
      const res = await fetch('/api/auth/setup-needed', { credentials: 'include' })
      if (res.ok) {
        const data = await res.json()
        return data?.needed === true
      }
    } catch {
      // ignore
    }
    return false
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
      if (!res.ok) {
        user.value = null
      }
      return res.ok
    } catch {
      authenticated.value = false
      user.value = null
      return false
    }
  }

  async function loginWithCredentials(email: string, password: string): Promise<boolean> {
    try {
      const res = await fetch('/api/auth/user-login', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ email, password }),
        credentials: 'include',
      })
      if (res.ok) {
        const data = await res.json()
        user.value = data?.user ?? null
        authenticated.value = true
        return true
      } else {
        user.value = null
        authenticated.value = false
        return false
      }
    } catch {
      authenticated.value = false
      user.value = null
      return false
    }
  }

  async function logout(): Promise<void> {
    try {
      await fetch('/api/auth/logout', { method: 'POST', credentials: 'include' })
    } finally {
      authenticated.value = false
      user.value = null
    }
  }

  async function userLogout(): Promise<void> {
    try {
      await fetch('/api/auth/user-logout', { method: 'POST', credentials: 'include' })
    } finally {
      authenticated.value = false
      user.value = null
    }
  }

  return {
    authenticated: computed(() => authenticated.value),
    loading: computed(() => loading.value),
    authDisabled: computed(() => authDisabled.value),
    user: computed(() => user.value),
    isAdmin,
    checkAuth,
    fetchMe,
    checkSetupNeeded,
    login,
    loginWithCredentials,
    logout,
    userLogout,
  }
}
