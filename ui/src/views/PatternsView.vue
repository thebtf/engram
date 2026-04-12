<script setup lang="ts">
import { ref, computed, watch } from 'vue'
import { usePatterns } from '@/composables/usePatterns'
import { formatRelativeTime } from '@/utils/formatters'
import { cleanupPatterns, type PatternCleanupResult } from '@/utils/api'
import EmptyState from '@/components/layout/EmptyState.vue'
import ConfirmDialog from '@/components/layout/ConfirmDialog.vue'
import Badge from '@/components/Badge.vue'
import Pagination from '@/components/layout/Pagination.vue'

const {
  patterns,
  total,
  loading,
  error,
  insights,
  insightLoading,
  loadPatterns,
  loadInsight,
  refreshInsight,
  deprecate,
  remove,
} = usePatterns()

// ── Cleanup state ────────────────────────────────────────────
const cleanupThreshold = ref(0.6)
const cleanupRunning = ref(false)
const cleanupError = ref<string | null>(null)
const cleanupPreview = ref<PatternCleanupResult | null>(null)
const cleanupResult = ref<PatternCleanupResult | null>(null)
const showCleanupConfirm = ref(false)

async function runCleanupPreview() {
  cleanupError.value = null
  cleanupPreview.value = null
  cleanupResult.value = null
  cleanupRunning.value = true
  try {
    cleanupPreview.value = await cleanupPatterns(cleanupThreshold.value, true)
  } catch (e) {
    cleanupError.value = e instanceof Error ? e.message : 'Preview failed'
  } finally {
    cleanupRunning.value = false
  }
}

async function runCleanupConfirmed() {
  showCleanupConfirm.value = false
  cleanupError.value = null
  cleanupRunning.value = true
  try {
    cleanupResult.value = await cleanupPatterns(cleanupThreshold.value, false)
    cleanupPreview.value = null
    // Reload patterns to reflect changes.
    loadPatterns({ limit: itemsPerPage.value, offset: currentOffset.value, sort: sortBy.value })
  } catch (e) {
    cleanupError.value = e instanceof Error ? e.message : 'Cleanup failed'
  } finally {
    cleanupRunning.value = false
  }
}

const expandedId = ref<number | null>(null)
const deleteTarget = ref<number | null>(null)
const showDeleteConfirm = ref(false)
const deprecateTarget = ref<number | null>(null)
const showDeprecateConfirm = ref(false)

// Server-side pagination and sort state
const itemsPerPage = ref(20)
const currentOffset = ref(0)
const sortBy = ref<'frequency' | 'confidence' | 'last_seen'>('frequency')
// Client-side search filter (filters within the loaded page)
const searchQuery = ref('')

const PATTERN_TYPE_CONFIG: Record<string, { colorClass: string; bgClass: string; borderClass: string; icon: string }> = {
  workflow: { colorClass: 'text-violet-300', bgClass: 'bg-violet-500/20', borderClass: 'border-violet-500/30', icon: 'fa-diagram-project' },
  best_practice: { colorClass: 'text-emerald-300', bgClass: 'bg-emerald-500/20', borderClass: 'border-emerald-500/30', icon: 'fa-check-circle' },
  anti_pattern: { colorClass: 'text-red-300', bgClass: 'bg-red-500/20', borderClass: 'border-red-500/30', icon: 'fa-ban' },
  bug: { colorClass: 'text-orange-300', bgClass: 'bg-orange-500/20', borderClass: 'border-orange-500/30', icon: 'fa-bug' },
  refactor: { colorClass: 'text-blue-300', bgClass: 'bg-blue-500/20', borderClass: 'border-blue-500/30', icon: 'fa-rotate' },
  architecture: { colorClass: 'text-cyan-300', bgClass: 'bg-cyan-500/20', borderClass: 'border-cyan-500/30', icon: 'fa-building' },
}

function getPatternTypeConfig(type: string) {
  return PATTERN_TYPE_CONFIG[type] || { colorClass: 'text-slate-300', bgClass: 'bg-slate-500/20', borderClass: 'border-slate-500/30', icon: 'fa-puzzle-piece' }
}

// Client-side name filter applied to the currently loaded page
const visiblePatterns = computed(() => {
  if (!searchQuery.value.trim()) return patterns.value
  const query = searchQuery.value.toLowerCase()
  return patterns.value.filter(p => p.name.toLowerCase().includes(query))
})

