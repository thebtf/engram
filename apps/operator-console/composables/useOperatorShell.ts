import { ref as sharedRef, watch, type Ref as SharedRef } from 'vue'
import { startedLoads } from './useConsolePreferences'
import { operatorConfig } from '../operator-config'

let sharedShellStatus: SharedRef<ShellInfo> | undefined
let sharedShellStatusPending: SharedRef<boolean> | undefined
let sharedShellStatusError: SharedRef<string | null> | undefined

import { operatorApiBase, operatorFetchJson, loadOperatorJson, operatorUnauthorizedRevision, authGeneration, type OperatorLoadState } from './useOperatorApi'

type ShellIdentity = Pick<ShellInfo, 'authDisabled' | 'authenticated' | 'role' | 'source' | 'synthetic' | 'authPosture' | 'identityName' | 'identityInitials' | 'identityProvider'>

function unknownIdentity(): ShellIdentity {
  return { authDisabled: false, authenticated: false, role: '', source: '', synthetic: false, authPosture: 'unknown', identityName: '', identityInitials: '?', identityProvider: '' }
}

function identityFrom(payload: unknown): ShellIdentity | null {
  if (!payload || typeof payload !== 'object' || Array.isArray(payload)) return null
  const authDisabled = Reflect.get(payload, 'auth_disabled')
  const authenticated = Reflect.get(payload, 'authenticated')
  if (authDisabled !== true && (authDisabled !== false || typeof authenticated !== 'boolean')) return null
  const identity = unknownIdentity()
  identity.authDisabled = authDisabled === true
  identity.authenticated = authenticated === true
  identity.authPosture = identity.authDisabled ? 'auth-disabled' : identity.authenticated ? 'authenticated' : 'locked'
  if (identity.authPosture === 'locked') return identity
  const role = Reflect.get(payload, 'role')
  const source = Reflect.get(payload, 'source')
  identity.role = typeof role === 'string' ? role : ''
  identity.source = typeof source === 'string' ? source : ''
  identity.synthetic = Reflect.get(payload, 'synthetic') === true
  identity.identityProvider = identity.source
  const user = Reflect.get(payload, 'user')
  if (!identity.authDisabled && user && typeof user === 'object' && !Array.isArray(user)) {
    const name = Reflect.get(user, 'name')
    const email = Reflect.get(user, 'email')
    identity.identityName = typeof name === 'string' && name.trim() ? name : typeof email === 'string' ? email : ''
    identity.identityInitials = initials(identity.identityName)
  }
  return identity
}

watch(operatorUnauthorizedRevision, () => {
  authGeneration.value++
  if (sharedShellStatus) sharedShellStatus.value = { ...sharedShellStatus.value, ...unknownIdentity(), authPosture: 'locked', backendStatus: 'auth-required' }
}, { flush: 'sync' })

interface ApiStats {
  uptime?: string
}

interface ApiSelfcheck {
  version?: string
  uptime?: string
  overall?: 'healthy' | 'degraded' | 'unhealthy'
  components?: Array<{ name?: string; status?: 'healthy' | 'degraded' | 'unhealthy' }>
}

interface ApiStatsVnext {
  noise_ratio?: number
  embedding?: {
    active_memory_count?: number
  }
}

export interface ShellInfo {
  host: string
  version: string
  uptime: string
  health: string | number
  noise: string
  authDisabled: boolean
  authenticated: boolean
  role: string
  source: string
  synthetic: boolean
  authPosture: 'auth-disabled' | 'authenticated' | 'locked' | 'unknown'
  identityName: string
  identityInitials: string
  identityProvider: string
  backendStatus: 'ready' | 'degraded' | 'unavailable' | 'auth-required' | 'diagnostics-unavailable' | 'unknown'
  activeMemoryCount: number | null
}

