<script setup lang="ts">
import { computed } from 'vue'
import type { MutationResult } from '../composables/useApi'

const props = withDefaults(defineProps<{
  result: MutationResult | null
  recheckLabel?: string
}>(), {
  recheckLabel: undefined,
})

const emit = defineEmits<{
  recheck: []
}>()

const { t } = useI18n()

const needsManualCheck = computed(() => {
  const kind: string | undefined = props.result?.kind
  return kind === 'committed_verification_pending' || kind === 'outcome_unknown'
})

const stateClass = computed(() => {
  if (!props.result) return 'empty'
  if (props.result.kind === 'committed_verified') return 'verified'
  if (props.result.kind === 'committed_verification_pending') return 'pending'
  if (props.result.kind === 'partial') return 'partial'
  return 'failure'
})
</script>

<template>
  <section
    v-if="result"
    class="mutation-result"
    :class="stateClass"
    :data-kind="result.kind"
    data-testid="mutation-result"
    role="status"
    aria-live="polite"
  >
    <strong>{{ t(`mutationOutcome.outcomes.${result.kind}.title`) }}</strong>
    <p>{{ t(`mutationOutcome.outcomes.${result.kind}.body`) }}</p>

    <dl class="mutation-evidence">
      <dt>{{ t('mutationOutcome.request') }}</dt>
      <dd class="mono" data-testid="mutation-request-reference">{{ result.request.requestId }}</dd>
      <dt>{{ t('mutationOutcome.action') }}</dt>
      <dd>{{ result.request.action }}</dd>
      <template v-if="result.operationId">
        <dt>{{ t('mutationOutcome.operation') }}</dt>
        <dd class="mono">{{ result.operationId }}</dd>
      </template>
      <template v-if="result.code">
        <dt>{{ t('mutationOutcome.code') }}</dt>
        <dd class="mono">{{ result.code }}</dd>
      </template>
      <template v-if="result.kind === 'committed_verified'">
        <dt>{{ t('mutationOutcome.readback') }}</dt>
        <dd>{{ t(`mutationOutcome.readbackKinds.${result.readback.kind}`) }}</dd>
        <template v-if="result.readback.version !== undefined">
          <dt>{{ t('mutationOutcome.version') }}</dt>
          <dd class="mono">{{ result.readback.version }}</dd>
        </template>
      </template>
    </dl>

    <ul v-if="result.kind === 'partial'" class="mutation-items" data-testid="mutation-item-outcomes">
      <li v-for="item in result.items" :key="item.targetId">
        <code>{{ item.targetId }}</code>
        <span>{{ t(`mutationOutcome.items.${item.outcome}`) }}</span>
      </li>
    </ul>

    <p v-if="needsManualCheck" class="manual-check" data-testid="mutation-retained-input">
      {{ t('mutationOutcome.manualCheck') }}
    </p>
    <button
      v-if="needsManualCheck && recheckLabel"
      class="tbtn"
      type="button"
      data-testid="mutation-recheck"
      @click="emit('recheck')"
    >
      {{ recheckLabel }}
    </button>
  </section>
</template>

<style scoped>
.mutation-result { display:grid; gap:8px; padding:12px 14px; border:1px solid var(--border); border-radius:var(--r-md); background:var(--surface); color:var(--fg); }
.mutation-result.verified { border-color:color-mix(in oklab, var(--accent), var(--border) 55%); }
.mutation-result.pending, .mutation-result.partial { border-color:color-mix(in oklab, var(--warning, #a16207), var(--border) 55%); }
.mutation-result.failure { border-color:color-mix(in oklab, var(--danger, #b91c1c), var(--border) 65%); }
.mutation-result strong { font-size:var(--text-sm); }
.mutation-result p { margin:0; color:var(--fg-2); font-size:var(--text-sm); }
.mutation-evidence { display:grid; grid-template-columns:max-content minmax(0, 1fr); gap:4px 10px; margin:0; font-size:var(--text-xs); }
.mutation-evidence dt { color:var(--muted); }
.mutation-evidence dd { min-width:0; margin:0; overflow-wrap:anywhere; }
.mono, code { font-family:var(--font-mono); }
.mutation-items { display:grid; gap:4px; margin:0; padding-left:18px; font-size:var(--text-xs); }
.mutation-items li { display:flex; gap:8px; align-items:baseline; }
.manual-check { font-weight:700; }
</style>