// Fetch from server whenever sort, page size, or offset changes
function fetchCurrentPage() {
  loadPatterns({ limit: itemsPerPage.value, offset: currentOffset.value, sort: sortBy.value })
}

// Reset to first page and reload when sort or page size changes
watch([sortBy, itemsPerPage], () => {
  currentOffset.value = 0
  fetchCurrentPage()
})

// Reload when offset changes (user clicked a pagination page)
watch(currentOffset, () => {
  fetchCurrentPage()
})

function toggleExpand(id: number) {
  if (expandedId.value === id) {
    expandedId.value = null
  } else {
    expandedId.value = id
    loadInsight(id)
  }
}

function confirmDeprecate(id: number) {
  deprecateTarget.value = id
  showDeprecateConfirm.value = true
}

async function handleDeprecate() {
  if (deprecateTarget.value === null) return
  showDeprecateConfirm.value = false
  try {
    await deprecate(deprecateTarget.value)
  } catch {
    // Error in composable
  }
  deprecateTarget.value = null
}

function confirmDelete(id: number) {
  deleteTarget.value = id
  showDeleteConfirm.value = true
}

async function handleDelete() {
  if (deleteTarget.value === null) return
  showDeleteConfirm.value = false
  try {
    await remove(deleteTarget.value)
  } catch {
    // Error in composable
  }
  deleteTarget.value = null
}

function confidencePercent(c: number): string {
  return (c * 100).toFixed(0)
}

function handleRefresh() {
  loadPatterns({ limit: itemsPerPage.value, offset: currentOffset.value, sort: sortBy.value })
}
</script>

