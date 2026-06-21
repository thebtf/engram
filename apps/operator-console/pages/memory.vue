<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useOperatorMemoryLab } from '../composables/useOperatorMemoryLab'
import type { Memory } from '../composables/useMockData'

const { t } = useI18n()
const {
  rows: all,
  loadState,
  pending,
  error,
  refresh,
  storeMemory,
  deleteMemory,
  auditGap,
  provenanceGap,
} = useOperatorMemoryLab()

type MemoryFilter = 'active' | 'flagged' | 'stale' | 'low' | 'chain'

const project = ref('all')
const filters = ref<Record<MemoryFilter, boolean>>({
  active: false,
  flagged: false,
  stale: false,
  low: false,
  chain: false,
})
const selected = ref<Record<string, boolean>>({})
const openId = ref<string | null>(null)
const page = ref(1)
const pageSize = ref(10)

const projects = computed(() => [...new Set(all.map((memory) => memory.project).filter(Boolean))].sort())
const activeFilterCount = computed(() => Object.values(filters.value).filter(Boolean).length + (project.value === 'all' ? 0 : 1))

function hasFilter(memory: Memory, key: MemoryFilter) {
  switch (key) {
    case 'active': return !memory.noise
    case 'flagged': return Boolean(memory.noise) || memory.conf < 0.8
    case 'stale': return memory.cite === 0 && memory.inj > 0
    case 'low': return memory.conf < 0.85
    case 'chain': return memory.tags.some((tag) => ['superseded', 'replace', 'replacement'].includes(tag))
    default: return true
  }
}

const filtered = computed(() => all.filter((memory) => {
  if (project.value !== 'all' && memory.project !== project.value) return false
  const activeFilters = Object.entries(filters.value).filter(([, enabled]) => enabled).map(([key]) => key as MemoryFilter)
  if (!activeFilters.length) return true
  return activeFilters.every((key) => hasFilter(memory, key))
}))

const pageCount = computed(() => Math.max(1, Math.ceil(filtered.value.length / pageSize.value)))
const pageRows = computed(() => {
  const start = (page.value - 1) * pageSize.value
  return filtered.value.slice(start, start + pageSize.value)
})
const selectedIds = computed(() => Object.entries(selected.value).filter(([, value]) => value).map(([id]) => id))
const selectedCount = computed(() => selectedIds.value.length)
const opened = computed(() => all.find((memory) => memory.id === openId.value) || null)
const noiseRatio = computed(() => {
  if (!all.length) return '—'
  return (all.filter((memory) => memory.noise).length / all.length).toFixed(2)
})

watch([filtered, pageSize], () => {
  if (page.value > pageCount.value) page.value = pageCount.value
})

function toggleFilter(key: MemoryFilter) {
  filters.value[key] = !filters.value[key]
  page.value = 1
}

function resetFilter() {
  project.value = 'all'
  filters.value = { active: false, flagged: false, stale: false, low: false, chain: false }
  page.value = 1
}

function selectAll() {
  const next = { ...selected.value }
  filtered.value.forEach((memory) => { next[memory.id] = true })
  selected.value = next
}

function clearSelected() {
  selected.value = {}
}

function toggleSelected(id: string) {
  selected.value = { ...selected.value, [id]: !selected.value[id] }
}

function rowState(memory: Memory) {
  if (memory.noise) return 'noise'
  if (memory.conf < 0.8) return 'stale'
  return 'active'
}

function rowStateClass(memory: Memory) {
  const state = rowState(memory)
  if (state === 'noise') return 'warn'
  if (state === 'stale') return 'stale'
  return 'live'
}

function rowStateLabel(memory: Memory) {
  return t(`memory.rowState.${rowState(memory)}`)
}

function formatConfidence(memory: Memory) {
  return `${Math.round(memory.conf * 100)}%`
}

async function deleteOpened() {
  if (!opened.value) return
  const id = opened.value.id
  openId.value = null
  selected.value = { ...selected.value, [id]: false }
  await deleteMemory(id)
}

