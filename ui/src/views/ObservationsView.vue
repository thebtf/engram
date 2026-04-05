<script setup lang="ts">
import { ref, computed, onMounted, onUnmounted } from 'vue'
import { useRouter } from 'vue-router'
import type { Observation, ObservationType } from '@/types'
import { TYPE_CONFIG, OBSERVATION_TYPES } from '@/types/observation'
import { fetchObservationsPaginated, fetchProjects, archiveObservations, updateObservationStatus, updateObservation } from '@/utils/api'
import { formatRelativeTime } from '@/utils/formatters'
import Pagination from '@/components/layout/Pagination.vue'
import EmptyState from '@/components/layout/EmptyState.vue'
import Badge from '@/components/Badge.vue'
import ConfirmDialog from '@/components/layout/ConfirmDialog.vue'

interface TagCloudItem {
  tag: string
  count: number
}

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
const currentStatus = ref<'active' | 'resolved' | null>(null)
const projects = ref<string[]>([])

// Resolve/reopen modal state
const showResolveModal = ref(false)
const resolveTargetId = ref<number | null>(null)
const resolveReason = ref('')
const resolveProcessing = ref(false)

// Bulk resolve state
const showBulkResolveModal = ref(false)
const bulkResolveReason = ref('')
const bulkResolveProcessing = ref(false)

// Bulk selection state
const selectedIds = ref<Set<number>>(new Set())
const showBatchConfirm = ref(false)
const batchAction = ref<'archive' | 'delete' | 'scope' | 'tag' | 'resolve' | null>(null)
const batchProcessing = ref(false)
const batchScopeInput = ref('')
const batchTagInput = ref('')
const showScopeInput = ref(false)
const showTagInput = ref(false)

// Tag cloud state
const tagCloud = ref<TagCloudItem[]>([])
const currentTagFilter = ref<string | null>(null)

// Memories view mode: 'all' shows regular observations, 'memories' filters by memory_type='any'
const viewMode = ref<'all' | 'memories'>('all')

// Inline edit state for memory cards
const editingTitleId = ref<number | null>(null)
const editingNarrativeId = ref<number | null>(null)
const editTitleValue = ref('')
const editNarrativeValue = ref('')
const editSaving = ref(false)

// Delete confirmation state for individual memory cards
const showDeleteMemoryDialog = ref(false)
const deleteMemoryTargetId = ref<number | null>(null)
const deleteMemoryProcessing = ref(false)

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

function startBatchAction(action: 'archive' | 'delete' | 'scope' | 'tag' | 'resolve') {
  batchAction.value = action
  if (action === 'scope') {
    showScopeInput.value = true
  } else if (action === 'tag') {
    showTagInput.value = true
  } else if (action === 'resolve') {
    showBulkResolveModal.value = true
  } else {
    showBatchConfirm.value = true
  }
}

async function executeBulkResolve() {
  if (selectedIds.value.size === 0) return
  bulkResolveProcessing.value = true
  const ids = Array.from(selectedIds.value)
  try {
    await Promise.all(ids.map(id => updateObservationStatus(id, 'resolved', bulkResolveReason.value || undefined)))
    selectedIds.value = new Set()
    await fetchPage()
  } catch {
    // errors are non-critical per-item; page reload will show state
  } finally {
    bulkResolveProcessing.value = false
    showBulkResolveModal.value = false
    bulkResolveReason.value = ''
    batchAction.value = null
  }
}

function openResolveModal(id: number, event: Event) {
  event.stopPropagation()
  resolveTargetId.value = id
  resolveReason.value = ''
  showResolveModal.value = true
}

async function confirmResolve() {
  if (resolveTargetId.value === null) return
  resolveProcessing.value = true
  try {
    await updateObservationStatus(resolveTargetId.value, 'resolved', resolveReason.value || undefined)
    await fetchPage()
  } finally {
    resolveProcessing.value = false
    showResolveModal.value = false
    resolveTargetId.value = null
    resolveReason.value = ''
  }
}

async function reopenObservation(id: number, event: Event) {
  event.stopPropagation()
  await updateObservationStatus(id, 'active')
  await fetchPage()
}

