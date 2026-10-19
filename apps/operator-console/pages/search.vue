<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, ref, watch } from 'vue'
import { operatorDiagnosisKey } from '../composables/useOperatorApi'
import { isCatalogProject, useOperatorProjectContext, projectName } from '../composables/useOperatorProjectContext'
import { useOperatorSearchNoise } from '../composables/useOperatorSearchNoise'
const { t } = useI18n()
const route = useRoute()
const router = useRouter()
const context = useOperatorProjectContext()
const query = ref(typeof route.query.draftQ === 'string' ? route.query.draftQ : '')
const requestedQuery = computed(() => typeof route.query.q === 'string' ? route.query.q.trim() : '')
const requestedProject = context.project
const appliedRouteSearch = ref('')
const {
  projects,
  selectedProject,
  searchResults,
  recentRows,
  searchState,
  recentState,
  analyticsState,
  pending,
  error,
  refresh,
  runSearch,
} = useOperatorSearchNoise()

const hasProjects = computed(() => projects.value.length > 0)
const hasRequestedProject = computed(() => isCatalogProject(requestedProject.value) && !context.selectionPending.value && !context.selectionError.value && !context.error.value)
const searchError = computed(() => context.selectionError.value || context.error.value || (searchState.value.kind === 'error' ? searchState.value.error : null))

async function refreshSearch() {
  await refresh()
  if (hasRequestedProject.value && requestedQuery.value && selectedProject.value === requestedProject.value) await runSearch(requestedQuery.value, requestedProject.value)
}

watch([requestedQuery, requestedProject, hasRequestedProject], async ([nextQuery, nextProject, projectAvailable]) => {
  query.value = typeof route.query.draftQ === 'string' ? route.query.draftQ : nextQuery
  if (!nextQuery || !nextProject || !projectAvailable) {
    appliedRouteSearch.value = ''
    await runSearch('', '')
    return
  }

  if (selectedProject.value !== nextProject) return
  const searchKey = `${nextProject}\u0000${nextQuery}`
  if (appliedRouteSearch.value === searchKey) return
  appliedRouteSearch.value = searchKey
  await runSearch(nextQuery, nextProject)
  if (requestedProject.value !== nextProject || requestedQuery.value !== nextQuery) return
  const memory = typeof route.query.focusMemory === 'string' ? route.query.focusMemory : ''
  if (memory) { await nextTick(); document.querySelector<HTMLElement>(`[data-testid="search-result-open-${CSS.escape(memory)}"]`)?.focus() }
}, { immediate: true, flush: 'sync' })

async function submitSearch() {
  if (!hasRequestedProject.value) return
  await router.push({ path: '/search', query: { project: selectedProject.value, q: query.value.trim() } })
}

function openResult(resultProject: string, memory: string) {
  const project = selectedProject.value
  if (!hasRequestedProject.value || resultProject !== project || !memory) return
  void router.push({ path: '/memory', query: { ...context.to('/memory').query, project, memory, from: 'search', searchQuery: requestedQuery.value } })
}
onBeforeUnmount(() => {
  if (route.path === '/search') context.remember('/search', { ...route.query, draftQ: query.value })
})
</script>

