<script setup lang="ts">
import { computed } from 'vue'
import type { CodeBootstrapEvidence, CodeBootstrapPhase, CodeSafeContext } from '~/composables/useOperatorCode'

const props = defineProps<{
  phase: CodeBootstrapPhase
  candidate: CodeSafeContext | null
  pinned: CodeSafeContext | null
  pending: boolean
  message: string
  evidence: CodeBootstrapEvidence
}>()

const emit = defineEmits<{
  refresh: []
  pin: []
  retry: []
}>()

const phaseLabel = computed(() => {
  switch (props.phase) {
    case 'binding': return 'Establishing a server-issued document binding'
    case 'ready': return 'Binding ready — no context selected'
    case 'collision': return 'Copied tab — fresh binding, no inherited context'
    case 'ambiguous': return 'Ambiguous bootstrap — fresh binding, no inherited context'
    case 'reload-pending': return 'Reload is pending server lease closure'
    case 'denied': return 'Binding was denied'
    case 'error': return 'Binding could not be established'
    default: return 'Waiting to establish the browser binding'
  }
})
</script>

<template>
  <section class="context-picker" aria-labelledby="code-context-heading">
    <div class="section-head">
      <div>
        <h2 id="code-context-heading">Pinned source view</h2>
        <p>Choose only the server-presented authorized view. This console never supplies a path, label, Space, or administrator selector.</p>
      </div>
      <span class="phase" :data-state="phase">{{ phaseLabel }}</span>
    </div>

    <p class="message" aria-live="polite" data-testid="code-context-message">{{ message }}</p>

    <dl v-if="candidate !== null" class="context-values" data-testid="code-context-candidate">
      <div><dt>Source</dt><dd>{{ candidate.source }}</dd></div>
      <div><dt>Checkout</dt><dd>{{ candidate.checkout }}</dd></div>
      <div><dt>View</dt><dd>{{ candidate.view }}</dd></div>
    </dl>
    <div v-else class="empty" data-testid="code-context-empty">
      <strong>No selectable view is available.</strong>
      <p>When the server cannot resolve exactly one active grant, it returns no labels or contextual facts.</p>
    </div>

    <div class="actions">
      <button class="btn" type="button" :disabled="pending" @click="emit('refresh')">Check authorized view</button>
      <button v-if="phase === 'reload-pending'" class="btn" type="button" :disabled="pending" @click="emit('retry')">Retry reload binding</button>
      <button class="btn primary" type="button" :disabled="pending || candidate === null || pinned !== null" data-testid="code-pin-context" @click="emit('pin')">
        {{ pinned === null ? 'Pin this view' : 'Pinned by server' }}
      </button>
    </div>

    <p v-if="pinned !== null" class="pinned" data-testid="code-context-pinned">
      Server pin confirmed for {{ pinned.source }} / {{ pinned.checkout }} / {{ pinned.view }}.
    </p>

    <dl class="bootstrap-evidence" data-testid="code-bootstrap-evidence">
      <div><dt>Navigation</dt><dd>{{ evidence.navigationType }}</dd></div>
      <div><dt>Opener before</dt><dd>{{ evidence.openerBefore ? 'present' : 'none' }}</dd></div>
      <div><dt>Opener readback</dt><dd>{{ evidence.openerAfter === null ? 'not needed' : evidence.openerAfter ? 'normalized to null' : 'normalization failed' }}</dd></div>
      <div><dt>Transition</dt><dd>{{ evidence.transition }}</dd></div>
    </dl>
  </section>
</template>

<style scoped>
.context-picker { border:1px solid var(--border); border-radius:var(--r-md); background:var(--surface); padding:16px; display:grid; gap:14px; }
.section-head { display:flex; align-items:flex-start; justify-content:space-between; gap:16px; }
h2 { margin:0; font-size:var(--text-sm); font-weight:800; }
.section-head p, .message, .empty p, .pinned { margin:4px 0 0; color:var(--muted); font-size:var(--text-sm); }
.phase { border:1px solid var(--border); border-radius:var(--radius-pill); padding:4px 8px; color:var(--fg-2); font-size:var(--text-xs); white-space:nowrap; }
.phase[data-state='collision'], .phase[data-state='ambiguous'], .phase[data-state='reload-pending'] { border-color:color-mix(in oklab, var(--warn), transparent 35%); color:var(--warn); }
.phase[data-state='denied'], .phase[data-state='error'] { border-color:color-mix(in oklab, var(--danger), transparent 35%); color:var(--danger); }
.message { margin:0; }
.context-values { display:grid; grid-template-columns:repeat(3,minmax(0,1fr)); gap:10px; margin:0; }
.context-values div, .bootstrap-evidence div { min-width:0; border:1px solid var(--border-soft); border-radius:var(--r-sm); background:var(--bg); padding:10px; }
dt { color:var(--muted); font-size:var(--text-xs); letter-spacing:.04em; text-transform:uppercase; }
dd { margin:5px 0 0; color:var(--fg); font-family:var(--font-mono); font-size:var(--text-sm); overflow-wrap:anywhere; }
.empty { border:1px dashed var(--border); border-radius:var(--r-sm); background:var(--bg); padding:14px; }
.empty strong { color:var(--fg); }
.actions { display:flex; flex-wrap:wrap; gap:8px; }
.btn { min-height:36px; border:1px solid var(--border); border-radius:var(--r-sm); background:var(--surface); color:var(--fg); padding:8px 12px; font:inherit; font-size:var(--text-sm); font-weight:700; cursor:pointer; }
.btn.primary { border-color:var(--accent); background:var(--accent); color:var(--accent-on); }
.btn:disabled { cursor:not-allowed; opacity:.55; }
.pinned { margin:0; color:var(--success); }
.bootstrap-evidence { display:grid; grid-template-columns:repeat(4,minmax(0,1fr)); gap:8px; margin:0; }
.bootstrap-evidence dd { font-size:var(--text-xs); }
@media (max-width: 720px) {
  .section-head, .context-values, .bootstrap-evidence { display:grid; grid-template-columns:1fr; }
  .phase { justify-self:start; white-space:normal; }
}
</style>