async function storeCopy() {
  if (!opened.value) return
  await storeMemory({
    project: opened.value.project,
    content: opened.value.content,
    tags: opened.value.tags,
  })
}
</script>

<template>
  <div class="memory-page">
    <header class="page-head">
      <h1>{{ t('memory.title') }}</h1>
      <p>{{ t('memory.subtitle') }}</p>
    </header>

    <section class="legend">
      <div class="legend-left">
        <span class="lg"><span class="ld live" />{{ t('memory.legend.active') }}</span>
        <span class="lg"><span class="ld dormant" />{{ t('memory.legend.dormant') }}</span>
        <span class="lg"><span class="ld stale" />{{ t('memory.legend.stale') }}</span>
        <span class="lg"><span class="ld mustbuild" />{{ t('memory.legend.mustBuild') }}</span>
      </div>
      <div class="legend-right">
        <span class="lg"><span class="ld live" />{{ t('memory.legend.used') }}</span>
        <span class="lg"><span class="ld warn" />{{ t('memory.legend.shownUnused') }}</span>
      </div>
    </section>

    <section class="ops">
      <div class="ops-left">
        <button class="tbtn" @click="selectAll">{{ t('memory.actions.selectAll') }}</button>
        <button class="tbtn" @click="clearSelected">{{ t('memory.actions.clearSelection') }}</button>
        <button class="tbtn" :disabled="activeFilterCount === 0" @click="resetFilter">{{ t('memory.actions.resetFilter') }}</button>
        <span class="cnt">{{ t('memory.countInFilter', { shown: filtered.length, total: all.length }) }}</span>
      </div>
      <div class="ops-right">
        <label>{{ t('memory.rowsLabel') }}</label>
        <select v-model.number="pageSize" class="select">
          <option :value="10">10</option>
          <option :value="25">25</option>
          <option :value="50">50</option>
        </select>
        <span class="cnt">{{ t('memory.pageRange', { from: filtered.length ? ((page - 1) * pageSize) + 1 : 0, to: Math.min(page * pageSize, filtered.length), total: filtered.length }) }}</span>
        <button class="pg" :disabled="page <= 1" @click="page = 1">«</button>
        <button class="pg" :disabled="page <= 1" @click="page--">‹</button>
        <button class="pg on">{{ page }}</button>
        <button class="pg" :disabled="page >= pageCount" @click="page++">›</button>
        <button class="pg" :disabled="page >= pageCount" @click="page = pageCount">»</button>
      </div>
    </section>

    <section class="filterbar">
      <select v-model="project" class="fsel" @change="page = 1">
        <option value="all">{{ t('memory.filters.allProjects') }}</option>
        <option v-for="item in projects" :key="item" :value="item">{{ item }}</option>
      </select>
      <button class="fchip" :aria-pressed="filters.active" @click="toggleFilter('active')">{{ t('memory.filters.active') }}</button>
      <button class="fchip" :aria-pressed="filters.flagged" @click="toggleFilter('flagged')">{{ t('memory.filters.flagged') }}</button>
      <button class="fchip preset" :aria-pressed="filters.stale" @click="toggleFilter('stale')">{{ t('memory.filters.stale') }}</button>
      <button class="fchip preset" :aria-pressed="filters.low" @click="toggleFilter('low')">{{ t('memory.filters.low') }}</button>
      <button class="fchip preset" :aria-pressed="filters.chain" @click="toggleFilter('chain')">{{ t('memory.filters.chain') }}</button>
      <span class="fcount">{{ t('memory.filterSummary', { count: filtered.length, noise: noiseRatio }) }}</span>
    </section>

    <section v-if="pending || error || loadState.kind === 'empty' || loadState.kind === 'gated'" class="statebar" :data-state="loadState.kind">
      <span v-if="pending">{{ t('memory.state.pending') }}</span>
      <span v-else-if="error">{{ t('memory.state.error', { message: error }) }}</span>
      <span v-else-if="loadState.kind === 'empty'">{{ t('memory.state.empty') }}</span>
      <span v-else-if="loadState.kind === 'gated'">{{ t('memory.state.gated', { flag: loadState.evidence.flag || 'VNEXT_F' }) }}</span>
      <button v-if="error" class="tbtn" @click="refresh">{{ t('memory.state.retry') }}</button>
    </section>

    <div class="area-body" :class="{ 'detail-open': opened }">
      <section class="grid">
        <div class="grid-h">
          <span>{{ t('memory.table.record') }}</span>
          <span>{{ t('memory.table.value') }}</span>
        </div>

        <button v-for="memory in pageRows" :key="memory.id" class="mrow" :class="{ open: openId === memory.id }" @click="openId = openId === memory.id ? null : memory.id">
          <span class="echk" :class="{ on: selected[memory.id] }" @click.stop="toggleSelected(memory.id)">{{ selected[memory.id] ? '✓' : '' }}</span>
          <span class="estate" :data-s="rowStateClass(memory)" />
          <span class="ebody">
            <span class="epreview">{{ memory.content }}</span>
            <span class="emeta">
              <span>{{ rowStateLabel(memory) }}</span>
              <span>{{ memory.type }}</span>
              <span>{{ memory.project }}</span>
              <span>{{ memory.age }}</span>
              <span>{{ memory.tier }}</span>
            </span>
          </span>
          <span class="heat" :class="{ noisy: memory.noise }"><b>{{ memory.cite }}</b><span>/</span>{{ memory.inj }}</span>
          <span class="rowmenu" aria-hidden="true">…</span>
        </button>

        <div v-if="!pageRows.length" class="empty">
          <b>{{ t('memory.empty.title') }}</b>
          <span>{{ activeFilterCount ? t('memory.empty.filtered') : t('memory.empty.noRows') }}</span>
        </div>
      </section>

      <aside v-if="opened" class="detail">
        <div class="detail-head">
          <h2>{{ t('memory.detail.title') }}</h2>
          <button class="tbtn" @click="openId = null">×</button>
        </div>
        <div class="dident mono">{{ opened.id }}</div>
        <div class="dcontent">{{ opened.content }}</div>
        <div class="operator-note">
          <span class="mark">?</span>
          <div><b>{{ t('memory.detail.operatorQuestion') }}</b> {{ t('memory.detail.operatorText') }}</div>
        </div>

        <dl class="dfields">
          <dt>{{ t('memory.detail.state') }}</dt><dd>{{ rowStateLabel(opened) }}</dd>
          <dt>{{ t('memory.detail.tier') }}</dt><dd>{{ opened.tier }}</dd>
          <dt>{{ t('memory.detail.type') }}</dt><dd>{{ opened.type }}</dd>
          <dt>{{ t('memory.detail.project') }}</dt><dd>{{ opened.project }}</dd>
          <dt>{{ t('memory.detail.age') }}</dt><dd>{{ opened.age }}</dd>
          <dt>{{ t('memory.detail.confidence') }}</dt><dd>{{ formatConfidence(opened) }}</dd>
        </dl>

        <section class="dsection">
          <div class="dsh">{{ t('memory.detail.tags') }}</div>
          <div class="tags">
            <span v-for="tag in opened.tags" :key="tag" class="tag">{{ tag }}</span>
            <span v-if="!opened.tags.length" class="tag muted">—</span>
          </div>
        </section>

        <section class="dsection">
          <div class="dsh">{{ t('memory.detail.utility') }}</div>
          <div class="gauge-mini">
            <span class="gv">{{ opened.cite }}/{{ opened.inj }}</span>
            <div class="confbar"><i :style="{ width: formatConfidence(opened) }" /></div>
            <span class="plain-help">{{ t('memory.detail.utilityHelp', { confidence: formatConfidence(opened) }) }}</span>
          </div>
          <div v-if="opened.inj >= 10 && opened.cite === 0" class="callout danger">{{ t('memory.detail.noiseWarning') }}</div>
        </section>

        <section class="dsection">
          <div class="dsh">{{ t('memory.detail.audit') }}</div>
          <div class="mb-card">
            {{ t('memory.detail.auditBody') }}
            <div class="mid">{{ auditGap.evidence.endpoint }}</div>
            <div class="mid">{{ provenanceGap.evidence.endpoint }}</div>
          </div>
        </section>

        <div class="dactions">
          <button class="act" @click="storeCopy">{{ t('memory.detail.actions.storeCopy') }}</button>
          <button class="act danger" @click="deleteOpened">{{ t('memory.detail.actions.delete') }}</button>
          <button class="act" disabled>{{ t('memory.detail.actions.hideNoise') }} <span class="mbp">{{ t('overview.badges.mustBuild') }}</span></button>
          <button class="act" disabled>{{ t('memory.detail.actions.editText') }} <span class="mbp">{{ t('overview.badges.mustBuild') }}</span></button>
          <button class="act" disabled>{{ t('memory.detail.actions.replace') }} <span class="mbp">{{ t('overview.badges.mustBuild') }}</span></button>
          <button class="act" disabled>{{ t('memory.detail.actions.flag') }} <span class="mbp">{{ t('overview.badges.mustBuild') }}</span></button>
        </div>
      </aside>
    </div>

    <BulkBar :count="selectedCount" :verbs="[]" :note="t('memory.bulk.mustBuildNote')" @clear="clearSelected" />
  </div>
