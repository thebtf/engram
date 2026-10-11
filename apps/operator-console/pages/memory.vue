<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, ref, watch } from 'vue'
import { useOperatorProjectContext, projectName } from '../composables/useOperatorProjectContext'
import { PRINCIPAL_CURRENT_PROJECT, useOperatorMemoryLab, useOperatorPrincipalMemorySurface, type MemoryAuditResponse } from '../composables/useOperatorMemoryLab'
import { operatorDiagnosisKey, operatorUnauthorizedRevision, type OperatorLoadState, type OperatorSourceError, toOperatorSourceError } from '../composables/useOperatorApi'
import type { Memory } from '../composables/useMockData'
import type { MutationResult } from '../composables/useApi'

const { t, locale } = useI18n()
const route = useRoute()
const router = useRouter()
const context = useOperatorProjectContext()
const { project } = context
const { rows, loadState, pending, total, hasMore, refresh, loadMemoryByID, deleteMemory, suppressMemory, suppressMemories, unsuppressMemory, archiveMemory, auditMemory } = useOperatorMemoryLab(project, false)
const query = computed(() => typeof route.query.q === 'string' ? route.query.q : '')
const filter = computed(() => { const value = route.query.filter; return value === undefined ? [] : Array.isArray(value) ? value : [value] })
const hasFilter = computed(() => filter.value.some((value) => value !== 'all'))
const filterChoices = [
  { value: 'active', label: 'memory.rowState.active' },
  { value: 'flagged', label: 'memory.rowState.flagged' },
  { value: 'stale', label: 'knowledge.notCurrent' },
  { value: 'low', label: 'memory.filters.low' },
  { value: 'chain', label: 'memory.filters.chain' },
] as const
const page = computed(() => Math.max(1, Number.parseInt(String(route.query.page || '1'), 10) || 1))
const pageSize = computed(() => [10, 20, 50].includes(Number(route.query.size)) ? Number(route.query.size) : 20)
const openId = computed(() => typeof route.query.memory === 'string' ? route.query.memory : '')
const detailProject = computed(() => project.value === 'all' && typeof route.query.memoryProject === 'string' ? route.query.memoryProject : project.value)
const draft = ref(typeof route.query.draftQ === 'string' ? route.query.draftQ : query.value)
const opened = ref<Memory | null>(null)
const detailPending = ref(false)
const detailError = ref<OperatorSourceError | null>(null)
const auditState = ref<OperatorLoadState<MemoryAuditResponse> | null>(null)
const selected = ref<string[]>([])
const confirmation = ref('')
const busy = ref(false)
const mutationResult = ref<MutationResult | null>(null)
const detailHeading = ref<HTMLElement | null>(null)
let detailGeneration = 0

const principal = useOperatorPrincipalMemorySurface(project)
const principalScope = principal.scope
const principalState = principal.loadState
if (typeof route.query.principal === 'string') principalScope.value.principal = route.query.principal
if (route.query.principalKind === 'human' || route.query.principalKind === 'agent' || route.query.principalKind === 'service') principalScope.value.principalKind = route.query.principalKind
if (typeof route.query.principalDomain === 'string') principalScope.value.domain = route.query.principalDomain
if (typeof route.query.principalProject === 'string') principalScope.value.project = route.query.principalProject
if (route.query.principalVisibility === 'all' || route.query.principalVisibility === 'shared' || route.query.principalVisibility === 'private') principalScope.value.visibility = route.query.principalVisibility
principalScope.value.includePrivate = route.query.principalPrivate === '1'
const principalAttribution = principal.attributionVisible
const principalItems = computed(() => principalState.value.data?.items || [])

const filtered = computed(() => rows)
const pageCount = computed(() => Math.max(1, Math.ceil((total.value || 0) / pageSize.value)))
const pageRows = filtered

function updateQuery(values: Record<string, string | string[] | undefined>, replace = false) {
  const location = { path: '/memory', query: { ...route.query, project: project.value || undefined, ...values } }
  return replace ? router.replace(location) : router.push(location)
}
function toggleFilter(value: typeof filterChoices[number]['value']) {
  const current = [...new Set(filter.value.filter((entry) => entry !== 'all').map((entry) => entry ?? ''))]
  const next = current.includes(value) ? current.filter((entry) => entry !== value) : [...current, value]
  return updateQuery({ filter: next.length ? next : undefined, page: '1', memory: undefined, memoryProject: undefined })
}


