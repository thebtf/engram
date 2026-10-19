<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { useOperatorSecrets, type OperatorCredential } from '../composables/useOperatorSecrets'
import type { MutationResult } from '../composables/useApi'
import { isCatalogProject, projectName, useOperatorProjectContext } from '../composables/useOperatorProjectContext'

const { t } = useI18n()
const context = useOperatorProjectContext()
const {
  creds,
  vault,
  loadState,
  vaultState,
  pending,
  error,
  refresh,
  revealSecret: fetchSecretValue,
  createSecret: runCreateSecret,
  deleteSecret,
  cleanupOrphans: runCleanupOrphans,
  rotationGap,
} = useOperatorSecrets()

const fpShown = ref(false)
const openedName = ref<string | null>(null)
const deleteConfirm = ref(false)
const revealError = ref<string | null>(null)
const revealed = ref<{ id: string; value: string } | null>(null)
const createName = ref('')
const createProject = ref('')
const createValue = ref('')
const mutationResult = ref<MutationResult | null>(null)
let fpTimer: ReturnType<typeof setTimeout> | null = null

const selected = computed(() => creds.find((cred) => cred.id === openedName.value) || null)
const scopeError = computed(() => context.selectionError.value || context.error.value)
const canCreate = computed(() => createName.value.trim().length > 0 && isCatalogProject(createProject.value) && createValue.value.length > 0 && !pending.value && !context.pending.value && !context.selectionPending.value && !scopeError.value)
let revealGeneration = 0
watch(context.project, (project) => {
  createProject.value = isCatalogProject(project) ? project : ''
  createValue.value = ''
  mutationResult.value = null
  openedName.value = null
  deleteConfirm.value = false
  revealed.value = null
  revealError.value = null
  revealGeneration += 1
}, { immediate: true, flush: 'sync' })
watch(loadState, () => { revealed.value = null; revealGeneration += 1 }, { flush: 'sync' })
watch([context.selectionPending, scopeError], () => {
  openedName.value = null
  deleteConfirm.value = false
  revealed.value = null
  revealError.value = null
  mutationResult.value = null
  revealGeneration += 1
}, { flush: 'sync' })
onBeforeUnmount(() => { revealGeneration += 1; revealed.value = null; createValue.value = ''; if (fpTimer) clearTimeout(fpTimer) })

function revealFp() {
  fpShown.value = !fpShown.value
  if (fpTimer) clearTimeout(fpTimer)
  if (fpShown.value) fpTimer = setTimeout(() => (fpShown.value = false), 30000)
}

function maskedFp() {
  if (!fpShown.value) return t('secrets.fingerprintMasked')
  return vault.value.fingerprint.replace(/(.{4})(?=.)/g, '$1 ').trim()
}

function copyFp() {
  try { navigator.clipboard?.writeText(vault.value.fingerprint) } catch {}
}

function openCredential(cred: OperatorCredential) {
  openedName.value = openedName.value === cred.id ? null : cred.id
  deleteConfirm.value = false
  revealError.value = null
}

async function revealSecret(cred: OperatorCredential) {
  if (context.selectionPending.value || scopeError.value) return
  const generation = ++revealGeneration
  revealError.value = null
  try {
    const value = await fetchSecretValue(cred)
    if (generation !== revealGeneration) return
    revealed.value = { id: cred.id, value }
  } catch (nextError) {
    if (generation !== revealGeneration) return
    revealError.value = nextError instanceof Error ? nextError.message : String(nextError)
  }
}

function hideSecret(id: string) {
  if (revealed.value?.id === id) {
    revealed.value = null
  }
}

async function createSecret() {
  if (!canCreate.value) return
  const scope = context.project.value
  const value = createValue.value
  createValue.value = ''
  const result = await runCreateSecret({
    name: createName.value.trim(),
    value,
    project: createProject.value.trim(),
    scope: 'project',
  })
  if (scope !== context.project.value || context.selectionPending.value || scopeError.value) return
  mutationResult.value = result
  if (result.kind !== 'committed_verified') return
  createName.value = ''
  createValue.value = ''
  await refresh()
}

