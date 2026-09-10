<script setup lang="ts">
import { computed, ref } from 'vue'
import { operatorDiagnosisKey } from '../composables/useOperatorApi'
import type { MutationResult } from '../composables/useApi'
import {
  useOperatorAccess,
  type OperatorAccessInvitation,
  type OperatorAccessProvider,
  type OperatorAccessSession,
  type OperatorAccessUser,
} from '../composables/useOperatorAccess'
import {
  useOperatorKeycards,
  type OperatorKeycard,
  type OperatorKeycardPrincipalKind,
  type OperatorKeycardScope,
} from '../composables/useOperatorKeycards'

const { t } = useI18n()
const {
  providers,
  invitations,
  users,
  roles,
  sessions,
  audit,
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
} = useOperatorAccess()

const {
  keycards,
  loadState: keycardLoadState,
  error: keycardError,
  refresh: refreshKeycards,
  createKeycard,
  revokeKeycard,
} = useOperatorKeycards()

const inviteForm = ref({
  email: '',
  role: 'operator',
  expiresInHours: 72,
})
const keycardForm = ref<{
  name: string
  scope: OperatorKeycardScope
  principal: string
  principalKind: OperatorKeycardPrincipalKind
  expiresAt: string
}>({
  name: '',
  scope: 'read-write',
  principal: '',
  principalKind: 'human',
  expiresAt: '',
})
const notice = ref<{ kind: 'success' | 'error'; text: string } | null>(null)
const inviteBusy = ref(false)
const inviteActionID = ref<number | null>(null)
const userActionID = ref<number | null>(null)
const sessionActionID = ref<string | null>(null)
const localAuthBlocked = ref(false)
const keycardBusy = ref(false)
const keycardActionID = ref<string | null>(null)
const mutationResult = ref<MutationResult | null>(null)
const route = useRoute()

const enabledProviderCount = computed(() => providers.filter((provider) => provider.enabled).length)
const pendingInvitationCount = computed(() => invitations.filter((invite) => invite.status === 'pending').length)
const activeSessionCount = computed(() => sessions.filter((session) => session.status === 'active').length)
const adminCount = computed(() => roles.find((role) => role.role === 'admin')?.userCount ?? 0)
const loadErrorMessage = computed(() => error.value ? t(operatorDiagnosisKey(error.value)) : null)
const drilldownErrorMessage = computed(() => drilldownState.value.kind === 'error' ? t(operatorDiagnosisKey(drilldownState.value.error)) : null)
const drilldownPending = computed(() => drilldownState.value.kind === 'pending')
const selectedUser = computed(() => drilldown.value.user)
const showDrilldown = computed(() => Boolean(selectedUserID.value))
const accessPresentation = computed(() => {
  if (localAuthBlocked.value || error.value?.status === 401) return 'unauthorized'
  if (forbidden.value) return 'forbidden'
  if (loadState.value.kind === 'pending') return 'pending'
  if (loadState.value.kind === 'error') return hasProvenSnapshot.value ? 'stale-snapshot' : 'error'
  return loadState.value.kind
})
const accessLocked = computed(() => ['unauthorized', 'forbidden', 'error'].includes(accessPresentation.value))
const accessAvailable = computed(() => ['live', 'empty', 'stale-snapshot'].includes(accessPresentation.value))
const accessMutationDisabled = computed(() => !['live', 'empty'].includes(accessPresentation.value) || summary.value.authDisabled)
const accessCapability = computed(() => 'live')
const accessRuntimeLabel = computed(() => t(`access.state.runtime.${accessPresentation.value}`))
const currentRoleLabel = computed(() => selectedUser.value ? roleLabel(selectedUser.value.role) : '')
const keycardPending = computed(() => keycardLoadState.value.kind === 'pending')
const keycardErrorMessage = computed(() => keycardError.value ? t(operatorDiagnosisKey(keycardError.value)) : '')
const keycardMutationDisabled = computed(() => accessMutationDisabled.value || keycardLoadState.value.kind !== 'live')

function relativeTime(value: string | null | undefined) {
  if (!value) return '—'
  const parsed = Date.parse(value)
  if (Number.isNaN(parsed)) return value
  const seconds = Math.max(0, Math.floor((Date.now() - parsed) / 1000))
  if (seconds < 60) return t('access.time.justNow')
  if (seconds < 3600) return t('access.time.minutesAgo', { count: Math.floor(seconds / 60) })
  if (seconds < 86400) return t('access.time.hoursAgo', { count: Math.floor(seconds / 3600) })
  return t('access.time.daysAgo', { count: Math.floor(seconds / 86400) })
}

function absoluteTime(value: string | null | undefined) {
  if (!value) return '—'
  const parsed = Date.parse(value)
  if (Number.isNaN(parsed)) return value
  return new Date(parsed).toLocaleString()
}

function roleLabel(role: string) {
  const key = `access.roles.labels.${role}`
  const translated = t(key)
  return translated === key ? role : translated
}

function providerHonestyLabel(provider: OperatorAccessProvider) {
  const key = `access.providers.honesty.${provider.honesty}`
  const translated = t(key)
  return translated === key ? provider.honesty : translated
}

function invitationStatusLabel(invitation: OperatorAccessInvitation) {
  const key = `access.invitations.status.${invitation.status}`
  const translated = t(key)
  return translated === key ? invitation.status : translated
}

