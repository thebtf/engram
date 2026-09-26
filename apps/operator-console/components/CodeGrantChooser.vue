<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { operatorApiUrl } from '../composables/useOperatorApi'

interface CheckoutChoice { choice_ref: string; repository: string; working_copy: string }
interface TargetChoice { target_ref: string; label: string }
interface Grant { grant_ref: string; state: 'active' | 'revoked' | 'expired'; expires_at: string | null; repository: string; working_copy: string; reader: string }

const { t } = useI18n()
const choices = ref<CheckoutChoice[]>([])
const targets = ref<TargetChoice[]>([])
const checkout = ref('')
const target = ref('')
const grants = ref<Grant[]>([])
const busy = ref(false)
const error = ref(false)
const inventoryError = ref(false)
const mutation = ref<'issued' | 'revoked' | null>(null)
const available = ref(false)
const denied = ref(false)
const loaded = ref(false)

async function call(path: string, method: 'GET' | 'POST', body?: object): Promise<Response> {
  if (!window.isSecureContext || typeof crypto.randomUUID !== 'function') throw new Error('Secure browser required')
  return fetch(operatorApiUrl(path), {
    method,
    credentials: 'include',
    headers: { 'X-Engram-Request-ID': crypto.randomUUID(), ...(body ? { 'Content-Type': 'application/json' } : {}) },
    ...(body ? { body: JSON.stringify(body) } : {}),
  })
}

function parsePage(value: unknown): { grants: Grant[]; next_ref?: string } {
  if (value === null || typeof value !== 'object' || Array.isArray(value)) throw new Error('Invalid inventory')
  const entries = Reflect.get(value, 'grants')
  const next = Reflect.get(value, 'next_ref')
  if (!Array.isArray(entries) || (next !== undefined && (typeof next !== 'string' || !next))) throw new Error('Invalid inventory')
  const parsed: Grant[] = []
  for (const entry of entries) {
    if (entry === null || typeof entry !== 'object' || Array.isArray(entry)) throw new Error('Invalid grant')
    const { grant_ref, state, expires_at, repository, working_copy, reader } = entry
    if (typeof grant_ref !== 'string' || !grant_ref || !['active', 'revoked', 'expired'].includes(state) || typeof repository !== 'string' || typeof working_copy !== 'string' || typeof reader !== 'string' || typeof expires_at !== 'string' && expires_at !== null || expires_at !== null && !Number.isFinite(Date.parse(expires_at))) throw new Error('Invalid grant')
    if (state === 'active' && (expires_at === null || Date.parse(expires_at) > Date.now())) parsed.push({ grant_ref, state, expires_at, repository, working_copy, reader })
  }
  return { grants: parsed, ...(next === undefined ? {} : { next_ref: next }) }
}

async function loadGrants() {
  grants.value = []
  const collected: Grant[] = []
  const seen = new Set<string>()
  const cursors = new Set<string>()
  let next: string | undefined
  do {
    const response = await call(`/code/grants${next === undefined ? '' : `?next_ref=${encodeURIComponent(next)}`}`, 'GET')
    if (response.status === 401 || response.status === 403) { denied.value = true; grants.value = []; return }
    if (!response.ok) throw new Error('Inventory unavailable')
    const page = parsePage(await response.json())
    for (const grant of page.grants) {
      if (seen.has(grant.grant_ref)) throw new Error('Duplicate grant')
      seen.add(grant.grant_ref)
      collected.push(grant)
    }
    next = page.next_ref
    if (next !== undefined) {
      if (cursors.has(next)) throw new Error('Repeated cursor')
      cursors.add(next)
    }
  } while (next !== undefined)
  grants.value = collected
}

async function refresh() {
  busy.value = true
  error.value = false
  inventoryError.value = false
  denied.value = false
  try {
    const response = await call('/code/grants/choices', 'GET')
    if (!response.ok) {
      available.value = false
      choices.value = []
      targets.value = []
      if (response.status !== 401 && response.status !== 403) inventoryError.value = true
    } else {
      const catalog = await response.json() as { choices: CheckoutChoice[]; targets: TargetChoice[] }
      choices.value = catalog.choices
      targets.value = catalog.targets
      available.value = choices.value.length > 0
      if (!choices.value.some(choice => choice.choice_ref === checkout.value)) checkout.value = choices.value[0]?.choice_ref ?? ''
      if (!targets.value.some(choice => choice.target_ref === target.value)) target.value = targets.value[0]?.target_ref ?? ''
    }
    await loadGrants()
  } catch { inventoryError.value = true; grants.value = [] } finally { loaded.value = true; busy.value = false }
}

async function issue() {
  if (!checkout.value || !target.value || busy.value || denied.value) return
  busy.value = true
  mutation.value = null
  error.value = false
  inventoryError.value = false
  try {
    const response = await call('/code/grants', 'POST', { choice_ref: checkout.value, target_ref: target.value })
    if (!response.ok) throw new Error('Grant denied')
    mutation.value = 'issued'
    try { await loadGrants() } catch { inventoryError.value = true; grants.value = [] }
  } catch { error.value = true } finally { busy.value = false }
}

