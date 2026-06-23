<script setup lang="ts">
const { t } = useI18n()
const {
  projectRows,
  sessions,
  selectedProject,
  selectedSession,
  projectsState,
  sessionsState,
  detailState,
  pending,
  error,
  refresh,
  openProject,
  openSession,
  deleteProject,
  sessionDetailGap,
  sessionStrategyGap,
  codeIntelGap,
} = useOperatorProjects()

const projectArchiveTarget = ref('')
const projectArchiveInput = ref('')
const projectArchivePending = ref('')
const projectArchiveError = ref('')

const selectedProjectRow = computed(() => projectRows.value.find((project) => project.id === selectedProject.value) || null)
const sessionCountLabel = computed(() => t('projects.sessionsCount', sessions.length))

function openProjectRow(project: string) {
  void openProject(project)
}

function openSessionRow(session: typeof sessions[number]) {
  void openSession(session)
}

function toggleArchiveProject(project: string) {
  if (projectArchivePending.value) return

  if (projectArchiveTarget.value === project) {
    cancelArchiveProject()
    return
  }

  projectArchiveTarget.value = project
  projectArchiveInput.value = ''
  projectArchiveError.value = ''
}

async function submitArchiveProject(project: string) {
  if (projectArchivePending.value || projectArchiveTarget.value !== project || projectArchiveInput.value !== project) return

  projectArchivePending.value = project
  projectArchiveError.value = ''
  const result = await deleteProject(project)
  projectArchivePending.value = ''
  if (result.kind === 'rollback') {
    projectArchiveError.value = result.error.message
    return
  }

  projectArchiveTarget.value = ''
  projectArchiveInput.value = ''
}

function cancelArchiveProject() {
  if (projectArchivePending.value) return
  projectArchiveTarget.value = ''
  projectArchiveInput.value = ''
  projectArchiveError.value = ''
}

function projectArchiveEndpoint(project: string) {
  return `/api/projects/${encodeURIComponent(project)}`
}
</script>

