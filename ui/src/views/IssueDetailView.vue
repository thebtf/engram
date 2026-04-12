<script setup lang="ts">
import { ref, onMounted, computed } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { fetchIssue, updateIssue, deleteIssue, type Issue, type IssueComment } from '@/utils/api'
import { formatRelativeTime } from '@/utils/formatters'
import { renderMarkdown } from '@/composables/useMarkdown'

const route = useRoute()
const router = useRouter()

const issue = ref<Issue | null>(null)
const comments = ref<IssueComment[]>([])
const loading = ref(true)
const error = ref<string | null>(null)

// Edit mode
const editing = ref(false)
const editTitle = ref('')
const editBody = ref('')
const editPriority = ref('')

// Comment form
const newComment = ref('')
const commenting = ref(false)

// Action states
const actionLoading = ref(false)
const showDeleteConfirm = ref(false)
const rejectReason = ref('')
const showRejectDialog = ref(false)

function statusColor(status: string): string {
  switch (status) {
    case 'open': return 'bg-blue-100 text-blue-800 dark:bg-blue-900 dark:text-blue-200'
    case 'acknowledged': return 'bg-yellow-100 text-yellow-800 dark:bg-yellow-900 dark:text-yellow-200'
    case 'resolved': return 'bg-green-100 text-green-800 dark:bg-green-900 dark:text-green-200'
    case 'reopened': return 'bg-red-100 text-red-800 dark:bg-red-900 dark:text-red-200'
    case 'closed': return 'bg-gray-100 text-gray-500 dark:bg-gray-800 dark:text-gray-500'
    case 'rejected': return 'bg-gray-100 text-gray-500 dark:bg-gray-800 dark:text-gray-500'
    default: return 'bg-gray-100 text-gray-800'
  }
}

function priorityColor(priority: string): string {
  switch (priority) {
    case 'critical': return 'text-red-600 dark:text-red-400'
    case 'high': return 'text-orange-600 dark:text-orange-400'
    case 'medium': return 'text-yellow-600 dark:text-yellow-400'
    case 'low': return 'text-gray-500 dark:text-gray-400'
    default: return 'text-gray-500'
  }
}

function shortProject(project: string): string {
  if (!project) return '?'
  const parts = project.split('/')
  return parts[parts.length - 1] || project
}

const allStatuses = ['open', 'acknowledged', 'resolved', 'reopened', 'closed', 'rejected']

interface TimelineEvent {
  type: 'created' | 'acknowledged' | 'resolved' | 'reopened' | 'closed' | 'rejected' | 'comment'
  date: string
  project: string
  agent: string
  body?: string
}

const timeline = computed(() => {
  if (!issue.value) return []
  return buildTimeline(issue.value, comments.value)
})

function buildTimeline(iss: Issue, cmts: IssueComment[]): TimelineEvent[] {
  const events: TimelineEvent[] = []

  events.push({
    type: 'created',
    date: iss.created_at,
    project: iss.source_project,
    agent: iss.source_agent || '',
    body: iss.body || undefined,
  })

  if (iss.acknowledged_at) {
    events.push({ type: 'acknowledged', date: iss.acknowledged_at, project: iss.target_project, agent: '' })
  }

  for (const c of cmts) {
    events.push({ type: 'comment', date: c.created_at, project: c.author_project, agent: c.author_agent || '', body: c.body })
  }

  if (iss.resolved_at) {
    events.push({ type: 'resolved', date: iss.resolved_at, project: '', agent: '' })
  }

  if (iss.reopened_at) {
    events.push({ type: 'reopened', date: iss.reopened_at, project: '', agent: '' })
  }

  if (iss.closed_at) {
    events.push({ type: 'closed', date: iss.closed_at, project: iss.source_project, agent: '' })
  }

  return events.sort((a, b) => new Date(a.date).getTime() - new Date(b.date).getTime())
}

function dotColor(type: string): string {
  switch (type) {
    case 'created': return 'bg-blue-500'
    case 'acknowledged': return 'bg-yellow-500'
    case 'resolved': return 'bg-green-500'
    case 'reopened': return 'bg-red-500'
    case 'closed': return 'bg-emerald-600'
    case 'rejected': return 'bg-gray-500'
    default: return 'bg-gray-400'
  }
}

