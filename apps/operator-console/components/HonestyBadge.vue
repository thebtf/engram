<script lang="ts">
export const TRUTH_STATES = ['loading', 'empty', 'denied', 'error', 'stale', 'partial', 'unsupported', 'timeout', 'offline'] as const
export type TruthState = typeof TRUTH_STATES[number]
</script>

<script setup lang="ts">
/**
 * HonestyBadge identifies both a surface classification and its current evidence state.
 * Classification answers whether a capability exists; the state answers what the operator
 * can truthfully do now. State text is always rendered so color is never the only cue.
 */
import { computed } from 'vue'
import { useHonesty, assertEvidence, type HonestyClass } from '../composables/useHonesty'

const { t } = useI18n()
const props = defineProps<{ cls: HonestyClass; evidence?: string; label?: string; state?: TruthState }>()
const meta = computed(() => useHonesty(props.cls))
const classificationLabel = computed(() => props.label ?? t(meta.value.labelKey))
const stateLabel = computed(() => props.state ? t(`honesty.states.${props.state}`) : '')
const accessibleLabel = computed(() => props.state
  ? t('honesty.a11y', { classification: classificationLabel.value, state: stateLabel.value })
  : classificationLabel.value)
assertEvidence(props.cls, props.evidence)
</script>

<template>
  <span class="hb" :data-cls="cls" :data-state="state" :aria-label="accessibleLabel">
    <!-- stale is an OUTLINE ring, never filled (Classification Rule) -->
    <span class="hb-dot" :class="{ ring: cls === 'stale' }" :style="{ background: cls === 'stale' ? 'transparent' : meta.color, borderColor: meta.color }" aria-hidden="true" />
    <span class="hb-lbl">{{ classificationLabel }}</span>
    <span v-if="state" class="hb-state">{{ stateLabel }}</span>
    <code v-if="meta.needsEvidence && evidence" class="hb-ev">{{ evidence }}</code>
  </span>
</template>

<style scoped>
.hb { display:inline-flex; align-items:center; gap:6px; max-width:100%; font-size:10px; font-weight:700; letter-spacing:.04em; text-transform:uppercase; }
.hb-dot { width:8px; height:8px; border-radius:50%; flex:none; }
.hb-dot.ring { border:1.5px solid; background:transparent !important; }
.hb-lbl { color:v-bind('meta.color'); }
.hb-state, .hb-ev { border:1px solid var(--border); border-radius:5px; color:var(--fg-2); font-size:10px; letter-spacing:0; text-transform:none; }
.hb-state { min-width:0; overflow-wrap:anywhere; padding:1px 5px; }
.hb-ev { font-family:var(--font-mono); color:var(--muted); padding:1px 5px; }
.hb[data-state="denied"] .hb-state, .hb[data-state="error"] .hb-state, .hb[data-state="timeout"] .hb-state, .hb[data-state="offline"] .hb-state { border-color:color-mix(in oklab,var(--state-danger),transparent 45%); color:var(--state-danger); }
.hb[data-state="partial"] .hb-state, .hb[data-state="unsupported"] .hb-state { border-color:color-mix(in oklab,var(--class-dormant),transparent 45%); color:var(--class-dormant); }
.hb[data-state="stale"] .hb-state { border-style:dashed; color:var(--class-stale); }
</style>