watch([project, query, filter, page, pageSize, () => route.query.allRead] as const, (next, previous) => {
  if (previous?.length && next.every((value, index) => {
    const before = previous[index]
    return Array.isArray(value) && Array.isArray(before) ? value.length === before.length && value.every((entry, position) => entry === before[position]) : value === before
  })) return
  selected.value = []
  confirmation.value = ''
  mutationResult.value = null
  void refresh()
}, { immediate: true, flush: 'sync' })
watch(query, (value) => { draft.value = value })

async function readDetail() {
  const generation = ++detailGeneration
  const id = openId.value
  const scope = project.value
  const targetProject = detailProject.value
  const authRevision = operatorUnauthorizedRevision.value
  opened.value = null
  auditState.value = null
  detailError.value = null
  confirmation.value = ''
  if (!id || !scope || !targetProject || targetProject === 'all') { detailPending.value = false; return }
  await nextTick()
  if (generation !== detailGeneration || authRevision !== operatorUnauthorizedRevision.value) return
  detailHeading.value?.focus()
  detailHeading.value?.scrollIntoView({ block: 'start' })
  detailPending.value = true
  try {
    const memory = await loadMemoryByID(id, targetProject)
    if (generation !== detailGeneration || scope !== project.value || targetProject !== detailProject.value || id !== openId.value || authRevision !== operatorUnauthorizedRevision.value) return
    if (memory.project !== targetProject) throw new Error(t('knowledge.scopeMismatch'))
    opened.value = memory
    await nextTick()
    if (generation !== detailGeneration || scope !== project.value || targetProject !== detailProject.value || id !== openId.value || authRevision !== operatorUnauthorizedRevision.value) return
    detailHeading.value?.focus()
    const audit = await auditMemory(id, targetProject, 200)
    if (generation === detailGeneration && scope === project.value && targetProject === detailProject.value && id === openId.value && authRevision === operatorUnauthorizedRevision.value) auditState.value = audit
  } catch (cause) {
    if (generation === detailGeneration) detailError.value = toOperatorSourceError(cause, { source: 'memory-read', path: `/api/memories/${id}`, method: 'GET' })
  } finally {
    if (generation === detailGeneration) detailPending.value = false
  }
}
watch([project, openId, detailProject], readDetail, { immediate: true, flush: 'sync' })
watch(operatorUnauthorizedRevision, () => {
  detailGeneration += 1
  opened.value = null
  auditState.value = null
  detailError.value = null
  detailPending.value = false
}, { flush: 'sync' })
watch(openId, async (value, previous) => {
  if (value || !previous) return
  await nextTick()
  document.querySelector<HTMLElement>(`[data-memory-id="${CSS.escape(previous)}"]`)?.focus()
})
onBeforeUnmount(() => {
  detailGeneration += 1
  opened.value = null
  auditState.value = null
  if (route.path === '/memory') context.remember('/memory', { ...route.query, draftQ: draft.value, principal: principalScope.value.principal, principalKind: principalScope.value.principalKind, principalProject: principalScope.value.project, principalDomain: principalScope.value.domain, principalVisibility: principalScope.value.visibility, principalPrivate: principalScope.value.includePrivate ? '1' : '0' })
})

async function closeDetail() {
  const id = openId.value
  if (route.query.from === 'search' && typeof route.query.searchQuery === 'string') {
    await router.push({ path: '/search', query: { ...context.to('/search').query, project: project.value, q: route.query.searchQuery, focusMemory: id } })
    return
  }
  await updateQuery({ memory: undefined, memoryProject: undefined, from: undefined, searchQuery: undefined })
  await nextTick()
  document.querySelector<HTMLElement>(`[data-memory-id="${CSS.escape(id)}"]`)?.focus()
}

