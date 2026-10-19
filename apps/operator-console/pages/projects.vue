<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useOperatorProjectContext, projectName } from '../composables/useOperatorProjectContext'
import { useOperatorProjects } from '../composables/useOperatorProjects'
import { operatorDiagnosisKey } from '../composables/useOperatorApi'
const { t } = useI18n()
const route = useRoute()
const router = useRouter()
const context = useOperatorProjectContext()
const { project, projects, descriptors, unresolved, pending: catalogPending, error: projectError, selectionPending, selectionError } = context
const { sessions, selectedSession, sessionsState, detailState, pending, refresh, openProject, openSession, openSessionByID, deleteProject } = useOperatorProjects(project)
const search = ref('')
const visibleProjects = computed(() => projects.value.filter((id) => `${projectName(id)} ${id}`.toLocaleLowerCase().includes(search.value.toLocaleLowerCase())))
const archiveInput = ref('')
const archiveBusy = ref(false)
const mutationResult = ref<Awaited<ReturnType<typeof deleteProject>> | null>(null)
let generation = 0
watch([project, () => route.query.session], async () => {
  const id = project.value
  archiveInput.value = ''
  const current = ++generation
  await openProject(id)
  if (current !== generation || !context.isCatalogProject(id)) return
  const requested = typeof route.query.session === 'string' ? route.query.session : ''
  const session = sessions.find((row) => row.claudeSessionId === requested || row.id === requested)
  if (session) await openSession(session)
  else if (requested) await openSessionByID(requested)
}, { immediate: true, flush: 'sync' })
async function archive() {
  const id = project.value
  if (!context.isCatalogProject(id) || archiveInput.value !== id || archiveBusy.value) return
  archiveBusy.value = true
  try {
    const result = await deleteProject(id)
    if (project.value !== id) return
    mutationResult.value = result
    if (result.kind === 'committed_verified') { await context.refresh(); archiveInput.value = '' }
  } finally { archiveBusy.value = false }
}
</script>

<template>
  <div class="projects-page">
    <header><h1>{{ t('nav.items.projects') }}</h1><p>{{ t('knowledge.entryLead') }}</p></header>
    <div v-if="projectError" class="state error"><span>{{ t(operatorDiagnosisKey(projectError)) }}</span><button class="btn" @click="context.refresh">{{ t('knowledge.retry') }}</button></div>
    <p v-if="selectionPending" role="status">{{ t('projectCatalog.loading') }}</p>
    <div v-if="selectionError" role="alert"><p>{{ t('projectCatalog.unknownProject') }} {{ t(operatorDiagnosisKey(selectionError)) }}</p><button class="btn" @click="context.refresh">{{ t('knowledge.retry') }}</button></div>
    <RouterLink v-if="unresolved.length" to="/projects/unresolved">{{ t('projectCatalog.review') }} ({{ unresolved.length }})</RouterLink>
    <div class="project-workspace">
      <section class="project-list"><label><span>{{ t('knowledge.findProject') }}</span><input v-model="search" type="search" /></label><p v-if="catalogPending" role="status">{{ t('knowledge.loading') }}</p><p v-else-if="!projectError && !projects.length">{{ t('knowledge.noProjects') }}</p><p v-else-if="!projectError && !visibleProjects.length">{{ t('knowledge.noProjectMatch') }}</p><button v-for="id in visibleProjects" :key="id" class="project-row" :aria-pressed="project === id" :data-testid="`project-row-${id}`" @click="context.select(id)"><strong>{{ projectName(id) }}</strong><span v-if="descriptors.filter(row => projectName(row.project_id) === projectName(id)).length > 1">{{ t('projectCatalog.ambiguousName') }} · {{ id }}</span></button><button class="btn" @click="context.select('all')">{{ t('knowledge.browseAll') }}</button></section>
      <section v-if="project" class="project-content">
        <h2>{{ project === 'all' ? t('knowledge.allProjects') : projectName(project) }}</h2>
        <nav class="project-links"><RouterLink :to="context.to('/memory')">{{ t('nav.items.memory') }}</RouterLink><RouterLink :to="context.to('/documents')">{{ t('nav.items.documents') }}</RouterLink><RouterLink :to="context.to('/code')">{{ t('nav.items.code') }}</RouterLink><RouterLink :to="context.to('/')">{{ t('nav.items.overview') }}</RouterLink></nav>
        <section class="sessions">
          <div class="section-head"><h3>{{ t('knowledge.recentSessions') }}</h3><button class="btn" :disabled="pending" @click="refresh">{{ t('knowledge.refresh') }}</button></div>
          <p v-if="project === 'all'">{{ t('knowledge.allSessionsHelp') }}</p>
          <p v-else-if="sessionsState.kind === 'pending'" role="status">{{ t('knowledge.loading') }}</p>
          <div v-else-if="sessionsState.kind === 'error'" class="state error" role="status"><p>{{ t(operatorDiagnosisKey(sessionsState.error)) }}</p><button class="btn" @click="refresh">{{ t('knowledge.retry') }}</button></div>
          <p v-else-if="!sessions.length">{{ t('knowledge.noSessions') }}</p>
          <button v-for="session in sessions" :key="session.id" class="session-row" :data-testid="`project-session-${session.id}`" :aria-pressed="selectedSession?.id === session.id" @click="router.push({ path: '/projects', query: { ...route.query, session: session.claudeSessionId || session.id } })"><strong>{{ session.userPrompt || t('knowledge.originSession') }}</strong><span>{{ session.status }} · {{ session.startedAt || t('knowledge.unknown') }} · {{ t('projects.meta.prompts', { count: session.promptCounter }) }}</span></button>
        </section>
        <section v-if="selectedSession || detailState.kind === 'pending' || detailState.kind === 'error'" class="session-detail">
          <h3>{{ t('knowledge.sessionDetails') }}</h3>
          <p v-if="detailState.kind === 'pending'" role="status">{{ t('knowledge.loading') }}</p>
          <div v-else-if="detailState.kind === 'error'" class="state error" role="status"><p>{{ t(operatorDiagnosisKey(detailState.error)) }}</p><button class="btn" @click="openSessionByID(String(route.query.session || ''))">{{ t('knowledge.retry') }}</button></div>
          <template v-else-if="selectedSession">
            <dl><dt>{{ t('projects.detail.status') }}</dt><dd>{{ selectedSession.status }}</dd><dt>{{ t('projects.detail.startedAt') }}</dt><dd>{{ selectedSession.startedAt || t('knowledge.unknown') }}</dd><dt>{{ t('projects.detail.completedAt') }}</dt><dd>{{ selectedSession.completedAt || t('knowledge.notRecorded') }}</dd><dt>{{ t('projects.detail.outcome') }}</dt><dd>{{ selectedSession.outcome }}</dd></dl>
            <p v-if="selectedSession.outcomeReason">{{ selectedSession.outcomeReason }}</p><div class="prompt" data-testid="project-session-user-prompt">{{ selectedSession.userPrompt || t('knowledge.notRecorded') }}</div>
            <details><summary>{{ t('knowledge.technical') }}</summary><dl><dt>ID</dt><dd>{{ selectedSession.id }}</dd><dt>{{ t('projects.detail.claudeSessionId') }}</dt><dd>{{ selectedSession.claudeSessionId }}</dd><dt>{{ t('projects.detail.sdkSessionId') }}</dt><dd>{{ selectedSession.sdkSessionId }}</dd><dt>{{ t('projects.detail.worker') }}</dt><dd>{{ selectedSession.workerPort }}</dd><dt>{{ t('projects.detail.strategy') }}</dt><dd>{{ selectedSession.injectionStrategy }}</dd></dl></details>
          </template>
          <RouterLink :to="context.to('/memory')">{{ t('knowledge.openMemory') }}</RouterLink>
        </section>
        <details v-if="project !== 'all'"><summary>{{ t('knowledge.projectActions') }}</summary><p>{{ t('projects.archive.body', { project: projectName(project) }) }}</p><label><span>{{ t('projects.archive.inputLabel') }}: <code>{{ project }}</code></span><input v-model="archiveInput" @keydown.enter.prevent="archive" /></label><button class="btn danger" :disabled="archiveBusy || archiveInput !== project" @click="archive">{{ t('projects.actions.confirmArchive') }}</button><MutationResultNotice :result="mutationResult" :recheck-label="t('knowledge.refresh')" @recheck="context.refresh" /></details>
      </section>
      <div v-else class="empty"><h2>{{ t('knowledge.chooseProject') }}</h2><p>{{ t('knowledge.chooseProjectHelp') }}</p></div>
    </div>
  </div>