<template>
  <div class="search-page">
    <header class="head">
      <div>
        <h1>{{ t('searchPage.title') }}</h1>
        <p>{{ t('searchPage.subtitle') }}</p>
      </div>
      <button class="btn" type="button" :disabled="pending" @click="refreshSearch">{{ t('searchPage.actions.refresh') }}</button>
    </header>

    <form class="search-panel" @submit.prevent="submitSearch">
      <label>
        <span>{{ t('searchPage.form.project') }}</span>
        <select :value="selectedProject" :disabled="!hasProjects" @change="context.select(($event.target as HTMLSelectElement).value)">
          <option v-if="!selectedProject" value="" disabled>{{ t('knowledge.chooseProject') }}</option>
          <option v-if="selectedProject === 'all'" value="all" disabled>{{ t('knowledge.chooseProject') }}</option>
          <option v-for="project in projects" :key="project" :value="project">{{ projectName(project) }}</option>
        </select>
      </label>
      <label class="query">
        <span>{{ t('searchPage.form.query') }}</span>
        <input v-model="query" :placeholder="t('searchPage.placeholder')" autocomplete="off">
      </label>
      <button class="btn primary" type="submit" :disabled="pending || !query.trim() || !hasRequestedProject">
        {{ t('searchPage.actions.search') }}
      </button>
    </form>

    <div v-if="pending || context.selectionPending.value" class="state pending" role="status">{{ t('searchPage.state.pending') }}</div>
    <div v-if="searchError" class="state error" role="status"><p>{{ t(operatorDiagnosisKey(searchError)) }}</p><button class="btn" type="button" :disabled="pending" @click="refreshSearch">{{ t('knowledge.retry') }}</button></div>
    <nav v-if="hasRequestedProject && requestedQuery" class="text-recovery" :aria-label="t('searchPage.recovery.title')">
      <p>{{ t('searchPage.recovery.body') }}</p>
      <RouterLink class="btn" data-testid="search-text-memory" :to="{ path: '/memory', query: { project: requestedProject, q: requestedQuery, page: '1' } }">{{ t('knowledge.searchMemory') }}</RouterLink>
      <RouterLink class="btn" data-testid="search-text-documents" :to="{ path: '/documents', query: { project: requestedProject, q: requestedQuery, page: '1' } }">{{ t('knowledge.searchDocuments') }}</RouterLink>
    </nav>

    <section class="grid">
      <article class="card results">
        <div class="card-head">
          <div>
            <h2>{{ t('searchPage.results.title') }}</h2>
          </div>
        </div>

        <div v-if="searchError || searchState.kind === 'stale'" class="empty">
          <strong>{{ t('searchPage.results.unavailableTitle') }}</strong>
          <p>{{ t('searchPage.results.unavailableBody') }}</p>
        </div>
        <div v-else-if="searchState.kind === 'empty'" class="empty">
          <strong>{{ t(requestedQuery && appliedRouteSearch ? 'knowledge.noMatches' : 'searchPage.results.emptyTitle') }}</strong>
          <p>{{ t('searchPage.results.emptyBody') }}</p>
        </div>
        <div v-else class="rows">
          <div v-for="row in searchResults" :key="row.id" class="result-row">
            <div>
              <strong>{{ row.title }}</strong>
              <p>{{ row.body || t('searchPage.results.noBody') }}</p>
            </div>
            <div class="meta">
              <span>{{ projectName(selectedProject) }}</span>
              <details><summary>{{ t('knowledge.technical') }}</summary><code>{{ row.type }} · {{ row.score }}</code></details>
              <button v-if="row.memoryBacked" class="btn" type="button" :data-testid="`search-result-open-${row.id}`" @click="openResult(row.project, row.id)">{{ t('searchPage.actions.openMemory') }}</button>
            </div>
          </div>
        </div>
      </article>

      <details class="side card">
        <summary>{{ t('knowledge.technical') }}</summary>
        <code>{{ searchState.evidence.endpoint }}</code>
        <p v-if="searchError">{{ searchError.message }}</p>
        <p v-if="searchState.kind === 'stale'">{{ searchState.reason }}</p>
        <p v-if="error && error !== searchError?.message">{{ error }}</p>
        <article class="card">
          <div class="card-head">
            <h2>{{ t('searchPage.recent.title') }}</h2>
            <code>{{ recentState.evidence.endpoint }}</code>
          </div>
          <div v-if="recentState.kind === 'empty'" class="empty small">{{ t('searchPage.recent.empty') }}</div>
          <div v-else class="recent-list">
            <div v-for="row in recentRows" :key="row.id" class="recent-row">
              <span>{{ row.query }}</span>
              <code>{{ row.resultCount }}</code>
            </div>
          </div>
        </article>

        <article class="card">
          <div class="card-head">
            <h2>{{ t('searchPage.analytics.title') }}</h2>
            <code>{{ analyticsState.evidence.endpoint }}</code>
          </div>
          <dl class="stats">
            <div>
              <dt>{{ t('searchPage.analytics.total') }}</dt>
              <dd>{{ analyticsState.kind === 'live' ? analyticsState.data.total_searches ?? '—' : '—' }}</dd>
            </div>
            <div>
              <dt>{{ t('searchPage.analytics.zeroRate') }}</dt>
              <dd>{{ analyticsState.kind === 'live' ? analyticsState.data.zero_result_rate ?? '—' : '—' }}</dd>
            </div>
          </dl>
        </article>

      </details>
    </section>
  </div>
