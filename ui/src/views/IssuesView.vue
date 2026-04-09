<script setup lang="ts">
import { onMounted } from 'vue'
import { useRouter } from 'vue-router'
import { useIssues } from '@/composables/useIssues'
import { formatRelativeTime } from '@/utils/formatters'
import EmptyState from '@/components/layout/EmptyState.vue'

const router = useRouter()
const { issues, total, loading, error, statusFilter, load } = useIssues()

onMounted(() => {
  load()
})

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

function shortProject(project: string): string {
  if (!project) return '?'
  const parts = project.split('/')
  return parts[parts.length - 1] || project
}
</script>

<template>
  <div class="space-y-4">
    <div class="flex items-center justify-between">
      <h1 class="text-xl font-semibold text-gray-900 dark:text-gray-100">Issues</h1>
      <span class="text-sm text-gray-500">{{ total }} total</span>
    </div>

    <!-- Filters -->
    <div class="flex gap-2 flex-wrap">
      <button
        v-for="s in ['open,reopened', 'open', 'acknowledged', 'resolved', 'reopened', 'closed', 'rejected', '']"
        :key="s"
        @click="statusFilter = s"
        :class="[
          'px-3 py-1 text-sm rounded-full border transition-colors',
          statusFilter === s
            ? 'bg-blue-600 text-white border-blue-600'
            : 'bg-white dark:bg-gray-800 text-gray-700 dark:text-gray-300 border-gray-300 dark:border-gray-600 hover:bg-gray-50 dark:hover:bg-gray-700'
        ]"
      >
        {{ s === '' ? 'All' : s === 'open,reopened' ? 'Active' : s }}
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
</template>
