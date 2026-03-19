<script setup lang="ts">
import { ref, computed, onMounted, onUnmounted } from 'vue'
import { useRouter } from 'vue-router'
import type { Observation, ObservationType } from '@/types'
import { TYPE_CONFIG, OBSERVATION_TYPES } from '@/types/observation'
import { fetchObservationsPaginated, fetchProjects, archiveObservations } from '@/utils/api'
import { formatRelativeTime } from '@/utils/formatters'
import Pagination from '@/components/layout/Pagination.vue'
import EmptyState from '@/components/layout/EmptyState.vue'
import Badge from '@/components/Badge.vue'
import ConfirmDialog from '@/components/layout/ConfirmDialog.vue'

const router = useRouter()

// Pagination state
const observations = ref<Observation[]>([])
const total = ref(0)
const offset = ref(0)
const loading = ref(false)
const error = ref<string | null>(null)
const PAGE_SIZE = 20

// Filter state
const currentProject = ref<string | null>(null)
const currentType = ref<ObservationType | null>(null)
const currentConcept = ref('')
const projects = ref<string[]>([])

// Bulk selection state
const selectedIds = ref<Set<number>>(new Set())
const showBatchConfirm = ref(false)
const batchAction = ref<'archive' | null>(null)
const batchProcessing = ref(false)

const allSelected = computed(() =>
  filteredObservations.value.length > 0 &&
  filteredObservations.value.every(o => selectedIds.value.has(o.id))
)

function toggleSelect(id: number, event: Event) {
  event.stopPropagation()
  const updated = new Set(selectedIds.value)
  if (updated.has(id)) {
    updated.delete(id)
  } else {
    updated.add(id)
  }
  selectedIds.value = updated
}

function toggleSelectAll() {
  if (allSelected.value) {
    selectedIds.value = new Set()
  } else {
    selectedIds.value = new Set(filteredObservations.value.map(o => o.id))
  }
}

function startBatchAction(action: 'archive') {
  batchAction.value = action
  showBatchConfirm.value = true
}

async function executeBatchAction() {
  showBatchConfirm.value = false
  if (!batchAction.value || selectedIds.value.size === 0) return

  batchProcessing.value = true
  try {
    const ids = Array.from(selectedIds.value)
    await archiveObservations(ids, 'Batch archive from dashboard')
    selectedIds.value = new Set()
    await fetchPage()
  } catch {
    // Error will show in page reload
  } finally {
    batchProcessing.value = false
    batchAction.value = null
  }
}

// Unique concepts from current page for quick filter
const availableConcepts = computed(() => {
  const conceptSet = new Set<string>()
  for (const obs of observations.value) {
    if (Array.isArray(obs.concepts)) {
      for (const c of obs.concepts) {
        conceptSet.add(c)
      }
    }
  }
  return Array.from(conceptSet).sort()
})

// Client-side filtering on paginated data
const filteredObservations = computed(() => {
  let items = observations.value
  if (currentType.value) {
    items = items.filter(o => o.type === currentType.value)
  }
  if (currentConcept.value) {
    items = items.filter(o =>
      Array.isArray(o.concepts) && o.concepts.includes(currentConcept.value)
    )
  }
  return items
})

let abortController: AbortController | null = null

async function fetchPage() {
  abortController?.abort()
  abortController = new AbortController()

  loading.value = true
  error.value = null

  try {
    const result = await fetchObservationsPaginated(
      {
        limit: PAGE_SIZE,
        offset: offset.value,
        project: currentProject.value || undefined,
      },
      abortController.signal
    )
    observations.value = result.observations || []
    total.value = result.total
  } catch (err) {
    if (err instanceof Error && err.name === 'AbortError') return
    error.value = err instanceof Error ? err.message : 'Failed to load observations'
  } finally {
    loading.value = false
  }
}

async function loadProjects() {
  try {
    projects.value = await fetchProjects()
  } catch {
    // Non-critical, ignore
  }
}

function handleOffsetUpdate(newOffset: number) {
  offset.value = newOffset
  fetchPage()
}

function setProject(project: string | null) {
  currentProject.value = project
  offset.value = 0
  fetchPage()
}

function setType(type: ObservationType | null) {
  currentType.value = currentType.value === type ? null : type
  offset.value = 0
  fetchPage()
}

function setConcept(concept: string) {
  currentConcept.value = currentConcept.value === concept ? '' : concept
  offset.value = 0
  fetchPage()
}

function navigateToDetail(id: number) {
  router.push({ name: 'observation-detail', params: { id } })
}

function getTypeConfig(type: string) {
  return TYPE_CONFIG[type as ObservationType] || TYPE_CONFIG.change
}

