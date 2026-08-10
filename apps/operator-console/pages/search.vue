<script setup lang="ts">
const { t } = useI18n()
const route = useRoute()
const router = useRouter()
const query = ref('')
const requestedQuery = computed(() => typeof route.query.q === 'string' ? route.query.q.trim() : '')
const requestedProject = computed(() => typeof route.query.project === 'string' ? route.query.project.trim() : '')
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
  tombstoneGap,
} = useOperatorSearchNoise()

const hasProjects = computed(() => projects.value.length > 0)
const hasRequestedProject = computed(() => Boolean(requestedProject.value) && projects.value.includes(requestedProject.value))

watch([requestedQuery, requestedProject, hasRequestedProject], async ([nextQuery, nextProject, projectAvailable]) => {
  query.value = nextQuery
  if (!nextQuery || !nextProject || !projectAvailable) {
    appliedRouteSearch.value = ''
    await runSearch('', '')
    return
  }

  selectedProject.value = nextProject
  const searchKey = `${nextProject}\u0000${nextQuery}`
  if (appliedRouteSearch.value === searchKey) return
  appliedRouteSearch.value = searchKey
  await runSearch(nextQuery, nextProject)
}, { immediate: true })

async function submitSearch() {
  await runSearch(query.value, selectedProject.value)
}

function openResult(project: string, memory: string) {
  if (!project || project === '-' || !memory) return
  void router.push({ path: '/memory', query: { project, memory } })
}
</script>

<template>
  <div class="search-page">
    <header class="head">
      <div>
        <h1>{{ t('searchPage.title') }}</h1>
        <p>{{ t('searchPage.subtitle') }}</p>
      </div>
      <button class="btn" type="button" :disabled="pending" @click="refresh">{{ t('searchPage.actions.refresh') }}</button>
    </header>

    <form class="search-panel" @submit.prevent="submitSearch">
      <label>
        <span>{{ t('searchPage.form.project') }}</span>
        <select v-model="selectedProject" :disabled="!hasProjects">
          <option v-if="!hasProjects" value="">{{ t('searchPage.form.noProjects') }}</option>
          <option v-for="project in projects" :key="project" :value="project">{{ project }}</option>
        </select>
      </label>
      <label class="query">
        <span>{{ t('searchPage.form.query') }}</span>
        <input v-model="query" :placeholder="t('searchPage.placeholder')" autocomplete="off">
      </label>
      <button class="btn primary" type="submit" :disabled="pending || !query.trim() || !selectedProject">
        {{ t('searchPage.actions.search') }}
      </button>
    </form>

    <div v-if="pending" class="state pending">{{ t('searchPage.state.pending') }}</div>
    <div v-if="error" class="state error">{{ t('searchPage.state.error', { message: error }) }}</div>

    <section class="grid">
      <article class="card results">
        <div class="card-head">
          <div>
            <h2>{{ t('searchPage.results.title') }}</h2>
            <p>{{ t('searchPage.results.source') }}</p>
          </div>
          <HonestyBadge :cls="searchState.kind === 'stale' ? 'stale' : searchState.kind === 'error' ? 'dormant' : 'live'" :evidence="searchState.kind === 'stale' ? searchState.evidence.endpoint : searchState.kind === 'error' ? searchState.error.message : undefined" />
        </div>

        <div v-if="searchState.kind === 'empty'" class="empty">
          <strong>{{ t('searchPage.results.emptyTitle') }}</strong>
          <p>{{ t('searchPage.results.emptyBody') }}</p>
        </div>
        <div v-else-if="searchState.kind === 'stale'" class="empty stale">
          <strong>{{ t('searchPage.results.staleTitle') }}</strong>
          <p>{{ searchState.reason }}</p>
        </div>
        <div v-else class="rows">
          <div v-for="row in searchResults" :key="row.id" class="result-row">
            <div>
              <strong>{{ row.title }}</strong>
              <p>{{ row.body || t('searchPage.results.noBody') }}</p>
            </div>
            <div class="meta">
              <span>{{ row.project }}</span>
              <code>{{ row.type }}</code>
              <code>{{ row.score }}</code>
              <button v-if="row.memoryBacked" class="btn" type="button" :data-testid="`search-result-open-${row.id}`" @click="openResult(row.project, row.id)">{{ t('searchPage.actions.openMemory') }}</button>
            </div>
          </div>
        </div>
      </article>

      <aside class="side">
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
              <dd>{{ analyticsState.kind === 'live' ? analyticsState.data.total_searches ?? 0 : '—' }}</dd>
            </div>
            <div>
              <dt>{{ t('searchPage.analytics.zeroRate') }}</dt>
              <dd>{{ analyticsState.kind === 'live' ? analyticsState.data.zero_result_rate ?? 0 : '—' }}</dd>
            </div>
          </dl>
        </article>

        <article class="card tombstone">
          <div class="card-head">
            <h2>{{ t('searchPage.tombstone.title') }}</h2>
            <HonestyBadge cls="stale" />
          </div>
          <p>{{ t('searchPage.tombstone.body') }}</p>
          <code>{{ tombstoneGap.evidence.endpoint }}</code>
        </article>
      </aside>
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
.result-row p, .tombstone p { margin:4px 0 0; color:var(--muted); font-size:var(--text-sm); }
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
.empty.stale { border-color:color-mix(in oklab,var(--class-stale),transparent 35%); }
@media (max-width: 920px) {
  .search-panel, .grid { grid-template-columns:1fr; }
  .head { display:grid; }
}
</style>
