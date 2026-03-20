<script setup lang="ts">
import { ref, onUnmounted, watch } from 'vue'
import {
  fetchSearchAnalytics,
  fetchRecentSearches,
  fetchSearchMisses,
  fetchRetrievalStats,
  type SearchAnalytics,
  type RecentQuery,
  type SearchMiss,
  type RetrievalStatsResponse,
} from '@/utils/api'
import { safeDateFormat } from '@/utils/formatters'
import TimeRangeSelector from '@/components/TimeRangeSelector.vue'

const analytics = ref<SearchAnalytics | null>(null)
const recentQueries = ref<RecentQuery[]>([])
const searchMisses = ref<SearchMiss[]>([])
const retrievalStats = ref<RetrievalStatsResponse | null>(null)
const loading = ref(false)
const error = ref<string | null>(null)
const since = ref<string | undefined>(undefined)

let abortController: AbortController | null = null

async function loadAll() {
  abortController?.abort()
  abortController = new AbortController()

  loading.value = true
  error.value = null

  try {
    const [analyticsData, recent, misses, retrieval] = await Promise.all([
      fetchSearchAnalytics(since.value, abortController.signal).catch(() => null),
      fetchRecentSearches(20, since.value, abortController.signal).catch(() => []),
      fetchSearchMisses(abortController.signal).catch(() => []),
      fetchRetrievalStats(undefined, since.value, abortController.signal).catch(() => null),
    ])
    analytics.value = analyticsData
    recentQueries.value = recent || []
    searchMisses.value = misses || []
    retrievalStats.value = retrieval
  } catch (err) {
    if (err instanceof Error && err.name === 'AbortError') return
    error.value = err instanceof Error ? err.message : 'Failed to load analytics'
  } finally {
    loading.value = false
  }
}

function barWidth(value: number, max: number): string {
  if (max === 0) return '0%'
  return `${Math.max(2, (value / max) * 100)}%`
}

watch(since, () => {
  loadAll()
}, { immediate: true })

onUnmounted(() => {
  abortController?.abort()
})
</script>