// Extract short project name
function shortProject(project: string): string {
  const parts = project.split('/')
  return parts[parts.length - 1] || project
}

onMounted(() => {
  fetchPage()
  loadProjects()
})

onUnmounted(() => {
  abortController?.abort()
})
</script>

<template>
  <div>
    <!-- Header -->
    <div class="flex items-center justify-between mb-6">
      <div class="flex items-center gap-3">
        <i class="fas fa-brain text-claude-400 text-xl" />
        <h1 class="text-2xl font-bold text-white">Observations</h1>
        <span v-if="total > 0" class="text-sm text-slate-500">({{ total }} total)</span>
      </div>
    </div>

    <!-- Filters Bar -->
    <div class="flex flex-wrap items-center gap-3 mb-6">
      <!-- Project filter -->
      <div class="relative">
        <select
          :value="currentProject || ''"
          @change="setProject(($event.target as HTMLSelectElement).value || null)"
          class="appearance-none pl-8 pr-8 py-2 rounded-lg bg-slate-800/50 border border-slate-700/50 text-sm text-slate-200 focus:outline-none focus:ring-2 focus:ring-claude-500/50 focus:border-claude-500 cursor-pointer"
        >
          <option value="">All Projects</option>
          <option v-for="p in projects" :key="p" :value="p">{{ shortProject(p) }}</option>
        </select>
        <i class="fas fa-folder absolute left-2.5 top-1/2 -translate-y-1/2 text-slate-500 text-xs pointer-events-none" />
        <i class="fas fa-chevron-down absolute right-2.5 top-1/2 -translate-y-1/2 text-slate-500 text-xs pointer-events-none" />
      </div>

      <!-- Type filter pills -->
      <div class="flex items-center gap-1.5">
        <button
          v-for="type in OBSERVATION_TYPES"
          :key="type"
          @click="setType(type)"
          :class="[
            'px-2.5 py-1 rounded-full text-xs font-medium border transition-colors',
            currentType === type
              ? `${getTypeConfig(type).bgClass} ${getTypeConfig(type).colorClass} ${getTypeConfig(type).borderClass}`
              : 'bg-slate-800/30 text-slate-500 border-slate-700/50 hover:text-slate-300 hover:border-slate-600',
          ]"
        >
          <i :class="['fas', getTypeConfig(type).icon, 'mr-1']" />
          {{ type }}
        </button>
      </div>

      <!-- Concept filter (if concepts exist on current page) -->
      <div v-if="availableConcepts.length > 0" class="flex items-center gap-1.5 ml-auto">
        <span class="text-xs text-slate-600">Concept:</span>
        <select
          :value="currentConcept"
          @change="setConcept(($event.target as HTMLSelectElement).value)"
          class="appearance-none pl-2 pr-6 py-1 rounded bg-slate-800/50 border border-slate-700/50 text-xs text-slate-300 focus:outline-none focus:ring-1 focus:ring-claude-500/50 cursor-pointer"
        >
          <option value="">All</option>
          <option v-for="c in availableConcepts" :key="c" :value="c">{{ c }}</option>
        </select>
      </div>
    </div>

    <!-- Batch Actions Toolbar -->
    <div
      v-if="selectedIds.size > 0"
      class="flex items-center gap-3 mb-4 p-3 rounded-lg bg-claude-500/10 border border-claude-500/30"
    >
      <span class="text-sm text-claude-300">
        <i class="fas fa-check-square mr-1" />
        {{ selectedIds.size }} selected
      </span>
      <button
        @click="startBatchAction('archive')"
        :disabled="batchProcessing"
        class="px-3 py-1 rounded-lg text-xs bg-amber-500/20 text-amber-300 border border-amber-500/30 hover:bg-amber-500/30 transition-colors disabled:opacity-50"
      >
        <i class="fas fa-archive mr-1" />
        Archive
      </button>
      <button
        @click="selectedIds = new Set()"
        class="ml-auto text-xs text-slate-400 hover:text-white transition-colors"
      >
        Clear selection
      </button>
    </div>

    <!-- Loading State -->
    <div v-if="loading && observations.length === 0" class="flex items-center justify-center py-20">
      <i class="fas fa-circle-notch fa-spin text-claude-400 text-2xl" />
    </div>

    <!-- Error State -->
    <div v-else-if="error" class="text-center py-16">
      <i class="fas fa-exclamation-triangle text-red-400 text-3xl mb-3 block" />
      <p class="text-red-400 mb-2">{{ error }}</p>
      <button
        @click="fetchPage()"
        class="text-sm text-slate-400 hover:text-white transition-colors"
      >
        Try again
      </button>
    </div>

    <!-- Empty State -->
    <EmptyState
      v-else-if="filteredObservations.length === 0 && !loading"
      icon="fa-brain"
      title="No observations found"
      :description="currentProject || currentType
        ? 'Try adjusting your filters.'
        : 'Observations will appear here as your agents work.'"
    />

    <!-- Observations List -->
    <div v-else class="space-y-2">
      <!-- Select all header -->
      <div class="flex items-center gap-2 px-4 py-1">
        <input
          type="checkbox"
          :checked="allSelected"
          @change="toggleSelectAll()"
          class="w-3.5 h-3.5 rounded border-slate-600 bg-slate-800 text-claude-500 focus:ring-claude-500/50 cursor-pointer"
        />
        <span class="text-[10px] text-slate-600 uppercase tracking-wide">Select all</span>
      </div>

      <div
        v-for="obs in filteredObservations"
        :key="obs.id"
        @click="navigateToDetail(obs.id)"
        :class="[
          'group flex items-center gap-4 p-4 rounded-xl border-2 bg-gradient-to-br cursor-pointer transition-all',
          selectedIds.has(obs.id)
            ? 'border-claude-500/40 from-claude-500/5 to-slate-900/50'
            : 'border-slate-700/50 from-slate-800/50 to-slate-900/50 hover:border-claude-500/30 hover:from-slate-800/70 hover:to-slate-900/70'
        ]"
      >
        <!-- Checkbox -->
        <input
          type="checkbox"
          :checked="selectedIds.has(obs.id)"
          @click="toggleSelect(obs.id, $event)"
          class="w-3.5 h-3.5 rounded border-slate-600 bg-slate-800 text-claude-500 focus:ring-claude-500/50 cursor-pointer flex-shrink-0"
        />

        <!-- Type icon -->
        <div
          class="w-10 h-10 flex items-center justify-center rounded-lg bg-gradient-to-br flex-shrink-0"
          :class="getTypeConfig(obs.type).gradient"
        >
          <i :class="['fas', getTypeConfig(obs.type).icon, 'text-white text-sm']" />
        </div>

        <!-- Content -->
        <div class="flex-1 min-w-0">
          <div class="flex items-center gap-2 mb-0.5">
            <h3 class="text-sm font-medium text-white truncate group-hover:text-claude-300 transition-colors">
              {{ obs.title || 'Untitled' }}
            </h3>
          </div>
          <div class="flex items-center gap-2 text-xs text-slate-500">
            <Badge
              :icon="getTypeConfig(obs.type).icon"
              :color-class="getTypeConfig(obs.type).colorClass"
              :bg-class="getTypeConfig(obs.type).bgClass"
              :border-class="getTypeConfig(obs.type).borderClass"
            >
              {{ obs.type }}
            </Badge>
            <span>{{ formatRelativeTime(obs.created_at) }}</span>
            <span v-if="obs.project" class="flex items-center gap-1">
              <span class="text-slate-600">|</span>
              <i class="fas fa-folder text-slate-600 text-[10px]" />
              <span class="text-amber-600/80 font-mono">{{ shortProject(obs.project) }}</span>
            </span>
          </div>
        </div>

        <!-- Subtitle / narrative preview -->
        <p v-if="obs.subtitle || obs.narrative" class="hidden lg:block max-w-xs text-xs text-slate-500 truncate flex-shrink-0">
          {{ obs.subtitle || obs.narrative }}
        </p>

        <!-- Score -->
        <div class="flex flex-col items-center gap-0.5 flex-shrink-0">
          <span class="text-[10px] font-mono text-slate-500">
            <i class="fas fa-chart-bar text-purple-500/60 mr-0.5" />
            {{ (obs.importance_score || 0).toFixed(2) }}
          </span>
        </div>

        <!-- Arrow -->
        <i class="fas fa-chevron-right text-slate-600 group-hover:text-claude-400 transition-colors flex-shrink-0" />
      </div>
    </div>

    <!-- Loading overlay for subsequent pages -->
    <div v-if="loading && observations.length > 0" class="flex items-center justify-center py-4">
      <i class="fas fa-circle-notch fa-spin text-claude-400" />
    </div>

    <!-- Pagination -->
    <div class="mt-6">
      <Pagination
        :total="total"
        :limit="PAGE_SIZE"
        :offset="offset"
        @update:offset="handleOffsetUpdate"
      />
    </div>

    <!-- Batch Action Confirmation -->
    <ConfirmDialog
      :show="showBatchConfirm"
      :title="`Archive ${selectedIds.size} Observations`"
      :message="`Are you sure you want to archive ${selectedIds.size} observation(s)? Archived observations can be restored.`"
      confirm-label="Archive"
      :danger="true"
      @confirm="executeBatchAction"
      @cancel="showBatchConfirm = false"
    />
  </div>
</template>
