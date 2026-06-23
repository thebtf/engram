<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { resolvePageSize, usePersistentPageSize } from '../composables/usePersistentPageSize'
import { useOperatorQueue, type OperatorCandidate } from '../composables/useOperatorQueue'

type CandidateAction = 'promote' | 'reject' | 'supersede'

const { t } = useI18n()
const {
  rows,
  projects,
  selectedProject,
  loadState,
  pending,
  error,
  refresh,
  promoteCandidate,
  rejectCandidate,
  supersedeCandidate,
} = useOperatorQueue()

const page = ref(1)
const { pageSize, pageSizeOptions } = usePersistentPageSize('engram.operatorConsole.queue.pageSize', 10)
const openId = ref<string | null>(null)
const selected = ref<Record<string, boolean>>({})
const confirm = ref<{ id: string; action: CandidateAction } | null>(null)
const busyActions = ref<Record<string, CandidateAction | undefined>>({})
const notice = ref<{ kind: 'success' | 'error'; text: string } | null>(null)

const effectivePageSize = computed(() => resolvePageSize(pageSize.value, rows.length))
const pageCount = computed(() => Math.max(1, Math.ceil(rows.length / effectivePageSize.value)))
const pageRange = computed(() => {
  if (!rows.length) return { from: 0, to: 0 }
  return {
    from: ((page.value - 1) * effectivePageSize.value) + 1,
    to: Math.min(page.value * effectivePageSize.value, rows.length),
  }
})
const pageRows = computed(() => rows.slice((page.value - 1) * effectivePageSize.value, page.value * effectivePageSize.value))
const selectedIds = computed(() => Object.entries(selected.value).filter(([, value]) => value).map(([id]) => id))
const selectedCount = computed(() => selectedIds.value.length)
const opened = computed(() => rows.find((candidate) => candidate.id === openId.value) || null)
const promotedTargetCount = computed(() => rows.filter((candidate) => candidate.target !== 'none').length)
const lowConfidenceCount = computed(() => rows.filter((candidate) => candidate.confidence !== null && candidate.confidence < 0.75).length)
const statebarKind = computed(() => error.value ? 'error' : pending.value ? 'pending' : loadState.value.kind)
const queueHonesty = computed(() => loadState.value.kind === 'gated' ? 'dormant' : 'live')

watch([pageSize, () => rows.length], () => {
  if (page.value > pageCount.value) page.value = pageCount.value
})

watch(pageSize, () => {
  page.value = 1
})

watch(selectedProject, () => {
  page.value = 1
  openId.value = null
  selected.value = {}
  confirm.value = null
  void refresh()
})

watch(openId, () => {
  confirm.value = null
})

function selectAll() {
  const next = { ...selected.value }
  rows.forEach((candidate) => { next[candidate.id] = true })
  selected.value = next
}

function clearSelected() {
  selected.value = {}
}

function toggleSelected(id: string) {
  selected.value = { ...selected.value, [id]: !selected.value[id] }
}

function confidenceLabel(candidate: OperatorCandidate) {
  return candidate.confidence === null ? '—' : `${Math.round(candidate.confidence * 100)}%`
}

function compactDate(value?: string) {
  if (!value) return '—'
  const parsed = Date.parse(value)
  if (Number.isNaN(parsed)) return '—'
  const seconds = Math.max(0, Math.floor((Date.now() - parsed) / 1000))
  if (seconds < 60) return `${seconds}s`
  if (seconds < 3600) return `${Math.floor(seconds / 60)}m`
  if (seconds < 86400) return `${Math.floor(seconds / 3600)}h`
  return `${Math.floor(seconds / 86400)}d`
}

function actionLabel(candidate: OperatorCandidate, action: CandidateAction) {
  if (busyActions.value[candidate.id] === action) {
    return t('queue.actions.running')
  }
  if (confirm.value?.id === candidate.id && confirm.value.action === action) {
    return t(`queue.actions.confirm.${action}`)
  }
  return t(`queue.actions.${action}`)
}

function isCandidateBusy(id: string) {
  return Boolean(busyActions.value[id])
}

function setCandidateBusy(id: string, action: CandidateAction) {
  busyActions.value = { ...busyActions.value, [id]: action }
}

function clearCandidateBusy(id: string) {
  const next = { ...busyActions.value }
  delete next[id]
  busyActions.value = next
}

function clearMatchingConfirm(id: string, action: CandidateAction) {
  if (confirm.value?.id === id && confirm.value.action === action) {
    confirm.value = null
  }
}

