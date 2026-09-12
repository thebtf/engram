<script setup lang="ts">
import { computed } from 'vue'
import type { CodeBootstrapEvidence, CodeBootstrapPhase, CodeSafeContext } from '~/composables/useOperatorCode'

const { t } = useI18n()

const props = defineProps<{
  phase: CodeBootstrapPhase
  candidate: CodeSafeContext | null
  pinned: CodeSafeContext | null
  pending: boolean
  evidence: CodeBootstrapEvidence
}>()

const emit = defineEmits<{
  refresh: []
  pin: []
  retry: []
}>()

const phaseLabel = computed(() => t(`codeExplorer.context.phases.${props.phase}`))
const phaseMessage = computed(() => {
  if (props.pinned !== null) return t('codeExplorer.context.pinnedMessage')
  if (props.candidate !== null) return t('codeExplorer.context.singleCandidate')
  return t(`codeExplorer.context.messages.${props.phase}`)
})
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

    <dl v-if="candidate !== null" class="context-values" data-testid="code-context-candidate">
      <div><dt>{{ t('codeExplorer.context.source') }}</dt><dd>{{ candidate.source }}</dd></div>
      <div><dt>{{ t('codeExplorer.context.checkout') }}</dt><dd>{{ candidate.checkout }}</dd></div>
      <div><dt>{{ t('codeExplorer.context.view') }}</dt><dd>{{ candidate.view }}</dd></div>
    </dl>
    <div v-else class="empty" data-testid="code-context-empty">
      <strong>{{ t('codeExplorer.context.emptyTitle') }}</strong>
      <p>{{ t('codeExplorer.context.emptyBody') }}</p>
    </div>

    <aside v-if="candidate !== null && pinned === null" class="catalog-gap" role="status">
      <strong>{{ t('codeExplorer.context.catalogGapTitle') }}</strong>
      <p>{{ t('codeExplorer.context.catalogGapBody') }}</p>
    </aside>

    <div class="actions">
      <button class="btn" type="button" :disabled="pending" @click="emit('refresh')">{{ t('codeExplorer.context.refresh') }}</button>
      <button v-if="phase === 'reload-pending'" class="btn" type="button" :disabled="pending" @click="emit('retry')">{{ t('codeExplorer.context.retryReload') }}</button>
      <button class="btn primary" type="button" :disabled="pending || candidate === null || pinned !== null" data-testid="code-pin-context" @click="emit('pin')">
        {{ pinned === null ? t('codeExplorer.context.pin') : t('codeExplorer.context.pinned') }}
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
.context-picker { border:1px solid var(--border); border-radius:var(--r-md); background:var(--surface); padding:16px; display:grid; gap:14px; }.section-head { display:flex; align-items:flex-start; justify-content:space-between; gap:16px; }h2 { margin:0; color:var(--fg); font-size:var(--text-sm); font-weight:800; }.section-head p, .message, .empty p, .pinned, .catalog-gap p { margin:4px 0 0; color:var(--muted); font-size:var(--text-sm); }.phase { border:1px solid var(--border); border-radius:var(--radius-pill); padding:4px 8px; color:var(--fg-2); font-size:var(--text-xs); white-space:nowrap; }.phase[data-state='collision'], .phase[data-state='ambiguous'], .phase[data-state='reload-pending'] { border-color:color-mix(in oklab,var(--warn),transparent 35%); color:var(--warn); }.phase[data-state='denied'], .phase[data-state='error'] { border-color:color-mix(in oklab,var(--danger),transparent 35%); color:var(--danger); }.message { margin:0; }.context-values { display:grid; grid-template-columns:repeat(3,minmax(0,1fr)); gap:10px; margin:0; }.context-values div, .bootstrap-evidence div { min-width:0; border:1px solid var(--border-soft); border-radius:var(--r-sm); background:var(--bg); padding:10px; }dt { color:var(--muted); font-size:var(--text-xs); letter-spacing:.04em; text-transform:uppercase; }dd { margin:5px 0 0; color:var(--fg); font-family:var(--font-mono); font-size:var(--text-sm); overflow-wrap:anywhere; }.empty { border:1px dashed var(--border); border-radius:var(--r-sm); background:var(--bg); padding:14px; }.empty strong, .catalog-gap strong { color:var(--fg); }.catalog-gap { border:1px solid color-mix(in oklab,var(--class-mustbuild),transparent 45%); border-radius:var(--r-sm); background:color-mix(in oklab,var(--class-mustbuild),transparent 92%); padding:12px; }.catalog-gap p { max-width:72ch; }.actions { display:flex; flex-wrap:wrap; gap:8px; }.btn { min-height:36px; border:1px solid var(--border); border-radius:var(--r-sm); background:var(--surface); color:var(--fg); padding:8px 12px; font:inherit; font-size:var(--text-sm); font-weight:700; cursor:pointer; }.btn.primary { border-color:var(--accent); background:var(--accent); color:var(--accent-on); }.btn:disabled { cursor:not-allowed; opacity:.55; }.pinned { margin:0; color:var(--success); }.bootstrap-evidence { color:var(--muted); font-size:var(--text-xs); }.bootstrap-evidence summary { cursor:pointer; font-weight:700; }.bootstrap-evidence dl { display:grid; grid-template-columns:repeat(4,minmax(0,1fr)); gap:8px; margin:10px 0 0; }.bootstrap-evidence dd { font-size:var(--text-xs); }
@media (max-width:720px) { .section-head, .context-values, .bootstrap-evidence dl { display:grid; grid-template-columns:1fr; }.phase { justify-self:start; white-space:normal; } }
</style>