function invitationTone(invitation: OperatorAccessInvitation) {
  switch (invitation.status) {
    case 'pending':
      return 'live'
    case 'used':
      return 'muted'
    case 'expired':
    case 'revoked':
      return 'warn'
    default:
      return 'muted'
  }
}

function sessionStatusLabel(session: OperatorAccessSession) {
  const key = `access.sessions.status.${session.status}`
  const translated = t(key)
  return translated === key ? session.status : translated
}

function auditActionLabel(action: string) {
  const key = `access.audit.actions.${action}`
  const translated = t(key)
  return translated === key ? action : translated
}

function sessionTone(session: OperatorAccessSession) {
  switch (session.status) {
    case 'active':
      return 'live'
    case 'expired':
      return 'muted'
    case 'revoked':
      return 'warn'
    default:
      return 'muted'
  }
}

function canMutate() {
  return !accessMutationDisabled.value
}

function clearNotice() {
  notice.value = null
}


function keycardExpiry(value: string): string | null {
  const trimmed = value.trim()
  if (!trimmed) return null
  const parsed = new Date(trimmed)
  return Number.isNaN(parsed.getTime()) ? trimmed : parsed.toISOString()
}

function keycardStatus(keycard: OperatorKeycard): 'active' | 'revoked' | 'expired' {
  if (keycard.revoked) return 'revoked'
  if (keycard.expiresAt && Date.parse(keycard.expiresAt) <= Date.now()) return 'expired'
  return 'active'
}

function keycardTone(keycard: OperatorKeycard) {
  const status = keycardStatus(keycard)
  switch (status) {
    case 'active':
      return 'live'
    case 'revoked':
      return 'warn'
    case 'expired':
      return 'muted'
    default: {
      const exhaustive: never = status
      return exhaustive
    }
  }
}

async function submitKeycard() {
  if (keycardBusy.value || keycardMutationDisabled.value) return
  const name = keycardForm.value.name.trim()
  const principal = keycardForm.value.principal.trim()
  if (!name || !principal) {
    notice.value = { kind: 'error', text: t('access.notice.keycardInvalid') }
    return
  }

  keycardBusy.value = true
  try {
    const result = await createKeycard({
      name,
      scope: keycardForm.value.scope,
      principal,
      principalKind: keycardForm.value.principalKind,
      expiresAt: keycardExpiry(keycardForm.value.expiresAt),
    })
    mutationResult.value = result
    if (result.kind === 'committed_verified') {
      keycardForm.value.name = ''
      keycardForm.value.principal = ''
      keycardForm.value.expiresAt = ''
      notice.value = null
    }
  } finally {
    keycardBusy.value = false
  }
}

async function onRevokeKeycard(keycard: OperatorKeycard) {
  if (keycardActionID.value === keycard.id || keycardMutationDisabled.value) return
  keycardActionID.value = keycard.id
  try {
    const result = await revokeKeycard(keycard.id)
    mutationResult.value = result
    if (result.kind === 'committed_verified') notice.value = null
  } finally {
    keycardActionID.value = null
  }
}


async function recheckMutations() {
  await Promise.all([recoverAccess(), refreshKeycards()])
}

async function recoverAccess() {
  await refresh()
  if (!error.value) {
    localAuthBlocked.value = false
  }
}

async function submitInvitation() {
  if (inviteBusy.value) return
  const email = inviteForm.value.email.trim()
  if (!email) {
    notice.value = { kind: 'error', text: t('access.notice.inviteInvalid') }
    return
  }
  if (!canMutate()) {
    notice.value = { kind: 'error', text: t('access.notice.authDisabled') }
    return
  }
  inviteBusy.value = true
  try {
    const result = await createInvitation({
      email,
      role: inviteForm.value.role,
      expiresInHours: inviteForm.value.expiresInHours,
    })
    mutationResult.value = result
    if (result.kind === 'committed_verified') {
      inviteForm.value.email = ''
      notice.value = null
    }
  } finally {
    inviteBusy.value = false
  }
}

async function onRevokeInvitation(invitation: OperatorAccessInvitation) {
  if (inviteActionID.value === invitation.id || !canMutate()) return
  inviteActionID.value = invitation.id
  try {
    const result = await revokeInvitation(invitation.id, t('access.notice.inviteRevokedReason'))
    mutationResult.value = result
    if (result.kind === 'committed_verified') notice.value = null
  } finally {
    inviteActionID.value = null
  }
}

async function onToggleUserRole(user: OperatorAccessUser) {
  if (userActionID.value === user.id || !canMutate()) return
  userActionID.value = user.id
  const nextRole = user.role === 'admin' ? 'operator' : 'admin'
  try {
    const result = await updateUser(user.id, { role: nextRole })
    mutationResult.value = result
    if (result.kind === 'committed_verified') notice.value = null
  } finally {
    userActionID.value = null
  }
}

async function onToggleUserDisabled(user: OperatorAccessUser) {
  if (userActionID.value === user.id || !canMutate()) return
  userActionID.value = user.id
  const nextDisabled = !user.disabled
  try {
    const result = await updateUser(user.id, { disabled: nextDisabled })
    mutationResult.value = result
    if (result.kind === 'committed_verified') notice.value = null
  } finally {
    userActionID.value = null
  }
}

