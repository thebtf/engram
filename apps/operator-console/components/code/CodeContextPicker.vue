<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import type { CodeBootstrapEvidence, CodeBootstrapPhase, CodeCatalogEntry, CodeCatalogState, CodeSafeContext, IndexIntentTarget } from '~/composables/useOperatorCode'

const { t } = useI18n()

const props = defineProps<{
  phase: CodeBootstrapPhase
  state: CodeCatalogState
  catalog: CodeCatalogEntry[]
  candidate: CodeSafeContext | null
  pinned: CodeSafeContext | null
  pending: boolean
  evidence: CodeBootstrapEvidence
}>()

const emit = defineEmits<{
  refresh: []
  select: [context: CodeSafeContext | null]
  pin: []
  retry: []
  requestIndex: [target: IndexIntentTarget]
}>()

const repository = ref('')
const workingCopy = ref('')
const snapshotRef = ref('')
const selectionDirty = ref(false)
const repositories = computed(() => [...new Set(props.catalog.map((entry) => entry.repository))])
const workingCopies = computed(() => [...new Set(props.catalog.filter((entry) => entry.repository === repository.value).map((entry) => entry.workingCopy))])
const snapshotEntries = computed(() => props.catalog.filter((entry) => entry.repository === repository.value && entry.workingCopy === workingCopy.value && entry.view !== null))
const noViewEntry = computed(() => props.catalog.find((entry) => entry.repository === repository.value && entry.workingCopy === workingCopy.value && entry.view === null) ?? null)
const samePinned = computed(() => props.candidate?.selectionRef === props.pinned?.selectionRef)
const phaseLabel = computed(() => t(`codeExplorer.context.phases.${props.phase}`))
const phaseMessage = computed(() => {
  if (props.phase !== 'ready') return t(`codeExplorer.context.messages.${props.phase}`)
  if (props.state !== 'ready') return t(`codeExplorer.context.catalogStates.${props.state}`)
  if (props.candidate === null) return noViewEntry.value === null ? t('codeExplorer.context.selectPrompt') : t('codeExplorer.context.noViewOnlyBody')
  return props.pinned === null ? t('codeExplorer.context.selectedPrompt') : t('codeExplorer.context.pinnedMessage')
})

watch([() => props.catalog, () => props.candidate, () => props.pinned], () => {
  if (selectionDirty.value) return
  const selected = props.candidate ?? props.pinned
  if (selected === null) return
  repository.value = selected.repository
  workingCopy.value = selected.workingCopy
  snapshotRef.value = selected.selectionRef
}, { immediate: true })

function chooseRepository(event: Event): void {
  selectionDirty.value = true
  repository.value = (event.target as HTMLSelectElement).value
  workingCopy.value = ''
  snapshotRef.value = ''
  emit('select', null)
}

function chooseWorkingCopy(event: Event): void {
  selectionDirty.value = true
  workingCopy.value = (event.target as HTMLSelectElement).value
  snapshotRef.value = ''
  emit('select', null)
}

function chooseSnapshot(event: Event): void {
  selectionDirty.value = true
  snapshotRef.value = (event.target as HTMLSelectElement).value
  const selected = snapshotEntries.value.find((entry) => entry.view?.selectionRef === snapshotRef.value)?.view ?? null
  emit('select', selected)
}
</script>

