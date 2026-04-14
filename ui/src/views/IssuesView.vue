<script setup lang="ts">
import { ref, onMounted } from 'vue'
import { useRouter } from 'vue-router'
import { useIssues } from '@/composables/useIssues'
import { createIssue, fetchTrackedProjects } from '@/utils/api'
import { formatRelativeTime } from '@/utils/formatters'
import EmptyState from '@/components/layout/EmptyState.vue'

const router = useRouter()
const { issues, total, loading, error, statusFilter, sourceProjectFilter, typeFilter, projectNames, load } = useIssues()
const myIssuesOnly = ref(false)

function toggleMyIssues() {
  myIssuesOnly.value = !myIssuesOnly.value
  sourceProjectFilter.value = myIssuesOnly.value ? 'dashboard' : ''
}

// --- New Issue modal state ---
const showNewIssue = ref(false)
const newTitle = ref('')
const newBody = ref('')
const newType = ref<'bug' | 'feature' | 'improvement' | 'task'>('task')
const newPriority = ref<'critical' | 'high' | 'medium' | 'low'>('medium')
const newTargetProject = ref('')
const trackedProjects = ref<string[]>([])
const creating = ref(false)
const createError = ref<string | null>(null)

// Restore last-used type and target project from localStorage (priority always resets to 'medium').
const STORAGE_KEY = 'engram:newIssueDefaults'

function loadDefaults() {
  try {
    const raw = localStorage.getItem(STORAGE_KEY)
    if (!raw) return
    const saved = JSON.parse(raw)
    if (saved.type) newType.value = saved.type
    if (saved.targetProject) newTargetProject.value = saved.targetProject
  } catch { /* ignore corrupt localStorage */ }
}

function saveDefaults() {
  localStorage.setItem(STORAGE_KEY, JSON.stringify({
    type: newType.value,
    targetProject: newTargetProject.value,
  }))
}

onMounted(async () => {
  load()
  trackedProjects.value = await fetchTrackedProjects()
  loadDefaults()
  // If saved target project is not in the list, fall back to first
  if (newTargetProject.value && !trackedProjects.value.includes(newTargetProject.value)) {
    newTargetProject.value = trackedProjects.value[0] || ''
  }
})

async function openNewIssue() {
  newTitle.value = ''
  newBody.value = ''
  // Keep type and targetProject from last submission (loaded from localStorage).
  // Priority always resets to 'medium' per issue #61 requirement.
  newPriority.value = 'medium'
  createError.value = null
  showNewIssue.value = true
}

async function submitNewIssue() {
  if (!newTitle.value.trim()) return
  creating.value = true
  createError.value = null
  try {
    await createIssue({
      title: newTitle.value.trim(),
      body: newBody.value.trim() || undefined,
      type: newType.value,
      priority: newPriority.value,
      target_project: newTargetProject.value,
    })
    saveDefaults()
    showNewIssue.value = false
    await load()
  } catch (err: any) {
    createError.value = err.message || 'Failed to create issue'
  } finally {
    creating.value = false
  }
}

