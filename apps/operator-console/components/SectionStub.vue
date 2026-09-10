<script setup lang="ts">
/**
 * SectionStub is the truthful fallback for a capability without an expanded surface.
 * It states the observed condition, its limitation, and the next safe operator step;
 * it never presents a decorative control for a missing backend.
 */
import { computed } from 'vue'
import type { HonestyClass } from '../composables/useHonesty'
import type { TruthState } from './HonestyBadge.vue'

const { t } = useI18n()
const props = defineProps<{
  title: string
  lead: string
  cls: HonestyClass
  evidence?: string
  state?: TruthState
  limitation?: string
  action?: string
  next?: string
}>()

const state = computed<TruthState>(() => props.state
  ?? (props.cls === 'stale' ? 'stale' : props.cls === 'live' ? 'empty' : 'unsupported'))
const stateTitle = computed(() => t(`honesty.states.${state.value}`))
const limitation = computed(() => props.limitation ?? t(`sectionStub.limitation.${state.value}`))
const action = computed(() => props.action ?? props.next ?? t(`sectionStub.action.${state.value}`))
const stateRole = computed(() => ['denied', 'error', 'timeout', 'offline'].includes(state.value) ? 'alert' : 'status')
</script>

<template>
  <div>
    <header class="head">
      <div class="row"><h1>{{ title }}</h1><HonestyBadge :cls="cls" :evidence="evidence" :state="state" /></div>
      <p>{{ lead }}</p>
    </header>
    <section class="empty" :data-cls="cls" :data-state="state" :role="stateRole" aria-atomic="true" aria-labelledby="section-stub-state" aria-describedby="section-stub-limitation section-stub-action">
      <h2 id="section-stub-state" class="big">{{ stateTitle }}</h2>
      <p id="section-stub-limitation" class="limitation">{{ limitation }}</p>
      <p id="section-stub-action" class="next">{{ action }}</p>
    </section>
  </div>
</template>

<style scoped>
.head .row { display:flex; align-items:center; gap:10px; }
.head h1 { margin:0 0 4px; font-size:var(--text-xl); font-weight:700; }
.head p { margin:0 0 16px; font-size:var(--text-sm); color:var(--muted); }
.empty { border:1px dashed var(--border); border-radius:var(--r-md); padding:40px 24px; text-align:center; background:var(--surface); }
.empty[data-cls="mustbuild"] { border-color:color-mix(in oklab,var(--class-mustbuild),transparent 55%); }
.empty[data-cls="dormant"], .empty[data-state="partial"], .empty[data-state="unsupported"] { border-color:color-mix(in oklab,var(--class-dormant),transparent 55%); }
.empty[data-state="denied"], .empty[data-state="error"], .empty[data-state="timeout"], .empty[data-state="offline"] { border-color:color-mix(in oklab,var(--state-danger),transparent 55%); }
.empty[data-state="stale"] { border-color:color-mix(in oklab,var(--class-stale),transparent 40%); }
.empty .big { margin:0 0 6px; font-size:var(--text-base); font-weight:600; color:var(--fg); }
.empty .limitation, .empty .next { margin:0; font-size:var(--text-sm); }
.empty .limitation { color:var(--muted); }
.empty .next { margin-top:8px; color:var(--fg-2); font-weight:600; }
</style>
