<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useMutation, useQueryClient } from '@tanstack/vue-query'
import { createIssue, type Issue } from '~/composables/useOperatorApi'
import { acknowledgeIssues } from '~/composables/useOperatorIssueActions'
import { useIssuesQuery } from '~/composables/useOperatorQueries'
import { useProjectCatalog } from '~/composables/useProjectCatalog'
import ProjectCatalogStatus from '~/components/ProjectCatalogStatus.vue'
import { useOperatorI18n } from '~/composables/useOperatorI18n'
import { formatRelativeTime } from '~/utils/formatters'

const queryClient = useQueryClient()

const { t } = useOperatorI18n()
const { projects: trackedProjects, unresolved, selectedProject: projectFilter, selectionPending, selectionError, ready, unresolvedReference, projectName } = useProjectCatalog('all')
const statusFilter = ref('open,acknowledged,resolved,reopened')
const typeFilter = ref('')
const sourceProjectFilter = ref('')
const myIssuesOnly = ref(false)
const sourceAgentFilter = computed(() => myIssuesOnly.value ? 'human' : '')

const issuesQuery = useIssuesQuery({
  project: projectFilter,
  ready,
  status: statusFilter,
  type: typeFilter,
  sourceProject: sourceProjectFilter,
  sourceAgent: sourceAgentFilter,
})


const newTitle = ref('')
const newBody = ref('')
const newTargetProject = ref('')
const newType = ref<'bug' | 'feature' | 'improvement' | 'task'>('task')
const newPriority = ref<'critical' | 'high' | 'medium' | 'low'>('medium')
const createError = ref('')

const issues = computed(() => issuesQuery.data.value?.issues ?? [])
const total = computed(() => issuesQuery.data.value?.total ?? 0)
const selectedIssueIds = ref<Set<number>>(new Set())
const bulkActionError = ref('')
const bulkActionMessage = ref('')

const selectableIssueIds = computed(() =>
  issues.value
    .filter((issue) => issue.status === 'open')
    .map((issue) => issue.id),
)
const selectedOpenIds = computed(() =>
  selectableIssueIds.value.filter((id) => selectedIssueIds.value.has(id)),
)
const allOpenSelected = computed(() =>
  selectableIssueIds.value.length > 0 && selectedOpenIds.value.length === selectableIssueIds.value.length,
)

const createIssueMutation = useMutation({
  mutationFn: () =>
    createIssue({
      title: newTitle.value.trim(),
      body: newBody.value.trim() || undefined,
      type: newType.value,
      priority: newPriority.value,
      target_project: newTargetProject.value,
      source_project: projectFilter.value,
    }),
  onSuccess: async () => {
    newTitle.value = ''
    newBody.value = ''
    newType.value = 'task'
    newPriority.value = 'medium'
    createError.value = ''
    await queryClient.invalidateQueries({ queryKey: ['issues'] })
  },
  onError: (error) => {
    createError.value = error instanceof Error ? error.message : 'Не удалось создать issue'
  },
})

const bulkAcknowledgeMutation = useMutation({
  mutationFn: () => acknowledgeIssues(selectedOpenIds.value, projectFilter.value),
  onSuccess: async (result) => {
    selectedIssueIds.value = new Set()
    bulkActionError.value = ''
    bulkActionMessage.value = `Acknowledged ${result.acknowledged} open issue(s).`
    await queryClient.invalidateQueries({ queryKey: ['issues'] })
  },
  onError: (error) => {
    bulkActionMessage.value = ''
    bulkActionError.value = error instanceof Error ? error.message : 'Не удалось выполнить bulk acknowledge'
  },
})

watch(issues, (currentIssues) => {
  const visibleIds = new Set(currentIssues.map((issue) => issue.id))
  selectedIssueIds.value = new Set([...selectedIssueIds.value].filter((id) => visibleIds.has(id)))
})