async function act(action: 'suppress' | 'delete' | 'bulk' | 'restore' | 'archive') {
  if (busy.value) return
  const memory = opened.value
  if (action !== 'bulk' && !memory) return
  if (action === 'bulk' && !selected.value.length) return
  const target = `${project.value}:${action}:${action === 'bulk' ? selected.value.join(',') : memory?.id}`
  if (confirmation.value !== target) { confirmation.value = target; return }
  const scope = project.value
  const id = openId.value
  busy.value = true
  try {
    const result = action === 'bulk' ? await suppressMemories([...selected.value]) : action === 'delete' ? await deleteMemory(memory!.id) : action === 'restore' ? await unsuppressMemory(memory!.id) : action === 'archive' ? await archiveMemory(memory!.id) : await suppressMemory(memory!.id)
    if (scope !== project.value || id !== openId.value) return
    mutationResult.value = result
    confirmation.value = ''
    if (result.kind === 'committed_verified') {
      selected.value = []
      await closeDetail()
      await refresh()
    }
    if (result.kind === 'partial') {
      const committed = new Set(result.items.filter((item) => item.outcome === 'committed').map((item) => String(item.targetId)))
      selected.value = selected.value.filter((id) => !committed.has(id))
    }
  } finally { busy.value = false }
}

async function refreshView() {
  await refresh()
  if (openId.value) await readDetail()
}

function time(value?: string) {
  return value && !Number.isNaN(Date.parse(value)) ? new Intl.DateTimeFormat(locale.value, { dateStyle: 'medium', timeStyle: 'short' }).format(new Date(value)) : t('knowledge.unknown')
}
</script>

