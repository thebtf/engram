import type { ComputedRef, Ref } from 'vue'
import { computed } from 'vue'
import {
  endpointEvidence,
  errorState,
  liveState,
  operatorFetchJson,
  pendingState,
  runOperatorMutation,
  toOperatorSourceError,
  type OperatorLoadState,
  type OperatorMutationResult,
  type OperatorSourceError,
} from './useOperatorApi'

const ACCESS_BASE = '/api/access'
const ACCESS_LIMIT = 100

interface ApiAccessProvider {
  id: string
  label: string
  kind: string
  enabled: boolean
  configured: boolean
  operable: boolean
  honesty: string
  evidence: string
  description: string
}

interface ApiAccessProvidersResponse {
  providers: ApiAccessProvider[]
  auth_disabled: boolean
  local_login_enabled: boolean
  authentik_trusted_proxy_count: number
}

interface ApiAccessInvitation {
  id: number
  code: string
  email: string
  role: string
  created_by: number
  created_by_email: string
  used_by?: number | null
  used_by_email?: string | null
  used_at?: string | null
  expires_at: string
  revoked_at?: string | null
  revoked_by?: number | null
  revocation_reason: string
  created_at: string
  status: string
}

interface ApiAccessInvitationsResponse {
  invitations: ApiAccessInvitation[]
}

interface ApiAccessUser {
  id: number
  email: string
  role: string
  disabled: boolean
  created_at: string
  last_login_at?: string | null
}

interface ApiAccessUsersResponse {
  users: ApiAccessUser[]
}

interface ApiAccessRole {
  role: string
  user_count: number
}

interface ApiAccessRolesResponse {
  roles: ApiAccessRole[]
}

interface ApiAccessSession {
  id: string
  user_id: number
  user_email: string
  user_role: string
  user_disabled: boolean
  created_at: string
  expires_at: string
  user_agent: string
  remote_addr: string
  revoked_at?: string | null
  revoked_by?: number | null
  revocation_reason: string
  status: string
}

interface ApiAccessSessionsResponse {
  sessions: ApiAccessSession[]
}

interface ApiAccessAuditEntry {
  id: number
  action: string
  actor: string
  reason: string
  created_at: string
  before_state?: Record<string, unknown> | null
  after_state?: Record<string, unknown> | null
}

interface ApiAccessAuditResponse {
  entries: ApiAccessAuditEntry[]
}

interface ApiAccessDrilldown {
  user: ApiAccessUser | null
  sessions: ApiAccessSession[]
  invitations_created: ApiAccessInvitation[]
  invitations_used: ApiAccessInvitation[]
  audit: ApiAccessAuditEntry[]
}

interface ApiAccessCreateInvitationResponse {
  invitation: ApiAccessInvitation
}

interface ApiAccessUpdateUserResponse {
  user: ApiAccessUser
}

interface ApiAccessMutationReceipt {
  status: string
  id: string | number
}

export interface OperatorAccessProvider {
  id: string
  label: string
  kind: string
  enabled: boolean
  configured: boolean
  operable: boolean
  honesty: string
  evidence: string
  description: string
}

export interface OperatorAccessInvitation {
  id: number
  email: string
  role: string
  createdBy: number
  createdByEmail: string
  usedBy: number | null
  usedByEmail: string | null
  usedAt: string | null
  expiresAt: string
  revokedAt: string | null
  revokedBy: number | null
  revocationReason: string
  createdAt: string
  status: string
}

export interface OperatorAccessUser {
  id: number
  email: string
  role: string
  disabled: boolean
  createdAt: string
  lastLoginAt: string | null
}

export interface OperatorAccessRoleSummary {
  role: string
  userCount: number
}

export interface OperatorAccessSession {
  id: string
  userID: number
  userEmail: string
  userRole: string
  userDisabled: boolean
  createdAt: string
  expiresAt: string
  userAgent: string
  remoteAddr: string
  revokedAt: string | null
  revokedBy: number | null
  revocationReason: string
  status: string
}

export interface OperatorAccessAuditEntry {
  id: number
  action: string
  actor: string
  reason: string
  createdAt: string
  beforeState: Record<string, unknown> | null
  afterState: Record<string, unknown> | null
}

