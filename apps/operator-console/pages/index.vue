<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { useOperatorProjectContext, projectName, loadKnowledgePage } from '../composables/useOperatorProjectContext'
import { operatorDiagnosisKey, toOperatorSourceError, type OperatorSourceError } from '../composables/useOperatorApi'
const { t } = useI18n()
const context = useOperatorProjectContext()
const { project, projects, descriptors, unresolved, pending, error, selectionPending, selectionError, label } = context
const projectQuery = ref('')
const visibleProjects = computed(() => projects.value.filter((id) => `${projectName(id)} ${id}`.toLocaleLowerCase().includes(projectQuery.value.toLocaleLowerCase())))
const recentMemories = ref<Array<{ id: number; project: string; content: string }>>([])
const recentDocuments = ref<Array<{ path: string; project: string; version: number; author: string }>>([])
const memoryTotal = ref<number | null>(null)
const documentTotal = ref<number | null>(null)
const homeError = ref<OperatorSourceError | null>(null)
const homePending = ref(false)
let generation = 0
let request: AbortController | undefined
watch(project, async (id) => {
  const current = ++generation
  request?.abort()
  const controller = new AbortController()
  request = controller
  recentMemories.value = []
  recentDocuments.value = []
  memoryTotal.value = null
  documentTotal.value = null
  homeError.value = null
  homePending.value = false
  if (!id || id === 'all') return
  homePending.value = true
  try {
    const params = new URLSearchParams({ offset: '0', limit: '3' })
    const [memories, documents] = await Promise.all([
      loadKnowledgePage<typeof recentMemories.value[number]>('/api/memories', id, params, controller.signal),
      loadKnowledgePage<typeof recentDocuments.value[number]>('/api/documents', id, params, controller.signal),
    ])
    if (current !== generation || project.value !== id) return
    if (memories.items.some((row) => row.project !== id) || documents.items.some((row) => row.project !== id)) throw new TypeError('Home scope mismatch')
    recentMemories.value = memories.items
    recentDocuments.value = documents.items
    memoryTotal.value = memories.total
    documentTotal.value = documents.total
  } catch (cause) {
    if (current === generation && !controller.signal.aborted) homeError.value = toOperatorSourceError(cause, { source: 'home-knowledge', path: '/api/memories, /api/documents', method: 'GET' })
  } finally { if (current === generation) homePending.value = false }
}, { immediate: true, flush: 'sync' })
onBeforeUnmount(() => { generation += 1; request?.abort() })
</script>

<template>
  <div class="home-page">
    <header><h1>{{ project ? t('knowledge.homeProject', { project: project === 'all' ? t('knowledge.allProjects') : label }) : t('knowledge.homeTitle') }}</h1><p>{{ t(project ? 'knowledge.homeLead' : 'knowledge.entryLead') }}</p></header>
    <p v-if="selectionPending" role="status">{{ t('projectCatalog.loading') }}</p>
    <div v-if="selectionError" role="alert"><p>{{ t('projectCatalog.unknownProject') }} {{ t(operatorDiagnosisKey(selectionError)) }}</p><button class="btn" @click="context.refresh">{{ t('knowledge.retry') }}</button></div>
    <RouterLink v-if="unresolved.length" to="/projects/unresolved">{{ t('projectCatalog.review') }} ({{ unresolved.length }})</RouterLink>
    <section v-if="!project" class="project-entry">
      <label class="field"><span>{{ t('knowledge.findProject') }}</span><input v-model="projectQuery" type="search" /></label>
      <p v-if="pending" role="status">{{ t('knowledge.loading') }}</p>
      <div v-else-if="error" role="status"><p>{{ t(operatorDiagnosisKey(error)) }}</p><button class="btn" @click="context.refresh">{{ t('knowledge.retry') }}</button></div>
      <p v-else-if="!projects.length">{{ t('knowledge.noProjects') }}</p>
      <p v-else-if="!visibleProjects.length">{{ t('knowledge.noProjectMatch') }}</p>
      <ul v-else class="project-list">
        <li v-for="id in visibleProjects" :key="id"><button @click="context.select(id)"><strong>{{ projectName(id) }}</strong><span v-if="descriptors.filter(row => projectName(row.project_id) === projectName(id)).length > 1">{{ t('projectCatalog.ambiguousName') }} · {{ id }}</span></button></li>
      </ul>
      <button class="btn" @click="context.select('all')">{{ t('knowledge.browseAll') }}</button>
      <RouterLink :to="context.to('/code')" data-testid="overview-workspace-entry">{{ t('nav.items.code') }}</RouterLink><p>{{ t('knowledge.codeScope') }}</p>
    </section>
    <template v-else>
      <section class="destinations" aria-labelledby="knowledge-home-heading">
        <h2 id="knowledge-home-heading">{{ t('knowledge.yourKnowledge') }}</h2>
        <RouterLink :to="context.to('/memory')"><strong>{{ t('nav.items.memory') }}</strong><span>{{ t('knowledge.memoryHome') }}</span><span aria-hidden="true">→</span></RouterLink>
        <RouterLink :to="context.to('/documents')"><strong>{{ t('nav.items.documents') }}</strong><span>{{ t('knowledge.documentsHome') }}</span><span aria-hidden="true">→</span></RouterLink>
        <RouterLink :to="context.to('/rules')"><strong>{{ t('nav.items.rules') }}</strong><span>{{ t('knowledge.rulesHome') }}</span><span aria-hidden="true">→</span></RouterLink>
      </section>
      <section v-if="project !== 'all'" class="recent-knowledge">
        <p v-if="homePending" role="status">{{ t('knowledge.loading') }}</p>
        <div v-else-if="homeError" role="status"><p>{{ t(operatorDiagnosisKey(homeError)) }}</p><RouterLink :to="context.to('/memory')">{{ t('knowledge.openMemory') }}</RouterLink></div>
        <template v-else>
          <section><h2>{{ t('knowledge.recentMemory') }}</h2><p>{{ memoryTotal === null ? t('knowledge.unknown') : t('knowledge.matches', { count: memoryTotal }) }}</p><p v-if="memoryTotal === 0">{{ t('knowledge.noMemoryHelp') }}</p><RouterLink v-for="memory in recentMemories" :key="memory.id" :to="{ path: '/memory', query: { project, memory: String(memory.id) } }">{{ memory.content }}</RouterLink></section>
          <section><h2>{{ t('knowledge.recentDocuments') }}</h2><p>{{ documentTotal === null ? t('knowledge.unknown') : t('knowledge.matches', { count: documentTotal }) }}</p><p v-if="documentTotal === 0">{{ t('knowledge.noDocumentsHelp') }}</p><RouterLink v-for="doc in recentDocuments" :key="doc.path" :to="{ path: '/documents', query: { project, path: doc.path } }"><strong>{{ doc.path }}</strong><span>{{ t('knowledge.version') }} {{ doc.version }} · {{ doc.author || t('knowledge.notRecorded') }}</span></RouterLink></section>
        </template>
      </section>
      <section class="destinations">
        <h2>{{ t('nav.groups.workspace') }}</h2>
        <RouterLink :to="context.to('/code')" data-testid="overview-workspace-entry"><strong>{{ t('nav.items.code') }}</strong><span>{{ t('overview.cards.code.sub') }}</span><span aria-hidden="true">→</span></RouterLink>
        <RouterLink :to="context.to('/projects')"><strong>{{ t('knowledge.sessions') }}</strong><span>{{ t('knowledge.sessionsHome') }}</span><span aria-hidden="true">→</span></RouterLink>
      </section>
      <section class="secondary-links"><h2>{{ t('nav.groups.attention') }}</h2><RouterLink :to="context.to('/queue')">{{ t('nav.items.queue') }}</RouterLink><RouterLink :to="context.to('/issues')">{{ t('nav.items.issues') }}</RouterLink><RouterLink to="/health">{{ t('knowledge.diagnostics') }}</RouterLink></section>
      <details><summary>{{ t('knowledge.projectIdentity') }}</summary><code>{{ project }}</code><p>{{ t('knowledge.identityHelp') }}</p></details>
    </template>
  </div>