<template>
  <div class="knowledge-page" @keydown.esc="openId && closeDetail()">
    <header class="page-head"><div><h1>{{ t('nav.items.memory') }}</h1><p>{{ t('knowledge.memoryLead') }}</p></div><button class="btn" :disabled="pending" @click="refreshView">{{ t('knowledge.refresh') }}</button></header>
    <div v-if="!project" class="empty"><h2>{{ t('knowledge.chooseProject') }}</h2><p>{{ t('knowledge.chooseProjectHelp') }}</p><RouterLink :to="context.to('/projects')">{{ t('nav.items.projects') }}</RouterLink></div>
    <template v-else>
      <form class="toolbar" @submit.prevent="updateQuery({ q: draft.trim() || undefined, page: '1', memory: undefined, memoryProject: undefined, allRead: project === 'all' ? '1' : undefined })">
        <label class="search-field"><span>{{ t('knowledge.searchMemory') }}</span><input v-model="draft" type="search" /></label>
        <button class="btn primary" type="submit">{{ t('knowledge.find') }}</button>
        <fieldset class="memory-filters"><legend>{{ t('knowledge.pageFilter') }}</legend><div class="filter-chips"><button class="btn" type="button" :aria-pressed="!hasFilter" data-memory-filter="all" @click="updateQuery({ filter: undefined, page: '1', memory: undefined, memoryProject: undefined })">{{ t('knowledge.anyState') }}</button><button v-for="choice in filterChoices" :key="choice.value" class="btn" type="button" :aria-pressed="filter.includes(choice.value)" :data-memory-filter="choice.value" @click="toggleFilter(choice.value)">{{ t(choice.label) }}</button></div></fieldset>
        <RouterLink :to="{ ...context.to('/search'), query: { ...context.to('/search').query, q: query || undefined } }">{{ t('knowledge.contextSearch') }}</RouterLink>
      </form>
      <div v-if="pending" class="state" role="status">{{ t('knowledge.loading') }}</div>
      <div v-else-if="loadState.kind === 'error'" class="state error" role="status"><span>{{ t(operatorDiagnosisKey(loadState.error)) }}</span><button class="btn" @click="refresh">{{ t('knowledge.retry') }}</button><details><summary>{{ t('knowledge.technical') }}</summary>{{ loadState.error.message }}</details></div>
      <div v-if="project === 'all'" class="state">{{ t('knowledge.allOrder') }}</div>
      <p v-if="loadState.kind === 'gated'" class="state" role="status">{{ t('knowledge.allReadHelp') }}</p>
      <div class="knowledge-workspace" :class="{ 'detail-open': openId }">
        <section class="list-panel" :aria-label="t('knowledge.memoryResults')">
          <div class="list-head"><span role="status">{{ total === null ? t('knowledge.unknown') : t('knowledge.matches', { count: total }) }}</span><label><span>{{ t('knowledge.perPage') }}</span><select :value="pageSize" @change="updateQuery({ size: ($event.target as HTMLSelectElement).value, page: '1', memory: undefined })"><option :value="10">10</option><option :value="20">20</option><option :value="50">50</option></select></label></div>
          <div v-for="memory in pageRows" :key="memory.id" class="memory-row" :class="{ selected: openId === memory.id }">
            <input v-model="selected" :value="memory.id" type="checkbox" :aria-label="t('knowledge.selectRecord', { id: memory.id })" />
            <button :data-memory-id="memory.id" :data-testid="`memory-row-${memory.id}`" :aria-pressed="openId === memory.id" @click="updateQuery({ memory: memory.id, memoryProject: project === 'all' ? memory.project : undefined })"><strong>{{ memory.content }}</strong><span>{{ t(`memory.rowState.${memory.status}`) }} · {{ memory.type }} · {{ memory.age }}<template v-if="project === 'all'"> · {{ projectName(memory.project) }}</template></span><span v-if="memory.tags.length" class="tags">{{ memory.tags.join(' · ') }}</span></button>
          </div>
          <div v-if="!pending && loadState.kind === 'empty'" class="empty"><h2>{{ t(total && page > 1 ? 'knowledge.pageEmpty' : query || hasFilter ? 'knowledge.noMatches' : 'knowledge.noMemory') }}</h2><p>{{ t(query || hasFilter ? 'knowledge.noMatchesHelp' : 'knowledge.noMemoryHelp') }}</p><button v-if="total && page > 1" class="btn" @click="updateQuery({ page: '1', memory: undefined })">{{ t('knowledge.firstPage') }}</button><button v-else-if="query || hasFilter" class="btn" @click="draft = ''; updateQuery({ q: undefined, filter: undefined, page: '1', memory: undefined })">{{ t('knowledge.clearFilters') }}</button><RouterLink v-else :to="context.to('/documents')">{{ t('knowledge.openDocuments') }}</RouterLink></div>
          <nav class="pagination" :aria-label="t('knowledge.pagination')"><button class="btn" :disabled="pending || page <= 1" @click="updateQuery({ page: String(page - 1), memory: undefined })">{{ t('knowledge.previous') }}</button><span>{{ total === null ? t('knowledge.unknown') : t('knowledge.pageOf', { page, total: pageCount }) }}</span><button class="btn" :disabled="pending || !hasMore" @click="updateQuery({ page: String(page + 1), memory: undefined })">{{ t('knowledge.next') }}</button></nav>
          <div v-if="selected.length" class="bulk"><span>{{ t('common.selectedCount', { count: selected.length }) }}</span><button class="btn" :disabled="busy" @click="act('bulk')">{{ t(confirmation.includes(':bulk:') ? 'memory.bulk.confirmHideNoise' : 'memory.bulk.hideNoise') }}</button><button class="btn" @click="selected = []; confirmation = ''">{{ t('common.clearSelection') }}</button></div>
        </section>
        <article v-if="openId" class="reader" aria-labelledby="memory-reader-heading">
          <button class="btn back" @click="closeDetail">{{ t('knowledge.backResults') }}</button>
          <h2 id="memory-reader-heading" ref="detailHeading" tabindex="-1">{{ t('knowledge.memoryContent') }}</h2>
          <div v-if="detailError" class="state error" role="status"><p>{{ t(operatorDiagnosisKey(detailError)) }}</p><button class="btn" @click="readDetail">{{ t('knowledge.retry') }}</button><details><summary>{{ t('knowledge.technical') }}</summary>{{ detailError.message }}</details></div>
          <p v-else-if="!detailProject || detailProject === 'all'" role="status">{{ t('knowledge.chooseRecordProject') }}</p>
          <p v-else-if="!opened" role="status">{{ t('knowledge.loading') }}</p>
          <template v-else>
            <p class="currentness">{{ t(`memory.rowState.${opened.status}`) }}<template v-if="opened.supersededBy"> · {{ t('knowledge.replacedBy') }} <button class="text-button" @click="updateQuery({ memory: opened.supersededBy })">#{{ opened.supersededBy }}</button></template></p>
            <div class="content-body" data-testid="memory-content">{{ opened.content }}</div>
            <section class="provenance"><h3>{{ t('knowledge.origin') }}</h3><dl><dt>{{ t('knowledge.project') }}</dt><dd>{{ projectName(detailProject) }}</dd><dt>{{ t('knowledge.created') }}</dt><dd>{{ time(opened.createdAt) }}</dd><dt>{{ t('knowledge.updated') }}</dt><dd>{{ time(opened.updatedAt) }}</dd><dt>{{ t('knowledge.lastConfirmed') }}</dt><dd>{{ time(opened.lastConfirmed) }}</dd><dt>{{ t('knowledge.reviewAfter') }}</dt><dd>{{ time(opened.reviewAfter) }}</dd><dt>{{ t('knowledge.author') }}</dt><dd>{{ opened.sourceAgent || t('knowledge.notRecorded') }}</dd></dl><p v-if="!opened.sourceSessions.length">{{ t('knowledge.noOriginSession') }}</p><RouterLink v-for="session in opened.sourceSessions" :key="session" :to="{ path: '/projects', query: { ...context.to('/projects').query, project: detailProject, session } }">{{ t('knowledge.originSession') }} · {{ session.slice(0, 8) }}</RouterLink><RouterLink :to="context.to('/documents')">{{ t('knowledge.relatedDocuments') }}</RouterLink></section>
            <section class="history" data-testid="memory-audit-panel"><h3>{{ t('knowledge.history') }}</h3><p v-if="!auditState" role="status">{{ t('knowledge.loading') }}</p><div v-else-if="auditState.kind === 'error'" class="state error"><p>{{ t(operatorDiagnosisKey(auditState.error)) }}</p><button class="btn" @click="readDetail">{{ t('knowledge.retry') }}</button></div><p v-else-if="!auditState.data?.entries.length">{{ t('knowledge.noMemoryHistory') }}</p><ol v-else><li v-for="entry in auditState.data.entries" :key="entry.id"><strong>{{ t(`knowledge.audit.${entry.action}`, entry.action) }}</strong><time>{{ time(entry.created_at) }}</time><span>{{ entry.actor || t('knowledge.notRecorded') }}</span><p v-if="entry.reason">{{ entry.reason }}</p></li></ol><p>{{ t('knowledge.auditLimit') }}</p></section>
            <details class="technical"><summary>{{ t('knowledge.technical') }}</summary><dl><dt>ID</dt><dd>{{ opened.id }}</dd><dt>{{ t('knowledge.projectIdentity') }}</dt><dd>{{ opened.project }}</dd><dt>{{ t('memory.detail.tier') }}</dt><dd>{{ opened.tier }}</dd><dt>{{ t('memory.detail.confidence') }}</dt><dd>{{ opened.confidenceKnown ? `${Math.round(opened.conf * 100)}%` : t('knowledge.unknown') }}</dd><dt>{{ t('memory.detail.utility') }}</dt><dd>{{ opened.utilityKnown ? `${opened.cite} / ${opened.inj}` : t('knowledge.unknown') }}</dd><dt>{{ t('knowledge.version') }}</dt><dd>{{ opened.version || t('knowledge.unknown') }}</dd></dl></details>
            <details class="actions"><summary>{{ t('knowledge.manageRecord') }}</summary><p>{{ t('knowledge.confirmAction') }}</p><div class="action-row"><button v-if="opened.noise || opened.status === 'flagged'" class="btn" :disabled="busy" @click="act('restore')">{{ t(confirmation.includes(':restore:') ? 'knowledge.confirmRestore' : 'knowledge.restore') }}</button><button v-else class="btn" data-testid="memory-suppress-action" :disabled="busy" @click="act('suppress')">{{ t(confirmation.includes(':suppress:') ? 'memory.detail.actions.confirmHideNoise' : 'memory.detail.actions.hideNoise') }}</button><button class="btn" :disabled="busy" @click="act('archive')">{{ t(confirmation.includes(':archive:') ? 'knowledge.confirmArchive' : 'knowledge.archive') }}</button><button class="btn danger" :disabled="busy" @click="act('delete')">{{ t(confirmation.includes(':delete:') ? 'memory.detail.actions.confirmDelete' : 'memory.detail.actions.delete') }}</button></div></details>
          </template>
        </article>
      </div>
      <MutationResultNotice :result="mutationResult" :recheck-label="t('knowledge.refresh')" @recheck="refreshView" />
      <details class="advanced" data-testid="principal-memory-surface">
        <summary>{{ t('knowledge.principalFilters') }}</summary><p>{{ t('knowledge.principalHelp') }}</p>
        <div class="advanced-fields">
          <label><span>{{ t('memory.principal.controls.principal') }}</span><input v-model.trim="principalScope.principal" autocomplete="off" data-testid="principal-select" /></label>
          <label><span>{{ t('memory.principal.controls.kind') }}</span><select v-model="principalScope.principalKind"><option value="human">{{ t('memory.principal.kinds.human') }}</option><option value="agent">{{ t('memory.principal.kinds.agent') }}</option><option value="service">{{ t('memory.principal.kinds.service') }}</option></select></label>
          <label><span>{{ t('memory.principal.controls.domain') }}</span><input v-model.trim="principalScope.domain" data-testid="domain-select" /></label>
          <label><span>{{ t('memory.principal.controls.project') }}</span><select v-model="principalScope.project" data-testid="project-scope"><option :value="PRINCIPAL_CURRENT_PROJECT">{{ t('memory.principal.projects.current') }}</option><option v-for="id in context.projects.value" :key="id" :value="id">{{ projectName(id) }}</option><option value="all">{{ t('knowledge.allProjects') }}</option></select></label>
          <label><span>{{ t('memory.principal.controls.visibility') }}</span><select v-model="principalScope.visibility"><option value="all">{{ t('memory.principal.visibility.all') }}</option><option value="shared">{{ t('memory.principal.visibility.shared') }}</option><option value="private">{{ t('memory.principal.visibility.private') }}</option></select></label>
          <label class="check"><input v-model="principalScope.includePrivate" type="checkbox" />{{ t('knowledge.includePrivate') }}</label>
          <label class="check"><input v-model="principalAttribution" type="checkbox" data-testid="attribution-toggle" />{{ t('memory.principal.controls.attribution') }}</label>
          <button class="btn" :disabled="principalState.kind === 'pending'" @click="principal.refresh()" data-testid="refresh">{{ t(principal.riskyConfirmation.value ? 'memory.principal.actions.confirmScope' : 'knowledge.find') }}</button>
        </div>
        <p v-if="principalState.kind === 'error'" role="status">{{ t(operatorDiagnosisKey(principalState.error)) }}</p>
        <p v-else-if="principalState.kind === 'gated' || principalState.kind === 'stale'" role="status">{{ t('knowledge.principalScopeChanged') }}</p>
        <p v-else-if="principalState.kind === 'pending'" role="status">{{ t('knowledge.loading') }}</p>
        <p v-else-if="!principalItems.length">{{ t('knowledge.noMatches') }}</p>
        <ul v-else class="principal-results"><li v-for="item in principalItems" :key="item.id"><p>{{ item.content }}</p><span>{{ projectName(item.project) }} · {{ item.domain }} · {{ t(`memory.principal.visibility.${item.visibility}`) }}</span><span v-if="principalAttribution"> · {{ item.ownerPrincipal }} · {{ time(item.createdAt) }}</span></li></ul>
        <details><summary>{{ t('memory.principal.brief.title') }}</summary><p>{{ t('memory.principal.brief.mustbuild') }}</p></details>
      </details>
    </template>
  </div>