async function deleteOpened() {
  if (!selected.value || context.selectionPending.value || scopeError.value) return
  if (!deleteConfirm.value) {
    deleteConfirm.value = true
    return
  }

  const cred = selected.value
  const scope = context.project.value
  const result = await deleteSecret(cred)
  if (scope !== context.project.value || context.selectionPending.value || scopeError.value) return
  mutationResult.value = result
  if (result.kind !== 'committed_verified') return
  openedName.value = null
  deleteConfirm.value = false
  hideSecret(cred.id)
  await refresh()
}

async function requestDelete(cred: OperatorCredential) {
  if (openedName.value !== cred.id) {
    openedName.value = cred.id
    deleteConfirm.value = false
  }

  await deleteOpened()
}

async function cleanupOrphans() {
  const result = await runCleanupOrphans()
  mutationResult.value = result
  if (result.kind === 'committed_verified') await refresh()
}
</script>

<template>
  <div class="secrets-page">
    <header class="head">
      <h1>{{ t('secrets.title') }}</h1>
      <p>{{ t('secrets.subtitle') }}</p>
    </header>

    <section v-if="context.selectionPending.value || scopeError" class="statebar" :data-state="scopeError ? 'error' : 'pending'" role="status">
      <span>{{ scopeError ? t('secrets.state.error', { message: scopeError.message }) : t('secrets.state.pending') }}</span>
      <button v-if="scopeError" class="tbtn" @click="context.refresh">{{ t('secrets.state.retry') }}</button>
    </section>
    <section v-if="pending || error || loadState.kind === 'empty'" class="statebar" :data-state="loadState.kind">
      <span v-if="pending">{{ t('secrets.state.pending') }}</span>
      <span v-else-if="error">{{ t('secrets.state.error', { message: error }) }}</span>
      <span v-else-if="loadState.kind === 'empty'">{{ t('secrets.state.empty') }}</span>
      <button v-if="error" class="tbtn" @click="refresh">{{ t('secrets.state.retry') }}</button>
    </section>
    <MutationResultNotice :result="mutationResult" :recheck-label="t('secrets.state.retry')" @recheck="refresh" />

    <section class="vault-status">
      <div class="section-head">
        <div>
          <p class="vs-title">{{ t('secrets.vaultState') }}</p>
          <p v-if="vaultState.kind === 'error'" class="warn-text">{{ t('secrets.state.vaultError') }}</p>
        </div>
        <HonestyBadge :cls="vault.encrypted ? 'live' : 'dormant'" :evidence="vault.encrypted ? undefined : 'ENGRAM_VAULT_KEY'" :label="vault.encrypted ? t('common.enabled') : t('secrets.vaultDisabled')" />
      </div>
      <div class="vs-grid">
        <div class="vs-field">
          <p class="vs-k">{{ t('secrets.encryption') }}</p>
          <div class="vs-v">{{ vault.source }}</div>
        </div>
        <div class="vs-field">
          <p class="vs-k">{{ t('secrets.fingerprint') }}</p>
          <div class="fp">
            <code>{{ maskedFp() }}</code>
            <button class="ic" :title="fpShown ? t('common.hide') : t('common.show')" @click="revealFp">{{ fpShown ? '🙈' : '👁' }}</button>
            <button v-if="fpShown" class="ic" :title="t('common.copy')" @click="copyFp">⧉</button>
          </div>
        </div>
        <div class="vs-field">
          <p class="vs-k">{{ t('secrets.count') }}</p>
          <div class="vs-v">{{ vault.count || creds.length }}</div>
        </div>
      </div>
      <div v-if="vault.mismatchWarning" class="callout warn">{{ vault.mismatchWarning }}</div>
    </section>

    <section class="create-card">
      <div class="section-head">
        <div>
          <h2>{{ t('secrets.create.title') }}</h2>
          <p>{{ t('secrets.create.subtitle') }}</p>
        </div>
        <HonestyBadge cls="live" />
      </div>
      <div class="inline-fields">
        <label>
          <span>{{ t('secrets.create.name') }}</span>
          <input v-model="createName" class="input" autocomplete="off" />
        </label>
        <label>
          <span>{{ t('secrets.create.project') }}</span>
          <select v-model="createProject" class="input" data-testid="secret-create-project" :disabled="context.pending.value || context.selectionPending.value || Boolean(scopeError)">
            <option value="" disabled>{{ t('knowledge.chooseProject') }}</option>
            <option v-for="project in context.projects.value" :key="project" :value="project">{{ projectName(project) }}</option>
          </select>
        </label>
        <label>
          <span>{{ t('secrets.create.value') }}</span>
          <input v-model="createValue" class="input mono" type="password" autocomplete="new-password" />
        </label>
        <button class="primary" :disabled="!canCreate" @click="createSecret">{{ t('secrets.create.submit') }}</button>
      </div>
    </section>

    <section class="rotation-card">
      <div>
        <b>{{ t('secrets.rotation.title') }}</b>
        <span>{{ t('secrets.rotation.body') }}</span>
        <code>{{ rotationGap.evidence.endpoint }}</code>
      </div>
      <button class="secondary" disabled>{{ t('secrets.rotation.action') }} <span>{{ t('overview.badges.mustBuild') }}</span></button>
      <button class="secondary" :disabled="pending" @click="cleanupOrphans">{{ t('secrets.rotation.cleanup') }}</button>
    </section>

    <table class="vault-tbl">
      <thead>
        <tr>
          <th>{{ t('secrets.colKey') }}</th>
          <th>{{ t('secrets.colProject') }}</th>
          <th>{{ t('secrets.colScope') }}</th>
          <th>{{ t('secrets.colCreated') }}</th>
          <th class="r">{{ t('secrets.colActions') }}</th>
        </tr>
      </thead>
      <tbody>
        <template v-for="c in creds" :key="c.id">
          <tr :class="{ open: openedName === c.id }" :data-testid="`secret-row-${c.id}`" @click="openCredential(c)">
            <td>
              <div class="vn">
                <span><span class="vk">🔑</span>{{ c.name }}</span>
                <RevealSecret v-if="revealed?.id === c.id" :value="revealed.value" :seconds="30" @hide="hideSecret(c.id)" />
                <span v-if="revealError && openedName === c.id" class="err">{{ revealError }}</span>
              </div>
            </td>
            <td><span class="vcol">{{ c.binding_error ? `Unavailable #${c.id}` : c.unresolved_reference ? `Retained #${c.id} · read-only` : c.scope === 'global' ? t('rules.scope.global') : projectName(c.project) }}</span><div v-if="c.binding_error" class="vcol">{{ c.binding_error === 'CREDENTIAL_BINDING_CONFLICT' ? 'Conflicting membership' : 'Membership unavailable' }}</div><div v-if="c.origin_project" class="vcol">Origin: {{ c.origin_project }}</div></td>
            <td><span class="bdg">{{ c.binding_error ? 'unavailable' : c.scope }}</span></td>
            <td><span class="vcol">{{ c.created }}</span></td>
            <td class="r">
              <button v-if="revealed?.id !== c.id" class="act" :data-testid="`secret-reveal-${c.id}`" :disabled="Boolean(c.binding_error)" :title="c.binding_error ? 'Credential binding unavailable' : undefined" @click.stop="revealSecret(c)">{{ t('secrets.reveal') }}</button>
              <button class="act danger" :data-testid="`secret-delete-${c.id}`" :disabled="Boolean(c.binding_error || c.unresolved_reference)" :title="c.binding_error ? 'Credential binding unavailable' : c.unresolved_reference ? 'Retained credential: read-only' : undefined" @click.stop="requestDelete(c)">{{ deleteConfirm && openedName === c.id ? t('secrets.confirmDelete') : t('secrets.delete') }}</button>
            </td>
          </tr>
        </template>
      </tbody>
    </table>
  </div>