function mutationError(result: unknown) {
  if (result && typeof result === 'object' && 'error' in result) {
    return (result as { error?: { message?: string } }).error?.message || ''
  }
  return ''
}

async function runAction(candidate: OperatorCandidate, action: CandidateAction) {
  if (isCandidateBusy(candidate.id)) return
  if (confirm.value?.id !== candidate.id || confirm.value.action !== action) {
    confirm.value = { id: candidate.id, action }
    notice.value = null
    return
  }

  setCandidateBusy(candidate.id, action)
  try {
    const result = action === 'promote'
      ? await promoteCandidate(candidate.id)
      : action === 'reject'
        ? await rejectCandidate(candidate.id)
        : await supersedeCandidate(candidate.id)

    if (result.kind === 'success') {
      const nextSelected = { ...selected.value }
      delete nextSelected[candidate.id]
      selected.value = nextSelected
      if (openId.value === candidate.id) openId.value = null
      clearMatchingConfirm(candidate.id, action)
      notice.value = { kind: 'success', text: t(`queue.notice.${action}`, { id: candidate.id }) }
    } else {
      clearMatchingConfirm(candidate.id, action)
      notice.value = { kind: 'error', text: t('queue.notice.error', { message: mutationError(result) || t('queue.notice.unknownError') }) }
    }
  } finally {
    clearCandidateBusy(candidate.id)
  }
}
</script>

