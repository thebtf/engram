<script setup lang="ts">
import { computed } from 'vue'
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
  select: [context: CodeSafeContext]
  pin: []
  retry: []
  requestIndex: [target: IndexIntentTarget]
}>()

const selectable = computed(() => props.catalog.flatMap((entry) => entry.view === null ? [] : [entry.view]))
const noViewEntries = computed(() => props.catalog.filter((entry) => entry.view === null))
const selectedKey = computed(() => props.candidate === null ? '' : [
  props.candidate.context.sourceId,
  props.candidate.context.checkoutId,
  props.candidate.context.viewId,
  props.candidate.context.profileId,
  props.candidate.context.generation,
].join('\u0000'))
const samePinned = computed(() => props.candidate !== null && props.pinned !== null
  && props.candidate.context.sourceId === props.pinned.context.sourceId
  && props.candidate.context.checkoutId === props.pinned.context.checkoutId
  && props.candidate.context.viewId === props.pinned.context.viewId
  && props.candidate.context.profileId === props.pinned.context.profileId
  && props.candidate.context.generation === props.pinned.context.generation)
const phaseLabel = computed(() => t(`codeExplorer.context.phases.${props.phase}`))
const phaseMessage = computed(() => {
  if (props.phase !== 'ready') return t(`codeExplorer.context.messages.${props.phase}`)
  if (props.state !== 'ready') return t(`codeExplorer.context.catalogStates.${props.state}`)
  if (props.candidate === null) return noViewEntries.value.length > 0 && selectable.value.length === 0
    ? t('codeExplorer.context.noViewOnlyBody')
    : t('codeExplorer.context.selectPrompt')
  return props.pinned === null ? t('codeExplorer.context.selectedPrompt') : t('codeExplorer.context.pinnedMessage')
})

function chooseContext(event: Event): void {
  const key = (event.target as HTMLSelectElement).value
  const context = selectable.value.find((candidate) => [
    candidate.context.sourceId,
    candidate.context.checkoutId,
    candidate.context.viewId,
    candidate.context.profileId,
    candidate.context.generation,
  ].join('\u0000') === key)
  if (context !== undefined) emit('select', context)
}
</script>