<template>
  <div>
    <!-- Header -->
    <div class="flex items-center justify-between mb-6">
      <div class="flex items-center gap-3">
        <i class="fas fa-chart-line text-claude-400 text-xl" />
        <h1 class="text-2xl font-bold text-white">Analytics</h1>
      </div>
      <div class="flex items-center gap-3">
        <TimeRangeSelector v-model="since" />
        <button
          @click="loadAll()"
          :disabled="loading"
          class="px-3 py-1.5 rounded-lg text-sm bg-slate-800/50 border border-slate-700/50 text-slate-300 hover:text-white hover:border-claude-500/50 transition-colors disabled:opacity-50"
        >
          <i :class="['fas fa-sync-alt mr-1.5', loading && 'fa-spin']" />
          Refresh
        </button>
      </div>
    </div>

    <!-- Loading -->
    <div v-if="loading && !analytics" class="flex items-center justify-center py-20">
      <i class="fas fa-circle-notch fa-spin text-claude-400 text-2xl" />
    </div>

    <!-- Error -->
    <div v-else-if="error" class="text-center py-16">
      <i class="fas fa-exclamation-triangle text-red-400 text-3xl mb-3 block" />
      <p class="text-red-400 mb-2">{{ error }}</p>
      <button @click="loadAll()" class="text-sm text-slate-400 hover:text-white transition-colors">
        Try again
      </button>
    </div>

    <template v-else>
      <!-- Search Performance Stats -->
      <div v-if="analytics" class="grid grid-cols-2 md:grid-cols-3 gap-4 mb-6">
        <div class="p-4 rounded-xl border-2 border-slate-700/50 bg-gradient-to-br from-slate-800/50 to-slate-900/50">
          <span class="text-xs text-slate-500 block mb-1">Total Searches</span>
          <span class="text-2xl font-bold text-white font-mono">{{ analytics.total_searches }}</span>
        </div>
        <div class="p-4 rounded-xl border-2 border-slate-700/50 bg-gradient-to-br from-slate-800/50 to-slate-900/50">
          <span class="text-xs text-slate-500 block mb-1">Avg Latency</span>
          <span class="text-2xl font-bold text-white font-mono">{{ (analytics.avg_latency_ms ?? 0).toFixed(1) }}<span class="text-sm text-slate-500">ms</span></span>
        </div>
        <div class="p-4 rounded-xl border-2 border-slate-700/50 bg-gradient-to-br from-slate-800/50 to-slate-900/50">
          <span class="text-xs text-slate-500 block mb-1">Errors</span>
          <span :class="['text-2xl font-bold font-mono', analytics.search_errors > 0 ? 'text-red-400' : 'text-slate-400']">
            {{ analytics.search_errors }}
          </span>
        </div>
      </div>

      <!-- No data state -->
      <div v-else class="text-center py-12 mb-6">
        <i class="fas fa-chart-bar text-slate-600 text-3xl mb-3 block" />
        <p class="text-slate-500">No search data for selected time range</p>
      </div>

      <!-- Retrieval Stats -->
      <div class="p-4 rounded-xl border-2 border-slate-700/50 bg-slate-800/30 mb-6">
        <h2 class="text-xs text-slate-500 uppercase tracking-wide mb-3">Retrieval Stats</h2>
        <div v-if="retrievalStats" class="grid grid-cols-2 md:grid-cols-4 gap-4 text-sm">
          <div>
            <span class="text-slate-600 text-xs block">Total Requests</span>
            <span class="text-slate-300 font-mono">{{ retrievalStats.total_requests }}</span>
          </div>
          <div>
            <span class="text-slate-600 text-xs block">Observations Served</span>
            <span class="text-slate-300 font-mono">{{ retrievalStats.observations_served }}</span>
          </div>
          <div>
            <span class="text-slate-600 text-xs block">Search Requests</span>
            <span class="text-slate-300 font-mono">{{ retrievalStats.search_requests }}</span>
          </div>
          <div>
            <span class="text-slate-600 text-xs block">Context Injections</span>
            <span class="text-slate-300 font-mono">{{ retrievalStats.context_injections }}</span>
          </div>
        </div>
        <div v-else class="text-xs text-slate-600 py-4 text-center">
          No retrieval data for selected time range
        </div>
      </div>

      <div class="grid grid-cols-1 md:grid-cols-2 gap-6">
        <!-- Search Misses -->
        <div class="p-4 rounded-xl border-2 border-slate-700/50 bg-slate-800/30">
          <h2 class="text-xs text-slate-500 uppercase tracking-wide mb-3">Search Misses</h2>
          <div v-if="searchMisses.length === 0" class="text-xs text-slate-600 py-4 text-center">
            No zero-result searches recorded
          </div>
          <div v-else class="space-y-2 max-h-80 overflow-y-auto">
            <div
              v-for="(miss, idx) in searchMisses"
              :key="idx"
              class="flex items-center justify-between py-1.5 border-b border-slate-700/30 last:border-0"
            >
              <div class="flex-1 min-w-0">
                <span class="text-xs text-slate-300 truncate block">{{ miss.query }}</span>
                <span v-if="miss.project" class="text-[10px] text-slate-600">{{ miss.project }}</span>
              </div>
              <div class="flex items-center gap-3 flex-shrink-0">
                <span class="text-[10px] text-slate-500">{{ miss.frequency }}x</span>
                <span class="text-[10px] text-slate-600">{{ safeDateFormat(miss.last_seen) }}</span>
              </div>
            </div>
          </div>
        </div>

        <!-- Recent Queries -->
        <div class="p-4 rounded-xl border-2 border-slate-700/50 bg-slate-800/30">
          <h2 class="text-xs text-slate-500 uppercase tracking-wide mb-3">Recent Queries</h2>
          <div v-if="recentQueries.length === 0" class="text-xs text-slate-600 py-4 text-center">
            No recent queries
          </div>
          <div v-else class="space-y-2 max-h-80 overflow-y-auto">
            <div
              v-for="(q, idx) in recentQueries"
              :key="idx"
              class="flex items-center justify-between py-1.5 border-b border-slate-700/30 last:border-0"
            >
              <div class="flex-1 min-w-0">
                <span class="text-xs text-slate-300 truncate block">{{ q.query }}</span>
                <div class="flex items-center gap-2">
                  <span v-if="q.project" class="text-[10px] text-slate-600">{{ q.project }}</span>
                  <span v-if="q.type" class="text-[10px] text-slate-600">{{ q.type }}</span>
                </div>
              </div>
              <div class="flex items-center gap-3 flex-shrink-0">
                <span class="text-[10px] text-slate-500">{{ q.count }}x</span>
                <span class="text-[10px] text-slate-600">{{ safeDateFormat(q.last_used) }}</span>
              </div>
            </div>
          </div>
        </div>
      </div>

      <!-- Search Type Distribution -->
      <div v-if="analytics" class="mt-6 p-4 rounded-xl border-2 border-slate-700/50 bg-slate-800/30">
        <h2 class="text-xs text-slate-500 uppercase tracking-wide mb-3">Search Type Distribution</h2>
        <div class="space-y-2">
          <div class="flex items-center gap-3">
            <span class="text-xs text-slate-400 w-28">Vector Searches</span>
            <div class="flex-1 bg-slate-900/50 rounded-full h-5 overflow-hidden">
              <div
                class="h-full bg-gradient-to-r from-cyan-500 to-cyan-400 rounded-full flex items-center justify-end pr-2"
                :style="{ width: barWidth(analytics.vector_searches, analytics.total_searches) }"
              >
                <span class="text-[10px] font-mono text-white">{{ analytics.vector_searches }}</span>
              </div>
            </div>
          </div>
          <div class="flex items-center gap-3">
            <span class="text-xs text-slate-400 w-28">Filter Searches</span>
            <div class="flex-1 bg-slate-900/50 rounded-full h-5 overflow-hidden">
              <div
                class="h-full bg-gradient-to-r from-amber-500 to-amber-400 rounded-full flex items-center justify-end pr-2"
                :style="{ width: barWidth(analytics.filter_searches, analytics.total_searches) }"
              >
                <span class="text-[10px] font-mono text-white">{{ analytics.filter_searches }}</span>
              </div>
            </div>
          </div>
          <div class="flex items-center gap-3">
            <span class="text-xs text-slate-400 w-28">Coalesced</span>
            <div class="flex-1 bg-slate-900/50 rounded-full h-5 overflow-hidden">
              <div
                class="h-full bg-gradient-to-r from-green-500 to-green-400 rounded-full flex items-center justify-end pr-2"
                :style="{ width: barWidth(analytics.coalesced_requests, analytics.total_searches) }"
              >
                <span class="text-[10px] font-mono text-white">{{ analytics.coalesced_requests }}</span>
              </div>
            </div>
          </div>
        </div>
      </div>
    </template>
  </div>
</template>