export interface OperatorAccessSummary {
  authDisabled: boolean
  localLoginEnabled: boolean
  authentikTrustedProxyCount: number
}

export interface OperatorAccessDrilldown {
  user: OperatorAccessUser | null
  sessions: OperatorAccessSession[]
  invitationsCreated: OperatorAccessInvitation[]
  invitationsUsed: OperatorAccessInvitation[]
  audit: OperatorAccessAuditEntry[]
}

export interface OperatorAccessSnapshot {
  providers: OperatorAccessProvider[]
  invitations: OperatorAccessInvitation[]
  users: OperatorAccessUser[]
  roles: OperatorAccessRoleSummary[]
  sessions: OperatorAccessSession[]
  audit: OperatorAccessAuditEntry[]
  summary: OperatorAccessSummary
}

export interface AccessCreateInvitationInput {
  email: string
  role: string
  expiresInHours: number
}

export interface AccessUpdateUserInput {
  role?: string
  disabled?: boolean
}

function jsonInit(method: 'POST' | 'PATCH', body?: unknown): RequestInit {
  return {
    method,
    headers: { 'Content-Type': 'application/json' },
    body: body === undefined ? undefined : JSON.stringify(body),
  }
}

function replaceArray<T>(target: T[], next: readonly T[]) {
  target.splice(0, target.length, ...next)
}

function startOnce(key: string, run: () => Promise<void>) {
  const started = useState<boolean>(`live:${key}:started`, () => false)
  if (import.meta.client && !started.value) {
    started.value = true
    void run().catch((error) => {
      if (import.meta.dev) {
        console.warn(`[useOperatorAccess] ${key} live load failed`, error)
      }
    })
  }
}

function emptySummary(): OperatorAccessSummary {
  return {
    authDisabled: false,
    localLoginEnabled: true,
    authentikTrustedProxyCount: 0,
  }
}

function emptyDrilldown(): OperatorAccessDrilldown {
  return {
    user: null,
    sessions: [],
    invitationsCreated: [],
    invitationsUsed: [],
    audit: [],
  }
}

function mapProvider(row: ApiAccessProvider): OperatorAccessProvider {
  return {
    id: row.id,
    label: row.label,
    kind: row.kind,
    enabled: Boolean(row.enabled),
    configured: Boolean(row.configured),
    operable: Boolean(row.operable),
    honesty: row.honesty || 'live',
    evidence: row.evidence || '',
    description: row.description || '',
  }
}

function mapInvitation(row: ApiAccessInvitation): OperatorAccessInvitation {
  return {
    id: Number(row.id),
    email: row.email || '',
    role: row.role || 'operator',
    createdBy: Number(row.created_by || 0),
    createdByEmail: row.created_by_email || '',
    usedBy: row.used_by == null ? null : Number(row.used_by),
    usedByEmail: row.used_by_email || null,
    usedAt: row.used_at || null,
    expiresAt: row.expires_at,
    revokedAt: row.revoked_at || null,
    revokedBy: row.revoked_by == null ? null : Number(row.revoked_by),
    revocationReason: row.revocation_reason || '',
    createdAt: row.created_at,
    status: row.status || 'pending',
  }
}

function mapUser(row: ApiAccessUser): OperatorAccessUser {
  return {
    id: Number(row.id),
    email: row.email || '',
    role: row.role || 'operator',
    disabled: Boolean(row.disabled),
    createdAt: row.created_at,
    lastLoginAt: row.last_login_at || null,
  }
}

function mapRole(row: ApiAccessRole): OperatorAccessRoleSummary {
  return {
    role: row.role || 'operator',
    userCount: Number(row.user_count || 0),
  }
}

function mapSession(row: ApiAccessSession): OperatorAccessSession {
  return {
    id: row.id || '',
    userID: Number(row.user_id || 0),
    userEmail: row.user_email || '',
    userRole: row.user_role || 'operator',
    userDisabled: Boolean(row.user_disabled),
    createdAt: row.created_at,
    expiresAt: row.expires_at,
    userAgent: row.user_agent || '',
    remoteAddr: row.remote_addr || '',
    revokedAt: row.revoked_at || null,
    revokedBy: row.revoked_by == null ? null : Number(row.revoked_by),
    revocationReason: row.revocation_reason || '',
    status: row.status || 'active',
  }
}

