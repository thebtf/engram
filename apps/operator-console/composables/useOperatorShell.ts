import { operatorApiBase, loadOperatorJson, type OperatorLoadState } from './useOperatorApi'

interface AuthMe {
  auth_disabled?: boolean
  authenticated?: boolean
  role?: string
  source?: string
  synthetic?: boolean
  user?: {
    name?: string
    email?: string
  }
}

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
}

function displayHost(base: string, configuredHost?: string): string {
  if (configuredHost && configuredHost.trim()) {
    return configuredHost.trim()
  }

  if (base.startsWith('/')) {
    if (import.meta.client && typeof window !== 'undefined') {
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
  const config = useRuntimeConfig().public
  const base = operatorApiBase()
  return {
    host: displayHost(base, config.apiDisplayHost as string | undefined),
    version: '-',
    uptime: '-',
    health: '-',
    noise: '-',
    authDisabled: false,
    authenticated: false,
    role: 'operator',
    source: 'unknown',
    synthetic: false,
    authPosture: 'unknown',
    identityName: 'operator',
    identityInitials: 'EG',
    identityProvider: 'session',
  }
}

function startOnce(key: string, run: () => Promise<void>) {
  const started = useState<boolean>(`live:${key}:started`, () => false)
  if (import.meta.client && !started.value) {
    started.value = true
    void run().catch((error) => {
      if (import.meta.dev) {
        console.warn(`[useOperatorShell] ${key} live load failed`, error)
      }
    })
  }
}

export function useOperatorShellStatus() {
  const info = useState<ShellInfo>('live:shell-status', initialShellInfo)
  const pending = useState<boolean>('live:shell-status:pending', () => false)
  const error = useState<string | null>('live:shell-status:error', () => null)

  async function refresh() {
    pending.value = true
    error.value = null

    const [authResult, selfcheckResult, statsResult, vnextResult] = await Promise.allSettled([
      loadOperatorJson<AuthMe>('/api/auth/me', { source: 'shell-auth' }),
      loadOperatorJson<ApiSelfcheck>('/api/selfcheck', { source: 'shell-selfcheck' }),
      loadOperatorJson<ApiStats>('/api/stats', { source: 'shell-stats' }),
      loadOperatorJson<ApiStatsVnext>('/api/stats/vnext', { source: 'shell-stats-vnext' }),
    ])

    const next = initialShellInfo()
    const failures: string[] = []

    if (authResult.status === 'fulfilled' && isLive(authResult.value)) {
      const auth = authResult.value.data
      next.authDisabled = Boolean(auth.auth_disabled)
      next.authenticated = Boolean(auth.authenticated)
      next.role = auth.role || (next.authDisabled ? 'admin' : 'operator')
      next.source = auth.source || (next.authDisabled ? 'auth-disabled' : 'unknown')
      next.synthetic = Boolean(auth.synthetic)
      next.authPosture = next.authDisabled ? 'auth-disabled' : next.authenticated ? 'authenticated' : 'locked'
      next.identityName = auth.user?.name || next.role || 'operator'
      next.identityInitials = initials(next.identityName)
      next.identityProvider = next.source
    } else {
      failures.push('/api/auth/me')
    }

    if (selfcheckResult.status === 'fulfilled' && isLive(selfcheckResult.value)) {
      const selfcheck = selfcheckResult.value.data
      next.version = selfcheck.version || next.version
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
      next.noise = normalizeNoise(vnextResult.value.data.noise_ratio)
    } else {
      failures.push('/api/stats/vnext')
    }

    info.value = next
    error.value = failures.length ? `Unavailable shell endpoints: ${failures.join(', ')}` : null
    pending.value = false
  }

  startOnce('shell-status', refresh)

  return {
    info,
    pending,
    error,
    refresh,
  }
}