</template>

<style scoped>
.home-page { max-width:1080px; display:grid; gap:28px; }
h1 { font-size:var(--text-xl); margin:0 0 8px; } h2 { font-size:var(--text-lg); margin:0 0 8px; }
p { color:var(--muted); margin:0; max-width:70ch; }
.project-entry { display:grid; gap:16px; max-width:760px; }
.field { display:grid; gap:6px; color:var(--muted); }
input { padding:10px 12px; background:var(--surface); color:var(--fg); border:1px solid var(--border); border-radius:var(--r-sm); }
.project-list { margin:0; padding:0; list-style:none; max-height:50vh; overflow:auto; }
.project-list button { width:100%; padding:14px; display:grid; gap:4px; text-align:left; background:var(--surface); color:var(--fg); border:0; border-bottom:1px solid var(--border); cursor:pointer; }
.project-list button:hover { background:var(--surface-warm); }
.project-list span { color:var(--muted); font-size:var(--text-xs); overflow-wrap:anywhere; }
.destinations { display:grid; }
.destinations > a { display:grid; grid-template-columns:160px minmax(0,1fr) 24px; align-items:center; gap:16px; padding:20px 0; color:var(--fg); border-bottom:1px solid var(--border); }
.destinations > a:hover { color:var(--accent); }
.destinations > a span { color:var(--muted); }
.secondary-links { display:flex; flex-wrap:wrap; align-items:center; gap:16px; }
.secondary-links h2 { flex-basis:100%; }
.recent-knowledge { display:grid; grid-template-columns:1fr 1fr; gap:28px; }
.recent-knowledge section { min-width:0; }
.recent-knowledge a { display:grid; gap:6px; padding:14px 0; border-bottom:1px solid var(--border); color:var(--fg); overflow-wrap:anywhere; }
.recent-knowledge a:first-of-type { display:-webkit-box; -webkit-line-clamp:3; -webkit-box-orient:vertical; overflow:hidden; }
.recent-knowledge a span { color:var(--muted); font-size:var(--text-sm); }
@media(max-width:760px) { .recent-knowledge { grid-template-columns:1fr; } }
.btn { min-height:40px; justify-self:start; padding:8px 14px; background:var(--surface); color:var(--fg); border:1px solid var(--border); border-radius:var(--r-sm); cursor:pointer; }
details { color:var(--muted); } summary { cursor:pointer; } code { overflow-wrap:anywhere; }
@media(max-width:760px) { .destinations > a { grid-template-columns:minmax(0,1fr) 24px; gap:6px; } .destinations > a span:first-of-type { grid-row:2; } .destinations > a span:last-child { grid-row:1 / 3; grid-column:2; } .btn, .project-list button { min-height:44px; } }
</style>