function mapAuditEntry(row: ApiAccessAuditEntry): OperatorAccessAuditEntry {
  return {
    id: Number(row.id),
    action: row.action || '',
    actor: row.actor || '',
    reason: row.reason || '',
    createdAt: row.created_at,
    beforeState: row.before_state || null,
    afterState: row.after_state || null,
  }
}

function mapDrilldown(payload: ApiAccessDrilldown): OperatorAccessDrilldown {
  return {
    user: payload.user ? mapUser(payload.user) : null,
    sessions: Array.isArray(payload.sessions) ? payload.sessions.map(mapSession) : [],
    invitationsCreated: Array.isArray(payload.invitations_created) ? payload.invitations_created.map(mapInvitation) : [],
    invitationsUsed: Array.isArray(payload.invitations_used) ? payload.invitations_used.map(mapInvitation) : [],
    audit: Array.isArray(payload.audit) ? payload.audit.map(mapAuditEntry) : [],
  }
}

export interface OperatorAccessComposable {
  providers: OperatorAccessProvider[]
  invitations: OperatorAccessInvitation[]
  users: OperatorAccessUser[]
  roles: OperatorAccessRoleSummary[]
  sessions: OperatorAccessSession[]
  audit: OperatorAccessAuditEntry[]
  summary: ComputedRef<OperatorAccessSummary>
  loadState: ComputedRef<OperatorLoadState<OperatorAccessSnapshot>>
  drilldownState: ComputedRef<OperatorLoadState<OperatorAccessDrilldown>>
  drilldown: ComputedRef<OperatorAccessDrilldown>
  selectedUserID: Ref<number | null>
  pending: ComputedRef<boolean>
  error: ComputedRef<OperatorSourceError | null>
  forbidden: ComputedRef<boolean>
  hasProvenSnapshot: ComputedRef<boolean>
  refresh: () => Promise<void>
  openUser: (userID: number) => Promise<void>
  closeUser: () => void
  createInvitation: (input: AccessCreateInvitationInput) => Promise<OperatorMutationResult<ApiAccessCreateInvitationResponse>>
  revokeInvitation: (invitationID: number, reason?: string) => Promise<OperatorMutationResult<ApiAccessMutationReceipt>>
  updateUser: (userID: number, input: AccessUpdateUserInput) => Promise<OperatorMutationResult<ApiAccessUpdateUserResponse>>
  revokeSession: (sessionID: string, reason?: string) => Promise<OperatorMutationResult<ApiAccessMutationReceipt>>
}

