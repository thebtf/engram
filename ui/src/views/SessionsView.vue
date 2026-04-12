<script setup lang="ts">
import { useRouter } from 'vue-router'
import { useSessions } from '@/composables/useSessions'
import { formatRelativeTime } from '@/utils/formatters'
import EmptyState from '@/components/layout/EmptyState.vue'

const router = useRouter()

const {
  sessions,
  totalSessions,
  projects,
  loading,
  error,
  searchQuery,
  filterProject,
  filterFrom,
  filterTo,
  loadSessions,
  search,
} = useSessions()

function shortProject(project: string): string {
  const parts = project.split('/')
  return parts[parts.length - 1] || project
}

function handleSearch() {
  search()
}

function setProject(project: string) {
  filterProject.value = project
  search()
}
</script>

<template>
  <div>
    <!-- Header -->
    <div class="flex items-center justify-between mb-6">
      <div class="flex items-center gap-3">
        <i class="fas fa-clock-rotate-left text-claude-400 text-xl" />
        <h1 class="text-2xl font-bold text-white">Sessions</h1>
        <span v-if="totalSessions > 0" class="text-sm text-slate-500">({{ totalSessions }})</span>
      </div>
      <button
        @click="loadSessions()"
        :disabled="loading"
        class="px-3 py-1.5 rounded-lg text-sm bg-slate-800/50 border border-slate-700/50 text-slate-300 hover:text-white hover:border-claude-500/50 transition-colors disabled:opacity-50"
      >
        <i :class="['fas fa-sync-alt mr-1.5', loading && 'fa-spin']" />
        Refresh
      </button>
    </div>

    <!-- Filters -->
    <div class="flex flex-wrap items-center gap-3 mb-6">
      <!-- Search -->
      <div class="relative flex-1 max-w-sm">
        <i class="fas fa-search absolute left-3 top-1/2 -translate-y-1/2 text-slate-600 text-xs" />
        <input
          v-model="searchQuery"
          type="text"
          placeholder="Search across transcripts..."
          class="w-full pl-9 pr-3 py-2 rounded-lg bg-slate-800/50 border border-slate-700/50 text-sm text-slate-200 placeholder-slate-600 focus:outline-none focus:ring-2 focus:ring-claude-500/50"
          @keydown.enter="handleSearch"
        />
      </div>

      <!-- Project filter -->
      <div class="relative">
        <select
          :value="filterProject"
          @change="setProject(($event.target as HTMLSelectElement).value)"
          class="appearance-none pl-8 pr-8 py-2 rounded-lg bg-slate-800/50 border border-slate-700/50 text-sm text-slate-200 focus:outline-none focus:ring-2 focus:ring-claude-500/50 cursor-pointer"
        >
          <option value="">All Projects</option>
          <option v-for="p in projects" :key="p" :value="p">{{ shortProject(p) }}</option>
        </select>
        <i class="fas fa-folder absolute left-2.5 top-1/2 -translate-y-1/2 text-slate-500 text-xs pointer-events-none" />
        <i class="fas fa-chevron-down absolute right-2.5 top-1/2 -translate-y-1/2 text-slate-500 text-xs pointer-events-none" />
      </div>

      <!-- Date range -->
      <input
        v-model="filterFrom"
        type="date"
        class="px-3 py-2 rounded-lg bg-slate-800/50 border border-slate-700/50 text-sm text-slate-200 focus:outline-none focus:ring-1 focus:ring-claude-500/50"
        @change="loadSessions()"
      />
      <span class="text-slate-600 text-xs">to</span>
      <input
        v-model="filterTo"
        type="date"
        class="px-3 py-2 rounded-lg bg-slate-800/50 border border-slate-700/50 text-sm text-slate-200 focus:outline-none focus:ring-1 focus:ring-claude-500/50"
        @change="loadSessions()"
      />
    </div>

    <!-- Loading -->
    <div v-if="loading && sessions.length === 0" class="flex items-center justify-center py-20">
      <i class="fas fa-circle-notch fa-spin text-claude-400 text-2xl" />
    </div>

    <!-- Error -->
    <div v-else-if="error" class="text-center py-16">
      <i class="fas fa-exclamation-triangle text-red-400 text-3xl mb-3 block" />
      <p class="text-red-400 mb-2">{{ error }}</p>
      <button @click="loadSessions()" class="text-sm text-slate-400 hover:text-white transition-colors">
        Try again
      </button>
    </div>

    <!-- Empty State -->
    <EmptyState
      v-else-if="sessions.length === 0 && !loading"
      icon="fa-clock-rotate-left"
      title="No sessions found"
      :description="searchQuery || filterProject
        ? 'Try adjusting your filters.'
        : 'Sessions will appear here as agents connect.'"
    />

    <!-- Sessions List -->
    <div v-else class="space-y-2">
      <div
        v-for="session in sessions"
        :key="session.id"
        class="p-4 rounded-xl border-2 border-slate-700/50 bg-gradient-to-br from-slate-800/50 to-slate-900/50 cursor-pointer hover:border-claude-500/50 transition-colors"
        @click="router.push('/sessions/' + session.id)"
      >
        <div class="flex items-center justify-between">
          <div class="flex-1 min-w-0">
            <div class="flex items-center gap-2 mb-1">
              <i class="fas fa-desktop text-slate-500 text-sm" />
              <h3 class="text-sm font-medium text-white truncate">{{ session.workstation }}</h3>
              <span v-if="session.project" class="flex items-center gap-1 text-xs text-slate-500">
                <i class="fas fa-folder text-slate-600 text-[10px]" />
                <span class="text-amber-600/80 font-mono">{{ shortProject(session.project) }}</span>
              </span>
            </div>
            <div class="flex items-center gap-3 text-xs text-slate-500">
              <span>
                <i class="fas fa-calendar text-slate-600 mr-1" />
                {{ session.date }}
              </span>
              <span>
                <i class="fas fa-comments text-slate-600 mr-1" />
                {{ session.message_count }} messages
              </span>
              <span>{{ formatRelativeTime(session.created_at) }}</span>
            </div>
          </div>

          <code class="text-[10px] font-mono text-slate-600 flex-shrink-0">
            {{ session.id.slice(0, 8) }}
          </code>
        </div>
      </div>
    </div>
  </div>
</template>