</template>

<style scoped>
.memory-page { display:flex; flex-direction:column; gap:14px; }
.page-head { padding-bottom:14px; border-bottom:1px solid var(--border); }
.page-head h1 { margin:0 0 4px; font-size:var(--text-xl); font-weight:800; letter-spacing:var(--tracking-display); }
.page-head p { margin:0; color:var(--muted); font-size:var(--text-sm); }
.legend { display:flex; align-items:center; justify-content:space-between; gap:16px; padding:10px 16px; border:1px solid var(--border); border-radius:var(--r-md); background:var(--surface); }
.legend-left, .legend-right { display:flex; align-items:center; gap:18px; flex-wrap:wrap; }
.lg { display:inline-flex; align-items:center; gap:8px; color:var(--fg-2); font-size:var(--text-sm); font-weight:700; }
.ld { width:9px; height:9px; border-radius:50%; }
.ld.live { background:var(--class-live); }
.ld.dormant { background:var(--class-dormant); }
.ld.stale { border:2px solid var(--class-stale); }
.ld.mustbuild { background:var(--class-mustbuild); }
.ld.warn { background:var(--state-warn); }
.ops { display:flex; align-items:center; justify-content:space-between; gap:12px; flex-wrap:wrap; }
.ops-left, .ops-right { display:flex; align-items:center; gap:8px; flex-wrap:wrap; }
.ops-right label, .cnt { color:var(--muted); font-size:var(--text-xs); }
.tbtn, .pg { display:inline-flex; align-items:center; justify-content:center; min-height:30px; padding:5px 10px; border:1px solid var(--border); border-radius:var(--r-sm); background:var(--surface); color:var(--fg-2); font-size:var(--text-xs); font-weight:800; cursor:pointer; }
.tbtn:hover:not(:disabled), .pg:hover:not(:disabled) { border-color:var(--accent); color:var(--fg); }
.tbtn:disabled, .pg:disabled { opacity:.45; cursor:not-allowed; }
.pg { min-width:34px; padding:5px 8px; }
.pg.on { background:var(--accent); border-color:var(--accent); color:var(--accent-on); }
.select, .fsel { min-height:32px; border:1px solid var(--border); border-radius:var(--r-sm); background:var(--surface); color:var(--fg); padding:0 10px; font-size:var(--text-sm); }
.filterbar { display:flex; align-items:center; gap:8px; flex-wrap:wrap; }
.fchip { border:0; border-radius:var(--radius-pill); background:var(--surface-warm); color:var(--fg-2); padding:5px 14px; font-size:var(--text-sm); font-weight:800; cursor:pointer; }
.fchip[aria-pressed="true"] { background:var(--accent); color:var(--accent-on); }
.fchip.preset { border:1px dashed var(--border); }
.fcount { margin-left:auto; color:var(--muted); font-size:var(--text-sm); }
.statebar { display:flex; align-items:center; justify-content:space-between; gap:12px; padding:10px 12px; border:1px solid var(--border); border-radius:var(--r-md); background:var(--surface); color:var(--fg-2); font-size:var(--text-sm); }
.statebar[data-state="pending"] { border-color:color-mix(in oklab,var(--accent),transparent 55%); }
.statebar[data-state="error"] { border-color:color-mix(in oklab,var(--state-warn),transparent 45%); color:var(--state-warn); }
.statebar[data-state="gated"] { border-color:color-mix(in oklab,var(--class-dormant),transparent 45%); }
.statebar[data-state="empty"] { color:var(--muted); }
.area-body { display:grid; grid-template-columns:minmax(0,1fr); gap:14px; }
.area-body.detail-open { grid-template-columns:minmax(0,1fr) minmax(340px,384px); align-items:start; }
.grid { border:1px solid var(--border); border-radius:var(--r-md); background:var(--surface); overflow:hidden; }
.grid-h { display:grid; grid-template-columns:1fr 152px; gap:12px; padding:10px 14px 10px 51px; border-bottom:1px solid var(--border); color:var(--muted); font-size:var(--text-xs); font-weight:900; text-transform:uppercase; letter-spacing:.08em; }
.grid-h span:last-child { text-align:right; }
.mrow { width:100%; display:grid; grid-template-columns:22px 8px minmax(0,1fr) 86px 28px; align-items:center; gap:10px; min-height:52px; padding:9px 12px; border:0; border-bottom:1px solid var(--border-soft); background:transparent; color:var(--fg); text-align:left; cursor:pointer; }
.mrow:hover, .mrow.open { background:var(--surface-warm); }
.mrow.open { box-shadow:inset 3px 0 0 var(--accent); }
.echk { width:22px; height:22px; border:1px solid var(--border); border-radius:5px; display:grid; place-items:center; color:var(--accent-on); font-size:12px; font-weight:900; }
.echk.on { background:var(--accent); border-color:var(--accent); }
.estate { width:8px; height:8px; border-radius:50%; }
.estate[data-s="live"] { background:var(--class-live); }
.estate[data-s="warn"] { background:var(--state-warn); }
.estate[data-s="stale"] { border:2px solid var(--class-stale); }
.ebody { min-width:0; display:flex; flex-direction:column; gap:3px; }
.epreview { overflow:hidden; text-overflow:ellipsis; white-space:nowrap; font-family:var(--font-mono); font-size:var(--text-sm); color:var(--fg); }
.emeta { display:flex; align-items:center; gap:11px; flex-wrap:wrap; color:var(--muted); font-size:var(--text-xs); }
.heat { justify-self:end; display:inline-flex; align-items:center; gap:6px; padding:4px 10px; border:1px solid var(--border); border-radius:var(--radius-pill); color:var(--muted); font-family:var(--font-mono); font-size:var(--text-xs); }
.heat b { color:var(--class-live); }
.heat.noisy { border-color:color-mix(in oklab,var(--state-warn),transparent 55%); }
.heat.noisy b { color:var(--state-warn); }
.rowmenu { color:var(--muted); text-align:center; font-size:18px; }
.empty { display:flex; flex-direction:column; align-items:center; justify-content:center; gap:5px; min-height:220px; color:var(--muted); }
.empty b { color:var(--fg-2); font-size:var(--text-lg); }
.detail { border:1px solid var(--border); border-radius:var(--r-md); background:var(--surface); padding:16px; position:sticky; top:0; }
.detail-head { display:flex; align-items:center; gap:12px; }
.detail-head h2 { margin:0; flex:1; font-size:var(--text-lg); }
.dident { margin:4px 0 12px; color:var(--muted); font-size:var(--text-xs); }
.dcontent { padding:12px; border:1px solid var(--border); border-radius:var(--r-sm); background:var(--surface-warm); font-family:var(--font-mono); font-size:var(--text-sm); line-height:1.5; }
.operator-note { display:flex; gap:10px; margin:12px 0; padding:11px 12px; border-radius:var(--r-sm); background:color-mix(in oklab,var(--accent),transparent 91%); border:1px solid color-mix(in oklab,var(--accent),transparent 65%); color:var(--fg-2); font-size:var(--text-xs); }
.mark { width:22px; height:22px; display:grid; place-items:center; flex:none; border-radius:50%; background:var(--accent); color:var(--accent-on); font-weight:900; }
.dfields { display:grid; grid-template-columns:120px minmax(0,1fr); gap:7px 12px; margin:14px 0; font-size:var(--text-sm); }
.dfields dt { color:var(--muted); }
.dfields dd { margin:0; color:var(--fg); font-family:var(--font-mono); }
.dsection { margin-top:14px; }
.dsh { margin-bottom:7px; color:var(--muted); font-size:var(--text-xs); font-weight:900; text-transform:uppercase; letter-spacing:.08em; }
.tags { display:flex; gap:6px; flex-wrap:wrap; }
.tag { display:inline-flex; border:1px solid var(--border); border-radius:var(--radius-pill); padding:3px 8px; color:var(--fg-2); font-family:var(--font-mono); font-size:var(--text-xs); }
.tag.muted { color:var(--muted); }
.gauge-mini { display:grid; grid-template-columns:58px 1fr; gap:8px 10px; align-items:center; }
.gv { font-family:var(--font-mono); font-weight:800; }
.confbar { height:8px; border-radius:var(--radius-pill); background:var(--border-soft); overflow:hidden; }
.confbar i { display:block; height:100%; background:var(--class-live); border-radius:var(--radius-pill); }
.plain-help { grid-column:1 / -1; color:var(--muted); font-size:var(--text-xs); }
.callout { margin-top:9px; padding:9px 11px; border-radius:var(--r-sm); font-size:var(--text-xs); }
.callout.danger { color:var(--state-warn); border:1px solid color-mix(in oklab,var(--state-warn),transparent 55%); background:color-mix(in oklab,var(--state-warn),transparent 90%); }
.mb-card { padding:12px; border-radius:var(--r-sm); border:1px solid color-mix(in oklab,var(--class-mustbuild),transparent 55%); background:color-mix(in oklab,var(--class-mustbuild),transparent 91%); color:var(--fg-2); font-size:var(--text-sm); }
.mb-card .mid { margin-top:4px; color:var(--muted); font-family:var(--font-mono); font-size:var(--text-xs); }
.dactions { display:flex; gap:8px; flex-wrap:wrap; margin-top:15px; }
.act { border:1px solid var(--border); border-radius:var(--r-sm); background:var(--surface); color:var(--fg-2); padding:7px 10px; font-size:var(--text-xs); font-weight:800; }
.act.danger { border-color:color-mix(in oklab,var(--state-warn),transparent 45%); color:var(--state-warn); }
.act:disabled { opacity:.55; cursor:not-allowed; }
.mbp { margin-left:5px; color:var(--class-mustbuild); }
@media (max-width:1120px) {
  .area-body.detail-open { grid-template-columns:1fr; }
  .detail { position:static; }
}
@media (max-width:760px) {
  .legend { align-items:flex-start; flex-direction:column; }
  .grid-h { display:none; }
  .mrow { grid-template-columns:22px 8px minmax(0,1fr); }
  .heat, .rowmenu { grid-column:3; justify-self:start; }
}
</style>