<template>
  <div>
    <!-- Header -->
    <div class="flex items-center justify-between mb-2">
      <div class="flex items-center gap-3">
        <i class="fas fa-puzzle-piece text-claude-400 text-xl" />
        <h1 class="text-2xl font-bold text-white">Patterns</h1>
        <span v-if="total > 0" class="text-sm text-slate-500">({{ total }})</span>
      </div>
      <button
        @click="handleRefresh()"
        :disabled="loading"
        class="px-3 py-1.5 rounded-lg text-sm bg-slate-800/50 border border-slate-700/50 text-slate-300 hover:text-white hover:border-claude-500/50 transition-colors disabled:opacity-50"
      >
        <i :class="['fas fa-sync-alt mr-1.5', loading && 'fa-spin']" />
        Refresh
      </button>
    </div>

    <!-- Explanation -->
    <p class="text-sm text-slate-500 mb-4">
      Patterns are recurring themes automatically detected across your observations.
    </p>

    <!-- Controls row -->
    <div class="flex items-center gap-3 mb-4 flex-wrap">
      <!-- Search -->
      <div class="relative flex-1 min-w-[200px] max-w-sm">
        <i class="fas fa-search absolute left-3 top-1/2 -translate-y-1/2 text-slate-600 text-xs" />
        <input
          v-model="searchQuery"
          type="text"
          placeholder="Filter by name..."
          class="w-full pl-8 pr-3 py-1.5 rounded-lg bg-slate-800/50 border border-slate-700/50 text-sm text-white placeholder-slate-600 focus:outline-none focus:ring-1 focus:ring-claude-500/50"
        />
      </div>

      <!-- Sort -->
      <select
        v-model="sortBy"
        class="px-3 py-1.5 rounded-lg bg-slate-800/50 border border-slate-700/50 text-sm text-white focus:outline-none focus:ring-1 focus:ring-claude-500/50"
      >
        <option value="frequency">Sort: Frequency</option>
        <option value="confidence">Sort: Confidence</option>
        <option value="last_seen">Sort: Last Seen</option>
      </select>

      <!-- Items per page -->
      <select
        v-model.number="itemsPerPage"
        class="px-3 py-1.5 rounded-lg bg-slate-800/50 border border-slate-700/50 text-sm text-white focus:outline-none focus:ring-1 focus:ring-claude-500/50"
      >
        <option :value="20">20 / page</option>
        <option :value="50">50 / page</option>
        <option :value="100">100 / page</option>
      </select>
    </div>

    <!-- Clean Up Section -->
    <div class="mb-5 rounded-xl border border-slate-700/50 bg-slate-800/30 p-4">
      <div class="flex items-center gap-2 mb-3">
        <i class="fas fa-broom text-amber-400 text-sm" />
        <span class="text-sm font-medium text-white">Clean Up Patterns</span>
      </div>

      <!-- Threshold control -->
      <div class="flex items-center gap-3 mb-3 flex-wrap">
        <label class="text-xs text-slate-400 whitespace-nowrap">
          Confidence threshold:
        </label>
        <input
          v-model.number="cleanupThreshold"
          type="number"
          min="0"
          max="1"
          step="0.05"
          class="w-24 px-2 py-1 rounded-lg bg-slate-900/50 border border-slate-700/50 text-sm text-white focus:outline-none focus:ring-1 focus:ring-amber-500/50"
        />
        <span class="text-xs text-slate-500">Patterns below this confidence will be archived</span>
      </div>

      <!-- Preview button -->
      <button
        @click="runCleanupPreview"
        :disabled="cleanupRunning"
        class="px-3 py-1.5 rounded-lg text-sm bg-amber-500/20 border border-amber-500/30 text-amber-300 hover:text-white hover:bg-amber-500/30 transition-colors disabled:opacity-50"
      >
        <i :class="['fas mr-1.5', cleanupRunning ? 'fa-circle-notch fa-spin' : 'fa-search']" />
        Clean Up Patterns
      </button>

      <!-- Error -->
      <p v-if="cleanupError" class="mt-2 text-xs text-red-400">
        <i class="fas fa-exclamation-circle mr-1" />{{ cleanupError }}
      </p>

      <!-- Preview results -->
      <div v-if="cleanupPreview" class="mt-3 rounded-lg bg-slate-900/50 border border-slate-700/30 p-3 text-sm">
        <p class="text-slate-300 font-medium mb-2">Preview (no changes applied)</p>
        <ul class="space-y-1 text-xs text-slate-400">
          <li>
            <i class="fas fa-ghost text-orange-400 mr-1.5" />
            Orphan patterns found: <span class="text-white font-medium">{{ cleanupPreview.orphans_found }}</span>
            ({{ cleanupPreview.orphans_archived }} would be archived)
          </li>
          <li>
            <i class="fas fa-chart-line text-blue-400 mr-1.5" />
            Low-confidence patterns: <span class="text-white font-medium">{{ cleanupPreview.low_confidence_found }}</span>
            (confidence &lt; {{ cleanupThreshold }})
          </li>
        </ul>
        <button
          v-if="cleanupPreview.orphans_archived > 0 || cleanupPreview.low_confidence_found > 0"
          @click="showCleanupConfirm = true"
          :disabled="cleanupRunning"
          class="mt-3 px-3 py-1.5 rounded-lg text-sm bg-red-500/20 border border-red-500/30 text-red-300 hover:text-white hover:bg-red-500/30 transition-colors disabled:opacity-50"
        >
          <i class="fas fa-check mr-1.5" />Confirm Cleanup
        </button>
        <p v-else class="mt-2 text-xs text-emerald-400">
          <i class="fas fa-check-circle mr-1" />Nothing to clean up.
        </p>
      </div>

      <!-- Final results -->
      <div v-if="cleanupResult" class="mt-3 rounded-lg bg-emerald-900/20 border border-emerald-700/30 p-3 text-sm">
        <p class="text-emerald-300 font-medium mb-2">
          <i class="fas fa-check-circle mr-1.5" />Cleanup complete
        </p>
        <ul class="space-y-1 text-xs text-slate-400">
          <li>Orphans archived: <span class="text-white font-medium">{{ cleanupResult.orphans_archived }}</span></li>
          <li>Low-confidence archived: <span class="text-white font-medium">{{ cleanupResult.low_confidence_archived }}</span></li>
          <li>Confidence recalculated: <span class="text-white font-medium">{{ cleanupResult.confidence_recalculated }}</span></li>
        </ul>
      </div>
    </div>

    <!-- Loading -->
    <div v-if="loading && patterns.length === 0" class="flex items-center justify-center py-20">
      <i class="fas fa-circle-notch fa-spin text-claude-400 text-2xl" />
    </div>

    <!-- Error -->
    <div v-else-if="error" class="text-center py-16">
      <i class="fas fa-exclamation-triangle text-red-400 text-3xl mb-3 block" />
      <p class="text-red-400 mb-2">{{ error }}</p>
      <button @click="handleRefresh()" class="text-sm text-slate-400 hover:text-white transition-colors">
        Try again
      </button>
    </div>

    <!-- Empty State -->
    <EmptyState
      v-else-if="patterns.length === 0 && !loading"
      icon="fa-puzzle-piece"
      title="No patterns detected"
      description="Patterns will appear here as the system learns from your observations."
    />

    <!-- No search results -->
    <div v-else-if="visiblePatterns.length === 0 && searchQuery.trim()" class="text-center py-12">
      <i class="fas fa-search text-slate-600 text-2xl mb-3 block" />
      <p class="text-slate-400">No patterns match "{{ searchQuery }}"</p>
    </div>

    <!-- Patterns List -->
    <div v-else class="space-y-2">
      <div
        v-for="pattern in visiblePatterns"
        :key="pattern.id"
        class="rounded-xl border-2 border-slate-700/50 bg-gradient-to-br from-slate-800/50 to-slate-900/50 overflow-hidden"
      >
        <!-- Pattern row -->
        <div class="flex items-center gap-4 p-4">
          <div
            class="w-10 h-10 flex items-center justify-center rounded-lg flex-shrink-0"
            :class="getPatternTypeConfig(pattern.type).bgClass"
          >
            <i :class="['fas', getPatternTypeConfig(pattern.type).icon, getPatternTypeConfig(pattern.type).colorClass]" />
          </div>

          <div class="flex-1 min-w-0">
            <div class="flex items-center gap-2 mb-0.5">
              <h3 class="text-sm font-medium text-white truncate">{{ pattern.name }}</h3>
              <Badge
                :icon="getPatternTypeConfig(pattern.type).icon"
                :color-class="getPatternTypeConfig(pattern.type).colorClass"
                :bg-class="getPatternTypeConfig(pattern.type).bgClass"
                :border-class="getPatternTypeConfig(pattern.type).borderClass"
              >
                {{ pattern.type.replace(/_/g, ' ') }}
              </Badge>
            </div>
            <div class="flex items-center gap-3 text-xs text-slate-500">
              <span>{{ pattern.frequency }} occurrences</span>
              <span class="flex items-center gap-1">
                <span class="text-slate-600">Confidence:</span>
                <span class="inline-flex items-center">
                  <span
                    class="inline-block h-1.5 rounded-full bg-claude-500"
                    :style="{ width: confidencePercent(pattern.confidence) + '%', minWidth: '4px', maxWidth: '60px' }"
                  />
                  <span class="ml-1">{{ confidencePercent(pattern.confidence) }}%</span>
                </span>
              </span>
              <span>{{ formatRelativeTime(pattern.created_at) }}</span>
            </div>
          </div>

          <div class="flex items-center gap-1.5 flex-shrink-0">
            <button
              @click="toggleExpand(pattern.id)"
              class="px-2.5 py-1.5 rounded-lg text-xs bg-slate-800/50 border border-slate-700/50 text-slate-300 hover:text-white hover:border-claude-500/50 transition-colors"
            >
              <i :class="['fas mr-1', expandedId === pattern.id ? 'fa-chevron-up' : 'fa-lightbulb']" />
              {{ expandedId === pattern.id ? 'Hide' : 'Insight' }}
            </button>
            <button
              @click="confirmDeprecate(pattern.id)"
              class="px-2 py-1.5 rounded-lg text-xs text-slate-500 hover:text-amber-400 hover:bg-amber-500/10 transition-colors"
              title="Deprecate"
            >
              <i class="fas fa-archive" />
            </button>
            <button
              @click="confirmDelete(pattern.id)"
              class="px-2 py-1.5 rounded-lg text-xs text-slate-500 hover:text-red-400 hover:bg-red-500/10 transition-colors"
              title="Delete"
            >
              <i class="fas fa-trash" />
            </button>
          </div>
        </div>

        <!-- Expanded insight panel -->
        <div v-if="expandedId === pattern.id" class="px-4 pb-4 border-t border-slate-700/30">
          <!-- Loading skeleton -->
          <div v-if="insightLoading[pattern.id]" class="pt-3 space-y-2 animate-pulse">
            <div class="h-3 bg-slate-700/60 rounded w-full" />
            <div class="h-3 bg-slate-700/60 rounded w-5/6" />
            <div class="h-3 bg-slate-700/60 rounded w-4/6" />
          </div>

          <!-- Loaded state -->
          <div v-else-if="insights[pattern.id]" class="pt-3">
            <!-- LLM summary -->
            <div v-if="insights[pattern.id].summary" class="mb-3">
              <div class="flex items-center gap-2 mb-1">
                <span class="text-[10px] text-slate-600 uppercase tracking-wide">AI Summary</span>
                <span
                  class="text-[10px] px-1.5 py-0.5 rounded"
                  :class="insights[pattern.id].cached
                    ? 'bg-slate-700/50 text-slate-500'
                    : 'bg-claude-500/20 text-claude-400'"
                >
                  {{ insights[pattern.id].cached ? 'Cached' : 'Generated just now' }}
                </span>
                <button
                  @click="refreshInsight(pattern.id)"
                  class="ml-auto text-[10px] text-slate-600 hover:text-slate-400 transition-colors"
                  title="Refresh summary"
                >
                  <i class="fas fa-rotate-right" />
                </button>
              </div>
              <p class="text-sm text-slate-300 leading-relaxed">
                {{ insights[pattern.id].summary }}
              </p>
            </div>

            <!-- Unavailable state -->
            <div v-else class="mb-3 flex items-center gap-2 text-xs text-slate-500">
              <i class="fas fa-info-circle text-slate-600" />
              <span>Summary unavailable — LLM not configured or returned empty.</span>
              <button
                @click="refreshInsight(pattern.id)"
                class="ml-1 text-slate-400 hover:text-white transition-colors underline"
              >
                Retry
              </button>
            </div>

            <!-- Source observations -->
            <div v-if="insights[pattern.id].source_observations?.length" class="space-y-1">
              <span class="text-[10px] text-slate-600 uppercase tracking-wide">
                Source Observations ({{ insights[pattern.id].source_observations.length }})
              </span>
              <div
                v-for="obs in insights[pattern.id].source_observations"
                :key="obs.id"
                class="flex items-center gap-2 py-1 border-l-2 border-slate-700 pl-3"
              >
                <a
                  :href="`/#/observations/${obs.id}`"
                  class="text-xs text-slate-300 hover:text-white transition-colors truncate flex-1"
                  :title="obs.title"
                >
                  {{ obs.title }}
                </a>
                <span class="text-[10px] text-slate-600 flex-shrink-0">{{ obs.type }}</span>
                <span class="text-[10px] text-slate-600 flex-shrink-0">
                  {{ formatRelativeTime(obs.created_at) }}
                </span>
              </div>
            </div>

            <div v-else class="text-xs text-slate-600">
              No source observations linked.
            </div>
          </div>

          <!-- Initial empty state (should not appear normally) -->
          <div v-else class="py-4 text-xs text-slate-600 text-center">
            No insight available
          </div>
        </div>
      </div>

      <!-- Pagination -->
      <div class="mt-4">
        <Pagination
          :total="total"
          :limit="itemsPerPage"
          :offset="currentOffset"
          @update:offset="currentOffset = $event"
        />
      </div>
    </div>

    <!-- Deprecate Confirmation -->
    <ConfirmDialog
      :show="showDeprecateConfirm"
      title="Deprecate Pattern"
      message="This pattern will be marked as deprecated and hidden from active results."
      confirm-label="Deprecate"
      :danger="false"
      @confirm="handleDeprecate"
      @cancel="showDeprecateConfirm = false"
    />

    <!-- Delete Confirmation -->
    <ConfirmDialog
      :show="showDeleteConfirm"
      title="Delete Pattern"
      message="Are you sure you want to permanently delete this pattern? This action cannot be undone."
      confirm-label="Delete"
      :danger="true"
      @confirm="handleDelete"
      @cancel="showDeleteConfirm = false"
    />

    <!-- Cleanup Confirmation -->
    <ConfirmDialog
      :show="showCleanupConfirm"
      title="Confirm Pattern Cleanup"
      :message="`This will archive ${cleanupPreview?.orphans_archived ?? 0} fully-orphaned pattern(s) and ${cleanupPreview?.low_confidence_found ?? 0} low-confidence pattern(s). This action cannot be undone.`"
      confirm-label="Run Cleanup"
      :danger="true"
      @confirm="runCleanupConfirmed"
      @cancel="showCleanupConfirm = false"
    />
  </div>
</template>