function toggleMyIssues() {
  if (!ready.value || !trackedProjects.value.some(project => project.project_id === projectFilter.value)) {
    createError.value = t('projectCatalog.selectionRequired')
    return
  }
  myIssuesOnly.value = !myIssuesOnly.value
  sourceProjectFilter.value = myIssuesOnly.value ? projectFilter.value : ''
}
watch(projectFilter, project => {
  if (myIssuesOnly.value) {
    if (!trackedProjects.value.some(item => item.project_id === project)) myIssuesOnly.value = false
    sourceProjectFilter.value = myIssuesOnly.value ? project : ''
  }
})

function submitIssue() {
  if (!ready.value || !trackedProjects.value.some(project => project.project_id === projectFilter.value) || !newTitle.value.trim() || !trackedProjects.value.some(project => project.project_id === newTargetProject.value)) {
    createError.value = 'Choose a canonical source project, title and target project.'
    return
  }

  createIssueMutation.mutate()
}

function canSelectIssue(issue: Issue): boolean {
  return issue.status === 'open'
}

function onIssueCheckboxChange(id: number, event: Event) {
  const checked = event.target instanceof HTMLInputElement ? event.target.checked : false
  const next = new Set(selectedIssueIds.value)
  if (checked) {
    next.add(id)
  } else {
    next.delete(id)
  }
  selectedIssueIds.value = next
  bulkActionError.value = ''
  bulkActionMessage.value = ''
}

function toggleAllOpenIssues() {
  const next = new Set(selectedIssueIds.value)
  if (allOpenSelected.value) {
    for (const id of selectableIssueIds.value) next.delete(id)
  } else {
    for (const id of selectableIssueIds.value) next.add(id)
  }
  selectedIssueIds.value = next
  bulkActionError.value = ''
  bulkActionMessage.value = ''
}

function submitBulkAcknowledge() {
  if (!ready.value || !trackedProjects.value.some(project => project.project_id === projectFilter.value)) {
    bulkActionError.value = t('projectCatalog.selectionRequired')
    return
  }
  if (selectedOpenIds.value.length === 0) {
    bulkActionError.value = 'Bulk acknowledge поддержан только для выбранных open issues.'
    bulkActionMessage.value = ''
    return
  }
  bulkAcknowledgeMutation.mutate()
}

function statusBadgeClass(status: string): string {
  switch (status) {
    case 'open':
      return 'border-[color:rgba(76,141,255,0.4)] bg-[color:rgba(76,141,255,0.12)] text-[var(--accent)]'
    case 'acknowledged':
      return 'border-[color:rgba(167,139,250,0.4)] bg-[color:rgba(167,139,250,0.12)] text-[var(--must-build)]'
    case 'resolved':
      return 'border-[color:rgba(52,211,153,0.4)] bg-[color:rgba(52,211,153,0.12)] text-[var(--success)]'
    case 'reopened':
      return 'border-[color:rgba(251,191,36,0.4)] bg-[color:rgba(251,191,36,0.12)] text-[var(--warn)]'
    case 'rejected':
      return 'border-[color:rgba(248,113,113,0.4)] bg-[color:rgba(248,113,113,0.12)] text-[var(--danger)]'
    default:
      return 'border-[color:rgba(107,114,128,0.4)] bg-[color:rgba(107,114,128,0.12)] text-[var(--stale)]'
  }
}

function priorityBadgeClass(priority: string): string {
  switch (priority) {
    case 'critical':
      return 'border-[color:rgba(248,113,113,0.4)] bg-[color:rgba(248,113,113,0.12)] text-[var(--danger)]'
    case 'high':
      return 'border-[color:rgba(251,191,36,0.4)] bg-[color:rgba(251,191,36,0.12)] text-[var(--warn)]'
    case 'medium':
      return 'border-[color:rgba(76,141,255,0.4)] bg-[color:rgba(76,141,255,0.12)] text-[var(--accent)]'
    default:
      return 'border-[color:rgba(107,114,128,0.4)] bg-[color:rgba(107,114,128,0.12)] text-[var(--stale)]'
  }
}
</script>