async function loadIssue() {
  const id = Number(route.params.id)
  if (!id) { error.value = 'Invalid issue ID'; loading.value = false; return }

  try {
    const result = await fetchIssue(id)
    issue.value = result.issue
    comments.value = result.comments || []
  } catch (err: any) {
    error.value = err.message || 'Failed to load issue'
  } finally {
    loading.value = false
  }
}

function startEdit() {
  if (!issue.value) return
  editTitle.value = issue.value.title
  editBody.value = issue.value.body
  editPriority.value = issue.value.priority
  editing.value = true
}

async function saveEdit() {
  if (!issue.value) return
  actionLoading.value = true
  try {
    const fields: Record<string, string> = {}
    if (editTitle.value !== issue.value.title) fields.title = editTitle.value
    if (editBody.value !== issue.value.body) fields.body = editBody.value
    if (editPriority.value !== issue.value.priority) fields.priority = editPriority.value
    if (Object.keys(fields).length > 0) {
      await updateIssue(issue.value.id, fields)
    }
    editing.value = false
    await loadIssue()
  } catch (err: any) {
    error.value = err.message
  } finally {
    actionLoading.value = false
  }
}

async function changeStatus(newStatus: string) {
  if (!issue.value) return
  actionLoading.value = true
  try {
    await updateIssue(issue.value.id, {
      status: newStatus,
      source_project: 'dashboard',
      source_agent: 'human',
    })
    await loadIssue()
  } catch (err: any) {
    error.value = err.message
  } finally {
    actionLoading.value = false
  }
}

async function submitComment() {
  if (!issue.value || !newComment.value.trim()) return
  commenting.value = true
  try {
    await updateIssue(issue.value.id, {
      comment: newComment.value,
      source_project: 'dashboard',
      source_agent: 'human',
    })
    newComment.value = ''
    await loadIssue()
  } catch (err: any) {
    error.value = err.message
  } finally {
    commenting.value = false
  }
}

async function confirmDelete() {
  if (!issue.value) return
  actionLoading.value = true
  try {
    await deleteIssue(issue.value.id)
    router.push('/issues')
  } catch (err: any) {
    error.value = err.message
  } finally {
    actionLoading.value = false
    showDeleteConfirm.value = false
  }
}

async function confirmReject() {
  if (!issue.value || !rejectReason.value.trim()) return
  actionLoading.value = true
  try {
    await updateIssue(issue.value.id, {
      status: 'rejected',
      comment: rejectReason.value,
      source_project: 'dashboard',
      source_agent: 'human',
    })
    showRejectDialog.value = false
    rejectReason.value = ''
    await loadIssue()
  } catch (err: any) {
    error.value = err.message
  } finally {
    actionLoading.value = false
  }
}

onMounted(loadIssue)
</script>

