<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { operatorApiUrl } from '../composables/useOperatorApi'

interface CheckoutChoice { choice_ref: string; repository: string; working_copy: string }
interface TargetChoice { target_ref: string; label: string }
interface Grant { grant_ref: string; state: string }

const { t } = useI18n()
const choices = ref<CheckoutChoice[]>([])
const targets = ref<TargetChoice[]>([])
const checkout = ref('')
const target = ref('')
const issued = ref<Grant | null>(null)
const busy = ref(false)
const error = ref(false)
const available = ref(false)

async function call(path: string, method: 'GET' | 'POST', body?: object): Promise<Response> {
  if (typeof crypto.randomUUID !== 'function') throw new Error('Secure browser required')
  return fetch(operatorApiUrl(path), {
    method,
    credentials: 'include',
    headers: { 'X-Engram-Request-ID': crypto.randomUUID(), ...(body ? { 'Content-Type': 'application/json' } : {}) },
    ...(body ? { body: JSON.stringify(body) } : {}),
  })
}

async function refresh() {
  busy.value = true
  error.value = false
  try {
    const response = await call('/code/grants/choices', 'GET')
    if (response.status === 403) { available.value = false; choices.value = []; targets.value = []; return }
    if (!response.ok) throw new Error('Choices unavailable')
    const catalog = await response.json() as { choices: CheckoutChoice[]; targets: TargetChoice[] }
    choices.value = catalog.choices
    targets.value = catalog.targets
    available.value = choices.value.length > 0
    if (!choices.value.some(choice => choice.choice_ref === checkout.value)) checkout.value = choices.value[0]?.choice_ref ?? ''
    if (!targets.value.some(choice => choice.target_ref === target.value)) target.value = targets.value[0]?.target_ref ?? ''
  } catch { error.value = true } finally { busy.value = false }
}

async function issue() {
  if (!checkout.value || !target.value || busy.value || issued.value) return
  busy.value = true
  error.value = false
  try {
    const response = await call('/code/grants', 'POST', { choice_ref: checkout.value, target_ref: target.value })
    if (!response.ok) throw new Error('Grant denied')
    issued.value = await response.json() as Grant
  } catch { error.value = true } finally { busy.value = false }
}

async function revoke() {
  if (!issued.value || busy.value) return
  busy.value = true
  error.value = false
  try {
    const response = await call(`/code/grants/${encodeURIComponent(issued.value.grant_ref)}/revoke`, 'POST')
    if (!response.ok) throw new Error('Revocation denied')
    issued.value = null
  } catch { error.value = true } finally { busy.value = false }
}

onMounted(() => { void refresh() })
</script>

<template>
  <details v-if="available || error" class="grant-chooser" data-testid="code-grant-chooser">
    <summary>{{ t('workspace.grantTitle') }}</summary>
    <p>{{ t('workspace.grantHelp') }}</p>
    <form v-if="available" @submit.prevent="issue">
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
      <button type="submit" :disabled="busy || !!issued || !checkout || !target">{{ t('workspace.grantIssue') }}</button>
    </form>
    <p v-if="issued" role="status">{{ t('workspace.grantIssued') }} <button type="button" :disabled="busy" @click="revoke">{{ t('workspace.grantRevoke') }}</button></p>
    <p v-if="error" role="alert">{{ t('workspace.grantError') }} <button type="button" :disabled="busy" @click="refresh">{{ t('workspace.grantRetry') }}</button></p>
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
</style>
