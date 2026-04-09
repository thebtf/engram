<script setup lang="ts">
import { ref, onMounted } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { fetchIssue, type Issue, type IssueComment } from '@/utils/api'
import { formatRelativeTime } from '@/utils/formatters'

const route = useRoute()
const router = useRouter()

const issue = ref<Issue | null>(null)
const comments = ref<IssueComment[]>([])
const loading = ref(true)
const error = ref<string | null>(null)

function statusColor(status: string): string {
  switch (status) {
    case 'open': return 'bg-blue-100 text-blue-800 dark:bg-blue-900 dark:text-blue-200'
    case 'acknowledged': return 'bg-yellow-100 text-yellow-800 dark:bg-yellow-900 dark:text-yellow-200'
    case 'resolved': return 'bg-green-100 text-green-800 dark:bg-green-900 dark:text-green-200'
    case 'reopened': return 'bg-red-100 text-red-800 dark:bg-red-900 dark:text-red-200'
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

interface TimelineEvent {
  type: 'created' | 'acknowledged' | 'resolved' | 'reopened' | 'comment'
  date: string
  project: string
  agent: string
  body?: string
}

function buildTimeline(issue: Issue, comments: IssueComment[]): TimelineEvent[] {
  const events: TimelineEvent[] = []

  events.push({
    type: 'created',
    date: issue.created_at,
    project: issue.source_project,
    agent: issue.source_agent || '',
    body: issue.body || undefined,
  })

  if (issue.acknowledged_at) {
    events.push({
      type: 'acknowledged',
      date: issue.acknowledged_at,
      project: issue.target_project,
      agent: '',
    })
  }

  for (const c of comments) {
    events.push({
      type: 'comment',
      date: c.created_at,
      project: c.author_project,
      agent: c.author_agent || '',
      body: c.body,
    })
  }

  if (issue.resolved_at) {
    events.push({
      type: 'resolved',
      date: issue.resolved_at,
      project: '',
      agent: '',
    })
  }

  if (issue.reopened_at) {
    events.push({
      type: 'reopened',
      date: issue.reopened_at,
      project: '',
      agent: '',
    })
  }

  return events.sort((a, b) => new Date(a.date).getTime() - new Date(b.date).getTime())
}

onMounted(async () => {
  const id = Number(route.params.id)
  if (!id) {
    error.value = 'Invalid issue ID'
    loading.value = false
    return
  }

  try {
    const result = await fetchIssue(id)
    issue.value = result.issue
    comments.value = result.comments || []
  } catch (err: any) {
    error.value = err.message || 'Failed to load issue'
  } finally {
    loading.value = false
  }
})
</script>

<template>
  <div class="space-y-4">
    <!-- Back button -->
    <button
      @click="router.push('/issues')"
      class="text-sm text-blue-600 dark:text-blue-400 hover:underline"
    >
      ← Back to Issues
    </button>

    <!-- Loading -->
    <div v-if="loading" class="text-center py-8 text-gray-500">Loading issue...</div>

    <!-- Error -->
    <div v-else-if="error" class="text-center py-8 text-red-500">{{ error }}</div>

    <!-- Issue detail -->
    <template v-else-if="issue">
      <!-- Header -->
      <div class="bg-white dark:bg-gray-800 rounded-lg border border-gray-200 dark:border-gray-700 p-4">
        <div class="flex items-center gap-2 mb-2">
          <span class="text-gray-400 text-sm">#{{ issue.id }}</span>
          <span :class="['font-bold text-sm uppercase', priorityColor(issue.priority)]">
            {{ issue.priority }}
          </span>
          <span :class="['px-2 py-0.5 text-xs rounded-full', statusColor(issue.status)]">
            {{ issue.status }}
          </span>
        </div>
        <h1 class="text-lg font-semibold text-gray-900 dark:text-gray-100">{{ issue.title }}</h1>
        <div class="text-sm text-gray-500 mt-1">
          {{ shortProject(issue.source_project) }} → {{ shortProject(issue.target_project) }}
          · Created {{ formatRelativeTime(issue.created_at) }}
        </div>
      </div>

      <!-- Timeline -->
      <div class="space-y-3">
        <h2 class="text-sm font-medium text-gray-700 dark:text-gray-300 uppercase tracking-wide">Timeline</h2>

        <div
          v-for="(event, idx) in buildTimeline(issue, comments)"
          :key="idx"
          class="flex gap-3"
        >
          <!-- Timeline dot -->
          <div class="flex flex-col items-center">
            <div :class="[
              'w-2.5 h-2.5 rounded-full mt-1.5',
              event.type === 'created' ? 'bg-blue-500' :
              event.type === 'acknowledged' ? 'bg-yellow-500' :
              event.type === 'resolved' ? 'bg-green-500' :
              event.type === 'reopened' ? 'bg-red-500' :
              'bg-gray-400'
            ]" />
            <div v-if="idx < buildTimeline(issue, comments).length - 1" class="w-px flex-1 bg-gray-200 dark:bg-gray-700" />
          </div>

          <!-- Content -->
          <div class="pb-4 flex-1">
            <div class="flex items-center gap-2 text-sm">
              <span class="font-medium text-gray-700 dark:text-gray-300 capitalize">{{ event.type }}</span>
              <span v-if="event.project" class="text-gray-500">by {{ shortProject(event.project) }}</span>
              <span v-if="event.agent" class="text-gray-400">({{ event.agent }})</span>
              <span class="text-gray-400 text-xs">{{ formatRelativeTime(event.date) }}</span>
            </div>
            <div v-if="event.body" class="mt-1 text-sm text-gray-600 dark:text-gray-400 whitespace-pre-wrap">
              {{ event.body }}
            </div>
          </div>
        </div>
      </div>
    </template>
  </div>
</template>
