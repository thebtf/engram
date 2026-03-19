<script setup lang="ts">
import { ref } from 'vue'
import { usePatterns } from '@/composables/usePatterns'
import { formatRelativeTime } from '@/utils/formatters'
import EmptyState from '@/components/layout/EmptyState.vue'
import ConfirmDialog from '@/components/layout/ConfirmDialog.vue'
import Badge from '@/components/Badge.vue'

const {
  patterns,
  loading,
  error,
  insights,
  insightLoading,
  loadPatterns,
  loadInsight,
  deprecate,
  remove,
} = usePatterns()

const expandedId = ref<number | null>(null)
const deleteTarget = ref<number | null>(null)
const showDeleteConfirm = ref(false)
const deprecateTarget = ref<number | null>(null)
const showDeprecateConfirm = ref(false)

const PATTERN_TYPE_CONFIG: Record<string, { colorClass: string; bgClass: string; borderClass: string; icon: string }> = {
  workflow: { colorClass: 'text-violet-300', bgClass: 'bg-violet-500/20', borderClass: 'border-violet-500/30', icon: 'fa-diagram-project' },
  best_practice: { colorClass: 'text-emerald-300', bgClass: 'bg-emerald-500/20', borderClass: 'border-emerald-500/30', icon: 'fa-check-circle' },
  anti_pattern: { colorClass: 'text-red-300', bgClass: 'bg-red-500/20', borderClass: 'border-red-500/30', icon: 'fa-ban' },
}

function getPatternTypeConfig(type: string) {
  return PATTERN_TYPE_CONFIG[type] || { colorClass: 'text-slate-300', bgClass: 'bg-slate-500/20', borderClass: 'border-slate-500/30', icon: 'fa-puzzle-piece' }
}

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
</script>

<template>
  <div>
    <!-- Header -->
    <div class="flex items-center justify-between mb-6">
      <div class="flex items-center gap-3">
        <i class="fas fa-puzzle-piece text-claude-400 text-xl" />
        <h1 class="text-2xl font-bold text-white">Patterns</h1>
        <span v-if="patterns.length > 0" class="text-sm text-slate-500">({{ patterns.length }})</span>
      </div>
      <button
        @click="loadPatterns()"
        :disabled="loading"
        class="px-3 py-1.5 rounded-lg text-sm bg-slate-800/50 border border-slate-700/50 text-slate-300 hover:text-white hover:border-claude-500/50 transition-colors disabled:opacity-50"
      >
        <i :class="['fas fa-sync-alt mr-1.5', loading && 'fa-spin']" />
        Refresh
      </button>
    </div>

    <!-- Loading -->
    <div v-if="loading && patterns.length === 0" class="flex items-center justify-center py-20">
      <i class="fas fa-circle-notch fa-spin text-claude-400 text-2xl" />
    </div>

    <!-- Error -->
    <div v-else-if="error" class="text-center py-16">
      <i class="fas fa-exclamation-triangle text-red-400 text-3xl mb-3 block" />
      <p class="text-red-400 mb-2">{{ error }}</p>
      <button @click="loadPatterns()" class="text-sm text-slate-400 hover:text-white transition-colors">
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

    <!-- Patterns List -->
    <div v-else class="space-y-2">
      <div
        v-for="pattern in patterns"
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
              <span>{{ pattern.occurrences }} occurrences</span>
              <span>{{ (pattern.confidence * 100).toFixed(0) }}% confidence</span>
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
          <div v-if="insightLoading[pattern.id]" class="py-4 text-center">
            <i class="fas fa-circle-notch fa-spin text-claude-400" />
          </div>
          <div v-else-if="insights[pattern.id]" class="pt-3">
            <p class="text-sm text-slate-300 whitespace-pre-wrap leading-relaxed mb-3">
              {{ insights[pattern.id].insight }}
            </p>
            <div v-if="insights[pattern.id].examples?.length" class="space-y-1">
              <span class="text-[10px] text-slate-600 uppercase">Examples:</span>
              <div
                v-for="(ex, idx) in insights[pattern.id].examples"
                :key="idx"
                class="text-xs text-slate-400 pl-3 border-l-2 border-slate-700"
              >
                {{ ex }}
              </div>
            </div>
          </div>
          <div v-else class="py-4 text-xs text-slate-600 text-center">
            No insight available
          </div>
        </div>
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
  </div>
</template>
