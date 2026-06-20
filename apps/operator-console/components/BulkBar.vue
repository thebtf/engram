<script setup lang="ts">
/**
 * BulkBar — sticky action bar that appears only after rows are selected (.od bulkbar).
 * Mirrors the contract: snapshot-then-apply, comment-required for destructive ops, never
 * inert buttons. Shows count + the verb set passed by the parent. Lives at the bottom of
 * the work area; not a separate page (DESIGN.md: it's a component, not a product screen).
 *
 * <BulkBar v-if="count" :count="count" :verbs="['Скрыть как шум','Заменить версией']"
 *   note="сначала снимок · затем применение" @act="onBulk" @clear="clearSel" />
 */
defineProps<{ count: number; verbs: string[]; note?: string }>()
const emit = defineEmits<{ act: [string]; clear: [] }>()
</script>

<template>
  <Transition name="bb">
    <div v-if="count > 0" class="bulkbar">
      <span class="bc">{{ count }} выбрано</span>
      <span class="bsp" />
      <button v-for="v in verbs" :key="v" class="act" @click="emit('act', v)">{{ v }}</button>
      <span v-if="note" class="note">{{ note }}</span>
      <button class="tbtn" @click="emit('clear')">Снять выбор</button>
    </div>
  </Transition>
</template>

<style scoped>
.bulkbar { position:sticky; bottom:0; display:flex; align-items:center; gap:10px; padding:11px 16px; margin-top:14px;
  background:var(--surface); border:1px solid var(--border); border-radius:var(--r-md); box-shadow:var(--elev-raised); }
.bc { font-size:var(--text-sm); font-weight:600; color:var(--fg); }
.bsp { width:1px; height:18px; background:var(--border); }
.act { font-size:var(--text-sm); padding:5px 11px; border:1px solid var(--border); border-radius:var(--r-sm); background:transparent; color:var(--fg); cursor:pointer; }
.act:hover { border-color:var(--accent); }
.note { margin-left:auto; font-family:var(--font-mono); font-size:11px; color:var(--muted); }
.tbtn { font-size:var(--text-sm); padding:5px 10px; border:0; background:transparent; color:var(--fg-2); cursor:pointer; }
.bb-enter-active, .bb-leave-active { transition:opacity var(--motion-fast) var(--ease-standard), transform var(--motion-fast) var(--ease-standard); }
.bb-enter-from, .bb-leave-to { opacity:0; transform:translateY(6px); }
</style>