<template>
  <section class="context-picker" aria-labelledby="code-context-heading">
    <div class="section-head">
      <div>
        <h2 id="code-context-heading">{{ t('workspace.contextTitle') }}</h2>
        <p>{{ t('workspace.contextHelp') }}</p>
      </div>
      <span class="phase" :data-state="phase">{{ phaseLabel }}</span>
    </div>

    <p class="message" aria-live="polite" data-testid="code-context-message">{{ phaseMessage }}</p>

    <div v-if="catalog.length > 0" class="selectors" aria-label="Workspace selection">
      <label class="selector">
        <span>{{ t('workspace.repository') }}</span>
        <select :value="repository" :disabled="pending" data-testid="code-context-repository" @change="chooseRepository">
          <option value="" disabled>{{ t('codeExplorer.context.chooseRepository') }}</option>
          <option v-for="name in repositories" :key="name" :value="name">{{ name }}</option>
        </select>
      </label>
      <label class="selector">
        <span>{{ t('workspace.workingCopy') }}</span>
        <select :value="workingCopy" :disabled="pending || repository === ''" data-testid="code-context-working-copy" @change="chooseWorkingCopy">
          <option value="" disabled>{{ t('codeExplorer.context.chooseWorkingCopy') }}</option>
          <option v-for="name in workingCopies" :key="name" :value="name">{{ name }}</option>
        </select>
      </label>
      <label class="selector">
        <span>{{ t('workspace.indexedSnapshot') }}</span>
        <select :value="snapshotRef" :disabled="pending || workingCopy === '' || snapshotEntries.length === 0" data-testid="code-context-snapshot" @change="chooseSnapshot">
          <option value="" disabled>{{ t('codeExplorer.context.chooseSnapshot') }}</option>
          <option v-for="entry in snapshotEntries" :key="entry.view?.selectionRef" :value="entry.view?.selectionRef">{{ entry.view?.snapshot.label }}</option>
        </select>
      </label>
    </div>
    <div v-else class="empty" data-testid="code-context-empty">
      <strong>{{ t('codeExplorer.context.emptyTitle') }}</strong>
      <p>{{ t('codeExplorer.context.emptyBody') }}</p>
    </div>

    <div v-if="noViewEntry !== null" class="no-view" data-testid="code-context-index-affordance">
      <strong>{{ t('codeExplorer.context.noViewOnlyTitle') }}</strong>
      <p>{{ noViewEntry.indexIntentTarget === null ? t('codeExplorer.context.noViewUnavailable') : t('codeExplorer.context.noViewIndexAvailable') }}</p>
      <button
        v-if="noViewEntry.indexIntentTarget !== null"
        class="btn"
        type="button"
        :disabled="pending"
        data-testid="code-request-first-index"
        @click="emit('requestIndex', noViewEntry.indexIntentTarget)"
      >{{ t('codeExplorer.context.requestIndex') }}</button>
    </div>

    <dl v-if="candidate !== null" class="context-values" data-testid="code-context-candidate">
      <div><dt>{{ t('workspace.repository') }}</dt><dd>{{ candidate.repository }}</dd></div>
      <div><dt>{{ t('workspace.workingCopy') }}</dt><dd>{{ candidate.workingCopy }}</dd></div>
      <div><dt>{{ t('workspace.indexedSnapshot') }}</dt><dd>{{ candidate.snapshot.label }}</dd></div>
      <div v-if="candidate.snapshot.revision !== null"><dt>{{ t('codeExplorer.context.revision') }}</dt><dd>{{ candidate.snapshot.revision }}</dd></div>
      <div v-if="candidate.snapshot.publishedAt !== null"><dt>{{ t('codeExplorer.context.publishedAt') }}</dt><dd>{{ candidate.snapshot.publishedAt }}</dd></div>
    </dl>

    <div class="actions">
      <button class="btn" type="button" :disabled="pending" @click="emit('refresh')">{{ t('codeExplorer.context.refresh') }}</button>
      <button v-if="phase === 'reload-pending'" class="btn" type="button" :disabled="pending" data-testid="code-retry-reload" @click="emit('retry')">{{ t('codeExplorer.context.retryReload') }}</button>
      <button class="btn primary" type="button" :disabled="pending || candidate === null || samePinned" data-testid="code-pin-context" @click="emit('pin')">
        {{ samePinned ? t('codeExplorer.context.pinned') : pinned === null ? t('codeExplorer.context.pin') : t('codeExplorer.context.switch') }}
      </button>
    </div>

    <p v-if="pinned !== null" class="pinned" data-testid="code-context-pinned">
      {{ t('codeExplorer.context.pinnedReadout', { repository: pinned.repository, workingCopy: pinned.workingCopy, snapshot: pinned.snapshot.label }) }}
    </p>

    <details class="bootstrap-evidence" data-testid="code-bootstrap-evidence">
      <summary>{{ t('codeExplorer.evidence.binding') }}</summary>
      <dl>
        <div><dt>{{ t('codeExplorer.evidence.navigation') }}</dt><dd>{{ evidence.navigationType }}</dd></div>
        <div><dt>{{ t('codeExplorer.evidence.openerBefore') }}</dt><dd>{{ evidence.openerBefore ? t('codeExplorer.evidence.present') : t('codeExplorer.evidence.none') }}</dd></div>
        <div><dt>{{ t('codeExplorer.evidence.openerReadback') }}</dt><dd>{{ evidence.openerAfter === null ? t('codeExplorer.evidence.notNeeded') : evidence.openerAfter ? t('codeExplorer.evidence.normalized') : t('codeExplorer.evidence.failed') }}</dd></div>
        <div><dt>{{ t('codeExplorer.evidence.transition') }}</dt><dd>{{ evidence.transition }}</dd></div>
      </dl>
    </details>
  </section>