// --- Display helpers ---
function statusColor(status: string): string {
  switch (status) {
    case 'open': return 'bg-blue-100 text-blue-800 dark:bg-blue-900 dark:text-blue-200'
    case 'acknowledged': return 'bg-yellow-100 text-yellow-800 dark:bg-yellow-900 dark:text-yellow-200'
    case 'resolved': return 'bg-green-100 text-green-800 dark:bg-green-900 dark:text-green-200'
    case 'reopened': return 'bg-red-100 text-red-800 dark:bg-red-900 dark:text-red-200'
    case 'closed': return 'bg-gray-100 text-gray-500 dark:bg-gray-800 dark:text-gray-500'
    case 'rejected': return 'bg-gray-100 text-gray-500 dark:bg-gray-800 dark:text-gray-500 line-through'
    default: return 'bg-gray-100 text-gray-800 dark:bg-gray-700 dark:text-gray-200'
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

function typeColor(type: string): string {
  switch (type) {
    case 'bug': return 'bg-red-100 text-red-700 dark:bg-red-900/40 dark:text-red-400'
    case 'feature': return 'bg-blue-100 text-blue-700 dark:bg-blue-900/40 dark:text-blue-400'
    case 'improvement': return 'bg-green-100 text-green-700 dark:bg-green-900/40 dark:text-green-400'
    case 'task': return 'bg-gray-100 text-gray-600 dark:bg-gray-700 dark:text-gray-400'
    default: return 'bg-gray-100 text-gray-600 dark:bg-gray-700 dark:text-gray-400'
  }
}

function shortProject(project: string): string {
  if (!project) return '?'
  if (projectNames.value[project]) return projectNames.value[project]
  const parts = project.split('/')
  return parts[parts.length - 1] || project
}

const issueTypes = ['', 'bug', 'feature', 'improvement', 'task'] as const
const typeLabel = (t: string) => t === '' ? 'All' : t.charAt(0).toUpperCase() + t.slice(1)
</script>

<template>
  <div class="space-y-4">
    <div class="flex items-center justify-between">
      <h1 class="text-xl font-semibold text-gray-900 dark:text-gray-100">Issues</h1>
      <div class="flex items-center gap-3">
        <span class="text-sm text-gray-500">{{ total }} total</span>
        <button
          @click="toggleMyIssues"
          :class="[
            'px-3 py-1.5 text-sm rounded-md transition-colors',
            myIssuesOnly
              ? 'bg-purple-600 text-white hover:bg-purple-500'
              : 'bg-gray-200 dark:bg-gray-700 text-gray-700 dark:text-gray-300 hover:bg-gray-300 dark:hover:bg-gray-600'
          ]"
        >
          <i class="fas fa-user mr-1" />My Issues
        </button>
        <button
          @click="openNewIssue"
          class="px-3 py-1.5 text-sm rounded-md bg-blue-600 text-white hover:bg-blue-500 transition-colors"
        >
          + New Issue
        </button>
      </div>
    </div>

    <!-- Status filters -->
    <div class="flex gap-2 flex-wrap">
      <button
        v-for="s in ['open,acknowledged,resolved,reopened', 'open', 'acknowledged', 'resolved', 'reopened', 'closed', 'rejected', '']"
        :key="s"
        @click="statusFilter = s"
        :class="[
          'px-3 py-1 text-sm rounded-full border transition-colors',
          statusFilter === s
            ? 'bg-blue-600 text-white border-blue-600'
            : 'bg-white dark:bg-gray-800 text-gray-700 dark:text-gray-300 border-gray-300 dark:border-gray-600 hover:bg-gray-50 dark:hover:bg-gray-700'
        ]"
      >
        {{ s === '' ? 'All' : s === 'open,acknowledged,resolved,reopened' ? 'Active' : s }}
      </button>
    </div>

    <!-- Type filters -->
    <div class="flex gap-2 flex-wrap">
      <button
        v-for="t in issueTypes"
        :key="t"
        @click="typeFilter = t"
        :class="[
          'px-3 py-1 text-sm rounded-full border transition-colors',
          typeFilter === t
            ? 'bg-indigo-600 text-white border-indigo-600'
            : 'bg-white dark:bg-gray-800 text-gray-700 dark:text-gray-300 border-gray-300 dark:border-gray-600 hover:bg-gray-50 dark:hover:bg-gray-700'
        ]"
      >
        {{ typeLabel(t) }}
      </button>
    </div>

    <!-- Loading -->
    <div v-if="loading" class="text-center py-8 text-gray-500">Loading issues...</div>

    <!-- Error -->
    <div v-else-if="error" class="text-center py-8 text-red-500">{{ error }}</div>

    <!-- Empty state -->
    <EmptyState
      v-else-if="issues.length === 0"
      icon="fa-circle-exclamation"
      title="No issues found"
      description="Issues are created by AI agents to communicate across projects. Use the MCP 'issues' tool to create one."
    />

    <!-- Issue list -->
    <div v-else class="space-y-2">
      <div
        v-for="issue in issues"
        :key="issue.id"
        @click="router.push(`/issues/${issue.id}`)"
        :class="[
          'p-4 rounded-lg border cursor-pointer transition-colors',
          issue.status === 'closed' || issue.status === 'rejected'
            ? 'bg-gray-50 dark:bg-gray-900 border-gray-200 dark:border-gray-800 opacity-60'
            : 'bg-white dark:bg-gray-800 border-gray-200 dark:border-gray-700 hover:border-blue-300 dark:hover:border-blue-600'
        ]"
      >
        <div class="flex items-start justify-between">
          <div class="flex-1 min-w-0">
            <div class="flex items-center gap-2 mb-1">
              <span class="text-sm text-gray-400">#{{ issue.id }}</span>
              <!-- Type badge -->
              <span v-if="issue.type" :class="['px-2 py-0.5 text-xs rounded-full', typeColor(issue.type)]">
                {{ issue.type }}
              </span>
              <span :class="['font-medium text-sm uppercase', priorityColor(issue.priority)]">
                {{ issue.priority }}
              </span>
              <span :class="['px-2 py-0.5 text-xs rounded-full', statusColor(issue.status)]">
                {{ issue.status }}
              </span>
            </div>
            <h3 class="text-sm font-medium text-gray-900 dark:text-gray-100 truncate">
              {{ issue.title }}
            </h3>
            <div class="flex items-center gap-2 mt-1 text-xs text-gray-500">
              <span>{{ shortProject(issue.source_project) }} → {{ shortProject(issue.target_project) }}</span>
              <span>·</span>
              <span>{{ formatRelativeTime(issue.created_at) }}</span>
              <span v-if="issue.comment_count" class="flex items-center gap-1">
                · {{ issue.comment_count }} comment{{ issue.comment_count === 1 ? '' : 's' }}
              </span>
            </div>
          </div>
        </div>
      </div>
    </div>
  </div>

  <!-- New Issue Modal -->
  <div v-if="showNewIssue" class="fixed inset-0 bg-black/50 flex items-center justify-center z-50">
    <div class="bg-white dark:bg-gray-800 rounded-lg p-6 max-w-lg w-full mx-4 space-y-4">
      <h2 class="text-lg font-semibold text-gray-900 dark:text-gray-100">New Issue</h2>

      <div v-if="createError" class="text-sm text-red-500">{{ createError }}</div>

      <!-- Title -->
      <div>
        <label class="block text-sm font-medium text-gray-700 dark:text-gray-300 mb-1">Title <span class="text-red-500">*</span></label>
        <input
          v-model="newTitle"
          type="text"
          placeholder="Brief description of the issue"
          class="w-full px-3 py-2 rounded border border-gray-300 dark:border-gray-600 bg-white dark:bg-gray-700 text-gray-900 dark:text-gray-100 text-sm"
        />
      </div>

      <!-- Body -->
      <div>
        <label class="block text-sm font-medium text-gray-700 dark:text-gray-300 mb-1">Description</label>
        <textarea
          v-model="newBody"
          rows="4"
          placeholder="Detailed description (optional)"
          class="w-full px-3 py-2 rounded border border-gray-300 dark:border-gray-600 bg-white dark:bg-gray-700 text-gray-900 dark:text-gray-100 text-sm resize-y"
        ></textarea>
      </div>

      <!-- Type + Priority row -->
      <div class="flex gap-3">
        <div class="flex-1">
          <label class="block text-sm font-medium text-gray-700 dark:text-gray-300 mb-1">Type</label>
          <select
            v-model="newType"
            class="w-full px-3 py-2 rounded border border-gray-300 dark:border-gray-600 bg-white dark:bg-gray-700 text-gray-900 dark:text-gray-100 text-sm"
          >
            <option value="task">Task</option>
            <option value="bug">Bug</option>
            <option value="feature">Feature</option>
            <option value="improvement">Improvement</option>
          </select>
        </div>
        <div class="flex-1">
          <label class="block text-sm font-medium text-gray-700 dark:text-gray-300 mb-1">Priority</label>
          <select
            v-model="newPriority"
            class="w-full px-3 py-2 rounded border border-gray-300 dark:border-gray-600 bg-white dark:bg-gray-700 text-gray-900 dark:text-gray-100 text-sm"
          >
            <option value="medium">Medium</option>
            <option value="low">Low</option>
            <option value="high">High</option>
            <option value="critical">Critical</option>
          </select>
        </div>
      </div>

      <!-- Target project -->
      <div v-if="trackedProjects.length > 0">
        <label class="block text-sm font-medium text-gray-700 dark:text-gray-300 mb-1">Target Project</label>
        <select
          v-model="newTargetProject"
          class="w-full px-3 py-2 rounded border border-gray-300 dark:border-gray-600 bg-white dark:bg-gray-700 text-gray-900 dark:text-gray-100 text-sm"
        >
          <option value="">— none —</option>
          <option v-for="p in trackedProjects" :key="p" :value="p">{{ p }}</option>
        </select>
      </div>

      <!-- Actions -->
      <div class="flex gap-2 justify-end pt-2">
        <button
          @click="showNewIssue = false"
          class="px-3 py-2 text-sm rounded bg-gray-200 dark:bg-gray-700 text-gray-700 dark:text-gray-300 hover:bg-gray-300 dark:hover:bg-gray-600"
        >
          Cancel
        </button>
        <button
          @click="submitNewIssue"
          :disabled="creating || !newTitle.trim()"
          class="px-4 py-2 text-sm rounded bg-blue-600 text-white hover:bg-blue-500 disabled:opacity-50"
        >
          {{ creating ? 'Creating...' : 'Create Issue' }}
        </button>
      </div>
    </div>
  </div>
</template>
