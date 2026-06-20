export interface OperatorUser {
  id?: string
  email?: string
  role?: string
}

export interface OperatorAuthState {
  phase: 'idle' | 'loading' | 'authenticated' | 'anonymous'
  hydrated: boolean
  authDisabled: boolean
  setupNeeded: boolean
  user: OperatorUser | null
}

export function useOperatorAuth() {
  const state = useState<OperatorAuthState>('operator-auth', () => ({
    phase: 'idle',
    hydrated: false,
    authDisabled: false,
    setupNeeded: false,
    user: null,
  }))

  async function authFetch<T>(path: string, init?: RequestInit): Promise<{
    ok: boolean
    status: number
    data: T | null
  }> {
    const response = await fetch(`${useRuntimeConfig().public.apiBase}${path}`, {
      credentials: 'include',
      headers: {
        'Content-Type': 'application/json',
        ...(init?.headers || {}),
      },
      ...init,
    })

    let data: T | null = null
    try {
      data = (await response.json()) as T
    } catch {
      data = null
    }

    return {
      ok: response.ok,
      status: response.status,
      data,
    }
  }

  async function restore(force = false) {
    if (state.value.hydrated && !force) {
      return
    }

    state.value.phase = 'loading'

    try {
      const result = await authFetch<{
        auth_disabled?: boolean
        user?: OperatorUser
      }>('/auth/me')

      state.value.hydrated = true
      state.value.authDisabled = result.data?.auth_disabled === true

      if (result.ok) {
        state.value.phase = 'authenticated'
        state.value.user = result.data?.user ?? null
        return
      }

      state.value.phase = 'anonymous'
      state.value.user = null
    } catch {
      state.value.phase = 'anonymous'
      state.value.hydrated = true
      state.value.user = null
    }
  }

  async function checkSetupNeeded(force = false): Promise<boolean> {
    if (state.value.hydrated && state.value.setupNeeded && !force) {
      return true
    }

    try {
      const result = await authFetch<{ needed?: boolean }>('/auth/setup-needed')
      state.value.setupNeeded = result.data?.needed === true
      return state.value.setupNeeded
    } catch {
      state.value.setupNeeded = false
      return false
    }
  }

  async function loginWithToken(token: string): Promise<boolean> {
    const result = await authFetch('/auth/login', {
      method: 'POST',
      body: JSON.stringify({ token }),
    })

    if (!result.ok) {
      state.value.phase = 'anonymous'
      state.value.user = null
      return false
    }

    await restore(true)
    return state.value.phase === 'authenticated'
  }

  async function loginWithCredentials(email: string, password: string): Promise<boolean> {
    const result = await authFetch('/auth/user-login', {
      method: 'POST',
      body: JSON.stringify({ email, password }),
    })

    if (!result.ok) {
      state.value.phase = 'anonymous'
      state.value.user = null
      return false
    }

    await restore(true)
    return state.value.phase === 'authenticated'
  }

  async function setupFirstUser(email: string, password: string): Promise<{
    ok: boolean
    error?: string
  }> {
    const result = await authFetch<{ error?: string }>('/auth/setup', {
      method: 'POST',
      body: JSON.stringify({ email, password }),
    })

    if (!result.ok) {
      return {
        ok: false,
        error: result.data?.error || 'Setup failed.',
      }
    }

    state.value.setupNeeded = false
    return { ok: true }
  }

  async function logout(): Promise<void> {
    try {
      await authFetch('/auth/user-logout', { method: 'POST' })
    } catch {
      // ignore and fall through
    }

    try {
      await authFetch('/auth/logout', { method: 'POST' })
    } catch {
      // ignore and continue clearing local state
    }

    state.value.phase = 'anonymous'
    state.value.user = null
    state.value.hydrated = true
  }

  return {
    state,
    restore,
    checkSetupNeeded,
    loginWithToken,
    loginWithCredentials,
    setupFirstUser,
    logout,
  }
}