</template>

<style scoped>
.knowledge-page { display:grid; gap:20px; min-width:0; }
.page-head { display:flex; align-items:start; justify-content:space-between; gap:16px; }
h1 { margin:0 0 6px; font-size:var(--text-xl); } h2 { font-size:var(--text-lg); margin:0 0 16px; } h3 { font-size:var(--text-base); margin:0 0 10px; }
p { margin:0 0 12px; color:var(--muted); }
.toolbar, .action-row, .pagination, .list-head { display:flex; align-items:end; gap:12px; flex-wrap:wrap; }
label { display:grid; gap:6px; font-size:var(--text-sm); color:var(--muted); }
.search-field { flex:1; min-width:180px; }
input:not([type=checkbox]), select { min-height:40px; padding:8px 10px; color:var(--fg); background:var(--surface); border:1px solid var(--border); border-radius:var(--r-sm); max-width:100%; }
.btn { display:inline-flex; align-items:center; justify-content:center; min-height:40px; padding:8px 12px; color:var(--fg); background:var(--surface); border:1px solid var(--border); border-radius:var(--r-sm); cursor:pointer; }
.btn:hover:not(:disabled), .memory-row button:hover { background:var(--surface-warm); }
.btn:disabled { opacity:.5; cursor:not-allowed; } .primary { background:var(--accent); color:var(--accent-on); } .danger { color:var(--state-danger); }
.memory-filters { margin:0; padding:0; border:0; min-width:0; flex-basis:100%; }
.memory-filters legend { margin-bottom:6px; color:var(--muted); font-size:var(--text-sm); }
.filter-chips { display:flex; gap:8px; flex-wrap:wrap; }
.filter-chips .btn[aria-pressed=true] { color:var(--accent); border-color:var(--accent); background:var(--surface-warm); }
.state { padding:14px; border:1px solid var(--border); border-radius:var(--r-sm); display:flex; align-items:center; flex-wrap:wrap; gap:12px; color:var(--muted); } .state.error { color:var(--state-warn); }
.knowledge-workspace { display:grid; gap:24px; min-width:0; }
.knowledge-workspace.detail-open { grid-template-columns:minmax(0,.85fr) minmax(0,1.3fr); align-items:start; }
.list-panel { min-width:0; border:1px solid var(--border); border-radius:var(--r-md); overflow:hidden; }
.list-head { justify-content:space-between; padding:12px; color:var(--muted); font-size:var(--text-sm); border-bottom:1px solid var(--border); }
.list-head label { display:flex; align-items:center; }
.memory-row { display:flex; align-items:start; gap:8px; border-bottom:1px solid var(--border-soft); padding:0 12px; }
.memory-row.selected { background:var(--surface-warm); }
.memory-row > input { margin-top:20px; width:18px; height:18px; accent-color:var(--accent); }
.memory-row button { flex:1; min-width:0; padding:16px 6px; border:0; background:transparent; color:var(--fg); display:grid; gap:8px; text-align:left; cursor:pointer; }
.memory-row strong { font-size:var(--text-base); font-weight:500; display:-webkit-box; -webkit-line-clamp:3; -webkit-box-orient:vertical; overflow:hidden; overflow-wrap:anywhere; }
.memory-row span { color:var(--muted); font-size:var(--text-xs); overflow-wrap:anywhere; }
.pagination { align-items:center; justify-content:space-between; padding:12px; font-size:var(--text-sm); }
.reader { min-width:0; padding:20px; background:var(--surface); border:1px solid var(--border); border-radius:var(--r-md); }
.back { margin-bottom:20px; }
.currentness { color:var(--fg-2); }
.content-body { white-space:pre-wrap; overflow-wrap:anywhere; line-height:1.65; max-width:75ch; font-size:var(--text-base); }
.reader section, .reader details { margin-top:28px; padding-top:18px; border-top:1px solid var(--border); }
dl { display:grid; grid-template-columns:120px minmax(0,1fr); gap:8px 12px; margin:0 0 16px; font-size:var(--text-sm); } dt { color:var(--muted); } dd { margin:0; overflow-wrap:anywhere; }
.provenance > a { display:block; margin:8px 0; }
.history ol { list-style:none; margin:0; padding:0; } .history li { display:grid; gap:4px; padding:12px 0; border-bottom:1px solid var(--border-soft); } .history time, .history li > span { color:var(--muted); font-size:var(--text-sm); }
summary { cursor:pointer; min-height:40px; color:var(--fg-2); padding:8px 0; } details dl { margin-top:12px; }
.advanced { border-top:1px solid var(--border); padding-top:8px; }
.advanced-fields { display:grid; grid-template-columns:repeat(3,minmax(0,1fr)); align-items:end; gap:12px; }
.check { display:flex; align-items:center; min-height:40px; } .principal-results { margin:16px 0 0; padding:0; list-style:none; } .principal-results li { padding:12px 0; border-top:1px solid var(--border); overflow-wrap:anywhere; }
.text-button { border:0; padding:4px; color:var(--accent); background:transparent; cursor:pointer; }
.empty { padding:32px 20px; } .bulk { display:flex; flex-wrap:wrap; gap:10px; padding:12px; }
.knowledge-page :is(label, input, select, section, article, details, form) { min-width:0; }
.reader h2 { scroll-margin-top:20px; }
@media(max-width:760px) { .knowledge-workspace.detail-open { grid-template-columns:1fr; } .detail-open .list-panel { display:none; } .reader { padding:16px; } .toolbar { align-items:stretch; } .search-field { flex-basis:100%; } .btn, input:not([type=checkbox]), select { min-height:44px; } .advanced-fields { grid-template-columns:1fr; } .page-head { flex-wrap:wrap; } dl { grid-template-columns:90px minmax(0,1fr); } }
</style>