export function useOperatorAccess(): OperatorAccessComposable {
  const evidence = endpointEvidence('/api/access/*', 'access-page')
  const drilldownEvidence = endpointEvidence('/api/access/users/{id}', 'access-drilldown')

  const providersState = useState<OperatorAccessProvider[]>('live:access:providers', () => [])
  const invitationsState = useState<OperatorAccessInvitation[]>('live:access:invitations', () => [])
  const usersState = useState<OperatorAccessUser[]>('live:access:users', () => [])
  const rolesState = useState<OperatorAccessRoleSummary[]>('live:access:roles', () => [])
  const sessionsState = useState<OperatorAccessSession[]>('live:access:sessions', () => [])
  const auditState = useState<OperatorAccessAuditEntry[]>('live:access:audit', () => [])
  const summaryState = useState<OperatorAccessSummary>('live:access:summary', emptySummary)
  const selectedUserID = useState<number | null>('live:access:selected-user', () => null)
  const drilldownData = useState<OperatorAccessDrilldown>('live:access:drilldown', emptyDrilldown)
  const loadStateValue = useState<OperatorLoadState<OperatorAccessSnapshot>>('live:access:state', () => pendingState(evidence, currentSnapshot()))
  const hasProvenSnapshotValue = useState<boolean>('live:access:has-proven-snapshot', () => false)
  const drilldownStateValue = useState<OperatorLoadState<OperatorAccessDrilldown>>('live:access:drilldown-state', () => liveState(drilldownEvidence, emptyDrilldown()))

  function currentSnapshot(): OperatorAccessSnapshot {
    return {
      providers: [...providersState.value],
      invitations: [...invitationsState.value],
      users: [...usersState.value],
      roles: [...rolesState.value],
      sessions: [...sessionsState.value],
      audit: [...auditState.value],
      summary: { ...summaryState.value },
    }
  }

  function currentDrilldown(): OperatorAccessDrilldown {
    return {
      user: drilldownData.value.user ? { ...drilldownData.value.user } : null,
      sessions: [...drilldownData.value.sessions],
      invitationsCreated: [...drilldownData.value.invitationsCreated],
      invitationsUsed: [...drilldownData.value.invitationsUsed],
      audit: [...drilldownData.value.audit],
    }
  }

  const summary = computed(() => summaryState.value)
  const loadState = computed(() => loadStateValue.value)
  const drilldownState = computed(() => drilldownStateValue.value)
  const drilldown = computed(() => drilldownData.value)
  const pending = computed(() => loadStateValue.value.kind === 'pending' || drilldownStateValue.value.kind === 'pending')
  const error = computed(() => {
    if (loadStateValue.value.kind === 'error') return loadStateValue.value.error
    if (drilldownStateValue.value.kind === 'error') return drilldownStateValue.value.error
    return null
  })
  const forbidden = computed(() => Boolean(error.value && (error.value.status === 401 || error.value.status === 403)))
  const hasProvenSnapshot = computed(() => hasProvenSnapshotValue.value)

  async function refresh() {
    loadStateValue.value = pendingState(evidence, currentSnapshot())
    try {
      const [providersPayload, invitationsPayload, usersPayload, rolesPayload, sessionsPayload, auditPayload] = await Promise.all([
        operatorFetchJson<ApiAccessProvidersResponse>(`${ACCESS_BASE}/providers`, undefined, 'access-providers'),
        operatorFetchJson<ApiAccessInvitationsResponse>(`${ACCESS_BASE}/invitations?limit=${ACCESS_LIMIT}`, undefined, 'access-invitations'),
        operatorFetchJson<ApiAccessUsersResponse>(`${ACCESS_BASE}/users`, undefined, 'access-users'),
        operatorFetchJson<ApiAccessRolesResponse>(`${ACCESS_BASE}/roles`, undefined, 'access-roles'),
        operatorFetchJson<ApiAccessSessionsResponse>(`${ACCESS_BASE}/sessions?limit=${ACCESS_LIMIT}`, undefined, 'access-sessions'),
        operatorFetchJson<ApiAccessAuditResponse>(`${ACCESS_BASE}/log?limit=${ACCESS_LIMIT}`, undefined, 'access-log'),
      ])

      replaceArray(providersState.value, Array.isArray(providersPayload.providers) ? providersPayload.providers.map(mapProvider) : [])
      replaceArray(invitationsState.value, Array.isArray(invitationsPayload.invitations) ? invitationsPayload.invitations.map(mapInvitation) : [])
      replaceArray(usersState.value, Array.isArray(usersPayload.users) ? usersPayload.users.map(mapUser) : [])
      replaceArray(rolesState.value, Array.isArray(rolesPayload.roles) ? rolesPayload.roles.map(mapRole) : [])
      replaceArray(sessionsState.value, Array.isArray(sessionsPayload.sessions) ? sessionsPayload.sessions.map(mapSession) : [])
      replaceArray(auditState.value, Array.isArray(auditPayload.entries) ? auditPayload.entries.map(mapAuditEntry) : [])
      summaryState.value = {
        authDisabled: Boolean(providersPayload.auth_disabled),
        localLoginEnabled: Boolean(providersPayload.local_login_enabled),
        authentikTrustedProxyCount: Number(providersPayload.authentik_trusted_proxy_count || 0),
      }
      loadStateValue.value = liveState(evidence, currentSnapshot())
      hasProvenSnapshotValue.value = true

      if (selectedUserID.value) {
        await openUser(selectedUserID.value)
      }
    } catch (nextError) {
      const mapped = toOperatorSourceError(nextError, {
        source: 'access-page',
        path: evidence.endpoint,
        method: 'GET',
      })
      loadStateValue.value = errorState(evidence, mapped, {
        source: 'access-page',
        run: async () => {
          await refresh()
          return loadStateValue.value
        },
      }, currentSnapshot())
    }
  }

  async function openUser(userID: number) {
    selectedUserID.value = userID
    const path = `${ACCESS_BASE}/users/${encodeURIComponent(String(userID))}`
    drilldownStateValue.value = pendingState(drilldownEvidence, currentDrilldown())
    try {
      const payload = await operatorFetchJson<ApiAccessDrilldown>(path, undefined, 'access-user-drilldown')
      drilldownData.value = mapDrilldown(payload)
      drilldownStateValue.value = liveState(drilldownEvidence, currentDrilldown())
    } catch (nextError) {
      const mapped = toOperatorSourceError(nextError, {
        source: 'access-user-drilldown',
        path,
        method: 'GET',
      })
      drilldownStateValue.value = errorState(drilldownEvidence, mapped, {
        source: 'access-user-drilldown',
        run: async () => {
          if (selectedUserID.value) {
            await openUser(selectedUserID.value)
          }
          return drilldownStateValue.value
        },
      }, currentDrilldown())
    }
  }

  function closeUser() {
    selectedUserID.value = null
    drilldownData.value = emptyDrilldown()
    drilldownStateValue.value = liveState(drilldownEvidence, currentDrilldown())
  }

  function createInvitation(input: AccessCreateInvitationInput) {
    return runOperatorMutation<ApiAccessCreateInvitationResponse>({
      action: 'access-create-invitation',
      evidence: endpointEvidence(`${ACCESS_BASE}/invitations`, 'access-create-invitation'),
      run: () => operatorFetchJson<ApiAccessCreateInvitationResponse>(`${ACCESS_BASE}/invitations`, jsonInit('POST', {
        email: input.email,
        role: input.role,
        expires_in_hours: input.expiresInHours,
      }), 'access-create-invitation'),
      refresh,
    })
  }

  function revokeInvitation(invitationID: number, reason = 'operator revoked invitation') {
    return runOperatorMutation<ApiAccessMutationReceipt>({
      action: 'access-revoke-invitation',
      evidence: endpointEvidence(`${ACCESS_BASE}/invitations/${invitationID}/revoke`, 'access-revoke-invitation'),
      run: () => operatorFetchJson<ApiAccessMutationReceipt>(`${ACCESS_BASE}/invitations/${encodeURIComponent(String(invitationID))}/revoke`, jsonInit('POST', { reason }), 'access-revoke-invitation'),
      refresh,
    })
  }

  function updateUser(userID: number, input: AccessUpdateUserInput) {
    return runOperatorMutation<ApiAccessUpdateUserResponse>({
      action: 'access-update-user',
      evidence: endpointEvidence(`${ACCESS_BASE}/users/${userID}`, 'access-update-user'),
      run: () => operatorFetchJson<ApiAccessUpdateUserResponse>(`${ACCESS_BASE}/users/${encodeURIComponent(String(userID))}`, jsonInit('PATCH', input), 'access-update-user'),
      refresh: async () => {
        await refresh()
        if (selectedUserID.value === userID) {
          await openUser(userID)
        }
      },
    })
  }

  function revokeSession(sessionID: string, reason = 'operator revoked session') {
    return runOperatorMutation<ApiAccessMutationReceipt>({
      action: 'access-revoke-session',
      evidence: endpointEvidence(`${ACCESS_BASE}/sessions/${sessionID}/revoke`, 'access-revoke-session'),
      run: () => operatorFetchJson<ApiAccessMutationReceipt>(`${ACCESS_BASE}/sessions/${encodeURIComponent(sessionID)}/revoke`, jsonInit('POST', { reason }), 'access-revoke-session'),
      refresh: async () => {
        await refresh()
        if (selectedUserID.value) {
          await openUser(selectedUserID.value)
        }
      },
    })
  }

  startOnce('access-page', refresh)

  return {
    providers: providersState.value,
    invitations: invitationsState.value,
    users: usersState.value,
    roles: rolesState.value,
    sessions: sessionsState.value,
    audit: auditState.value,
    summary,
    loadState,
    drilldownState,
    drilldown,
    selectedUserID,
    pending,
    error,
    forbidden,
    hasProvenSnapshot,
    refresh,
    openUser,
    closeUser,
    createInvitation,
    revokeInvitation,
    updateUser,
    revokeSession,
  }
}