</template>

<style scoped>
.projects-page { display:grid; gap:24px; min-width:0; } h1 { font-size:var(--text-xl); margin:0 0 6px; } h2 { font-size:var(--text-lg); margin:0 0 12px; } h3 { font-size:var(--text-base); margin:0; } p { color:var(--muted); }
.project-workspace { display:grid; grid-template-columns:minmax(230px,.7fr) minmax(0,1.5fr); gap:28px; align-items:start; }
.project-list, .project-content { min-width:0; } .project-list { max-height:75vh; overflow:auto; } .project-list > label { margin-bottom:12px; }
.project-row, .session-row { width:100%; display:grid; text-align:left; gap:8px; padding:16px 12px; background:transparent; color:var(--fg); border:0; border-bottom:1px solid var(--border); cursor:pointer; }
.project-row:hover, .session-row:hover, [aria-pressed=true] { background:var(--surface-warm); } .project-row span { color:var(--muted); font-size:var(--text-xs); overflow-wrap:anywhere; } .session-row strong { display:-webkit-box; -webkit-line-clamp:2; -webkit-box-orient:vertical; overflow:hidden; font-weight:500; white-space:pre-wrap; overflow-wrap:anywhere; } .session-row span { color:var(--muted); font-size:var(--text-sm); }
.project-links, .section-head { display:flex; flex-wrap:wrap; gap:16px; align-items:center; } .section-head { justify-content:space-between; margin-bottom:8px; } .project-content > section, .project-content > details { margin-top:28px; border-top:1px solid var(--border); padding-top:18px; }
.btn { min-height:40px; padding:8px 12px; color:var(--fg); background:var(--surface); border:1px solid var(--border); border-radius:var(--r-sm); cursor:pointer; } .btn:disabled { opacity:.5; cursor:not-allowed; } .danger { color:var(--state-danger); }
label { display:grid; gap:6px; color:var(--muted); } input { width:100%; min-height:40px; padding:8px 10px; color:var(--fg); background:var(--surface); border:1px solid var(--border); border-radius:var(--r-sm); }
.state { padding:14px; border:1px solid var(--border); border-radius:var(--r-sm); } .error { color:var(--state-warn); }
dl { display:grid; grid-template-columns:140px minmax(0,1fr); gap:8px 12px; margin:16px 0; } dt { color:var(--muted); } dd { margin:0; overflow-wrap:anywhere; } .prompt { white-space:pre-wrap; overflow-wrap:anywhere; } summary { min-height:40px; padding:8px 0; cursor:pointer; color:var(--fg-2); }
@media(max-width:760px) { .project-workspace { grid-template-columns:1fr; } .project-list { max-height:35vh; } .btn, input { min-height:44px; } dl { grid-template-columns:100px minmax(0,1fr); } }
</style>