function setStatus(status: 'active' | 'resolved' | null) {
  currentStatus.value = status
  offset.value = 0
  fetchPage()
}

async function executeBatchAction() {
  showBatchConfirm.value = false
  showScopeInput.value = false
  showTagInput.value = false
  if (!batchAction.value || selectedIds.value.size === 0) return

  batchProcessing.value = true
  const ids = Array.from(selectedIds.value)
  try {
    if (batchAction.value === 'archive') {
      await archiveObservations(ids, 'Batch archive from dashboard')
    } else if (batchAction.value === 'delete') {
      await fetch('/api/observations/bulk', {
        method: 'DELETE',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ ids }),
      })
    } else if (batchAction.value === 'scope') {
      await fetch('/api/observations/bulk-scope', {
        method: 'PATCH',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ ids, scope: batchScopeInput.value }),
      })
      batchScopeInput.value = ''
    } else if (batchAction.value === 'tag') {
      await fetch('/api/observations/batch-tag', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ ids, tag: batchTagInput.value, action: 'add' }),
      })
      batchTagInput.value = ''
    }
    selectedIds.value = new Set()
    await fetchPage()
    loadTagCloud()
  } catch {
    // Error will show in page reload
  } finally {
    batchProcessing.value = false
    batchAction.value = null
  }
}

async function loadTagCloud() {
  try {
    const params = new URLSearchParams({ limit: '20' })
    if (currentProject.value) {
      params.set('project', currentProject.value)
    }
    const response = await fetch(`/api/observations/tag-cloud?${params}`)
    if (response.ok) {
      tagCloud.value = await response.json()
    }
  } catch {
    // Non-critical, ignore
  }
}

function filterByTag(tag: string) {
  if (currentTagFilter.value === tag) {
    currentTagFilter.value = null
  } else {
    currentTagFilter.value = tag
  }
  offset.value = 0
  fetchPage()
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
  // Type filtering happens server-side; only concept filter remains client-side
  if (currentConcept.value) {
    return observations.value.filter(o =>
      Array.isArray(o.concepts) && o.concepts.includes(currentConcept.value)
    )
  }
  return observations.value
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
        type: currentType.value || undefined,
        status: currentStatus.value || undefined,
        memory_type: viewMode.value === 'memories' ? 'any' : undefined,
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
  loadTagCloud()
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

function setViewMode(mode: 'all' | 'memories') {
  viewMode.value = mode
  offset.value = 0
  fetchPage()
}

function startEditTitle(obs: Observation, event: Event) {
  event.stopPropagation()
  editingTitleId.value = obs.id
  editTitleValue.value = obs.title || ''
}

async function saveTitle(obs: Observation) {
  if (editSaving.value) return
  const newTitle = editTitleValue.value.trim()
  if (!newTitle || newTitle === obs.title) {
    editingTitleId.value = null
    return
  }
  editSaving.value = true
  try {
    await updateObservation(obs.id, { title: newTitle })
    await fetchPage()
  } finally {
    editSaving.value = false
    editingTitleId.value = null
  }
}

function startEditNarrative(obs: Observation, event: Event) {
  event.stopPropagation()
  editingNarrativeId.value = obs.id
  editNarrativeValue.value = obs.narrative || ''
}

async function saveNarrative(obs: Observation) {
  if (editSaving.value) return
  const newNarrative = editNarrativeValue.value.trim()
  if (newNarrative === (obs.narrative || '')) {
    editingNarrativeId.value = null
    return
  }
  editSaving.value = true
  try {
    await updateObservation(obs.id, { narrative: newNarrative })
    await fetchPage()
  } finally {
    editSaving.value = false
    editingNarrativeId.value = null
  }
}

function openDeleteMemoryDialog(id: number, event: Event) {
  event.stopPropagation()
  deleteMemoryTargetId.value = id
  showDeleteMemoryDialog.value = true
}

async function confirmDeleteMemory() {
  if (deleteMemoryTargetId.value === null) return
  deleteMemoryProcessing.value = true
  try {
    await fetch(`/api/observations/bulk`, {
      method: 'DELETE',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ ids: [deleteMemoryTargetId.value] }),
    })
    await fetchPage()
  } finally {
    deleteMemoryProcessing.value = false
    showDeleteMemoryDialog.value = false
    deleteMemoryTargetId.value = null
  }
}