async function revoke(grantRef: string) {
  if (busy.value || !grants.value.some(grant => grant.grant_ref === grantRef)) return
  busy.value = true
  error.value = false
  mutation.value = null
  inventoryError.value = false
  try {
    const response = await call(`/code/grants/${encodeURIComponent(grantRef)}/revoke`, 'POST')
    if (!response.ok) throw new Error('Revocation denied')
    mutation.value = 'revoked'
    try { await loadGrants() } catch { inventoryError.value = true; grants.value = [] }
  } catch { error.value = true } finally { busy.value = false }
}

onMounted(() => { void refresh() })
</script>

<template>
  <details class="grant-chooser" data-testid="code-grant-chooser">
    <summary>{{ t('workspace.grantTitle') }}</summary>
    <p>{{ t('workspace.grantHelp') }}</p>
    <form v-if="available && !denied" @submit.prevent="issue">
      <label>{{ t('workspace.grantCheckout') }}
        <select v-model="checkout" :disabled="busy">
          <option v-for="choice in choices" :key="choice.choice_ref" :value="choice.choice_ref">{{ choice.repository }} · {{ choice.working_copy || t('workspace.grantUnnamed') }}</option>
        </select>
      </label>
      <label>{{ t('workspace.grantReader') }}
        <select v-model="target" :disabled="busy">
          <option v-for="reader in targets" :key="reader.target_ref" :value="reader.target_ref">{{ reader.label }}</option>
        </select>
      </label>
      <p v-if="targets.length === 0" role="status">{{ t('workspace.grantNoReaders') }}</p>
      <button type="submit" :disabled="busy || !checkout || !target">{{ t('workspace.grantIssue') }}</button>
    </form>
    <p v-if="busy" role="status">{{ t('workspace.grantLoading') }}</p>
    <p v-else-if="denied" role="alert">{{ t('workspace.grantDenied') }}</p>
    <p v-else-if="loaded && !inventoryError && grants.length === 0" role="status">{{ t('workspace.grantEmpty') }}</p>
    <div v-if="grants.length > 0" class="inventory">
      <h3>{{ t('workspace.grantActive') }}</h3>
      <ul>
        <li v-for="grant in grants" :key="grant.grant_ref">
          <span class="grant-label">{{ grant.repository }} · {{ grant.working_copy || t('workspace.grantUnnamed') }} — {{ grant.reader }}</span>
          <span class="grant-expiry">{{ grant.expires_at === null ? t('workspace.grantNoExpiry') : t('workspace.grantExpires', { date: new Date(grant.expires_at).toLocaleString() }) }}</span>
          <button type="button" :disabled="busy" :aria-label="t('workspace.grantRevokeFor', { reader: grant.reader, workingCopy: grant.working_copy || t('workspace.grantUnnamed') })" @click="revoke(grant.grant_ref)">{{ t('workspace.grantRevoke') }}</button>
        </li>
      </ul>
    </div>
    <p v-if="mutation !== null" role="status">{{ t(mutation === 'issued' ? 'workspace.grantIssued' : 'workspace.grantRevoked') }}</p>
    <p v-if="inventoryError" role="alert">{{ t('workspace.grantInventoryUnavailable') }} <button type="button" :disabled="busy" @click="refresh">{{ t('workspace.grantRetry') }}</button></p>
    <p v-if="error" role="alert">{{ t('workspace.grantError') }}</p>
  </details>
</template>

<style scoped>
.grant-chooser { border:1px solid var(--border); border-radius:var(--r-sm); background:var(--surface); padding:12px; color:var(--fg); font-size:var(--text-sm); }
.grant-chooser summary { cursor:pointer; font-weight:700; }
.grant-chooser form { display:flex; flex-wrap:wrap; align-items:end; gap:12px; }
.grant-chooser label { display:grid; gap:4px; min-width:180px; flex:1; }
.grant-chooser select, .grant-chooser button { min-height:44px; border:1px solid var(--border); border-radius:var(--r-sm); background:var(--surface); color:var(--fg); padding:8px; font:inherit; }
.grant-chooser button { cursor:pointer; font-weight:700; }
.grant-chooser button:disabled { cursor:not-allowed; opacity:.55; }
.inventory h3 { font-size:var(--text-sm); }
.inventory ul { list-style:none; padding:0; margin:0; display:grid; gap:8px; }
.inventory li { display:flex; flex-wrap:wrap; align-items:center; gap:8px 16px; border-top:1px solid var(--border); padding:12px 0; min-width:0; }
.grant-label { overflow-wrap:anywhere; flex:1 1 220px; }
.grant-expiry { color:var(--fg-2); flex:1 1 180px; }
.grant-chooser :is(button, select, summary):focus-visible { outline:2px solid var(--accent); outline-offset:2px; }
</style>
