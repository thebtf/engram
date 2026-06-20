<script setup lang="ts">
/** Memory — list via EntityRow with full filter set + bulk-bar. Seam: useMemories() → GET /api/memories. */
import { ref, computed } from 'vue'
import { useMemories } from '../composables/useMockData'

const all = useMemories()
type Filter = 'all' | 'noise' | 'lowconf' | 'uncited' | 'semantic'
const FILTERS: { id: Filter; label: string }[] = [
  { id: 'all', label: 'Все' },
  { id: 'noise', label: 'Шум' },
  { id: 'lowconf', label: 'Низкая уверенность' },
  { id: 'uncited', label: 'Не использовались' },
  { id: 'semantic', label: 'semantic' },
]
const filter = ref<Filter>('all')
const rows = computed(() => all.filter(m => {
  switch (filter.value) {
    case 'noise': return m.noise
    case 'lowconf': return m.conf < 0.85
    case 'uncited': return m.cite === 0
    case 'semantic': return m.tier === 'semantic'
    default: return true
  }
}))

const selected = ref<Record<string, boolean>>({})
const selCount = computed(() => Object.values(selected.value).filter(Boolean).length)
const openId = ref<string | null>(null)

function selectAll() { rows.value.forEach(m => (selected.value[m.id] = true)) }
function clearSel() { selected.value = {} }
function onBulk(verb: string) { console.log('bulk:', verb, Object.keys(selected.value).filter(id => selected.value[id])); clearSel() }
</script>

<template>
  <div>
    <header class="head"><h1>Память</h1><p>Записи памяти: что оператор решает, как используется, где шум.</p></header>

    <div class="ops">
      <button class="tbtn" @click="selectAll">Выделить все</button>
      <button class="tbtn" @click="clearSel">Снять выбор</button>
      <button class="tbtn" @click="filter='all'">Сбросить фильтр</button>
      <span class="cnt">{{ rows.length }} из {{ all.length }}</span>
    </div>
    <div class="filterbar">
      <button v-for="f in FILTERS" :key="f.id" class="fchip" :aria-pressed="filter===f.id" @click="filter=f.id">{{ f.label }}</button>
    </div>

    <div class="grid">
      <EntityRow
        v-for="m in rows" :key="m.id"
        :preview="m.content"
        :meta="[m.project, m.type, `${m.cite}/${m.inj}`, `conf ${m.conf}`, m.age]"
        :status="m.noise ? 'warn' : 'live'"
        selectable
        v-model:selected="selected[m.id]"
        :open="openId === m.id"
        @open="openId = openId === m.id ? null : m.id">
        <template #side>
          <HonestyBadge :cls="m.noise ? 'dormant' : 'live'" :evidence="m.noise ? 'noise' : undefined" />
        </template>
      </EntityRow>
    </div>

    <BulkBar :count="selCount" :verbs="['Скрыть как шум', 'Заменить версией', 'Сменить проект…']"
      note="сначала снимок · затем применение" @act="onBulk" @clear="clearSel" />
  </div>
</template>

<style scoped>
.head h1 { margin:0 0 4px; font-size:var(--text-xl); font-weight:700; }
.head p { margin:0 0 16px; font-size:var(--text-sm); color:var(--muted); }
.ops { display:flex; align-items:center; gap:8px; margin-bottom:10px; }
.ops .tbtn { font-size:var(--text-xs); padding:5px 10px; border:1px solid var(--border); border-radius:var(--r-sm); background:transparent; color:var(--fg-2); cursor:pointer; }
.ops .cnt { margin-left:auto; font-size:var(--text-xs); color:var(--muted); }
.filterbar { display:flex; align-items:center; gap:8px; flex-wrap:wrap; margin-bottom:12px; }
.fchip { font-size:var(--text-sm); padding:5px 14px; border:0; border-radius:var(--radius-pill); background:var(--surface-warm); color:var(--fg-2); cursor:pointer; }
.fchip[aria-pressed="true"] { background:var(--accent); color:var(--accent-on); }
.grid { border:1px solid var(--border); border-radius:var(--r-md); background:var(--surface); overflow:hidden; }
</style>