</template>

<style scoped>
.secrets-page { display:flex; flex-direction:column; gap:14px; }
.head { padding-bottom:14px; border-bottom:1px solid var(--border); }
.head h1 { margin:0 0 4px; font-size:var(--text-xl); font-weight:800; letter-spacing:var(--tracking-display); }
.head p { margin:0; font-size:var(--text-sm); color:var(--muted); }
.statebar { display:flex; align-items:center; justify-content:space-between; gap:12px; padding:10px 12px; border:1px solid var(--border); border-radius:var(--r-md); background:var(--surface); color:var(--fg-2); font-size:var(--text-sm); }
.statebar[data-state="pending"] { border-color:color-mix(in oklab,var(--accent),transparent 55%); }
.statebar[data-state="error"] { border-color:color-mix(in oklab,var(--state-warn),transparent 45%); color:var(--state-warn); }
.statebar[data-state="empty"] { color:var(--muted); }
.vault-status, .create-card, .rotation-card { border:1px solid var(--border); border-radius:var(--r-md); background:var(--surface); padding:16px 20px; }
.section-head { display:flex; align-items:flex-start; justify-content:space-between; gap:12px; }
.section-head h2 { margin:0; font-size:var(--text-lg); }
.section-head p, .vs-title { margin:4px 0 0; color:var(--muted); font-size:var(--text-sm); }
.vs-title { margin:0; }
.vs-grid { display:grid; grid-template-columns:repeat(3,1fr); gap:24px; margin-top:14px; }
.vs-k { margin:0 0 5px; font-size:var(--text-xs); color:var(--muted); font-weight:800; text-transform:uppercase; letter-spacing:.06em; }
.vs-v { font-size:var(--text-sm); font-family:var(--font-mono); }
.fp { display:flex; align-items:center; gap:6px; }
.fp code { font-family:var(--font-mono); font-size:var(--text-sm); letter-spacing:.04em; }
.fp .ic { border:0; background:transparent; cursor:pointer; }
.callout { margin-top:12px; padding:9px 11px; border-radius:var(--r-sm); font-size:var(--text-xs); }
.callout.warn, .warn-text { color:var(--state-warn); }
.inline-fields { display:grid; grid-template-columns:minmax(0,1fr) minmax(0,1fr) minmax(0,1.2fr) auto; gap:10px; align-items:end; margin-top:12px; }
label span { display:block; margin-bottom:5px; color:var(--muted); font-size:var(--text-xs); font-weight:800; text-transform:uppercase; letter-spacing:.06em; }
.input { width:100%; min-height:34px; border:1px solid var(--border); border-radius:var(--r-sm); background:var(--surface-warm); color:var(--fg); padding:0 10px; font:inherit; }
.input.mono { font-family:var(--font-mono); }
.primary, .secondary { min-height:34px; padding:7px 11px; border:1px solid var(--border); border-radius:var(--r-sm); font-size:var(--text-xs); font-weight:900; cursor:pointer; }
.primary { background:var(--accent); border-color:var(--accent); color:var(--accent-on); }
.secondary { background:var(--surface); color:var(--fg-2); }
.primary:disabled, .secondary:disabled { opacity:.5; cursor:not-allowed; }
.rotation-card { display:flex; align-items:center; justify-content:space-between; gap:12px; }
.rotation-card div { display:flex; flex-direction:column; gap:5px; color:var(--fg-2); font-size:var(--text-sm); }
.rotation-card code { width:max-content; color:var(--muted); font-family:var(--font-mono); font-size:var(--text-xs); }
.rotation-card span span, .secondary span { margin-left:4px; color:var(--class-mustbuild); }
.vault-tbl { width:100%; border-collapse:collapse; border:1px solid var(--border); border-radius:var(--r-md); overflow:hidden; background:var(--surface); }
.vault-tbl th { text-align:left; font-size:var(--text-xs); font-weight:800; color:var(--muted); padding:10px 14px; border-bottom:1px solid var(--border); text-transform:uppercase; letter-spacing:.06em; }
.vault-tbl th.r, .vault-tbl td.r { text-align:right; }
.vault-tbl td { padding:13px 14px; border-bottom:1px solid var(--border-soft); vertical-align:top; }
.vault-tbl tr.open { background:color-mix(in oklab,var(--accent),transparent 91%); }
.vn { display:flex; flex-direction:column; font-size:var(--text-sm); font-weight:500; }
.vn .vk { margin-right:6px; }
.vcol { font-size:var(--text-xs); color:var(--muted); font-family:var(--font-mono); }
.bdg { font-size:10px; padding:2px 7px; border:1px solid var(--border); border-radius:5px; background:var(--surface-warm); color:var(--fg-2); }
.act { font-size:var(--text-sm); padding:5px 11px; border:1px solid var(--border); border-radius:var(--r-sm); background:transparent; color:var(--fg); cursor:pointer; }
.act:hover { background:var(--surface-warm); }
.act.danger { margin-left:6px; border-color:color-mix(in oklab,var(--state-warn),transparent 45%); color:var(--state-warn); }
.act:disabled { opacity:.5; cursor:not-allowed; }
.err { margin-top:6px; color:var(--state-warn); font-size:var(--text-xs); }
@media (max-width:960px) {
  .vs-grid, .inline-fields { grid-template-columns:1fr; }
  .rotation-card { align-items:flex-start; flex-direction:column; }
}
</style>
