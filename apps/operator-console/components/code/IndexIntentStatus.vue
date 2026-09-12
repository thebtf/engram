<script setup lang="ts">
import { computed } from 'vue'
import type { CodeSafeContext, IndexIntentKind, IndexIntentPresentationState } from '~/composables/useOperatorCode'

const { t } = useI18n()

const props = defineProps<{
  pinned: CodeSafeContext | null
  state: IndexIntentPresentationState
  busy: boolean
  pending: boolean
}>()

const emit = defineEmits<{
  submit: [kind: IndexIntentKind]
  retry: []
  refresh: []
}>()

const active = computed(() => ['loading', 'submitted', 'queued', 'acknowledged', 'running'].includes(props.state.kind))
const canStart = computed(() => props.pinned !== null && !props.busy && !props.pending && !active.value)
const canRetry = computed(() => props.pinned !== null && props.state.kind === 'unavailable' && !props.busy && !props.pending)
const canRefresh = computed(() => props.pinned !== null && props.state.kind !== 'idle' && !props.busy && !props.pending)
const statusCopy = computed(() => ({
  title: t(`codeExplorer.intent.states.${props.state.kind}.title`),
  message: t(`codeExplorer.intent.states.${props.state.kind}.message`),
}))
</script>

<template>
  <section class="index-intent" aria-labelledby="index-intent-heading" data-testid="index-intent-status">
    <div class="head">
      <div>
        <h2 id="index-intent-heading">{{ t('codeExplorer.intent.title') }}</h2>
        <p>{{ t('codeExplorer.intent.lead') }}</p>
      </div>
      <span class="state" :data-state="state.kind" data-testid="index-intent-state">{{ t(`codeExplorer.intent.states.${state.kind}.label`) }}</span>
    </div>

    <div class="readout" role="status" aria-live="polite" aria-atomic="true">
      <strong>{{ statusCopy.title }}</strong>
      <p>{{ statusCopy.message }}</p>
      <small v-if="state.attempt !== null">{{ t('codeExplorer.intent.attempt', { attempt: state.attempt }) }}</small>
    </div>

    <div v-if="pinned !== null" class="actions">
      <button class="btn primary" type="button" :disabled="!canStart" data-testid="index-intent-reindex" @click="emit('submit', 'reindex')">{{ t('codeExplorer.intent.reindex') }}</button>
      <button class="btn" type="button" :disabled="!canStart" data-testid="index-intent-reconcile" @click="emit('submit', 'reconcile')">{{ t('codeExplorer.intent.reconcile') }}</button>
      <button v-if="state.kind === 'unavailable'" class="btn" type="button" :disabled="!canRetry" data-testid="index-intent-retry" @click="emit('retry')">{{ t('codeExplorer.intent.retry') }}</button>
      <button v-if="state.kind !== 'idle'" class="btn" type="button" :disabled="!canRefresh" data-testid="index-intent-check-status" @click="emit('refresh')">{{ t('codeExplorer.intent.refresh') }}</button>
    </div>
    <p v-else class="recovery">{{ t('codeExplorer.intent.unpinned') }}</p>
  </section>
</template>

<style scoped>
.index-intent { display:grid; gap:12px; min-width:0; border:1px solid var(--border); border-radius:var(--r-md); background:var(--surface); padding:14px 16px; }
.head { display:flex; align-items:flex-start; justify-content:space-between; gap:16px; }
h2 { margin:0; color:var(--fg); font-size:var(--text-sm); font-weight:800; }
.head p, .readout p, .recovery { max-width:78ch; margin:4px 0 0; color:var(--muted); font-size:var(--text-sm); }
.state { flex:0 0 auto; border:1px solid var(--border); border-radius:var(--radius-pill); padding:4px 8px; color:var(--fg-2); font-size:var(--text-xs); font-weight:700; white-space:nowrap; }
.state[data-state='unavailable'] { border-color:color-mix(in oklab,var(--warn),transparent 35%); color:var(--warn); }
.state[data-state='failed'], .state[data-state='error'], .state[data-state='denied'], .state[data-state='offline'] { border-color:color-mix(in oklab,var(--danger),transparent 35%); color:var(--danger); }
.state[data-state='completed'] { border-color:color-mix(in oklab,var(--success),transparent 35%); color:var(--success); }
.readout { min-width:0; border-block:1px solid var(--border-soft); padding:10px 0; }
.readout strong { color:var(--fg); font-size:var(--text-sm); }
.readout small { display:block; margin-top:6px; color:var(--fg-2); font-family:var(--font-mono); font-size:var(--text-xs); font-variant-numeric:tabular-nums; }
.actions { display:flex; flex-wrap:wrap; gap:8px; }
.btn { min-height:36px; border:1px solid var(--border); border-radius:var(--r-sm); background:var(--surface); color:var(--fg); padding:8px 12px; font:inherit; font-size:var(--text-sm); font-weight:700; cursor:pointer; }
.btn.primary { border-color:var(--accent); background:var(--accent); color:var(--accent-on); }
.btn:disabled { cursor:not-allowed; opacity:.55; }
@media (pointer:coarse) { .btn { min-height:44px; } }
@media (max-width:720px) { .head { display:grid; }.state { justify-self:start; white-space:normal; } }
</style>