<template>
  <div class="queue-page">
    <header class="page-head">
      <div>
        <h1>{{ t('queue.title') }}</h1>
        <p>{{ t('queue.subtitle') }}</p>
      </div>
      <HonestyBadge :cls="queueHonesty" :evidence="loadState.kind === 'gated' ? 'VNEXT_F' : '/api/memory/candidates'" />
    </header>

    <section class="queue-brief">
      <div class="metric">
        <b>{{ rows.length }}</b>
        <span>{{ t('queue.metrics.pending') }}</span>
      </div>
      <div class="metric">
        <b>{{ promotedTargetCount }}</b>
        <span>{{ t('queue.metrics.promotable') }}</span>
      </div>
      <div class="metric">
        <b>{{ lowConfidenceCount }}</b>
        <span>{{ t('queue.metrics.lowConfidence') }}</span>
      </div>
      <div class="brief-copy">
        <strong>{{ t('queue.brief.title') }}</strong>
        <span>{{ t('queue.brief.body') }}</span>
      </div>
    </section>

    <section class="ops">
      <div class="ops-left">
        <select id="queue-project-filter" v-model="selectedProject" class="select" name="queue-project-filter" :aria-label="t('queue.filters.project')">
          <option v-for="project in projects" :key="project" :value="project">{{ project }}</option>
        </select>
        <button class="tbtn" @click="selectAll">{{ t('queue.actions.selectAll') }}</button>
        <button class="tbtn" @click="clearSelected">{{ t('queue.actions.clearSelection') }}</button>
        <button class="tbtn" @click="refresh">{{ t('queue.actions.refresh') }}</button>
        <span class="cnt">{{ t('queue.countInFilter', { shown: rows.length, selected: selectedCount }) }}</span>
      </div>
      <div class="ops-right">
        <label class="rows">
          <span>{{ t('queue.rowsLabel') }}</span>
          <select id="queue-page-size" v-model="pageSize" class="select" name="queue-page-size">
            <option v-for="size in pageSizeOptions" :key="size" :value="size">
              {{ size === 'all' ? t('queue.allRows') : size }}
            </option>
          </select>
        </label>
        <span class="cnt">{{ t('queue.pageRange', { from: pageRange.from, to: pageRange.to, total: rows.length }) }}</span>
        <button class="pg" :disabled="page <= 1" @click="page = 1">«</button>
        <button class="pg" :disabled="page <= 1" @click="page--">‹</button>
        <button class="pg on">{{ page }}</button>
        <button class="pg" :disabled="page >= pageCount" @click="page++">›</button>
        <button class="pg" :disabled="page >= pageCount" @click="page = pageCount">»</button>
      </div>
    </section>

    <section v-if="notice" class="statebar" :data-state="notice.kind" data-testid="queue-notice">
      <span>{{ notice.text }}</span>
      <button class="tbtn" @click="notice = null">{{ t('common.hide') }}</button>
    </section>

    <section v-if="pending || error || loadState.kind === 'empty' || loadState.kind === 'gated'" class="statebar" :data-state="statebarKind">
      <span v-if="pending">{{ t('queue.state.pending') }}</span>
      <span v-else-if="error">{{ t('queue.state.error', { message: error }) }}</span>
      <span v-else-if="loadState.kind === 'empty'">{{ t('queue.state.empty') }}</span>
      <span v-else-if="loadState.kind === 'gated'">{{ t('queue.state.gated', { flag: loadState.evidence.flag || 'VNEXT_F' }) }}</span>
      <button v-if="error" class="tbtn" @click="refresh">{{ t('queue.state.retry') }}</button>
    </section>

    <div class="area-body" :class="{ 'detail-open': opened }">
      <section class="queue-list">
        <div class="grid-h">
          <span>{{ t('queue.table.candidate') }}</span>
          <span>{{ t('queue.table.confidence') }}</span>
          <span>{{ t('queue.table.actions') }}</span>
        </div>

        <article
          v-for="candidate in pageRows"
          :key="candidate.id"
          class="qrow"
          :class="{ open: openId === candidate.id }"
          :data-testid="`queue-row-${candidate.id}`"
        >
          <button class="qbody" @click="openId = openId === candidate.id ? null : candidate.id">
            <span class="echk" :class="{ on: selected[candidate.id] }" @click.stop="toggleSelected(candidate.id)">{{ selected[candidate.id] ? '✓' : '' }}</span>
            <span class="estate" />
            <span class="qcopy">
              <span class="qtitle">{{ candidate.content }}</span>
              <span class="qmeta">
                <span>{{ candidate.target }}</span>
                <span>{{ candidate.tier }}</span>
                <span>{{ candidate.affectedProjects.join(', ') || selectedProject }}</span>
                <ClientOnly fallback="—">
                  <span>{{ compactDate(candidate.createdAt) }}</span>
                </ClientOnly>
              </span>
            </span>
          </button>
          <div class="qconf">
            <b>{{ confidenceLabel(candidate) }}</b>
            <span>{{ t('queue.meta.recurrence', { count: candidate.recurrenceCount }) }}</span>
          </div>
          <div class="qactions">
            <button class="act primary" :data-testid="`queue-action-promote-${candidate.id}`" :disabled="isCandidateBusy(candidate.id)" @click="runAction(candidate, 'promote')">
              {{ actionLabel(candidate, 'promote') }}
            </button>
            <button class="act" :data-testid="`queue-action-reject-${candidate.id}`" :disabled="isCandidateBusy(candidate.id)" @click="runAction(candidate, 'reject')">
              {{ actionLabel(candidate, 'reject') }}
            </button>
            <button class="act muted" :data-testid="`queue-action-supersede-${candidate.id}`" :disabled="isCandidateBusy(candidate.id)" @click="runAction(candidate, 'supersede')">
              {{ actionLabel(candidate, 'supersede') }}
            </button>
          </div>
        </article>

        <div v-if="!pageRows.length" class="empty" data-testid="queue-empty">
          <b>{{ t('queue.empty.title') }}</b>
          <span>{{ t('queue.empty.body') }}</span>
        </div>
      </section>

      <aside v-if="opened" class="detail" data-testid="queue-detail">
        <div class="detail-head">
          <h2>{{ t('queue.detail.title', { id: opened.id }) }}</h2>
          <button class="tbtn" @click="openId = null">×</button>
        </div>
        <div class="dcontent">{{ opened.content }}</div>
        <dl class="dfields">
          <dt>{{ t('queue.detail.status') }}</dt><dd>{{ opened.status }}</dd>
          <dt>{{ t('queue.detail.target') }}</dt><dd>{{ opened.target }}</dd>
          <dt>{{ t('queue.detail.tier') }}</dt><dd>{{ opened.tier }}</dd>
          <dt>{{ t('queue.detail.type') }}</dt><dd>{{ opened.epistemicType }}</dd>
          <dt>{{ t('queue.detail.project') }}</dt><dd>{{ opened.affectedProjects.join(', ') || selectedProject }}</dd>
          <dt>{{ t('queue.detail.source') }}</dt><dd>{{ opened.sourceSessionId }}</dd>
          <dt>{{ t('queue.detail.fingerprint') }}</dt><dd>{{ opened.fingerprint || '—' }}</dd>
          <dt>{{ t('queue.detail.reviewAfter') }}</dt><dd>{{ opened.reviewAfter || '—' }}</dd>
        </dl>
        <section class="dsection">
          <div class="dsh">{{ t('queue.detail.evidence') }}</div>
          <div class="tags">
            <span v-for="item in opened.evidenceHandles" :key="item" class="tag">{{ item }}</span>
            <span v-if="!opened.evidenceHandles.length" class="tag muted">—</span>
          </div>
        </section>
        <section class="operator-note">
          <b>{{ t('queue.detail.operatorQuestion') }}</b>
          <span>{{ t('queue.detail.operatorText') }}</span>
        </section>
      </aside>
    </div>
  </div>