<template>
  <section class="context-picker" aria-labelledby="code-context-heading">
    <div class="section-head">
      <div>
        <h2 id="code-context-heading">{{ t('codeExplorer.context.title') }}</h2>
        <p>{{ t('codeExplorer.context.lead') }}</p>
      </div>
      <span class="phase" :data-state="phase">{{ phaseLabel }}</span>
    </div>

    <p class="message" aria-live="polite" data-testid="code-context-message">{{ phaseMessage }}</p>

    <label v-if="selectable.length > 0" class="selector">
      <span>{{ t('codeExplorer.context.catalog') }}</span>
      <select :value="selectedKey" :disabled="pending" data-testid="code-context-select" @change="chooseContext">
        <option value="" disabled>{{ t('codeExplorer.context.choose') }}</option>
        <option v-for="context in selectable" :key="context.context.viewId" :value="[context.context.sourceId, context.context.checkoutId, context.context.viewId, context.context.profileId, context.context.generation].join('\u0000')">
          {{ context.source }} · {{ context.checkout }} · {{ context.view }}
        </option>
      </select>
    </label>
    <div v-else class="empty" data-testid="code-context-empty">
      <strong>{{ t(noViewEntries.length > 0 ? 'codeExplorer.context.noViewOnlyTitle' : 'codeExplorer.context.emptyTitle') }}</strong>
      <p>{{ t(noViewEntries.length > 0 ? 'codeExplorer.context.noViewOnlyBody' : 'codeExplorer.context.emptyBody') }}</p>
    </div>

    <dl v-if="candidate !== null" class="context-values" data-testid="code-context-candidate">
      <div><dt>{{ t('codeExplorer.context.source') }}</dt><dd>{{ candidate.source }}</dd></div>
      <div><dt>{{ t('codeExplorer.context.checkout') }}</dt><dd>{{ candidate.checkout }}</dd></div>
      <div><dt>{{ t('codeExplorer.context.view') }}</dt><dd>{{ candidate.view }}</dd></div>
    </dl>

    <ul v-if="noViewEntries.length > 0" class="no-view-list">
      <li v-for="entry in noViewEntries" :key="`${entry.source.id}:${entry.checkout.id}`" data-testid="code-context-index-affordance">
        <strong>{{ entry.source.label }} · {{ entry.checkout.label }}</strong>
        <p>{{ entry.indexIntentTarget === null ? t('codeExplorer.context.noViewUnavailable') : t('codeExplorer.context.noViewIndexAvailable') }}</p>
        <button
          v-if="entry.indexIntentTarget !== null"
          class="btn"
          type="button"
          :disabled="pending"
          :aria-label="t('codeExplorer.context.requestIndexFor', { source: entry.source.label, checkout: entry.checkout.label })"
          data-testid="code-request-first-index"
          @click="emit('requestIndex', entry.indexIntentTarget)"
        >{{ t('codeExplorer.context.requestIndex') }}</button>
      </li>
    </ul>

    <div class="actions">
      <button class="btn" type="button" :disabled="pending" @click="emit('refresh')">{{ t('codeExplorer.context.refresh') }}</button>
      <button v-if="phase === 'reload-pending'" class="btn" type="button" :disabled="pending" @click="emit('retry')">{{ t('codeExplorer.context.retryReload') }}</button>
      <button class="btn primary" type="button" :disabled="pending || candidate === null || samePinned" data-testid="code-pin-context" @click="emit('pin')">
        {{ samePinned ? t('codeExplorer.context.pinned') : pinned === null ? t('codeExplorer.context.pin') : t('codeExplorer.context.switch') }}
      </button>
    </div>

    <p v-if="pinned !== null" class="pinned" data-testid="code-context-pinned">
      {{ t('codeExplorer.context.pinnedReadout', pinned) }}
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
.context-picker { border:1px solid var(--border); border-radius:var(--r-md); background:var(--surface); padding:16px; display:grid; gap:14px; }.section-head { display:flex; align-items:flex-start; justify-content:space-between; gap:16px; }h2 { margin:0; color:var(--fg); font-size:var(--text-sm); font-weight:800; }.section-head p, .message, .empty p, .pinned, .no-view-list p { margin:4px 0 0; color:var(--muted); font-size:var(--text-sm); }.phase { border:1px solid var(--border); border-radius:var(--radius-pill); padding:4px 8px; color:var(--fg-2); font-size:var(--text-xs); white-space:nowrap; }.phase[data-state='collision'], .phase[data-state='ambiguous'], .phase[data-state='reload-pending'] { border-color:color-mix(in oklab,var(--warn),transparent 35%); color:var(--warn); }.selector { display:grid; gap:5px; }.selector > span, dt { color:var(--muted); font-size:var(--text-xs); font-weight:700; letter-spacing:.04em; text-transform:uppercase; }.selector select { min-height:38px; border:1px solid var(--border); border-radius:var(--r-sm); background:var(--bg); color:var(--fg); padding:8px; font:inherit; }.selector select:focus-visible, .btn:focus-visible { outline:2px solid var(--accent); outline-offset:2px; }.context-values, .bootstrap-evidence dl { display:grid; grid-template-columns:repeat(3,minmax(0,1fr)); gap:10px; margin:0; }.context-values div, .bootstrap-evidence div { min-width:0; }dd { margin:4px 0 0; color:var(--fg); font-family:var(--font-mono); font-size:var(--text-xs); overflow-wrap:anywhere; }.no-view-list { display:grid; gap:8px; margin:0; padding:0; list-style:none; }.no-view-list li { border:1px solid var(--border-soft); border-radius:var(--r-sm); padding:10px; }.no-view-list strong { color:var(--fg-2); font-family:var(--font-mono); font-size:var(--text-xs); overflow-wrap:anywhere; }.actions { display:flex; flex-wrap:wrap; gap:8px; }.btn { min-height:36px; border:1px solid var(--border); border-radius:var(--r-sm); background:var(--surface); color:var(--fg); padding:8px 12px; font:inherit; font-size:var(--text-sm); font-weight:700; cursor:pointer; }.btn.primary { border-color:var(--accent); background:var(--accent); color:var(--accent-on); }.btn:disabled { cursor:not-allowed; opacity:.55; }.pinned { border-top:1px solid var(--border-soft); padding-top:10px; }.bootstrap-evidence { border-top:1px solid var(--border-soft); padding-top:10px; }.bootstrap-evidence summary { color:var(--fg-2); cursor:pointer; font-size:var(--text-sm); font-weight:700; }@media (pointer:coarse) { .btn, .selector select { min-height:44px; } }@media (max-width:720px) { .section-head, .context-values, .bootstrap-evidence dl { display:grid; grid-template-columns:1fr; }.phase { justify-self:start; white-space:normal; } }
</style>
