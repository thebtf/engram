<script setup lang="ts">
/**
 * HonestyBadge — the classification chip. Not in shadcn/Nuxt UI; this is .od-specific.
 * Renders the live/dormant/stale/mustbuild marker, and (for dormant/mustbuild) the
 * mandatory evidence (gate flag or endpoint) as a second-layer mono note.
 *
 * <HonestyBadge cls="mustbuild" evidence="GET /api/migrations" />
 * <HonestyBadge cls="dormant"   evidence="VNEXT_F" />
 * <HonestyBadge cls="live" />
 */
import { computed } from 'vue'
import { useHonesty, assertEvidence, type HonestyClass } from '../composables/useHonesty'

const { t } = useI18n()
const props = defineProps<{ cls: HonestyClass; evidence?: string; label?: string }>()
const meta = computed(() => useHonesty(props.cls))
assertEvidence(props.cls, props.evidence)
</script>

<template>
  <span class="hb" :data-cls="cls">
    <!-- stale is an OUTLINE ring, never filled (Classification Rule) -->
    <span class="hb-dot" :class="{ ring: cls === 'stale' }" :style="{ background: cls === 'stale' ? 'transparent' : meta.color, borderColor: meta.color }" />
    <span class="hb-lbl">{{ label ?? t(meta.labelKey) }}</span>
    <code v-if="meta.needsEvidence && evidence" class="hb-ev">{{ evidence }}</code>
  </span>
</template>

<style scoped>
.hb { display:inline-flex; align-items:center; gap:6px; font-size:10px; font-weight:700; letter-spacing:.04em; text-transform:uppercase; }
.hb-dot { width:8px; height:8px; border-radius:50%; flex:none; }
.hb-dot.ring { border:1.5px solid; background:transparent !important; }
.hb-lbl { color:v-bind('meta.color'); }
.hb-ev { font-family:var(--font-mono); font-size:10px; text-transform:none; letter-spacing:0; color:var(--muted); padding:1px 5px; border:1px solid var(--border); border-radius:5px; }
</style>