<template>
  <div class="projects-page">
    <header class="head">
      <div>
        <h1>{{ t('projects.title') }}</h1>
        <p>{{ t('projects.subtitle') }}</p>
      </div>
      <button class="btn" type="button" :disabled="pending" @click="refresh">{{ t('projects.actions.refresh') }}</button>
    </header>

    <div v-if="pending" class="state pending">{{ t('projects.state.pending') }}</div>
    <div v-if="error" class="state error">
      <span>{{ t('projects.state.error', { message: error }) }}</span>
      <button class="link" type="button" @click="refresh">{{ t('projects.state.retry') }}</button>
    </div>
    <div v-if="projectsState.kind === 'empty'" class="state">{{ t('projects.state.emptyProjects') }}</div>
    <div v-if="sessionsState.kind === 'empty' && selectedProject" class="state">{{ t('projects.state.emptySessions') }}</div>
    <div v-if="projectsState.kind === 'error' || sessionsState.kind === 'error' || detailState.kind === 'error'" class="state muted">
      {{ t('projects.state.source') }}:
      <code>{{ projectsState.kind === 'error' ? projectsState.evidence.endpoint : sessionsState.kind === 'error' ? sessionsState.evidence.endpoint : detailState.evidence.endpoint }}</code>
    </div>

    <section class="summary">
      <div class="metric">
        <span>{{ t('projects.summary.projects') }}</span>
        <strong>{{ projectRows.length }}</strong>
      </div>
      <div class="metric">
        <span>{{ t('projects.summary.selected') }}</span>
        <strong>{{ selectedProject || '—' }}</strong>
      </div>
      <div class="metric">
        <span>{{ t('projects.summary.sessions') }}</span>
        <strong>{{ sessions.length }}</strong>
      </div>
      <div class="metric">
        <span>{{ t('projects.summary.active') }}</span>
        <strong>{{ selectedProjectRow?.activeSessions ?? 0 }}</strong>
      </div>
    </section>

    <section class="content">
      <div class="panel">
        <div class="panel-head">
          <h2>{{ t('projects.list.title') }}</h2>
          <span>{{ t('projects.list.count', projectRows.length) }}</span>
        </div>
        <div class="rows">
          <template v-for="project in projectRows" :key="project.id">
          <EntityRow
            :data-testid="`project-row-${project.id}`"
            :open="project.id === selectedProject"
            status="live"
            :preview="project.id"
            :meta="[t('projects.meta.sessions', { count: project.sessionCount }), t('projects.meta.active', { count: project.activeSessions }), t('projects.meta.last', { value: project.lastActivity })]"
            @open="openProjectRow(project.id)"
          >
            <template #side>
              <button
                class="mini danger"
                type="button"
                :data-testid="`project-archive-open-${project.id}`"
                :aria-expanded="projectArchiveTarget === project.id"
                :aria-label="t('projects.actions.archiveProject', { project: project.id })"
                :disabled="projectArchivePending === project.id"
                @click="toggleArchiveProject(project.id)"
              >
                {{ projectArchivePending === project.id ? t('projects.actions.archiving') : t('projects.actions.archive') }}
              </button>
              <HonestyBadge cls="live" />
            </template>
          </EntityRow>
          <div
            v-if="projectArchiveTarget === project.id"
            class="archive-confirm"
            :data-testid="`project-archive-confirm-${project.id}`"
          >
            <div>
              <strong>{{ t('projects.archive.title') }}</strong>
              <p>{{ t('projects.archive.body', { project: project.id }) }}</p>
              <code>{{ t('projects.archive.endpoint', { endpoint: projectArchiveEndpoint(project.id) }) }}</code>
            </div>
            <label>
              <span>{{ t('projects.archive.inputLabel') }}</span>
              <input
                v-model="projectArchiveInput"
                type="text"
                :disabled="projectArchivePending === project.id"
                :placeholder="t('projects.archive.inputPlaceholder')"
                :data-testid="`project-archive-input-${project.id}`"
                @keydown.enter.prevent="submitArchiveProject(project.id)"
              />
            </label>
            <div class="archive-actions">
              <button class="mini" type="button" :disabled="Boolean(projectArchivePending)" @click="cancelArchiveProject">
                {{ t('projects.actions.cancel') }}
              </button>
              <button
                class="mini danger"
                type="button"
                :disabled="projectArchiveInput !== project.id || Boolean(projectArchivePending)"
                :data-testid="`project-archive-confirm-button-${project.id}`"
                @click="submitArchiveProject(project.id)"
              >
                {{ projectArchivePending === project.id ? t('projects.actions.archiving') : t('projects.actions.confirmArchive') }}
              </button>
            </div>
            <p v-if="projectArchiveError" class="archive-error">{{ t('projects.archive.error', { message: projectArchiveError }) }}</p>
          </div>
          </template>
        </div>
      </div>

      <div class="panel">
        <div class="panel-head">
          <h2>{{ t('projects.sessions.title') }}</h2>
          <span>{{ sessionCountLabel }}</span>
        </div>
        <div v-if="!sessions.length" class="empty">
          <strong>{{ t('projects.sessions.emptyTitle') }}</strong>
          <span>{{ t('projects.sessions.emptyBody') }}</span>
        </div>
        <div v-else class="sessions">
          <button
            v-for="session in sessions"
            :key="session.id"
            type="button"
            class="session-row"
            :data-testid="`project-session-${session.id}`"
            :class="{ open: selectedSession?.id === session.id }"
            @click="openSessionRow(session)"
          >
            <span class="session-id">{{ session.claudeSessionId || session.id }}</span>
            <span>{{ session.status }}</span>
            <span>{{ t('projects.meta.prompts', { count: session.promptCounter }) }}</span>
            <span>{{ session.startedAt || '—' }}</span>
          </button>
        </div>
      </div>
    </section>

    <section class="detail">
      <div class="panel-head">
        <h2>{{ t('projects.detail.title') }}</h2>
        <HonestyBadge cls="live" />
      </div>
      <div v-if="detailState.kind === 'pending'" class="state pending">{{ t('projects.detail.loading') }}</div>
      <div v-if="!selectedSession" class="empty">
        <strong>{{ t('projects.detail.emptyTitle') }}</strong>
        <span>{{ t('projects.detail.emptyBody') }}</span>
      </div>
      <div v-else class="detail-grid">
        <div><span>{{ t('projects.detail.project') }}</span><strong>{{ selectedSession.project }}</strong></div>
        <div><span>{{ t('projects.detail.status') }}</span><strong>{{ selectedSession.status }}</strong></div>
        <div><span>{{ t('projects.detail.prompts') }}</span><strong>{{ selectedSession.promptCounter }}</strong></div>
        <div><span>{{ t('projects.detail.worker') }}</span><strong>{{ selectedSession.workerPort }}</strong></div>
        <div><span>{{ t('projects.detail.strategy') }}</span><strong>{{ selectedSession.injectionStrategy }}</strong></div>
        <div><span>{{ t('projects.detail.outcome') }}</span><strong>{{ selectedSession.outcome }}</strong></div>
      </div>
      <div class="gaps">
        <div class="gap">
          <HonestyBadge cls="mustbuild" :evidence="sessionDetailGap.evidence.endpoint" />
          <span>{{ t('projects.gaps.transcript') }}</span>
        </div>
        <div class="gap">
          <HonestyBadge cls="mustbuild" :evidence="sessionStrategyGap.evidence.endpoint" />
          <span>{{ t('projects.gaps.strategy') }}</span>
        </div>
        <div class="gap">
          <HonestyBadge cls="mustbuild" :evidence="codeIntelGap.evidence.endpoint" />
          <span>{{ t('projects.gaps.codeIntel') }}</span>
        </div>
      </div>
    </section>
  </div>
</template>