<template>
  <section class="space-y-5">
    <ProjectCatalogStatus :records="unresolved" :pending="selectionPending" :error="selectionError" :reference="unresolvedReference" />
    <label class="block text-sm">Project context (source for human actions)
      <select v-model="projectFilter" :disabled="selectionPending" class="ml-2 rounded border p-2">
        <option value="all">{{ t('common.allProjects') }}</option>
        <option v-for="project in trackedProjects" :key="project.project_id" :value="project.project_id">{{ projectName(project.project_id) }}</option>
      </select>
    </label>
    <div class="surface-panel p-5">
      <div class="mb-4 flex flex-wrap items-center justify-between gap-3">
        <div>
          <h1 class="text-xl font-semibold">Issues</h1>
          <p class="mt-2 text-sm text-[var(--fg-2)]">
            Первый реальный issues slice в новом app-shell: list + filters + create поверх текущего Go API.
          </p>
        </div>
        <div class="mono-data text-sm text-[var(--muted)]">
          {{ total }} rows
        </div>
      </div>

      <div class="mb-4 flex flex-wrap gap-2">
        <button
          class="rounded-full border px-3 py-1 text-xs"
          :class="statusFilter === 'open,acknowledged,resolved,reopened'
            ? 'border-[var(--accent)] bg-[color:rgba(76,141,255,0.12)] text-[var(--fg)]'
            : 'border-[var(--border)] bg-[var(--surface-warm)] text-[var(--fg-2)]'"
          @click="statusFilter = 'open,acknowledged,resolved,reopened'"
        >
          active
        </button>
        <button
          class="rounded-full border px-3 py-1 text-xs"
          :class="statusFilter === 'open'
            ? 'border-[var(--accent)] bg-[color:rgba(76,141,255,0.12)] text-[var(--fg)]'
            : 'border-[var(--border)] bg-[var(--surface-warm)] text-[var(--fg-2)]'"
          @click="statusFilter = 'open'"
        >
          open
        </button>
        <button
          class="rounded-full border px-3 py-1 text-xs"
          :class="statusFilter === ''
            ? 'border-[var(--accent)] bg-[color:rgba(76,141,255,0.12)] text-[var(--fg)]'
            : 'border-[var(--border)] bg-[var(--surface-warm)] text-[var(--fg-2)]'"
          @click="statusFilter = ''"
        >
          all
        </button>
        <button
          class="rounded-full border px-3 py-1 text-xs"
          :class="typeFilter === 'bug'
            ? 'border-[var(--accent)] bg-[color:rgba(76,141,255,0.12)] text-[var(--fg)]'
            : 'border-[var(--border)] bg-[var(--surface-warm)] text-[var(--fg-2)]'"
          @click="typeFilter = typeFilter === 'bug' ? '' : 'bug'"
        >
          bug
        </button>
        <button
          class="rounded-full border px-3 py-1 text-xs"
          :class="myIssuesOnly
            ? 'border-[var(--accent)] bg-[color:rgba(76,141,255,0.12)] text-[var(--fg)]'
            : 'border-[var(--border)] bg-[var(--surface-warm)] text-[var(--fg-2)]'"
          @click="toggleMyIssues"
        >
          my issues
        </button>
      </div>

      <div class="mb-4 rounded-xl border border-[var(--border)] bg-[var(--surface-warm)] p-3">
        <div class="flex flex-wrap items-center justify-between gap-3">
          <div>
            <div class="text-sm font-semibold text-[var(--fg)]">Bulk acknowledge</div>
            <div class="mt-1 text-xs text-[var(--muted)]">
              Backed by <span class="mono-data">POST /api/issues/acknowledge</span>; server updates only issues currently in <span class="mono-data">open</span>.
            </div>
          </div>
          <div class="flex flex-wrap items-center gap-2">
            <button
              class="rounded-lg border border-[var(--border)] bg-[var(--surface)] px-3 py-2 text-xs font-semibold text-[var(--fg-2)] disabled:cursor-not-allowed disabled:opacity-60"
              :disabled="selectableIssueIds.length === 0"
              @click="toggleAllOpenIssues"
            >
              {{ allOpenSelected ? 'Clear open selection' : `Select open (${selectableIssueIds.length})` }}
            </button>
            <button
              class="rounded-lg border border-[var(--accent)] bg-[var(--accent)] px-3 py-2 text-xs font-semibold text-[var(--accent-on)] disabled:cursor-not-allowed disabled:opacity-60"
              :disabled="!ready || !trackedProjects.some(project => project.project_id === projectFilter) || selectedOpenIds.length === 0 || bulkAcknowledgeMutation.isPending.value"
              @click="submitBulkAcknowledge"
            >
              {{ bulkAcknowledgeMutation.isPending.value ? 'Acknowledging...' : `Acknowledge selected (${selectedOpenIds.length})` }}
            </button>
          </div>
        </div>
        <div v-if="bulkActionError" class="mt-3 text-sm text-[var(--danger)]">
          {{ bulkActionError }}
        </div>
        <div v-else-if="bulkActionMessage" class="mt-3 text-sm text-[var(--success)]">
          {{ bulkActionMessage }}
        </div>
      </div>

      <div class="grid gap-4 xl:grid-cols-[minmax(0,1fr),360px]">
        <div class="surface-panel-warm p-4">
          <div v-if="issuesQuery.isPending.value" class="text-sm text-[var(--muted)]">
            Загрузка issues...
          </div>
          <div v-else-if="issuesQuery.isError.value" class="text-sm text-[var(--danger)]">
            {{ issuesQuery.error.value?.message || 'Не удалось загрузить issues' }}
          </div>
          <div v-else-if="issues.length === 0" class="text-sm text-[var(--muted)]">
            Нет issues под текущими фильтрами.
          </div>
          <div v-else class="space-y-3">
            <div
              v-for="issue in issues"
              :key="issue.id"
              class="rounded-xl border border-[var(--border)] bg-[var(--surface)] p-4 transition-colors hover:border-[var(--accent)]"
            >
              <div class="flex gap-3">
                <label class="mt-0.5 flex h-6 w-6 shrink-0 items-center justify-center rounded-md border border-[var(--border)] bg-[var(--surface-warm)]">
                  <input
                    type="checkbox"
                    class="h-4 w-4 accent-[var(--accent)] disabled:cursor-not-allowed disabled:opacity-40"
                    :checked="selectedIssueIds.has(issue.id)"
                    :disabled="!canSelectIssue(issue)"
                    :title="canSelectIssue(issue) ? 'Select for bulk acknowledge' : 'Bulk acknowledge only applies to open issues'"
                    @change="onIssueCheckboxChange(issue.id, $event)"
                  />
                </label>

                <NuxtLink :to="{ path: `/issues/${issue.id}`, query: { project: projectFilter } }" class="min-w-0 flex-1">
                  <div class="mb-2 flex flex-wrap items-start justify-between gap-3">
                    <div>
                      <div class="text-sm font-semibold">#{{ issue.id }} · {{ issue.title }}</div>
                      <div class="mt-1 text-xs text-[var(--muted)]">
                        {{ projectName(issue.source_project) }} → {{ projectName(issue.target_project) }}
                      </div>
                    </div>
                    <div class="flex flex-wrap gap-2">
                      <span class="rounded-full border px-2 py-1 text-[10px] font-semibold uppercase tracking-[0.04em]" :class="statusBadgeClass(issue.status)">
                        {{ issue.status }}
                      </span>
                      <span class="rounded-full border px-2 py-1 text-[10px] font-semibold uppercase tracking-[0.04em]" :class="priorityBadgeClass(issue.priority)">
                        {{ issue.priority }}
                      </span>
                    </div>
                  </div>

                  <div class="mb-3 text-sm leading-6 text-[var(--fg-2)]">
                    {{ issue.body || 'Без описания' }}
                  </div>

                  <div class="flex flex-wrap items-center justify-between gap-3 text-xs text-[var(--muted)]">
                    <span>{{ issue.type }} · {{ issue.comment_count || 0 }} comments</span>
                    <span class="mono-data">{{ formatRelativeTime(issue.created_at) }}</span>
                  </div>
                </NuxtLink>
              </div>
            </div>
          </div>
        </div>

        <div class="surface-panel-warm p-4">
          <div class="mb-3 text-xs uppercase tracking-[0.08em] text-[var(--muted)]">
            New issue
          </div>

          <div class="space-y-3">
            <label class="block">
              <span class="mb-1 block text-xs text-[var(--muted)]">Title</span>
              <input
                v-model="newTitle"
                type="text"
                class="w-full rounded-lg border border-[var(--border)] bg-[var(--surface)] px-3 py-2 text-sm text-[var(--fg)]"
              />
            </label>

            <label class="block">
              <span class="mb-1 block text-xs text-[var(--muted)]">Description</span>
              <textarea
                v-model="newBody"
                rows="5"
                class="w-full rounded-lg border border-[var(--border)] bg-[var(--surface)] px-3 py-2 text-sm text-[var(--fg)]"
              />
            </label>

            <label class="block">
              <span class="mb-1 block text-xs text-[var(--muted)]">Target project</span>
              <select
                v-model="newTargetProject"
                class="w-full rounded-lg border border-[var(--border)] bg-[var(--surface)] px-3 py-2 text-sm text-[var(--fg)]"
              >
                <option value="">select target</option>
                <option v-for="project in trackedProjects" :key="project.project_id" :value="project.project_id">
                  {{ projectName(project.project_id) }}
                </option>
              </select>
            </label>

            <div class="grid gap-3 sm:grid-cols-2">
              <label class="block">
                <span class="mb-1 block text-xs text-[var(--muted)]">Type</span>
                <select
                  v-model="newType"
                  class="w-full rounded-lg border border-[var(--border)] bg-[var(--surface)] px-3 py-2 text-sm text-[var(--fg)]"
                >
                  <option value="task">task</option>
                  <option value="bug">bug</option>
                  <option value="feature">feature</option>
                  <option value="improvement">improvement</option>
                </select>
              </label>

              <label class="block">
                <span class="mb-1 block text-xs text-[var(--muted)]">Priority</span>
                <select
                  v-model="newPriority"
                  class="w-full rounded-lg border border-[var(--border)] bg-[var(--surface)] px-3 py-2 text-sm text-[var(--fg)]"
                >
                  <option value="medium">medium</option>
                  <option value="low">low</option>
                  <option value="high">high</option>
                  <option value="critical">critical</option>
                </select>
              </label>
            </div>

            <div v-if="createError" class="rounded-lg border border-[color:rgba(248,113,113,0.4)] bg-[color:rgba(248,113,113,0.12)] px-3 py-2 text-sm text-[var(--danger)]">
              {{ createError }}
            </div>

            <button
              class="rounded-lg border border-[var(--accent)] bg-[var(--accent)] px-4 py-2 text-sm font-semibold text-[var(--accent-on)] disabled:cursor-not-allowed disabled:opacity-60"
              :disabled="!ready || !trackedProjects.some(project => project.project_id === projectFilter) || createIssueMutation.isPending.value"
              @click="submitIssue"
            >
              {{ createIssueMutation.isPending.value ? 'Создание...' : 'Создать issue' }}
            </button>
          </div>
        </div>
      </div>
    </div>
  </section>
</template>
