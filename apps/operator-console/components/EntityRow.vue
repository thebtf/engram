<script setup lang="ts">
/**
 * EntityRow — the keystone list row used by Memory, Rules, Issues, Secrets, etc.
 * Not in shadcn (its Table row is generic); .od EntityRow carries: optional checkbox,
 * status dot, preview+meta body, and a side slot for row actions. Accent appears ONLY
 * as the selected/open inset stripe — never a decorative left-border (Don't rule).
 *
 * <EntityRow :selectable="true" v-model:selected="sel" :open="isOpen"
 *   status="live" preview="prod auto-deploys on tag" :meta="['engram','3д']"
 *   @open="openDetail">
 *   <template #side><button class="act">Открыть</button></template>
 * </EntityRow>
 */
const props = defineProps<{
  preview: string
  meta?: string[]
  status?: 'live' | 'dormant' | 'stale' | 'mustbuild' | 'ok' | 'warn' | 'off'
  selectable?: boolean
  open?: boolean
}>()
const selected = defineModel<boolean>('selected', { default: false })
const emit = defineEmits<{ open: [] }>()

const dotColor: Record<string, string> = {
  live: 'var(--class-live)', ok: 'var(--class-live)',
  dormant: 'var(--class-dormant)', warn: 'var(--state-warn)',
  stale: 'var(--class-stale)', off: 'var(--muted)',
  mustbuild: 'var(--class-mustbuild)',
}
</script>

<template>
  <div class="erow" :class="{ sel: selected, open }" @click="emit('open')">
    <input v-if="selectable" type="checkbox" class="echk" :checked="selected"
           @click.stop @change="selected = ($event.target as HTMLInputElement).checked" />
    <span v-if="status" class="estate" :style="{ background: dotColor[status] ?? 'var(--muted)' }" />
    <div class="ebody">
      <div class="epreview">{{ preview }}</div>
      <div v-if="meta?.length" class="emeta"><span v-for="(m, i) in meta" :key="i">{{ m }}</span></div>
    </div>
    <div class="eside" @click.stop><slot name="side" /></div>
  </div>
</template>

<style scoped>
.erow { display:flex; align-items:flex-start; gap:13px; padding:14px; border-bottom:1px solid var(--border-soft); cursor:pointer; position:relative; }
.erow:hover { background:var(--surface-warm); }
/* accent ONLY as inset stripe on select/open — never a plain border-left */
.erow.sel  { background:color-mix(in oklab,var(--accent),transparent 92%); box-shadow:inset 2px 0 0 var(--accent); }
.erow.open { background:color-mix(in oklab,var(--accent),transparent 88%); box-shadow:inset 3px 0 0 var(--accent); }
.echk { margin-top:3px; flex:none; }
.estate { width:8px; height:8px; border-radius:50%; flex:none; margin-top:5px; }
.ebody { flex:1; min-width:0; }
.epreview { font-family:var(--font-mono); font-size:var(--text-sm); color:var(--fg); white-space:nowrap; overflow:hidden; text-overflow:ellipsis; }
.emeta { display:flex; gap:9px; flex-wrap:wrap; margin-top:4px; font-size:var(--text-xs); color:var(--muted); }
.eside { display:flex; align-items:center; gap:10px; flex:none; }
</style>