<style scoped>
.projects-page { display:grid; gap:16px; }
.head { display:flex; align-items:flex-start; justify-content:space-between; gap:16px; }
.head h1 { margin:0 0 4px; font-size:var(--text-xl); font-weight:700; }
.head p { margin:0; font-size:var(--text-sm); color:var(--muted); }
.btn, .mini, .link {
  border:1px solid var(--border);
  border-radius:var(--r-sm);
  background:var(--surface);
  color:var(--fg);
  font:inherit;
  cursor:pointer;
}
.btn { padding:9px 14px; font-weight:700; }
.btn:disabled { opacity:.55; cursor:wait; }
.mini { padding:5px 9px; font-size:var(--text-xs); }
.mini.danger { border-color:color-mix(in oklab,var(--state-warn),transparent 45%); color:var(--state-warn); }
.mini:disabled { opacity:.55; cursor:not-allowed; }
.link { padding:3px 8px; color:var(--accent); }
.state { display:flex; align-items:center; gap:10px; border:1px solid var(--border); border-radius:var(--r-md); background:var(--surface); padding:10px 12px; font-size:var(--text-sm); color:var(--fg-2); }
.state.pending { border-color:color-mix(in oklab,var(--accent),transparent 55%); }
.state.error { border-color:color-mix(in oklab,var(--state-warn),transparent 45%); color:var(--state-warn); }
.state.muted { color:var(--muted); }
.summary { display:grid; grid-template-columns:repeat(auto-fit,minmax(150px,1fr)); gap:12px; }
.metric, .panel, .detail {
  border:1px solid var(--border);
  border-radius:var(--r-md);
  background:var(--surface);
}
.metric { padding:14px 16px; }
.metric span { display:block; color:var(--muted); font-size:var(--text-xs); text-transform:uppercase; letter-spacing:.04em; }
.metric strong { display:block; margin-top:7px; font-family:var(--font-mono); font-size:var(--text-2xl); color:var(--fg); overflow:hidden; text-overflow:ellipsis; }
.content { display:grid; grid-template-columns:minmax(260px,.9fr) minmax(320px,1.1fr); gap:16px; align-items:start; }
.panel, .detail { overflow:hidden; }
.panel-head { display:flex; align-items:center; justify-content:space-between; gap:12px; padding:14px 16px; border-bottom:1px solid var(--border-soft); }
.panel-head h2 { margin:0; font-size:var(--text-sm); font-weight:800; }
.panel-head span { color:var(--muted); font-size:var(--text-xs); }
.rows { max-height:440px; overflow:auto; }
.archive-confirm {
  display:grid;
  gap:12px;
  padding:14px;
  border-bottom:1px solid var(--border-soft);
  background:color-mix(in oklab,var(--state-warn),transparent 92%);
}
.archive-confirm strong { display:block; margin-bottom:4px; color:var(--fg); }
.archive-confirm p { margin:0; color:var(--fg-2); font-size:var(--text-xs); }
.archive-confirm code { display:inline-block; margin-top:8px; color:var(--muted); font-size:var(--text-xs); }
.archive-confirm label { display:grid; gap:6px; color:var(--muted); font-size:var(--text-xs); }
.archive-confirm input {
  width:100%;
  border:1px solid var(--border);
  border-radius:var(--r-sm);
  background:var(--bg);
  color:var(--fg);
  padding:8px 10px;
  font:inherit;
}
.archive-actions { display:flex; flex-wrap:wrap; gap:8px; justify-content:flex-end; }
.archive-error { color:var(--state-warn); }
.sessions { display:grid; }
.session-row { display:grid; grid-template-columns:minmax(0,1.8fr) .7fr .7fr 1fr; gap:12px; align-items:center; width:100%; border:0; border-bottom:1px solid var(--border-soft); background:transparent; color:var(--fg-2); padding:12px 16px; text-align:left; cursor:pointer; }
.session-row:hover, .session-row.open { background:var(--surface-warm); color:var(--fg); }
.session-row.open { box-shadow:inset 3px 0 0 var(--accent); }
.session-id { font-family:var(--font-mono); overflow:hidden; text-overflow:ellipsis; white-space:nowrap; }
.empty { display:grid; gap:5px; padding:24px 16px; color:var(--muted); text-align:center; }
.empty strong { color:var(--fg); }
.detail-grid { display:grid; grid-template-columns:repeat(auto-fit,minmax(180px,1fr)); gap:10px; padding:16px; }
.detail-grid div { border:1px solid var(--border-soft); border-radius:var(--r-sm); padding:10px; }
.detail-grid span { display:block; color:var(--muted); font-size:var(--text-xs); text-transform:uppercase; letter-spacing:.04em; }
.detail-grid strong { display:block; margin-top:6px; color:var(--fg); font-family:var(--font-mono); overflow:hidden; text-overflow:ellipsis; }
.gaps { display:grid; gap:10px; padding:0 16px 16px; }
.gap { display:flex; align-items:center; justify-content:space-between; gap:12px; border:1px dashed var(--border); border-radius:var(--r-sm); padding:10px 12px; color:var(--muted); font-size:var(--text-xs); }
@media (max-width: 860px) {
  .head { display:grid; }
  .content { grid-template-columns:1fr; }
  .session-row { grid-template-columns:1fr; }
}
</style>
