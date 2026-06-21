<script setup lang="ts">
/** SectionStub — honest placeholder for sections whose backend is not built/enabled.
 *  Renders the classification + evidence and an empty-state with a concrete next step —
 *  never an inert operable control on a must-build surface (DESIGN.md Don't). */
import type { HonestyClass } from '../composables/useHonesty'
const { t } = useI18n()
defineProps<{ title: string; lead: string; cls: HonestyClass; evidence?: string; next?: string }>()
const stateKeys: Record<HonestyClass, string> = {
  live: 'sectionStub.state.live',
  dormant: 'sectionStub.state.dormant',
  stale: 'sectionStub.state.stale',
  mustbuild: 'sectionStub.state.mustbuild',
}
</script>

<template>
  <div>
    <header class="head">
      <div class="row"><h1>{{ title }}</h1><HonestyBadge :cls="cls" :evidence="evidence" /></div>
      <p>{{ lead }}</p>
    </header>
    <div class="empty" :data-cls="cls">
      <p class="big">{{ t(stateKeys[cls]) }}</p>
      <p v-if="next" class="next">{{ next }}</p>
    </div>
  </div>
</template>

<style scoped>
.head .row { display:flex; align-items:center; gap:10px; }
.head h1 { margin:0 0 4px; font-size:var(--text-xl); font-weight:700; }
.head p { margin:0 0 16px; font-size:var(--text-sm); color:var(--muted); }
.empty { border:1px dashed var(--border); border-radius:var(--r-md); padding:40px 24px; text-align:center; background:var(--surface); }
.empty[data-cls="mustbuild"] { border-color:color-mix(in oklab,var(--class-mustbuild),transparent 55%); }
.empty[data-cls="dormant"] { border-color:color-mix(in oklab,var(--class-dormant),transparent 55%); }
.empty .big { margin:0 0 6px; font-size:var(--text-base); font-weight:600; color:var(--fg); }
.empty .next { margin:0; font-size:var(--text-sm); color:var(--muted); }
</style>
