<script setup lang="ts">
/**
 * SwitchRow — a runtime flag toggle with the honesty + restart-required contract.
 * Not in shadcn; .od-specific because of: classification dot, reload badge, evidence
 * line, and the secret-discipline note. Use for Settings → Runtime flags.
 *
 * <SwitchRow v-model="quietMode" cls="live" title="Тихий режим"
 *   desc="Глушит только injection-хуки; capture продолжает работать."
 *   evidence="ENGRAM_QUIET" :reload="false" />
 *
 * reload=true means the change does NOT take effect until the server restarts —
 * the badge is the operator's only warning (Do: restart-required badge).
 */
import { useHonesty, type HonestyClass } from '../composables/useHonesty'

const model = defineModel<boolean>({ default: false })
const props = defineProps<{
  title: string
  desc?: string
  cls?: HonestyClass
  evidence?: string      // env name / flag — shown as second-layer mono
  reload?: boolean       // restart-required?
  danger?: boolean       // on-state is red instead of green (destructive flag)
  disabled?: boolean
}>()
const meta = props.cls ? useHonesty(props.cls) : null
function toggle() { if (!props.disabled && (meta?.operable ?? true)) model.value = !model.value }
</script>

<template>
  <div class="sw-row" :class="{ dis: disabled || (meta && !meta.operable) }">
    <div class="sw-main">
      <div class="sw-head">
        <span v-if="meta" class="sw-dot" :style="{ background: cls === 'stale' ? 'transparent' : meta.color, border: cls === 'stale' ? `1.5px solid ${meta.color}` : '0' }" />
        <b class="sw-title">{{ title }}</b>
        <span v-if="reload" class="rbadge restart">restart</span>
      </div>
      <p v-if="desc" class="sw-desc">{{ desc }}</p>
      <code v-if="evidence" class="sw-ev">{{ evidence }}</code>
    </div>
    <button
      class="toggle" role="switch" :aria-checked="model" :data-danger="danger || undefined"
      :disabled="disabled || (meta && !meta.operable)" @click="toggle">
      <span class="knob" />
    </button>
  </div>
</template>

<style scoped>
.sw-row { display:flex; align-items:flex-start; gap:16px; padding:14px 0; border-bottom:1px solid var(--border-soft); }
.sw-row.dis { opacity:.55; }
.sw-main { flex:1; min-width:0; }
.sw-head { display:flex; align-items:center; gap:8px; }
.sw-dot { width:8px; height:8px; border-radius:50%; flex:none; }
.sw-title { font-size:var(--text-sm); font-weight:600; color:var(--fg); }
.sw-desc { margin:4px 0 0; font-size:var(--text-sm); color:var(--fg-2); line-height:1.45; max-width:62ch; }
.sw-ev { display:inline-block; margin-top:6px; font-family:var(--font-mono); font-size:11px; color:var(--muted); }
.rbadge { font-size:9px; font-weight:700; text-transform:uppercase; letter-spacing:.05em; padding:2px 6px; border-radius:5px; }
.rbadge.restart { color:var(--state-warn); background:color-mix(in oklab,var(--state-warn),transparent 88%); border:1px solid color-mix(in oklab,var(--state-warn),transparent 62%); }
/* toggle: 42×24 pill, --border off, --class-live on, --state-danger on for danger */
.toggle { width:42px; height:24px; flex:none; border:0; border-radius:var(--radius-pill); background:var(--border); position:relative; cursor:pointer; transition:background var(--motion-fast) var(--ease-standard); }
.toggle[aria-checked="true"] { background:var(--class-live); }
.toggle[aria-checked="true"][data-danger] { background:var(--state-danger); }
.toggle:disabled { cursor:not-allowed; }
.knob { position:absolute; top:3px; left:3px; width:18px; height:18px; border-radius:50%; background:#fff; transition:left var(--motion-fast) var(--ease-standard); }
.toggle[aria-checked="true"] .knob { left:21px; }
</style>