</template>

<style scoped>
.context-picker { display:grid; gap:14px; border:1px solid var(--border); border-radius:var(--r-md); background:var(--surface); padding:16px; }
.section-head { display:flex; align-items:flex-start; justify-content:space-between; gap:16px; }
h2 { margin:0; color:var(--fg); font-size:var(--text-sm); font-weight:800; }
.section-head p, .message, .empty p, .no-view p, .pinned { margin:4px 0 0; color:var(--muted); font-size:var(--text-sm); }
.phase { border:1px solid var(--border); border-radius:var(--radius-pill); padding:4px 8px; color:var(--fg-2); font-size:var(--text-xs); white-space:nowrap; }
.phase[data-state='collision'], .phase[data-state='ambiguous'], .phase[data-state='reload-pending'] { border-color:color-mix(in oklab,var(--warn),transparent 35%); color:var(--warn); }
.selectors { display:grid; grid-template-columns:repeat(3,minmax(0,1fr)); gap:10px; }
.selector { display:grid; gap:5px; min-width:0; }
.selector > span, dt { color:var(--muted); font-size:var(--text-xs); font-weight:700; letter-spacing:.04em; text-transform:uppercase; }
.selector select { min-height:40px; min-width:0; border:1px solid var(--border); border-radius:var(--r-sm); background:var(--bg); color:var(--fg); padding:8px; font:inherit; }
.selector select:focus-visible, .btn:focus-visible { outline:2px solid var(--accent); outline-offset:2px; }
.context-values { display:grid; grid-template-columns:repeat(3,minmax(0,1fr)); gap:10px; margin:0; }
.context-values div { min-width:0; }
dd { margin:4px 0 0; color:var(--fg); font-family:var(--font-mono); font-size:var(--text-xs); overflow-wrap:anywhere; }
.empty, .no-view { display:grid; gap:8px; border:1px solid var(--border-soft); border-radius:var(--r-sm); padding:12px; }
.no-view strong, .empty strong { color:var(--fg-2); font-size:var(--text-sm); }
.actions { display:flex; flex-wrap:wrap; gap:8px; }
.btn { min-height:36px; border:1px solid var(--border); border-radius:var(--r-sm); background:var(--surface); color:var(--fg); padding:8px 12px; font:inherit; font-size:var(--text-sm); font-weight:700; cursor:pointer; }
.btn.primary { border-color:var(--accent); background:var(--accent); color:var(--accent-on); }
.btn:disabled { cursor:not-allowed; opacity:.55; }
.bootstrap-evidence { border-top:1px solid var(--border-soft); padding-top:10px; }
.bootstrap-evidence summary { color:var(--fg-2); cursor:pointer; font-size:var(--text-sm); font-weight:700; }
.bootstrap-evidence dl { display:grid; grid-template-columns:repeat(4,minmax(0,1fr)); gap:10px; margin:10px 0 0; }
.bootstrap-evidence div { min-width:0; }
@media (pointer:coarse) { .btn, .selector select { min-height:44px; } }
@media (max-width:720px) { .section-head, .selectors, .context-values, .bootstrap-evidence dl { grid-template-columns:1fr; display:grid; }.phase { justify-self:start; white-space:normal; } }
</style>