async function onRevokeSession(session: OperatorAccessSession) {
  if (sessionActionID.value === session.id || !canMutate()) return
  sessionActionID.value = session.id
  try {
    const result = await revokeSession(session.id, t('access.notice.sessionRevokedReason'))
    mutationResult.value = result
    if (result.kind === 'committed_verified') notice.value = null
  } finally {
    sessionActionID.value = null
  }
}

async function selectUser(user: OperatorAccessUser) {
  clearNotice()
  await openUser(user.id)
}
</script>

<template>
  <div class="access-page" :data-state="accessPresentation">
    <header class="page-head">
      <div>
        <h1>{{ t('access.title') }}</h1>
        <p>{{ t('access.subtitle') }}</p>
      </div>
      <div class="runtime-state" :data-state="accessPresentation" role="status">{{ accessRuntimeLabel }}</div>
      <HonestyBadge v-if="['live', 'empty'].includes(accessPresentation)" :cls="accessCapability" evidence="/api/access/*" />
    </header>

    <section v-if="accessLocked" class="guard-panel" role="alert">
      <strong>{{ accessRuntimeLabel }}</strong>
      <p>{{ accessPresentation === 'unauthorized' ? t('access.state.unauthorizedBody') : accessPresentation === 'forbidden' ? t('access.state.forbiddenBody') : loadErrorMessage }}</p>
      <details v-if="error"><summary>{{ t('access.state.technicalEvidence') }}</summary><code>{{ error.method }} {{ error.path }} · {{ error.status || 'network' }}</code></details>
      <button class="tbtn" @click="recoverAccess">{{ t('access.actions.retry') }}</button>
    </section>

    <section v-else-if="accessPresentation === 'pending'" class="statebar pending" role="status">
      <span>{{ t('access.state.pending') }}</span>
    </section>

    <template v-else-if="accessAvailable">
      <section v-if="accessPresentation === 'stale-snapshot'" class="statebar error" role="status" data-state="stale-snapshot">
        <span>{{ t('access.state.stale', { updatedAt: loadState.updatedAt, message: loadErrorMessage }) }}</span>
        <details v-if="error"><summary>{{ t('access.state.technicalEvidence') }}</summary><code>{{ error.method }} {{ error.path }} · {{ error.status || 'network' }}</code></details>
        <button class="tbtn" @click="refresh">{{ t('access.actions.retry') }}</button>
      </section>
      <section class="access-brief">
        <div class="metric">
          <b>{{ enabledProviderCount }}</b>
          <span>{{ t('access.metrics.providers') }}</span>
        </div>
        <div class="metric">
          <b>{{ pendingInvitationCount }}</b>
          <span>{{ t('access.metrics.invites') }}</span>
        </div>
        <div class="metric">
          <b>{{ adminCount }}</b>
          <span>{{ t('access.metrics.admins') }}</span>
        </div>
        <div class="metric">
          <b>{{ activeSessionCount }}</b>
          <span>{{ t('access.metrics.sessions') }}</span>
        </div>
        <div class="brief-copy">
          <strong>{{ t('access.brief.title') }}</strong>
          <span>{{ t('access.brief.body', { proxies: summary.authentikTrustedProxyCount }) }}</span>
        </div>
      </section>

      <section v-if="summary.authDisabled" class="statebar warn">
        <span>{{ t('access.state.authDisabled') }}</span>
      </section>
      <section v-else-if="notice" class="statebar" :data-kind="notice.kind">
        <span>{{ notice.text }}</span>
        <button class="tbtn" @click="notice = null">{{ t('common.hide') }}</button>
      </section>
      <MutationResultNotice :result="mutationResult" :recheck-label="t('access.actions.refresh')" @recheck="recheckMutations" />

      <div class="access-grid">
        <section class="panel providers-panel">
          <div class="panel-head">
            <div>
              <h2>{{ t('access.providers.title') }}</h2>
              <p>{{ t('access.providers.subtitle') }}</p>
            </div>
            <button class="tbtn" @click="refresh">{{ t('access.actions.refresh') }}</button>
          </div>
          <div class="providers-grid">
            <article v-for="provider in providers" :key="provider.id" class="provider-card">
              <div class="provider-head">
                <div>
                  <strong>{{ provider.label }}</strong>
                  <span>{{ t('access.providers.kind', { kind: provider.kind }) }}</span>
                </div>
                <span class="pill" :data-kind="provider.enabled ? 'live' : 'muted'">{{ providerHonestyLabel(provider) }}</span>
              </div>
              <p>{{ provider.description }}</p>
              <dl class="mini-fields">
                <div>
                  <dt>{{ t('access.providers.fields.evidence') }}</dt>
                  <dd>{{ provider.evidence || '—' }}</dd>
                </div>
                <div>
                  <dt>{{ t('access.providers.fields.operable') }}</dt>
                  <dd>{{ provider.operable ? t('access.providers.values.yes') : t('access.providers.values.no') }}</dd>
                </div>
              </dl>
            </article>
          </div>
        </section>

        <section class="panel keycards-panel" data-testid="keycards-panel">
          <div class="panel-head">
            <div>
              <h2>{{ t('access.keycards.title') }}</h2>
              <p>{{ t('access.keycards.subtitle') }}</p>
            </div>
            <button class="tbtn" type="button" :disabled="keycardPending" @click="refreshKeycards">{{ t('access.actions.refresh') }}</button>
          </div>
          <section v-if="keycardPending" class="keycard-state" role="status">
            <span>{{ t('access.keycards.pending') }}</span>
          </section>
          <section v-else-if="keycardError" class="keycard-state error" role="alert">
            <strong>{{ t('access.keycards.errorTitle') }}</strong>
            <p>{{ t('access.keycards.error', { message: keycardErrorMessage }) }}</p>
            <details><summary>{{ t('access.state.technicalEvidence') }}</summary><code>{{ keycardError.method }} {{ keycardError.path }} · {{ keycardError.status || 'network' }}</code></details>
            <button class="tbtn" type="button" @click="refreshKeycards">{{ t('access.actions.retry') }}</button>
          </section>
          <template v-else>
            <form class="keycard-form" @submit.prevent="submitKeycard">
              <label>
                <span>{{ t('access.keycards.form.name') }}</span>
                <input id="access-keycard-name" v-model="keycardForm.name" name="access-keycard-name" class="input" type="text" autocomplete="off" required :disabled="keycardMutationDisabled" :placeholder="t('access.keycards.form.namePlaceholder')">
              </label>
              <label>
                <span>{{ t('access.keycards.form.scope') }}</span>
                <select id="access-keycard-scope" v-model="keycardForm.scope" name="access-keycard-scope" class="select" :disabled="keycardMutationDisabled">
                  <option value="read-write">{{ t('access.keycards.scopes.readWrite') }}</option>
                  <option value="read-only">{{ t('access.keycards.scopes.readOnly') }}</option>
                </select>
              </label>
              <label>
                <span>{{ t('access.keycards.form.principal') }}</span>
                <input id="access-keycard-principal" v-model="keycardForm.principal" name="access-keycard-principal" class="input" type="text" autocomplete="off" required :disabled="keycardMutationDisabled" :placeholder="t('access.keycards.form.principalPlaceholder')">
              </label>
              <label>
                <span>{{ t('access.keycards.form.principalKind') }}</span>
                <select id="access-keycard-principal-kind" v-model="keycardForm.principalKind" name="access-keycard-principal-kind" class="select" :disabled="keycardMutationDisabled">
                  <option value="human">{{ t('access.keycards.principalKinds.human') }}</option>
                  <option value="agent">{{ t('access.keycards.principalKinds.agent') }}</option>
                  <option value="service">{{ t('access.keycards.principalKinds.service') }}</option>
                </select>
              </label>
              <label>
                <span>{{ t('access.keycards.form.expiresAt') }}</span>
                <input id="access-keycard-expires-at" v-model="keycardForm.expiresAt" name="access-keycard-expires-at" class="input" type="datetime-local" :disabled="keycardMutationDisabled">
                <small>{{ t('access.keycards.form.expiresAtHint') }}</small>
              </label>
              <button class="act primary" data-testid="keycard-issue" type="submit" :disabled="keycardBusy || keycardMutationDisabled">{{ keycardBusy ? t('access.actions.working') : t('access.keycards.form.submit') }}</button>
            </form>
            <table class="tbl">
              <thead>
                <tr>
                  <th>{{ t('access.keycards.columns.name') }}</th>
                  <th>{{ t('access.keycards.columns.identity') }}</th>
                  <th>{{ t('access.keycards.columns.scope') }}</th>
                  <th>{{ t('access.keycards.columns.expires') }}</th>
                  <th>{{ t('access.keycards.columns.activity') }}</th>
                  <th>{{ t('access.keycards.columns.status') }}</th>
                  <th class="right">{{ t('access.keycards.columns.actions') }}</th>
                </tr>
              </thead>
              <tbody>
                <tr v-if="!keycards.length">
                  <td colspan="7" class="empty-row">{{ t('access.keycards.empty') }}</td>
                </tr>
                <tr v-for="keycard in keycards" :key="keycard.id" :data-status="keycardStatus(keycard)" :data-testid="`keycard-row-${keycard.id}`">
                  <td>
                    <div class="row-main">{{ keycard.name }}</div>
                    <div class="row-sub mono">{{ keycard.tokenPrefix ? `engram_${keycard.tokenPrefix}…` : '—' }}</div>
                  </td>
                  <td>
                    <div class="row-main">{{ keycard.principal || '—' }}</div>
                    <div class="row-sub">{{ keycard.principalKind || '—' }}</div>
                  </td>
                  <td class="mono small">{{ keycard.scope || '—' }}</td>
                  <td>
                    <div class="row-main">{{ relativeTime(keycard.expiresAt) }}</div>
                    <div class="row-sub">{{ absoluteTime(keycard.expiresAt) }}</div>
                  </td>
                  <td>
                    <div class="row-main">{{ relativeTime(keycard.lastUsedAt) }}</div>
                    <div class="row-sub">{{ t('access.keycards.requestCount', { count: keycard.requestCount, errors: keycard.errorCount }) }}</div>
                  </td>
                  <td><span class="pill" :data-kind="keycardTone(keycard)">{{ t(`access.keycards.status.${keycardStatus(keycard)}`) }}</span></td>
                  <td class="right">
                    <button class="act danger" :data-testid="`keycard-revoke-${keycard.id}`" :disabled="keycardActionID === keycard.id || keycard.revoked || keycardMutationDisabled" @click="onRevokeKeycard(keycard)">
                      {{ keycardActionID === keycard.id ? t('access.actions.working') : t('access.actions.revoke') }}
                    </button>
                  </td>
                </tr>
              </tbody>
            </table>
          </template>
        </section>

        <section class="panel invites-panel">
          <div class="panel-head">
            <div>
              <h2>{{ t('access.invitations.title') }}</h2>
              <p>{{ t('access.invitations.subtitle') }}</p>
            </div>
          </div>
          <form class="invite-form" @submit.prevent="submitInvitation">
            <label>
              <span>{{ t('access.invitations.form.email') }}</span>
              <input id="access-invitation-email" v-model="inviteForm.email" name="access-invitation-email" class="input" type="email" :disabled="accessMutationDisabled" :placeholder="t('access.invitations.form.emailPlaceholder')">
            </label>
            <label>
              <span>{{ t('access.invitations.form.role') }}</span>
              <select id="access-invitation-role" v-model="inviteForm.role" name="access-invitation-role" class="select" :disabled="accessMutationDisabled">
                <option value="operator">{{ roleLabel('operator') }}</option>
                <option value="admin">{{ roleLabel('admin') }}</option>
              </select>
            </label>
            <label>
              <span>{{ t('access.invitations.form.ttl') }}</span>
              <select id="access-invitation-ttl" v-model="inviteForm.expiresInHours" name="access-invitation-ttl" class="select" :disabled="accessMutationDisabled">
                <option :value="24">{{ t('access.invitations.form.ttl24') }}</option>
                <option :value="72">{{ t('access.invitations.form.ttl72') }}</option>
                <option :value="168">{{ t('access.invitations.form.ttl168') }}</option>
              </select>
            </label>
            <button class="act primary" type="submit" :disabled="inviteBusy || accessMutationDisabled">{{ inviteBusy ? t('access.actions.working') : t('access.invitations.form.submit') }}</button>
          </form>
          <table class="tbl">
            <thead>
              <tr>
                <th>{{ t('access.invitations.columns.email') }}</th>
                <th>{{ t('access.invitations.columns.role') }}</th>
                <th>{{ t('access.invitations.columns.status') }}</th>
                <th>{{ t('access.invitations.columns.expires') }}</th>
                <th>{{ t('access.invitations.columns.issuedBy') }}</th>
                <th class="right">{{ t('access.invitations.columns.actions') }}</th>
              </tr>
            </thead>
            <tbody>
              <tr v-if="!invitations.length">
                <td colspan="6" class="empty-row">{{ t('access.invitations.empty') }}</td>
              </tr>
              <tr v-for="invitation in invitations" :key="invitation.id">
                <td>
                  <div class="row-main">{{ invitation.email || '—' }}</div>
                </td>
                <td>{{ roleLabel(invitation.role) }}</td>
                <td><span class="pill" :data-kind="invitationTone(invitation)">{{ invitationStatusLabel(invitation) }}</span></td>
                <td>
                  <div class="row-main">{{ relativeTime(invitation.expiresAt) }}</div>
                  <div class="row-sub">{{ absoluteTime(invitation.expiresAt) }}</div>
                </td>
                <td>{{ invitation.createdByEmail || '—' }}</td>
                <td class="right">
                  <button class="act danger" :disabled="inviteActionID === invitation.id || invitation.status !== 'pending' || accessMutationDisabled" @click="onRevokeInvitation(invitation)">
                    {{ inviteActionID === invitation.id ? t('access.actions.working') : t('access.actions.revoke') }}
                  </button>
                </td>
              </tr>
            </tbody>
          </table>
        </section>

        <section class="panel users-panel">
          <div class="panel-head">
            <div>
              <h2>{{ t('access.users.title') }}</h2>
              <p>{{ t('access.users.subtitle') }}</p>
            </div>
          </div>
          <table class="tbl">
            <thead>
              <tr>
                <th>{{ t('access.users.columns.user') }}</th>
                <th>{{ t('access.users.columns.role') }}</th>
                <th>{{ t('access.users.columns.status') }}</th>
                <th>{{ t('access.users.columns.lastLogin') }}</th>
                <th class="right">{{ t('access.users.columns.actions') }}</th>
              </tr>
            </thead>
            <tbody>
              <tr v-if="!users.length">
                <td colspan="5" class="empty-row">{{ t('access.users.empty') }}</td>
              </tr>
              <tr v-for="user in users" :key="user.id" :class="{ selected: selectedUserID === user.id }">
                <td>
                  <button class="linkish" @click="selectUser(user)">
                    <span class="row-main">{{ user.email }}</span>
                    <span class="row-sub">{{ absoluteTime(user.createdAt) }}</span>
                  </button>
                </td>
                <td><span class="pill" :data-kind="user.role === 'admin' ? 'live' : 'muted'">{{ roleLabel(user.role) }}</span></td>
                <td><span class="pill" :data-kind="user.disabled ? 'warn' : 'live'">{{ user.disabled ? t('access.users.status.disabled') : t('access.users.status.active') }}</span></td>
                <td>{{ relativeTime(user.lastLoginAt) }}</td>
                <td class="right actions-cell">
                  <button class="act" :disabled="userActionID === user.id || accessMutationDisabled" @click="onToggleUserRole(user)">
                    {{ user.role === 'admin' ? t('access.actions.makeOperator') : t('access.actions.makeAdmin') }}
                  </button>
                  <button class="act danger" :disabled="userActionID === user.id || accessMutationDisabled" @click="onToggleUserDisabled(user)">
                    {{ user.disabled ? t('access.actions.enable') : t('access.actions.disable') }}
                  </button>
                </td>
              </tr>
            </tbody>
          </table>
        </section>

        <section class="panel roles-panel">
          <div class="panel-head">
            <div>
              <h2>{{ t('access.roles.title') }}</h2>
              <p>{{ t('access.roles.subtitle') }}</p>
            </div>
          </div>
          <div class="roles-grid">
            <article v-for="role in roles" :key="role.role" class="role-card">
              <span class="pill" :data-kind="role.role === 'admin' ? 'live' : 'muted'">{{ roleLabel(role.role) }}</span>
              <strong>{{ role.userCount }}</strong>
              <p>{{ t(`access.roles.descriptions.${role.role}`) }}</p>
            </article>
          </div>
        </section>

        <section class="panel sessions-panel">
          <div class="panel-head">
            <div>
              <h2>{{ t('access.sessions.title') }}</h2>
              <p>{{ t('access.sessions.subtitle') }}</p>
            </div>
          </div>
          <table class="tbl">
            <thead>
              <tr>
                <th>{{ t('access.sessions.columns.user') }}</th>
                <th>{{ t('access.sessions.columns.status') }}</th>
                <th>{{ t('access.sessions.columns.started') }}</th>
                <th>{{ t('access.sessions.columns.expires') }}</th>
                <th>{{ t('access.sessions.columns.agent') }}</th>
                <th class="right">{{ t('access.sessions.columns.actions') }}</th>
              </tr>
            </thead>
            <tbody>
              <tr v-if="!sessions.length">
                <td colspan="6" class="empty-row">{{ t('access.sessions.empty') }}</td>
              </tr>
              <tr v-for="session in sessions" :key="session.id">
                <td>
                  <div class="row-main">{{ session.userEmail }}</div>
                  <div class="row-sub mono">{{ session.remoteAddr || session.id }}</div>
                </td>
                <td><span class="pill" :data-kind="sessionTone(session)">{{ sessionStatusLabel(session) }}</span></td>
                <td>{{ relativeTime(session.createdAt) }}</td>
                <td>{{ relativeTime(session.expiresAt) }}</td>
                <td class="mono small">{{ session.userAgent || '—' }}</td>
                <td class="right">
                  <button class="act danger" :disabled="sessionActionID === session.id || session.status !== 'active' || accessMutationDisabled" @click="onRevokeSession(session)">
                    {{ sessionActionID === session.id ? t('access.actions.working') : t('access.actions.revoke') }}
                  </button>
                </td>
              </tr>
            </tbody>
          </table>
        </section>

        <section class="panel audit-panel">
          <div class="panel-head">
            <div>
              <h2>{{ t('access.audit.title') }}</h2>
              <p>{{ t('access.audit.subtitle') }}</p>
            </div>
          </div>
          <table class="tbl">
            <thead>
              <tr>
                <th>{{ t('access.audit.columns.time') }}</th>
                <th>{{ t('access.audit.columns.actor') }}</th>
                <th>{{ t('access.audit.columns.action') }}</th>
                <th>{{ t('access.audit.columns.reason') }}</th>
              </tr>
            </thead>
            <tbody>
              <tr v-if="!audit.length">
                <td colspan="4" class="empty-row">{{ t('access.audit.empty') }}</td>
              </tr>
              <tr v-for="entry in audit" :key="entry.id">
                <td>{{ relativeTime(entry.createdAt) }}</td>
                <td>{{ entry.actor || 'system' }}</td>
                <td><span class="pill" data-kind="muted">{{ auditActionLabel(entry.action) }}</span></td>
                <td>{{ entry.reason || '—' }}</td>
              </tr>
            </tbody>
          </table>
        </section>

        <aside class="panel drilldown-panel" v-if="showDrilldown">
          <div class="panel-head">
            <div>
              <h2>{{ t('access.drilldown.title') }}</h2>
              <p v-if="selectedUser">{{ selectedUser.email }} · {{ currentRoleLabel }}</p>
            </div>
            <button class="tbtn" @click="closeUser">{{ t('access.actions.close') }}</button>
          </div>
          <div v-if="drilldownPending" class="empty-card">
            <strong>{{ t('access.drilldown.loadingTitle') }}</strong>
            <span>{{ t('access.drilldown.loadingBody') }}</span>
          </div>
          <div v-else-if="drilldownErrorMessage" class="empty-card">
            <strong>{{ t('access.drilldown.errorTitle') }}</strong>
            <span>{{ t('access.drilldown.errorBody', { message: drilldownErrorMessage }) }}</span>
          </div>
          <template v-else-if="selectedUser">
            <dl class="mini-fields">
              <div>
                <dt>{{ t('access.drilldown.fields.created') }}</dt>
                <dd>{{ absoluteTime(selectedUser.createdAt) }}</dd>
              </div>
              <div>
                <dt>{{ t('access.drilldown.fields.lastLogin') }}</dt>
                <dd>{{ relativeTime(selectedUser.lastLoginAt) }}</dd>
              </div>
              <div>
                <dt>{{ t('access.drilldown.fields.disabled') }}</dt>
                <dd>{{ selectedUser.disabled ? t('access.users.status.disabled') : t('access.users.status.active') }}</dd>
              </div>
            </dl>
            <section class="drill-block">
              <h3>{{ t('access.drilldown.sessionsTitle') }}</h3>
              <ul class="stack-list">
                <li v-if="!drilldown.sessions.length" class="empty-row">{{ t('access.drilldown.emptySessions') }}</li>
                <li v-for="session in drilldown.sessions" :key="session.id">
                  <strong>{{ sessionStatusLabel(session) }}</strong>
                  <span>{{ relativeTime(session.createdAt) }} · {{ session.remoteAddr || session.userAgent || session.id }}</span>
                </li>
              </ul>
            </section>
            <section class="drill-block">
              <h3>{{ t('access.drilldown.invitesCreatedTitle') }}</h3>
              <ul class="stack-list">
                <li v-if="!drilldown.invitationsCreated.length" class="empty-row">{{ t('access.drilldown.emptyInvitesCreated') }}</li>
                <li v-for="invite in drilldown.invitationsCreated" :key="`created-${invite.id}`">
                  <strong>{{ invite.email || '—' }}</strong>
                  <span>{{ invitationStatusLabel(invite) }} · {{ relativeTime(invite.createdAt) }}</span>
                </li>
              </ul>
            </section>
            <section class="drill-block">
              <h3>{{ t('access.drilldown.invitesUsedTitle') }}</h3>
              <ul class="stack-list">
                <li v-if="!drilldown.invitationsUsed.length" class="empty-row">{{ t('access.drilldown.emptyInvitesUsed') }}</li>
                <li v-for="invite in drilldown.invitationsUsed" :key="`used-${invite.id}`">
                  <strong>{{ invite.email || '—' }}</strong>
                  <span>{{ invitationStatusLabel(invite) }} · {{ relativeTime(invite.usedAt || invite.createdAt) }}</span>
                </li>
              </ul>
            </section>
            <section class="drill-block">
              <h3>{{ t('access.drilldown.auditTitle') }}</h3>
              <ul class="stack-list">
                <li v-if="!drilldown.audit.length" class="empty-row">{{ t('access.drilldown.emptyAudit') }}</li>
                <li v-for="entry in drilldown.audit" :key="entry.id">
                  <strong>{{ auditActionLabel(entry.action) }}</strong>
                  <span>{{ relativeTime(entry.createdAt) }} · {{ entry.reason || '—' }}</span>
                </li>
              </ul>
            </section>
          </template>
        </aside>
      </div>
    </template>
  </div>