<template>
  <div class="space-y-4">
    <!-- Back button -->
    <button @click="router.push('/issues')" class="text-sm text-blue-600 dark:text-blue-400 hover:underline">
      ← Back to Issues
    </button>

    <div v-if="loading" class="text-center py-8 text-gray-500">Loading issue...</div>
    <div v-else-if="error" class="text-center py-8 text-red-500">{{ error }}</div>

    <template v-else-if="issue">
      <!-- Header card -->
      <div class="bg-white dark:bg-gray-800 rounded-lg border border-gray-200 dark:border-gray-700 p-4">
        <template v-if="!editing">
          <div class="flex items-start justify-between">
            <div>
              <div class="flex items-center gap-2 mb-2">
                <span class="text-gray-400 text-sm">#{{ issue.id }}</span>
                <span :class="['font-bold text-sm uppercase', priorityColor(issue.priority)]">{{ issue.priority }}</span>
                <span :class="['px-2 py-0.5 text-xs rounded-full', statusColor(issue.status)]">{{ issue.status }}</span>
              </div>
              <h1 class="text-lg font-semibold text-gray-900 dark:text-gray-100">{{ issue.title }}</h1>
              <div class="text-sm text-gray-500 mt-1">
                {{ shortProject(issue.source_project) }} → {{ shortProject(issue.target_project) }}
                · Created {{ formatRelativeTime(issue.created_at) }}
              </div>
            </div>

            <!-- Operator actions -->
            <div class="flex items-center gap-2 flex-shrink-0">
              <button @click="startEdit" class="px-2 py-1 text-xs rounded bg-gray-100 dark:bg-gray-700 text-gray-600 dark:text-gray-300 hover:bg-gray-200 dark:hover:bg-gray-600">
                <i class="fas fa-pen mr-1" /> Edit
              </button>
              <button @click="showRejectDialog = true" class="px-2 py-1 text-xs rounded bg-orange-100 dark:bg-orange-900/30 text-orange-600 dark:text-orange-400 hover:bg-orange-200">
                <i class="fas fa-ban mr-1" /> Reject
              </button>
              <button @click="showDeleteConfirm = true" class="px-2 py-1 text-xs rounded bg-red-100 dark:bg-red-900/30 text-red-600 dark:text-red-400 hover:bg-red-200">
                <i class="fas fa-trash mr-1" /> Delete
              </button>
            </div>
          </div>

          <!-- Status dropdown (operator override) -->
          <div class="mt-3 flex items-center gap-2">
            <span class="text-xs text-gray-500">Status override:</span>
            <select
              :value="issue.status"
              @change="changeStatus(($event.target as HTMLSelectElement).value)"
              :disabled="actionLoading"
              class="text-xs px-2 py-1 rounded border border-gray-300 dark:border-gray-600 bg-white dark:bg-gray-700 text-gray-700 dark:text-gray-300"
            >
              <option v-for="s in allStatuses" :key="s" :value="s">{{ s }}</option>
            </select>
          </div>
        </template>

        <!-- Edit mode -->
        <template v-else>
          <div class="space-y-3">
            <input v-model="editTitle" class="w-full px-3 py-2 rounded border border-gray-300 dark:border-gray-600 bg-white dark:bg-gray-700 text-gray-900 dark:text-gray-100 text-lg font-semibold" />
            <textarea v-model="editBody" rows="4" class="w-full px-3 py-2 rounded border border-gray-300 dark:border-gray-600 bg-white dark:bg-gray-700 text-gray-900 dark:text-gray-100 text-sm" />
            <select v-model="editPriority" class="px-3 py-2 rounded border border-gray-300 dark:border-gray-600 bg-white dark:bg-gray-700 text-sm">
              <option v-for="p in ['critical', 'high', 'medium', 'low']" :key="p" :value="p">{{ p }}</option>
            </select>
            <div class="flex gap-2">
              <button @click="saveEdit" :disabled="actionLoading" class="px-3 py-1 text-sm rounded bg-blue-600 text-white hover:bg-blue-500 disabled:opacity-50">Save</button>
              <button @click="editing = false" class="px-3 py-1 text-sm rounded bg-gray-200 dark:bg-gray-700 text-gray-700 dark:text-gray-300">Cancel</button>
            </div>
          </div>
        </template>
      </div>

      <!-- Labels -->
      <div v-if="issue.labels && issue.labels.length > 0" class="flex gap-1 flex-wrap">
        <span v-for="label in issue.labels" :key="label"
          class="px-2 py-0.5 text-xs rounded-full bg-slate-200 dark:bg-slate-700 text-slate-700 dark:text-slate-300">
          {{ label }}
        </span>
      </div>

      <!-- Comment form -->
      <div class="bg-white dark:bg-gray-800 rounded-lg border border-gray-200 dark:border-gray-700 p-4">
        <h3 class="text-sm font-medium text-gray-700 dark:text-gray-300 mb-2">Add Comment (as operator)</h3>
        <div class="flex flex-col gap-2">
          <textarea
            v-model="newComment"
            @keydown.ctrl.enter="submitComment"
            @keydown.meta.enter="submitComment"
            placeholder="Write a comment... (Ctrl+Enter to send)"
            rows="4"
            class="w-full px-3 py-2 rounded border border-gray-300 dark:border-gray-600 bg-white dark:bg-gray-700 text-gray-900 dark:text-gray-100 text-sm resize-y"
            style="min-height: 80px; max-height: 400px"
          ></textarea>
          <div class="flex justify-end">
            <button
              @click="submitComment"
              :disabled="commenting || !newComment.trim()"
              class="px-4 py-2 text-sm rounded bg-blue-600 text-white hover:bg-blue-500 disabled:opacity-50"
            >
              {{ commenting ? '...' : 'Send' }}
            </button>
          </div>
        </div>
      </div>

      <!-- Timeline -->
      <div class="space-y-3">
        <h2 class="text-sm font-medium text-gray-700 dark:text-gray-300 uppercase tracking-wide">Timeline</h2>

        <div v-for="(event, idx) in timeline" :key="idx" class="flex gap-3">
          <div class="flex flex-col items-center">
            <div :class="['w-2.5 h-2.5 rounded-full mt-1.5', dotColor(event.type)]" />
            <div v-if="idx < timeline.length - 1" class="w-px flex-1 bg-gray-200 dark:bg-gray-700" />
          </div>

          <div class="pb-4 flex-1">
            <div class="flex items-center gap-2 text-sm">
              <span class="font-medium text-gray-700 dark:text-gray-300 capitalize">{{ event.type }}</span>
              <span v-if="event.project" class="text-gray-500">by {{ shortProject(event.project) }}</span>
              <span v-if="event.agent" class="text-gray-400">({{ event.agent }})</span>
              <span class="text-gray-400 text-xs">{{ formatRelativeTime(event.date) }}</span>
            </div>
            <div v-if="event.body" class="markdown-body mt-1 text-sm text-gray-600 dark:text-gray-400 break-words" v-html="renderMarkdown(event.body)" />
          </div>
        </div>
      </div>

      <!-- Delete confirmation -->
      <div v-if="showDeleteConfirm" class="fixed inset-0 bg-black/50 flex items-center justify-center z-50">
        <div class="bg-white dark:bg-gray-800 rounded-lg p-6 max-w-sm mx-4">
          <h3 class="text-lg font-semibold text-gray-900 dark:text-gray-100 mb-2">Delete Issue #{{ issue.id }}?</h3>
          <p class="text-sm text-gray-500 mb-4">This permanently deletes the issue and all comments. Cannot be undone.</p>
          <div class="flex gap-2 justify-end">
            <button @click="showDeleteConfirm = false" class="px-3 py-2 text-sm rounded bg-gray-200 dark:bg-gray-700 text-gray-700 dark:text-gray-300">Cancel</button>
            <button @click="confirmDelete" :disabled="actionLoading" class="px-3 py-2 text-sm rounded bg-red-600 text-white hover:bg-red-500 disabled:opacity-50">Delete</button>
          </div>
        </div>
      </div>

      <!-- Reject dialog -->
      <div v-if="showRejectDialog" class="fixed inset-0 bg-black/50 flex items-center justify-center z-50">
        <div class="bg-white dark:bg-gray-800 rounded-lg p-6 max-w-sm mx-4">
          <h3 class="text-lg font-semibold text-gray-900 dark:text-gray-100 mb-2">Reject Issue #{{ issue.id }}</h3>
          <p class="text-sm text-gray-500 mb-3">Rejected issues are hidden from all agent sessions. Provide a reason:</p>
          <textarea v-model="rejectReason" rows="3" placeholder="Rejection reason (required)..."
            class="w-full px-3 py-2 rounded border border-gray-300 dark:border-gray-600 bg-white dark:bg-gray-700 text-gray-900 dark:text-gray-100 text-sm mb-4" />
          <div class="flex gap-2 justify-end">
            <button @click="showRejectDialog = false; rejectReason = ''" class="px-3 py-2 text-sm rounded bg-gray-200 dark:bg-gray-700 text-gray-700 dark:text-gray-300">Cancel</button>
            <button @click="confirmReject" :disabled="actionLoading || !rejectReason.trim()" class="px-3 py-2 text-sm rounded bg-orange-600 text-white hover:bg-orange-500 disabled:opacity-50">Reject</button>
          </div>
        </div>
      </div>
    </template>
  </div>
</template>

<style scoped>
.markdown-body :deep(code) {
  @apply bg-gray-100 dark:bg-gray-700 px-1 py-0.5 rounded text-sm font-mono;
}
.markdown-body :deep(pre) {
  @apply bg-gray-100 dark:bg-gray-700 p-3 rounded-lg overflow-x-auto my-2;
}
.markdown-body :deep(pre code) {
  @apply bg-transparent p-0;
}
.markdown-body :deep(ul),
.markdown-body :deep(ol) {
  @apply pl-5 my-1;
}
.markdown-body :deep(ul) {
  @apply list-disc;
}
.markdown-body :deep(ol) {
  @apply list-decimal;
}
.markdown-body :deep(a) {
  @apply text-blue-500 hover:underline;
}
.markdown-body :deep(p) {
  @apply my-1;
}
.markdown-body :deep(h1) {
  @apply text-xl font-bold mt-4 mb-2;
}
.markdown-body :deep(h2) {
  @apply text-lg font-bold mt-3 mb-1;
}
.markdown-body :deep(h3) {
  @apply text-base font-bold mt-2 mb-1;
}
</style>