</template>

<style scoped>
.search-page { display:grid; gap:16px; }
.head { display:flex; align-items:flex-start; justify-content:space-between; gap:16px; }
.head h1 { margin:0 0 4px; font-size:var(--text-xl); font-weight:700; }
.head p { margin:0; font-size:var(--text-sm); color:var(--muted); }
.btn { border:1px solid var(--border); border-radius:var(--r-sm); background:var(--surface); color:var(--fg); padding:9px 14px; font:inherit; font-weight:700; cursor:pointer; }
.btn.primary { background:var(--accent); border-color:var(--accent); color:var(--accent-on); }
.btn:disabled { opacity:.55; cursor:not-allowed; }
.search-panel { border:1px solid var(--border); border-radius:var(--r-md); background:var(--surface); padding:14px; display:grid; grid-template-columns:minmax(160px,220px) 1fr auto; gap:12px; align-items:end; }
label { display:grid; gap:6px; color:var(--muted); font-size:var(--text-xs); text-transform:uppercase; letter-spacing:.04em; }
input, select { width:100%; border:1px solid var(--border); border-radius:var(--r-sm); background:var(--bg); color:var(--fg); padding:9px 10px; font:inherit; text-transform:none; letter-spacing:0; }
.text-recovery { display:flex; flex-wrap:wrap; gap:10px; align-items:center; }
.text-recovery p { flex-basis:100%; margin:0; color:var(--muted); font-size:var(--text-sm); }
.side summary { cursor:pointer; min-height:44px; display:flex; align-items:center; }
.card code { overflow-wrap:anywhere; }
.state { border:1px solid var(--border); border-radius:var(--r-md); background:var(--surface); padding:10px 12px; color:var(--fg-2); font-size:var(--text-sm); }
.state.pending { border-color:color-mix(in oklab,var(--accent),transparent 55%); }
.state.error { color:var(--state-warn); border-color:color-mix(in oklab,var(--state-warn),transparent 45%); }
.grid { display:grid; grid-template-columns:minmax(0,1fr) 340px; gap:14px; align-items:start; }
.card { border:1px solid var(--border); border-radius:var(--r-md); background:var(--surface); padding:16px; }
.card-head { display:flex; align-items:center; justify-content:space-between; gap:12px; margin-bottom:12px; }
.card-head h2 { margin:0; font-size:var(--text-sm); font-weight:800; }
.card-head p { margin:3px 0 0; color:var(--muted); font-size:var(--text-xs); }
.card-head code, .card code { font-family:var(--font-mono); font-size:10px; color:var(--muted); }
.rows, .recent-list, .side { display:grid; gap:10px; }
.result-row { display:grid; grid-template-columns:minmax(0,1fr) auto; gap:14px; border-top:1px solid var(--border-soft); padding:12px 0; }
.result-row:first-child { border-top:0; padding-top:0; }
.result-row strong { display:block; color:var(--fg); }
.result-row p { margin:4px 0 0; color:var(--muted); font-size:var(--text-sm); }
.meta { display:flex; flex-wrap:wrap; justify-content:flex-end; gap:6px; color:var(--muted); font-size:var(--text-xs); }
.recent-row { display:flex; justify-content:space-between; gap:10px; border-top:1px solid var(--border-soft); padding-top:8px; color:var(--fg-2); }
.recent-row:first-child { border-top:0; padding-top:0; }
.stats { display:grid; grid-template-columns:1fr 1fr; gap:10px; margin:0; }
.stats dt { color:var(--muted); font-size:var(--text-xs); text-transform:uppercase; letter-spacing:.04em; }
.stats dd { margin:5px 0 0; font-family:var(--font-mono); font-size:var(--text-xl); font-weight:800; }
.empty { border:1px dashed var(--border); border-radius:var(--r-md); padding:28px; text-align:center; color:var(--muted); background:var(--bg); }
.empty strong { display:block; color:var(--fg); margin-bottom:5px; }
.empty p { margin:0; }
.empty.small { padding:16px; }
@media (max-width: 920px) {
  .search-panel, .grid { grid-template-columns:1fr; }
  .head { display:grid; }
}
</style>
