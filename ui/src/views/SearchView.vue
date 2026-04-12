<script setup lang="ts">
import { ref, onMounted, watch } from 'vue'
import { useRoute } from 'vue-router'
import { useSearch } from '@/composables'
import { fetchProjects } from '@/utils/api'
import SearchBar from '@/components/search/SearchBar.vue'
import SearchResults from '@/components/search/SearchResults.vue'

const route = useRoute()
const { query, project, results, loading, error, decisionMode, intent, search } = useSearch()

const projects = ref<string[]>([])

// Load projects for filter
async function loadProjects() {
  try {
    projects.value = await fetchProjects()
  } catch {
    // Non-critical
  }
}

function handleSearch(q: string) {
  query.value = q
  search()
}

function setProject(p: string | null) {
  project.value = p || ''
  if (query.value) search()
}

function toggleDecisionMode() {
  decisionMode.value = !decisionMode.value
  if (query.value) search()
}

function shortProject(p: string): string {
  const parts = p.split('/')
  return parts[parts.length - 1] || p
}

// Initialize from URL query params
onMounted(() => {
  loadProjects()
  const urlQuery = route.query.q as string
  if (urlQuery) {
    query.value = urlQuery
    search()
  }
})

// Watch for URL changes (e.g., from header search)
watch(() => route.query.q, (newQ) => {
  if (newQ && typeof newQ === 'string' && newQ !== query.value) {
    query.value = newQ
    search()
  }
})
</script>

<template>
  <div>
    <!-- Header -->
    <div class="flex items-center gap-3 mb-6">
      <i class="fas fa-magnifying-glass text-claude-400 text-xl" />
      <h1 class="text-2xl font-bold text-white">Search</h1>
    </div>

    <!-- Search Controls -->
    <div class="space-y-4 mb-8">
      <!-- Search bar -->
      <SearchBar
        v-model="query"
        placeholder="Search observations with semantic search..."
        @submit="handleSearch"
      />

      <!-- Filters row -->
      <div class="flex items-center gap-3 flex-wrap">
        <!-- Project filter -->
        <div class="relative">
          <select
            :value="project"
            @change="setProject(($event.target as HTMLSelectElement).value || null)"
            class="appearance-none pl-8 pr-8 py-1.5 rounded-lg bg-slate-800/50 border border-slate-700/50 text-sm text-slate-200 focus:outline-none focus:ring-1 focus:ring-claude-500/50 cursor-pointer"
          >
            <option value="">All Projects</option>
            <option v-for="p in projects" :key="p" :value="p">{{ shortProject(p) }}</option>
          </select>
          <i class="fas fa-folder absolute left-2.5 top-1/2 -translate-y-1/2 text-slate-500 text-xs pointer-events-none" />
          <i class="fas fa-chevron-down absolute right-2.5 top-1/2 -translate-y-1/2 text-slate-500 text-xs pointer-events-none" />
        </div>

        <!-- Decision mode toggle -->
        <button
          @click="toggleDecisionMode"
          :class="[
            'flex items-center gap-2 px-3 py-1.5 rounded-lg text-sm border transition-colors',
            decisionMode
              ? 'bg-yellow-500/20 text-yellow-300 border-yellow-500/50'
              : 'bg-slate-800/50 text-slate-400 border-slate-700/50 hover:text-slate-200 hover:border-slate-600',
          ]"
        >
          <i class="fas fa-scale-balanced" />
          Decision Mode
        </button>

        <!-- Decision mode help -->
        <span v-if="decisionMode" class="text-xs text-yellow-400/60">
          Searches decisions with rejected alternatives. Project required.
        </span>
      </div>
    </div>

    <!-- Error -->
    <div v-if="error" class="mb-4 p-3 rounded-lg bg-red-500/10 border border-red-500/30 text-sm text-red-400">
      <i class="fas fa-exclamation-triangle mr-2" />
      {{ error }}
    </div>

    <!-- Results -->
    <SearchResults
      :results="results"
      :loading="loading"
      :query="query"
      :decision-mode="decisionMode"
      :intent="intent"
    />

    <!-- Initial state -->
    <div v-if="!query && !loading && results.length === 0" class="text-center py-16">
      <i class="fas fa-magnifying-glass text-4xl text-slate-700 mb-4 block" />
      <h3 class="text-lg font-medium text-slate-400 mb-1">Semantic Search</h3>
      <p class="text-sm text-slate-500 max-w-md mx-auto">
        Search across all observations using hybrid vector + full-text search.
        Toggle Decision Mode to find architectural decisions with rejected alternatives.
      </p>
      <p class="text-xs text-slate-600 mt-4">
        Press <kbd class="px-1.5 py-0.5 bg-slate-800 border border-slate-700 rounded text-[10px] font-mono">/</kbd> to focus search
      </p>
    </div>
  </div>
</template>