</template>

<style scoped>
.queue-page { display:flex; flex-direction:column; gap:14px; }
.page-head { display:flex; align-items:flex-start; justify-content:space-between; gap:18px; padding-bottom:14px; border-bottom:1px solid var(--border); }
.page-head h1 { margin:0 0 4px; font-size:var(--text-xl); font-weight:800; letter-spacing:var(--tracking-display); }
.page-head p { margin:0; color:var(--muted); font-size:var(--text-sm); }
.queue-brief { display:grid; grid-template-columns:repeat(3, minmax(110px, 160px)) minmax(260px, 1fr); gap:12px; }
.metric, .brief-copy { border:1px solid var(--border); border-radius:var(--r-md); background:var(--surface); padding:14px; }
.metric { display:flex; flex-direction:column; gap:3px; }
.metric b { font-family:var(--font-mono); font-size:var(--text-xl); line-height:1; color:var(--fg); }
.metric span, .brief-copy span { color:var(--muted); font-size:var(--text-xs); }
.brief-copy { display:flex; flex-direction:column; justify-content:center; gap:5px; }
.brief-copy strong { color:var(--fg-2); font-size:var(--text-sm); }
.ops { display:flex; align-items:center; justify-content:space-between; gap:12px; flex-wrap:wrap; }
.ops-left, .ops-right { display:flex; align-items:center; gap:8px; flex-wrap:wrap; }
.ops-right label, .cnt { color:var(--muted); font-size:var(--text-xs); }
.select { min-height:32px; border:1px solid var(--border); border-radius:var(--r-sm); background:var(--surface); color:var(--fg); padding:0 10px; font-size:var(--text-sm); }
.tbtn, .pg { display:inline-flex; align-items:center; justify-content:center; min-height:30px; padding:5px 10px; border:1px solid var(--border); border-radius:var(--r-sm); background:var(--surface); color:var(--fg-2); font-size:var(--text-xs); font-weight:800; cursor:pointer; }
.tbtn:hover:not(:disabled), .pg:hover:not(:disabled) { border-color:var(--accent); color:var(--fg); }
.tbtn:disabled, .pg:disabled { opacity:.45; cursor:not-allowed; }
.pg { min-width:34px; padding:5px 8px; }
.pg.on { background:var(--accent); border-color:var(--accent); color:var(--accent-on); }
.statebar { display:flex; align-items:center; justify-content:space-between; gap:12px; padding:10px 12px; border:1px solid var(--border); border-radius:var(--r-md); background:var(--surface); color:var(--fg-2); font-size:var(--text-sm); }
.statebar[data-state="pending"] { border-color:color-mix(in oklab,var(--accent),transparent 55%); }
.statebar[data-state="error"] { border-color:color-mix(in oklab,var(--state-warn),transparent 45%); color:var(--state-warn); }
.statebar[data-state="success"], .statebar[data-state="live"] { border-color:color-mix(in oklab,var(--class-live),transparent 55%); }
.statebar[data-state="gated"] { border-color:color-mix(in oklab,var(--class-dormant),transparent 45%); }
.area-body { display:grid; grid-template-columns:minmax(0,1fr); gap:14px; }
.area-body.detail-open { grid-template-columns:minmax(0,1fr) minmax(340px,400px); align-items:start; }
.queue-list { border:1px solid var(--border); border-radius:var(--r-md); background:var(--surface); overflow:hidden; }
.grid-h { display:grid; grid-template-columns:minmax(0,1fr) 132px 340px; gap:12px; padding:10px 14px 10px 50px; border-bottom:1px solid var(--border); color:var(--muted); font-size:var(--text-xs); font-weight:900; text-transform:uppercase; letter-spacing:.08em; }
.qrow { display:grid; grid-template-columns:minmax(0,1fr) 132px 340px; align-items:center; gap:12px; min-height:62px; padding:9px 12px; border-bottom:1px solid var(--border-soft); }
.qrow:hover, .qrow.open { background:var(--surface-warm); }
.qrow.open { box-shadow:inset 3px 0 0 var(--accent); }
.qbody { min-width:0; display:grid; grid-template-columns:22px 8px minmax(0,1fr); align-items:center; gap:10px; border:0; background:transparent; color:var(--fg); text-align:left; cursor:pointer; }
.echk { width:22px; height:22px; border:1px solid var(--border); border-radius:5px; display:grid; place-items:center; color:var(--accent-on); font-size:12px; font-weight:900; }
.echk.on { background:var(--accent); border-color:var(--accent); }
.estate { width:8px; height:8px; border-radius:50%; background:var(--class-dormant); }
.qcopy { min-width:0; display:flex; flex-direction:column; gap:4px; }
.qtitle { overflow:hidden; text-overflow:ellipsis; white-space:nowrap; font-family:var(--font-mono); font-size:var(--text-sm); color:var(--fg); }
.qmeta { display:flex; align-items:center; gap:11px; flex-wrap:wrap; color:var(--muted); font-size:var(--text-xs); }
.qconf { display:flex; flex-direction:column; align-items:flex-start; gap:3px; color:var(--muted); font-size:var(--text-xs); }
.qconf b { color:var(--fg); font-family:var(--font-mono); font-size:var(--text-sm); }
.qactions { display:flex; align-items:center; justify-content:flex-end; gap:6px; flex-wrap:wrap; }
.act { border:1px solid var(--border); border-radius:var(--r-sm); background:var(--surface); color:var(--fg-2); padding:6px 9px; font-size:var(--text-xs); font-weight:800; cursor:pointer; }
.act.primary { border-color:color-mix(in oklab,var(--class-live),transparent 45%); color:var(--class-live); }
.act.muted { color:var(--muted); }
.act:hover:not(:disabled) { border-color:var(--accent); color:var(--fg); }
.act:disabled { opacity:.5; cursor:not-allowed; }
.empty { display:flex; flex-direction:column; align-items:center; justify-content:center; gap:5px; min-height:220px; color:var(--muted); }
.empty b { color:var(--fg-2); font-size:var(--text-lg); }
.detail { border:1px solid var(--border); border-radius:var(--r-md); background:var(--surface); padding:16px; position:sticky; top:0; }
.detail-head { display:flex; align-items:center; gap:12px; margin-bottom:12px; }
.detail-head h2 { margin:0; flex:1; font-size:var(--text-lg); }
.dcontent { padding:12px; border:1px solid var(--border); border-radius:var(--r-sm); background:var(--surface-warm); font-family:var(--font-mono); font-size:var(--text-sm); line-height:1.5; }
.dfields { display:grid; grid-template-columns:132px minmax(0,1fr); gap:7px 12px; margin:14px 0; font-size:var(--text-sm); }
.dfields dt { color:var(--muted); }
.dfields dd { margin:0; min-width:0; overflow:hidden; text-overflow:ellipsis; color:var(--fg); font-family:var(--font-mono); }
.dsection { margin-top:14px; }
.dsh { margin-bottom:7px; color:var(--muted); font-size:var(--text-xs); font-weight:900; text-transform:uppercase; letter-spacing:.08em; }
.tags { display:flex; gap:6px; flex-wrap:wrap; }
.tag { display:inline-flex; border:1px solid var(--border); border-radius:var(--radius-pill); padding:3px 8px; color:var(--fg-2); font-family:var(--font-mono); font-size:var(--text-xs); }
.tag.muted { color:var(--muted); }
.operator-note { display:flex; flex-direction:column; gap:5px; margin-top:14px; padding:11px 12px; border-radius:var(--r-sm); background:color-mix(in oklab,var(--accent),transparent 91%); border:1px solid color-mix(in oklab,var(--accent),transparent 65%); color:var(--fg-2); font-size:var(--text-xs); }
@media (max-width:1180px) {
  .queue-brief { grid-template-columns:repeat(3, minmax(110px, 1fr)); }
  .brief-copy { grid-column:1 / -1; }
  .area-body.detail-open { grid-template-columns:1fr; }
  .detail { position:static; }
  .grid-h, .qrow { grid-template-columns:minmax(0,1fr) 120px; }
  .grid-h span:last-child, .qactions { grid-column:1 / -1; justify-content:flex-start; }
}
@media (max-width:760px) {
  .page-head { flex-direction:column; }
  .queue-brief { grid-template-columns:1fr; }
  .grid-h { display:none; }
  .qrow { grid-template-columns:1fr; align-items:start; }
  .qconf, .qactions { padding-left:40px; }
}
</style>