</template>

<style scoped>
.access-page { display:flex; flex-direction:column; gap:14px; }
.page-head { display:flex; align-items:flex-start; justify-content:space-between; gap:18px; padding-bottom:14px; border-bottom:1px solid var(--border); }
.page-head h1 { margin:0 0 4px; font-size:var(--text-xl); font-weight:800; letter-spacing:var(--tracking-display); }
.page-head p { margin:0; color:var(--muted); font-size:var(--text-sm); }
.access-brief { display:grid; grid-template-columns:repeat(4, minmax(120px, 180px)) minmax(260px, 1fr); gap:12px; }
.metric, .brief-copy, .panel, .guard-panel, .statebar { border:1px solid var(--border); border-radius:var(--r-md); background:var(--surface); }
.keycard-state { display:grid; gap:8px; margin:0; padding:14px; border:1px solid color-mix(in oklab,var(--accent),transparent 48%); border-radius:var(--r-sm); background:color-mix(in oklab,var(--accent),transparent 92%); }
.keycard-state p { margin:0; color:var(--fg-2); font-size:var(--text-sm); }
.keycard-state.error { border-color:color-mix(in oklab,var(--state-warn),transparent 45%); }
.metric, .brief-copy, .guard-panel, .statebar { padding:14px; }
.metric { display:flex; flex-direction:column; gap:3px; }
.metric b { font-family:var(--font-mono); font-size:var(--text-xl); line-height:1; color:var(--fg); }
.metric span, .brief-copy span, .guard-panel p { color:var(--muted); font-size:var(--text-xs); }
.brief-copy { display:flex; flex-direction:column; justify-content:center; gap:5px; }
.brief-copy strong, .guard-panel strong { color:var(--fg-2); font-size:var(--text-sm); }
.guard-panel { display:flex; flex-direction:column; gap:10px; }
.guard-panel p { margin:0; }
.statebar { display:flex; align-items:center; justify-content:space-between; gap:12px; font-size:var(--text-sm); }
.statebar.pending { border-color:color-mix(in oklab,var(--accent),transparent 55%); }
.statebar.error, .statebar.warn { border-color:color-mix(in oklab,var(--state-warn),transparent 45%); }
.statebar[data-kind="success"] { border-color:color-mix(in oklab,var(--class-live),transparent 45%); }
.access-grid { display:grid; grid-template-columns:minmax(0, 1.25fr) minmax(0, 1.25fr) minmax(300px, .9fr); gap:12px; align-items:start; }
.panel { padding:14px; display:flex; flex-direction:column; gap:12px; }
.providers-panel, .keycards-panel, .invites-panel, .users-panel, .sessions-panel, .audit-panel { grid-column:span 2; }
.roles-panel, .drilldown-panel { grid-column:3; }
.panel-head { display:flex; align-items:flex-start; justify-content:space-between; gap:10px; }
.panel-head h2 { margin:0; font-size:var(--text-sm); font-weight:900; letter-spacing:.04em; text-transform:uppercase; }
.panel-head p { margin:4px 0 0; color:var(--muted); font-size:var(--text-xs); }
.providers-grid, .roles-grid { display:grid; grid-template-columns:repeat(2, minmax(0, 1fr)); gap:12px; }
.provider-card, .role-card { border:1px solid var(--border); border-radius:var(--r-sm); background:var(--surface-warm); padding:12px; display:flex; flex-direction:column; gap:8px; }
.provider-head { display:flex; align-items:flex-start; justify-content:space-between; gap:10px; }
.provider-head strong { color:var(--fg); font-size:var(--text-sm); }
.provider-head span { color:var(--muted); font-size:var(--text-xs); }
.provider-card p, .role-card p { margin:0; color:var(--muted); font-size:var(--text-xs); }
.role-card strong { font-family:var(--font-mono); font-size:var(--text-xl); color:var(--fg); }
.invite-form { display:grid; grid-template-columns:minmax(0, 1.5fr) 180px 180px 160px; gap:10px; align-items:end; }
.keycard-form { display:grid; grid-template-columns:minmax(150px, 1.3fr) minmax(120px, .8fr) minmax(150px, 1.2fr) minmax(120px, .8fr) minmax(180px, 1fr) auto; gap:10px; align-items:end; }
.keycard-form label { display:flex; flex-direction:column; gap:6px; color:var(--muted); font-size:var(--text-xs); }
.keycard-form small { color:var(--muted); font-size:var(--text-xs); }
.invite-form label { display:flex; flex-direction:column; gap:6px; color:var(--muted); font-size:var(--text-xs); }
.select, .input { min-height:34px; border:1px solid var(--border); border-radius:var(--r-sm); background:var(--surface); color:var(--fg); padding:0 10px; font-size:var(--text-sm); }
.tbl { width:100%; border-collapse:collapse; font-size:var(--text-sm); }
.tbl th, .tbl td { padding:10px 8px; border-top:1px solid var(--border); vertical-align:top; text-align:left; }
.tbl th.right, .tbl td.right { text-align:right; }
.row-main { color:var(--fg); font-weight:700; }
.row-sub { color:var(--muted); font-size:var(--text-xs); }
.mono { font-family:var(--font-mono); }
.small { font-size:var(--text-xs); }
.pill { display:inline-flex; align-items:center; justify-content:center; min-height:24px; padding:4px 8px; border:1px solid var(--border); border-radius:999px; font-size:var(--text-xs); font-weight:800; }
.pill[data-kind="live"] { border-color:color-mix(in oklab,var(--class-live),transparent 45%); color:var(--class-live); }
.pill[data-kind="warn"] { border-color:color-mix(in oklab,var(--state-warn),transparent 45%); color:var(--state-warn); }
.pill[data-kind="muted"] { color:var(--muted); }
.act, .tbtn, .linkish { display:inline-flex; align-items:center; justify-content:center; min-height:32px; padding:6px 10px; border:1px solid var(--border); border-radius:var(--r-sm); background:var(--surface); color:var(--fg-2); font-size:var(--text-xs); font-weight:800; cursor:pointer; }
.act.primary { border-color:color-mix(in oklab,var(--class-live),transparent 45%); color:var(--class-live); }
.act.danger { border-color:color-mix(in oklab,var(--state-warn),transparent 35%); color:var(--state-warn); }
.act:disabled, .tbtn:disabled { opacity:.45; cursor:not-allowed; }
.actions-cell { display:flex; justify-content:flex-end; gap:8px; flex-wrap:wrap; }
.linkish { width:100%; justify-content:flex-start; text-align:left; gap:2px; padding:0; border:0; background:transparent; flex-direction:column; }
.empty-row { color:var(--muted); font-size:var(--text-xs); }
.drilldown-panel { position:sticky; top:0; }
.mini-fields { display:grid; grid-template-columns:110px minmax(0, 1fr); gap:6px 12px; margin:0; font-size:var(--text-sm); }
.mini-fields dt { color:var(--muted); }
.mini-fields dd { margin:0; color:var(--fg); }
.drill-block { display:flex; flex-direction:column; gap:8px; }
.drill-block h3 { margin:0; font-size:var(--text-xs); text-transform:uppercase; letter-spacing:.04em; color:var(--muted); }
.stack-list { display:flex; flex-direction:column; gap:8px; margin:0; padding:0; list-style:none; }
.stack-list li { display:flex; flex-direction:column; gap:3px; padding:10px 12px; border:1px solid var(--border); border-radius:var(--r-sm); background:var(--surface-warm); }
.stack-list strong { color:var(--fg); font-size:var(--text-sm); }
.stack-list span { color:var(--muted); font-size:var(--text-xs); }
.empty-card { display:flex; flex-direction:column; gap:6px; color:var(--muted); min-height:120px; justify-content:center; }
.empty-card strong { color:var(--fg-2); }
tr.selected { background:color-mix(in oklab,var(--accent),transparent 92%); }
@media (max-width: 1260px) {
  .access-brief { grid-template-columns:repeat(2, minmax(0, 1fr)); }
  .brief-copy { grid-column:1 / -1; }
  .access-grid { grid-template-columns:1fr; }
  .providers-panel, .keycards-panel, .invites-panel, .users-panel, .sessions-panel, .audit-panel, .roles-panel, .drilldown-panel { grid-column:auto; }
  .drilldown-panel { position:static; }
}
@media (max-width: 860px) {
  .invite-form, .keycard-form, .providers-grid, .roles-grid { grid-template-columns:1fr; }
  .page-head, .statebar { flex-direction:column; align-items:stretch; }
  .actions-cell { justify-content:flex-start; }
}
</style>