function displayHost(base: string, configuredHost?: string): string {
  if (configuredHost && configuredHost.trim()) {
    return configuredHost.trim()
  }

  if (base.startsWith('/')) {
    if (typeof window !== 'undefined' && typeof window !== 'undefined') {
      return window.location.host
    }

    return 'engram'
  }

  try {
    return new URL(base).host
  } catch {
    return base.replace(/^https?:\/\//, '').replace(/\/$/, '') || 'unleashed.lan:37777'
  }
}

function normalizeNoise(value?: number): string {
  if (typeof value !== 'number' || Number.isNaN(value)) return '-'
  return value.toFixed(2)
}


function preferKnownUptime(current: string, fallback?: string): string {
  return current && current !== '-' ? current : fallback || current || '-'
}

function initials(name: string): string {
  const letters = name
    .split(/\s+/)
    .filter(Boolean)
    .slice(0, 2)
    .map((part) => part.slice(0, 1).toUpperCase())
    .join('')

  return letters || 'EG'
}

function isLive<T>(state: OperatorLoadState<T>): state is Extract<OperatorLoadState<T>, { kind: 'live' | 'empty' }> {
  return state.kind === 'live' || state.kind === 'empty'
}

function initialShellInfo(): ShellInfo {
  const config = operatorConfig
  const base = operatorApiBase()
  return {
    host: displayHost(base, config.apiDisplayHost as string | undefined),
    version: '-',
    uptime: '-',
    health: '-',
    noise: '-',
    ...unknownIdentity(),
    backendStatus: 'unknown',
    activeMemoryCount: null,
  }
}

function startOnce(key: string, run: () => Promise<void>) {
  const started = startedLoads.has(key)
  if (typeof window !== 'undefined' && !started) {
    startedLoads.add(key)
    void run().catch((error) => {
      if (import.meta.env.DEV) {
        console.warn(`[useOperatorShell] ${key} live load failed`, error)
      }
    })
  }
}

export function useOperatorShellStatus() {
  const info = (sharedShellStatus ??= sharedRef<ShellInfo>((initialShellInfo)()))
  const pending = (sharedShellStatusPending ??= sharedRef<boolean>((() => false)()))
  const error = (sharedShellStatusError ??= sharedRef<string | null>((() => null)()))

  async function refreshIdentity() {
    const generation = ++authGeneration.value
    info.value = { ...info.value, ...unknownIdentity() }
    const state = await loadOperatorJson<unknown>('/api/auth/me', { source: 'shell-auth', cache: 'no-store' })
    if (generation !== authGeneration.value) return false
    const identity = isLive(state) ? identityFrom(state.data) : state.kind === 'error' && state.error.status === 401 ? { ...unknownIdentity(), authPosture: 'locked' as const } : null
    info.value = { ...info.value, ...(identity ?? unknownIdentity()) }
    return identity !== null
  }

  async function login(email: string, password: string) {
    await operatorFetchJson<unknown>('/api/auth/user-login', {
      method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ email, password }),
    }, 'shell-user-login')
    await refreshIdentity()
    return info.value.authPosture === 'authenticated'
  }

  async function logout() {
    await operatorFetchJson<unknown>('/api/auth/user-logout', { method: 'POST' }, 'shell-user-logout')
    await refreshIdentity()
    return info.value.authPosture === 'locked'
  }

  async function refresh() {
    pending.value = true
    error.value = null

    const identityRequest = refreshIdentity()
    const generation = authGeneration.value
    const [authResult, selfcheckResult, statsResult, vnextResult] = await Promise.allSettled([
      identityRequest,
      loadOperatorJson<ApiSelfcheck>('/api/selfcheck', { source: 'shell-selfcheck' }),
      loadOperatorJson<ApiStats>('/api/stats', { source: 'shell-stats' }),
      loadOperatorJson<ApiStatsVnext>('/api/stats/vnext', { source: 'shell-stats-vnext' }),
    ])

    const next = initialShellInfo()
    const failures: string[] = []

    if (authResult.status !== 'fulfilled' || !authResult.value) failures.push('/api/auth/me')

    if (selfcheckResult.status === 'fulfilled' && isLive(selfcheckResult.value)) {
      const selfcheck = selfcheckResult.value.data
      next.version = typeof selfcheck.version === 'string' && selfcheck.version.trim() ? selfcheck.version : next.version
      next.uptime = selfcheck.uptime || next.uptime
      const degraded = (selfcheck.components || []).filter((component) => component.status && component.status !== 'healthy').length
      next.health = selfcheck.overall === 'healthy' ? 0 : degraded || selfcheck.overall || next.health
    } else {
      failures.push('/api/selfcheck')
    }

    if (statsResult.status === 'fulfilled' && isLive(statsResult.value)) {
      next.uptime = preferKnownUptime(next.uptime, statsResult.value.data.uptime)
    } else {
      failures.push('/api/stats')
    }

    if (vnextResult.status === 'fulfilled' && isLive(vnextResult.value)) {
      const stats = vnextResult.value.data
      const activeMemoryCount = stats.embedding?.active_memory_count
      next.noise = normalizeNoise(stats.noise_ratio)
      next.activeMemoryCount = typeof activeMemoryCount === 'number'
        && Number.isSafeInteger(activeMemoryCount)
        && activeMemoryCount >= 0
        ? activeMemoryCount
        : null
    } else {
      failures.push('/api/stats/vnext')
    }


    const coreResults = [selfcheckResult, statsResult]
    const coreAuthRequired = generation === authGeneration.value && coreResults.some((result) => result.status === 'fulfilled' && result.value.kind === 'error' && result.value.error.status === 401)
    const coreOutages = coreResults.filter((result) => result.status === 'fulfilled' && result.value.kind === 'error' && (result.value.error.category === 'unreachable' || result.value.error.category === 'server-unavailable')).length
    next.backendStatus = info.value.authPosture === 'locked' || coreAuthRequired ? 'auth-required'
      : coreOutages === 2 ? 'unavailable'
        : coreOutages || (next.health !== '-' && next.health !== 0) ? 'degraded'
          : failures.length ? 'diagnostics-unavailable'
            : next.health === 0 ? 'ready' : 'unknown'

    info.value = { ...next, authDisabled: info.value.authDisabled, authenticated: info.value.authenticated, role: info.value.role, source: info.value.source, synthetic: info.value.synthetic, authPosture: info.value.authPosture, identityName: info.value.identityName, identityInitials: info.value.identityInitials, identityProvider: info.value.identityProvider }
    error.value = failures.length ? `Diagnostic requests failed: ${failures.join(', ')}` : null
    pending.value = false
  }

  startOnce('shell-status', refresh)

  return {
    info,
    pending,
    error,
    refresh,
    login,
    logout,
  }
}