onMounted(() => {
  fetchPage()
  loadProjects()
  loadTagCloud()
})

onUnmounted(() => {
  abortController?.abort()
})
</script>

<template>
  <div class="flex gap-6">
    <!-- Main content -->
    <div class="flex-1 min-w-0">
    <!-- Header -->
    <div class="flex items-center justify-between mb-6">
      <div class="flex items-center gap-3">
        <i class="fas fa-brain text-claude-400 text-xl" />
        <h1 class="text-2xl font-bold text-white">Observations</h1>
        <span v-if="total > 0" class="text-sm text-slate-500">({{ total }} total)</span>
      </div>
    </div>

    <!-- Filters Bar -->
    <div class="flex flex-col gap-2 mb-6">

      <!-- Row 1: Project dropdown + View mode toggle (left) | Concept dropdown (right) -->
      <div class="flex flex-wrap items-center gap-2">
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

        <!-- View mode toggle: All | Memories -->
        <div class="flex items-center gap-1 p-0.5 rounded-lg bg-slate-800/50 border border-slate-700/50">
          <button
            @click="setViewMode('all')"
            :class="[
              'px-3 py-1 rounded-md text-xs font-medium transition-colors',
              viewMode === 'all'
                ? 'bg-slate-700 text-white shadow'
                : 'text-slate-400 hover:text-slate-200',
            ]"
          >
            <i class="fas fa-list mr-1" />
            All
          </button>
          <button
            @click="setViewMode('memories')"
            :class="[
              'px-3 py-1 rounded-md text-xs font-medium transition-colors',
              viewMode === 'memories'
                ? 'bg-purple-600/40 text-purple-200 shadow'
                : 'text-slate-400 hover:text-slate-200',
            ]"
          >
            <i class="fas fa-brain mr-1" />
            Memories
          </button>
        </div>

        <!-- Concept filter (if concepts exist on current page) — right-aligned -->
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

      <!-- Row 2: Type filter chips + Status pills (hidden in Memories view) -->
      <div v-if="viewMode === 'all'" class="flex flex-wrap items-center gap-1.5">
        <!-- Type filter pills -->
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

        <!-- Divider between type chips and status pills -->
        <span class="w-px h-5 bg-slate-700 mx-1 self-center" />

        <!-- Status filter pills -->
        <button
          @click="setStatus(null)"
          :class="[
            'px-2.5 py-1 rounded-full text-xs font-medium border transition-colors',
            currentStatus === null
              ? 'bg-slate-500/20 text-slate-200 border-slate-500/40'
              : 'bg-slate-800/30 text-slate-500 border-slate-700/50 hover:text-slate-300 hover:border-slate-600',
          ]"
        >
          All
        </button>
        <button
          @click="setStatus('active')"
          :class="[
            'px-2.5 py-1 rounded-full text-xs font-medium border transition-colors',
            currentStatus === 'active'
              ? 'bg-emerald-500/20 text-emerald-300 border-emerald-500/40'
              : 'bg-slate-800/30 text-slate-500 border-slate-700/50 hover:text-slate-300 hover:border-slate-600',
          ]"
        >
          <i class="fas fa-circle-check mr-1" />
          Active
        </button>
        <button
          @click="setStatus('resolved')"
          :class="[
            'px-2.5 py-1 rounded-full text-xs font-medium border transition-colors',
            currentStatus === 'resolved'
              ? 'bg-slate-500/20 text-slate-400 border-slate-500/40'
              : 'bg-slate-800/30 text-slate-500 border-slate-700/50 hover:text-slate-300 hover:border-slate-600',
          ]"
        >
          <i class="fas fa-check-double mr-1" />
          Resolved
        </button>
      </div>

    </div>

    <!-- Batch Actions Toolbar -->
    <div
      v-if="selectedIds.size > 0"
      class="mb-4 p-3 rounded-lg bg-claude-500/10 border border-claude-500/30"
    >
      <div class="flex items-center gap-3">
        <span class="text-sm text-claude-300">
          <i class="fas fa-check-square mr-1" />
          {{ selectedIds.size }} selected
        </span>
        <button
          @click="startBatchAction('archive')"
          :disabled="batchProcessing"
          class="px-3 py-1 rounded-lg text-xs bg-red-500/20 text-red-400 border border-red-500/30 hover:bg-red-500/30 hover:text-red-300 transition-colors duration-200 disabled:opacity-50 cursor-pointer"
          title="Archive selected observations permanently — cannot be undone"
        >
          <i class="fas fa-archive mr-1" />
          Archive
        </button>
        <button
          @click="startBatchAction('delete')"
          :disabled="batchProcessing"
          class="px-3 py-1 rounded-lg text-xs bg-red-500/20 text-red-400 border border-red-500/30 hover:bg-red-500/30 hover:text-red-300 transition-colors duration-200 disabled:opacity-50 cursor-pointer"
          title="Permanently delete selected observations — cannot be undone"
        >
          <i class="fas fa-trash mr-1" />
          Delete
        </button>
        <button
          @click="startBatchAction('scope')"
          :disabled="batchProcessing"
          class="px-3 py-1 rounded-lg text-xs bg-blue-500/20 text-blue-300 border border-blue-500/30 hover:bg-blue-500/30 transition-colors duration-200 disabled:opacity-50 cursor-pointer"
          title="Change scope of selected observations (global or project)"
        >
          <i class="fas fa-layer-group mr-1" />
          Change Scope
        </button>
        <button
          @click="startBatchAction('tag')"
          :disabled="batchProcessing"
          class="px-3 py-1 rounded-lg text-xs bg-purple-500/20 text-purple-300 border border-purple-500/30 hover:bg-purple-500/30 transition-colors duration-200 disabled:opacity-50 cursor-pointer"
          title="Add a tag to selected observations"
        >
          <i class="fas fa-tag mr-1" />
          Add Tag
        </button>
        <button
          @click="startBatchAction('resolve')"
          :disabled="batchProcessing"
          class="px-3 py-1 rounded-lg text-xs bg-emerald-500/20 text-emerald-300 border border-emerald-500/30 hover:bg-emerald-500/30 transition-colors duration-200 disabled:opacity-50 cursor-pointer"
          title="Mark selected as resolved — removes from context injection, reversible"
        >
          <i class="fas fa-check-circle mr-1" />
          Resolve Selected
        </button>
        <button
          @click="selectedIds = new Set()"
          class="ml-auto text-xs text-slate-400 hover:text-white transition-colors duration-200 cursor-pointer"
          title="Deselect all observations"
        >
          Clear selection
        </button>
      </div>

      <!-- Scope input inline form -->
      <div v-if="showScopeInput" class="flex items-center gap-2 mt-2">
        <input
          v-model="batchScopeInput"
          type="text"
          placeholder="Enter new scope..."
          class="flex-1 px-2 py-1 rounded bg-slate-900/50 border border-slate-700/50 text-xs text-white placeholder-slate-600 focus:outline-none focus:ring-1 focus:ring-claude-500/50"
          @keydown.enter="executeBatchAction"
        />
        <button
          @click="executeBatchAction"
          :disabled="!batchScopeInput.trim()"
          class="px-3 py-1 rounded text-xs bg-blue-500/20 text-blue-300 border border-blue-500/30 hover:bg-blue-500/30 transition-colors disabled:opacity-50"
        >
          Apply
        </button>
        <button
          @click="showScopeInput = false; batchAction = null"
          class="px-2 py-1 rounded text-xs text-slate-400 hover:text-white transition-colors"
        >
          Cancel
        </button>
      </div>

      <!-- Tag input inline form -->
      <div v-if="showTagInput" class="flex items-center gap-2 mt-2">
        <input
          v-model="batchTagInput"
          type="text"
          placeholder="Enter tag to add..."
          class="flex-1 px-2 py-1 rounded bg-slate-900/50 border border-slate-700/50 text-xs text-white placeholder-slate-600 focus:outline-none focus:ring-1 focus:ring-claude-500/50"
          @keydown.enter="executeBatchAction"
        />
        <button
          @click="executeBatchAction"
          :disabled="!batchTagInput.trim()"
          class="px-3 py-1 rounded text-xs bg-purple-500/20 text-purple-300 border border-purple-500/30 hover:bg-purple-500/30 transition-colors disabled:opacity-50"
        >
          Apply
        </button>
        <button
          @click="showTagInput = false; batchAction = null"
          class="px-2 py-1 rounded text-xs text-slate-400 hover:text-white transition-colors"
        >
          Cancel
        </button>
      </div>
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

      <!-- Per-observation: memory card or regular card -->
      <template v-for="obs in filteredObservations" :key="obs.id">

      <!-- Memory card (observations with memory_type set) -->
      <div
        v-if="obs.memory_type"
        :class="[
          'group p-4 rounded-xl border-2 bg-gradient-to-br transition-all duration-200',
          obs.status === 'resolved'
            ? 'border-purple-500/10 from-slate-800/20 to-slate-900/20 opacity-50'
            : selectedIds.has(obs.id)
              ? 'border-purple-500/50 from-purple-500/10 to-slate-900/50'
              : 'border-purple-500/30 from-slate-800/50 to-slate-900/50 hover:border-purple-500/50 hover:from-slate-800/70 hover:to-slate-900/70',
        ]"
      >
        <!-- Memory card header row -->
        <div class="flex items-start gap-3">
          <!-- Checkbox -->
          <input
            type="checkbox"
            :checked="selectedIds.has(obs.id)"
            @click="toggleSelect(obs.id, $event)"
            class="mt-0.5 w-3.5 h-3.5 rounded border-slate-600 bg-slate-800 text-claude-500 focus:ring-claude-500/50 cursor-pointer flex-shrink-0"
          />

          <!-- Memory brain icon -->
          <div class="w-9 h-9 flex items-center justify-center rounded-lg bg-gradient-to-br from-purple-600/40 to-purple-800/30 border border-purple-500/30 flex-shrink-0">
            <i class="fas fa-brain text-purple-300 text-sm" />
          </div>

          <!-- Title (inline editable) -->
          <div class="flex-1 min-w-0">
            <div class="flex items-center gap-2 mb-1">
              <input
                v-if="editingTitleId === obs.id"
                :value="editTitleValue"
                @input="editTitleValue = ($event.target as HTMLInputElement).value"
                @blur="saveTitle(obs)"
                @keydown.enter="saveTitle(obs)"
                @keydown.esc="editingTitleId = null"
                @click.stop
                class="flex-1 px-2 py-0.5 rounded bg-slate-900/70 border border-purple-500/40 text-sm text-white focus:outline-none focus:ring-1 focus:ring-purple-500/50"
                autofocus
              />
              <h3
                v-else
                @click="startEditTitle(obs, $event)"
                :class="[
                  'text-sm font-medium text-white truncate cursor-text hover:text-purple-200 transition-colors',
                  obs.status === 'resolved' ? 'line-through text-slate-400' : '',
                ]"
                title="Click to edit"
              >
                {{ obs.title || 'Untitled' }}
              </h3>

              <!-- Memory type badge -->
              <span class="flex-shrink-0 px-1.5 py-0.5 rounded text-[10px] bg-purple-500/20 text-purple-300 border border-purple-500/30 font-mono">
                {{ obs.memory_type }}
              </span>

              <!-- Scope badge -->
              <span
                v-if="obs.scope === 'global'"
                class="flex-shrink-0 px-1.5 py-0.5 rounded text-[10px] bg-green-500/20 text-green-400 border border-green-500/30"
              >
                global
              </span>
              <span
                v-else
                class="flex-shrink-0 px-1.5 py-0.5 rounded text-[10px] bg-blue-500/20 text-blue-400 border border-blue-500/30"
              >
                project
              </span>

              <!-- Resolved badge -->
              <span
                v-if="obs.status === 'resolved'"
                :title="obs.status_reason || 'Resolved'"
                class="flex-shrink-0 px-1.5 py-0.5 rounded text-[10px] bg-slate-600/30 text-slate-400 border border-slate-600/30 cursor-default"
              >
                <i class="fas fa-check-double mr-0.5" />
                resolved
              </span>
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

          <!-- Action buttons (delete + navigate) -->
          <div class="flex items-center gap-1 flex-shrink-0" @click.stop>
            <button
              @click="openDeleteMemoryDialog(obs.id, $event)"
              class="opacity-0 group-hover:opacity-100 p-1.5 rounded-lg text-xs bg-slate-700/50 text-red-400 border border-slate-600/30 hover:bg-red-500/20 hover:text-red-300 hover:border-red-500/30 transition-all duration-200 cursor-pointer"
              title="Permanently delete this memory — cannot be undone"
            >
              <i class="fas fa-trash text-[11px]" />
            </button>
            <button
              @click="navigateToDetail(obs.id)"
              class="p-1.5 rounded-lg text-xs bg-slate-700/50 text-slate-400 border border-slate-600/30 hover:bg-slate-600/50 hover:text-slate-200 transition-all duration-200 cursor-pointer"
              title="View full observation details"
            >
              <i class="fas fa-arrow-right text-[11px]" />
            </button>
          </div>
        </div>

        <!-- Narrative (inline editable) -->
        <div class="mt-2 ml-12 pl-0.5">
          <textarea
            v-if="editingNarrativeId === obs.id"
            :value="editNarrativeValue"
            @input="editNarrativeValue = ($event.target as HTMLTextAreaElement).value"
            @blur="saveNarrative(obs)"
            @keydown.enter.ctrl="saveNarrative(obs)"
            @keydown.esc="editingNarrativeId = null"
            @click.stop
            rows="3"
            class="w-full px-2 py-1.5 rounded bg-slate-900/70 border border-purple-500/40 text-xs text-slate-300 focus:outline-none focus:ring-1 focus:ring-purple-500/50 resize-y"
            autofocus
          />
          <p
            v-else-if="obs.narrative"
            @click="startEditNarrative(obs, $event)"
            class="text-xs text-slate-400 cursor-text hover:text-slate-300 transition-colors line-clamp-2"
            title="Click to edit"
          >
            {{ obs.narrative }}
          </p>
          <p
            v-else
            @click="startEditNarrative(obs, $event)"
            class="text-xs text-slate-600 italic cursor-text hover:text-slate-500 transition-colors"
          >
            Click to add narrative...
          </p>
        </div>
      </div>

      <!-- Regular observation card -->
      <div
        v-else
        @click="navigateToDetail(obs.id)"
        :class="[
          'group flex items-center gap-4 p-4 rounded-xl border-2 bg-gradient-to-br cursor-pointer transition-all duration-200',
          obs.status === 'resolved'
            ? 'border-slate-700/30 from-slate-800/30 to-slate-900/30 opacity-50 hover:opacity-75'
            : selectedIds.has(obs.id)
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
            <h3
              :class="[
                'text-sm font-medium text-white truncate group-hover:text-claude-300 transition-colors',
                obs.status === 'resolved' ? 'line-through text-slate-400' : '',
              ]"
            >
              {{ obs.title || 'Untitled' }}
            </h3>
            <!-- Resolved badge with optional tooltip -->
            <span
              v-if="obs.status === 'resolved'"
              :title="obs.status_reason || 'Resolved'"
              class="flex-shrink-0 px-1.5 py-0.5 rounded text-[10px] bg-slate-600/30 text-slate-400 border border-slate-600/30 cursor-default"
            >
              <i class="fas fa-check-double mr-0.5" />
              resolved
            </span>
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

        <!-- Score + Effectiveness badge -->
        <div class="flex flex-col items-center gap-0.5 flex-shrink-0">
          <span class="text-[10px] font-mono text-slate-500">
            <i class="fas fa-chart-bar text-purple-500/60 mr-0.5" />
            {{ (obs.importance_score || 0).toFixed(2) }}
          </span>
          <!-- Effectiveness dot: green ≥0.7, yellow ≥0.4, red <0.4, gray = insufficient data -->
          <span
            :class="[
              'w-2 h-2 rounded-full flex-shrink-0',
              (obs.effectiveness_injections ?? 0) < 10
                ? 'bg-slate-500/60'
                : (obs.effectiveness_score ?? 0) >= 0.7
                  ? 'bg-green-500'
                  : (obs.effectiveness_score ?? 0) >= 0.4
                    ? 'bg-yellow-500'
                    : 'bg-red-500',
            ]"
            :title="(obs.effectiveness_injections ?? 0) < 10
              ? 'Insufficient data (< 10 injections)'
              : `Effectiveness: ${((obs.effectiveness_score ?? 0) * 100).toFixed(0)}% (${obs.effectiveness_injections} injections)`"
          />
        </div>

        <!-- Resolve / Reopen button -->
        <div class="flex-shrink-0" @click.stop>
          <button
            v-if="obs.status !== 'resolved'"
            @click="openResolveModal(obs.id, $event)"
            class="opacity-0 group-hover:opacity-100 px-2 py-1 rounded-lg text-xs bg-slate-700/50 text-emerald-400 border border-slate-600/30 hover:bg-emerald-500/20 hover:text-emerald-300 hover:border-emerald-500/30 transition-all duration-200 cursor-pointer"
            title="Mark as resolved — removes from context injection, reversible"
          >
            <i class="fas fa-check-circle" />
          </button>
          <button
            v-else
            @click="reopenObservation(obs.id, $event)"
            class="opacity-0 group-hover:opacity-100 px-2 py-1 rounded-lg text-xs bg-slate-700/50 text-blue-400 border border-slate-600/30 hover:bg-blue-500/20 hover:text-blue-300 hover:border-blue-500/30 transition-all duration-200 cursor-pointer"
            title="Reopen — restores to active state and context injection"
          >
            <i class="fas fa-rotate-left" />
          </button>
        </div>

        <!-- Arrow -->
        <i class="fas fa-chevron-right text-slate-600 group-hover:text-claude-400 transition-colors flex-shrink-0" />
      </div>

      </template><!-- end per-observation template -->
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
      :title="batchAction === 'delete' ? `Delete ${selectedIds.size} Observations` : `Archive ${selectedIds.size} Observations`"
      :message="batchAction === 'delete'
        ? `Are you sure you want to permanently delete ${selectedIds.size} observation(s)? This cannot be undone.`
        : `Are you sure you want to archive ${selectedIds.size} observation(s)? Archived observations can be restored.`"
      :confirm-label="batchAction === 'delete' ? 'Delete' : 'Archive'"
      :danger="true"
      @confirm="executeBatchAction"
      @cancel="showBatchConfirm = false; batchAction = null"
    />

    <!-- Resolve observation modal -->
    <Teleport to="body">
      <div
        v-if="showResolveModal"
        class="fixed inset-0 z-50 flex items-center justify-center bg-black/60 backdrop-blur-sm"
        @click.self="showResolveModal = false"
      >
        <div class="w-full max-w-md rounded-xl bg-slate-900 border border-slate-700/60 p-6 shadow-2xl">
          <h2 class="text-base font-semibold text-white mb-1">
            <i class="fas fa-check-circle text-emerald-400 mr-2" />
            Resolve Observation
          </h2>
          <p class="text-xs text-slate-400 mb-4">Optionally explain why this observation is resolved.</p>
          <input
            v-model="resolveReason"
            type="text"
            placeholder="Reason (optional)..."
            class="w-full px-3 py-2 rounded-lg bg-slate-800/60 border border-slate-700/50 text-sm text-white placeholder-slate-600 focus:outline-none focus:ring-2 focus:ring-emerald-500/40 focus:border-emerald-500/50 mb-4 transition-colors"
            @keydown.enter="confirmResolve"
            @keydown.esc="showResolveModal = false"
          />
          <div class="flex justify-end gap-2">
            <button
              @click="showResolveModal = false"
              class="px-4 py-1.5 rounded-lg text-sm text-slate-400 hover:text-white border border-slate-700/50 hover:border-slate-600 transition-colors"
            >
              Cancel
            </button>
            <button
              @click="confirmResolve"
              :disabled="resolveProcessing"
              class="px-4 py-1.5 rounded-lg text-sm bg-emerald-500/20 text-emerald-300 border border-emerald-500/30 hover:bg-emerald-500/30 transition-colors disabled:opacity-50 cursor-pointer"
            >
              <i v-if="resolveProcessing" class="fas fa-circle-notch fa-spin mr-1" />
              Resolve
            </button>
          </div>
        </div>
      </div>
    </Teleport>

    <!-- Bulk resolve modal -->
    <Teleport to="body">
      <div
        v-if="showBulkResolveModal"
        class="fixed inset-0 z-50 flex items-center justify-center bg-black/60 backdrop-blur-sm"
        @click.self="showBulkResolveModal = false; batchAction = null"
      >
        <div class="w-full max-w-md rounded-xl bg-slate-900 border border-slate-700/60 p-6 shadow-2xl">
          <h2 class="text-base font-semibold text-white mb-1">
            <i class="fas fa-check-circle text-emerald-400 mr-2" />
            Resolve {{ selectedIds.size }} Observation{{ selectedIds.size === 1 ? '' : 's' }}
          </h2>
          <p class="text-xs text-slate-400 mb-4">Optionally explain why these observations are resolved.</p>
          <input
            v-model="bulkResolveReason"
            type="text"
            placeholder="Reason (optional)..."
            class="w-full px-3 py-2 rounded-lg bg-slate-800/60 border border-slate-700/50 text-sm text-white placeholder-slate-600 focus:outline-none focus:ring-2 focus:ring-emerald-500/40 focus:border-emerald-500/50 mb-4 transition-colors"
            @keydown.enter="executeBulkResolve"
            @keydown.esc="showBulkResolveModal = false; batchAction = null"
          />
          <div class="flex justify-end gap-2">
            <button
              @click="showBulkResolveModal = false; batchAction = null"
              class="px-4 py-1.5 rounded-lg text-sm text-slate-400 hover:text-white border border-slate-700/50 hover:border-slate-600 transition-colors"
            >
              Cancel
            </button>
            <button
              @click="executeBulkResolve"
              :disabled="bulkResolveProcessing"
              class="px-4 py-1.5 rounded-lg text-sm bg-emerald-500/20 text-emerald-300 border border-emerald-500/30 hover:bg-emerald-500/30 transition-colors disabled:opacity-50 cursor-pointer"
            >
              <i v-if="bulkResolveProcessing" class="fas fa-circle-notch fa-spin mr-1" />
              Resolve All
            </button>
          </div>
        </div>
      </div>
    </Teleport>
    <!-- Delete Memory Confirmation Dialog -->
    <ConfirmDialog
      :show="showDeleteMemoryDialog"
      title="Delete Memory"
      message="Are you sure you want to permanently delete this memory? This cannot be undone."
      confirm-label="Delete"
      :danger="true"
      @confirm="confirmDeleteMemory"
      @cancel="showDeleteMemoryDialog = false; deleteMemoryTargetId = null"
    />
    </div><!-- end main content -->

    <!-- Tag Cloud Sidebar -->
    <div v-if="tagCloud.length > 0" class="w-52 flex-shrink-0">
      <div class="p-3 rounded-xl border border-slate-700/50 bg-slate-800/30 sticky top-4">
        <div class="text-[10px] text-slate-500 uppercase tracking-wider font-medium mb-2">
          <i class="fas fa-tags mr-1" />
          Top Tags
        </div>
        <div class="flex flex-wrap gap-1.5">
          <span
            v-for="item in tagCloud"
            :key="item.tag"
            @click="filterByTag(item.tag)"
            :class="[
              'cursor-pointer px-2 py-0.5 rounded-full text-[10px] border transition-colors duration-200',
              currentTagFilter === item.tag
                ? 'bg-claude-500/20 text-claude-300 border-claude-500/30'
                : 'bg-slate-700/30 text-slate-400 border-slate-600/50 hover:text-slate-200 hover:border-slate-500',
            ]"
          >
            {{ item.tag }} ({{ item.count }})
          </span>
        </div>
        <button
          v-if="currentTagFilter"
          @click="filterByTag(currentTagFilter)"
          class="mt-2 text-[10px] text-slate-500 hover:text-slate-300 transition-colors"
        >
          <i class="fas fa-times mr-0.5" />
          Clear filter
        </button>
      </div>
    </div>
  </div>
</template>
