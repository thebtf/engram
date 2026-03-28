<script setup lang="ts">
import { ref, computed, onMounted, onBeforeUnmount } from 'vue'
import { useRoute, useRouter, onBeforeRouteLeave } from 'vue-router'
import type { ObservationType } from '@/types'
import { TYPE_CONFIG } from '@/types/observation'
import { CONCEPT_CONFIG, type ConceptType } from '@/types/observation'
import { formatRelativeTime } from '@/utils/formatters'
import { useObservation } from '@/composables'
import ObservationEditor from '@/components/observation/ObservationEditor.vue'
import Badge from '@/components/Badge.vue'
import IconBox from '@/components/IconBox.vue'
import ConfirmDialog from '@/components/layout/ConfirmDialog.vue'

const route = useRoute()
const router = useRouter()
const observationId = computed(() => Number(route.params.id))

const { observation, loading, saving, error, load, save, archive, feedback } = useObservation()

// Edit mode
const isEditing = ref(false)
const editorRef = ref<InstanceType<typeof ObservationEditor> | null>(null)
const showArchiveConfirm = ref(false)
const feedbackSubmitting = ref(false)

// Track unsaved changes for beforeunload warning
const hasUnsavedChanges = computed(() => {
  return isEditing.value && editorRef.value?.hasChanges
})

// Warn before leaving with unsaved changes
function handleBeforeUnload(e: BeforeUnloadEvent) {
  if (hasUnsavedChanges.value) {
    e.preventDefault()
    e.returnValue = ''
  }
}

onMounted(() => {
  load(observationId.value)
  window.addEventListener('beforeunload', handleBeforeUnload)
})

onBeforeUnmount(() => {
  window.removeEventListener('beforeunload', handleBeforeUnload)
})

onBeforeRouteLeave((_to, _from, next) => {
  if (hasUnsavedChanges.value) {
    const leave = window.confirm('You have unsaved changes. Are you sure you want to leave?')
    if (!leave) {
      next(false)
      return
    }
  }
  next()
})

async function handleSave(updates: Record<string, unknown>) {
  try {
    await save(updates)
    isEditing.value = false
  } catch {
    // Error is handled by useObservation
  }
}

function handleCancelEdit() {
  if (hasUnsavedChanges.value) {
    const discard = window.confirm('Discard unsaved changes?')
    if (!discard) return
  }
  isEditing.value = false
}

async function handleArchive() {
  showArchiveConfirm.value = false
  try {
    await archive('Archived from dashboard')
    router.push({ name: 'observations' })
  } catch {
    // Error is handled by useObservation
  }
}

async function handleFeedback(value: number) {
  feedbackSubmitting.value = true
  try {
    await feedback(value)
  } finally {
    feedbackSubmitting.value = false
  }
}

function getTypeConfig(type: string) {
  return TYPE_CONFIG[type as ObservationType] || TYPE_CONFIG.change
}

function getConceptConfig(concept: string) {
  return CONCEPT_CONFIG[concept as ConceptType] || { icon: 'fa-tag', colorClass: 'text-slate-300', bgClass: 'bg-slate-500/20', borderClass: 'border-slate-500/40' }
}

function shortProject(project: string): string {
  const parts = project.split('/')
  return parts[parts.length - 1] || project
}

// Split path for file display
function splitPath(path: string, components = 3) {
  const parts = path.split('/').filter(Boolean)
  if (parts.length <= components) {
    return { root: '', path }
  }
  const relevant = parts.slice(-components)
  return {
    root: relevant[0],
    path: relevant.slice(1).join('/'),
  }
}
</script>

<template>
  <div>
    <!-- Back navigation -->
    <button
      @click="router.push({ name: 'observations' })"
      class="flex items-center gap-2 text-sm text-slate-400 hover:text-white mb-6 transition-colors duration-200 cursor-pointer"
      title="Return to observations list"
    >
      <i class="fas fa-arrow-left" />
      Back to Observations
    </button>

    <!-- Loading State -->
    <div v-if="loading" class="flex items-center justify-center py-20">
      <i class="fas fa-circle-notch fa-spin text-claude-400 text-2xl" />
    </div>

    <!-- Error State -->
    <div v-else-if="error" class="text-center py-16">
      <i class="fas fa-exclamation-triangle text-red-400 text-3xl mb-3 block" />
      <p class="text-red-400 mb-2">{{ error }}</p>
      <button
        @click="load(observationId)"
        class="text-sm text-slate-400 hover:text-white transition-colors duration-200 cursor-pointer"
      >
        Try again
      </button>
    </div>

    <!-- Observation Detail -->
    <div v-else-if="observation" class="max-w-4xl">
      <!-- Header -->
      <div class="flex items-start gap-4 mb-6">
        <IconBox
          :icon="getTypeConfig(observation.type).icon"
          :gradient="getTypeConfig(observation.type).gradient"
          size="lg"
        />

        <div class="flex-1 min-w-0">
          <div class="flex items-center gap-2 mb-2 flex-wrap">
            <Badge
              :icon="getTypeConfig(observation.type).icon"
              :color-class="getTypeConfig(observation.type).colorClass"
              :bg-class="getTypeConfig(observation.type).bgClass"
              :border-class="getTypeConfig(observation.type).borderClass"
            >
              {{ observation.type.toUpperCase() }}
            </Badge>
            <span class="text-xs text-slate-500">{{ formatRelativeTime(observation.created_at) }}</span>
            <span v-if="observation.project" class="text-xs text-slate-500 flex items-center gap-1">
              <span class="text-slate-600">|</span>
              <i class="fas fa-folder text-slate-600 text-[10px]" />
              <span class="text-amber-600/80 font-mono">{{ shortProject(observation.project) }}</span>
            </span>
            <span class="text-xs text-slate-600 font-mono">#{{ observation.id }}</span>
          </div>

          <h1 class="text-2xl font-bold text-white mb-1">{{ observation.title || 'Untitled' }}</h1>
          <p v-if="observation.subtitle" class="text-sm text-slate-400">{{ observation.subtitle }}</p>
        </div>

        <!-- Actions -->
        <div class="flex items-center gap-2 flex-shrink-0">
          <button
            v-if="!isEditing"
            @click="isEditing = true"
            class="px-3 py-1.5 rounded-lg text-sm bg-slate-800/50 border border-slate-700/50 text-slate-300 hover:text-white hover:border-claude-500/50 transition-colors duration-200 cursor-pointer"
            title="Edit observation title and content"
          >
            <i class="fas fa-pen mr-1.5" />
            Edit
          </button>
          <button
            @click="showArchiveConfirm = true"
            class="px-3 py-1.5 rounded-lg text-sm bg-slate-800/50 border border-slate-700/50 text-red-400 hover:text-red-300 hover:border-red-500/50 transition-colors duration-200 cursor-pointer"
            title="Archive permanently — cannot be undone"
          >
            <i class="fas fa-archive mr-1.5" />
            Archive
          </button>
        </div>
      </div>

      <!-- Edit Mode -->
      <div v-if="isEditing" class="p-6 rounded-xl border-2 border-claude-500/30 bg-slate-800/30 mb-6">
        <div class="flex items-center gap-2 mb-4">
          <i class="fas fa-pen text-claude-400" />
          <h2 class="text-sm font-semibold text-claude-400 uppercase tracking-wide">Editing</h2>
          <span v-if="saving" class="text-xs text-slate-500">
            <i class="fas fa-circle-notch fa-spin mr-1" />
            Saving...
          </span>
        </div>
        <ObservationEditor
          ref="editorRef"
          :observation="observation"
          @save="handleSave"
          @cancel="handleCancelEdit"
        />
      </div>

      <!-- Read-only Content -->
      <div v-else class="space-y-6">
        <!-- Narrative -->
        <div v-if="observation.narrative" class="p-4 rounded-xl border-2 border-slate-700/50 bg-slate-800/30">
          <h2 class="text-xs text-slate-500 uppercase tracking-wide mb-2">Narrative</h2>
          <p class="text-sm text-slate-300 whitespace-pre-wrap leading-relaxed">{{ observation.narrative }}</p>
        </div>

        <!-- Facts -->
        <div v-if="observation.facts?.length > 0" class="p-4 rounded-xl border-2 border-slate-700/50 bg-slate-800/30">
          <h2 class="text-xs text-slate-500 uppercase tracking-wide mb-3">Key Facts</h2>
          <div class="space-y-2">
            <div v-for="(fact, idx) in observation.facts" :key="idx" class="flex items-start gap-2 text-sm text-slate-300">
              <i class="fas fa-check text-amber-500/70 mt-0.5 flex-shrink-0 text-xs" />
              <span>{{ fact }}</span>
            </div>
          </div>
        </div>

        <!-- Rejected alternatives (for decisions) -->
        <div v-if="observation.rejected?.length" class="p-4 rounded-xl border-2 border-red-500/20 bg-red-500/5">
          <h2 class="text-xs text-red-400/70 uppercase tracking-wide mb-3">Rejected Alternatives</h2>
          <div class="space-y-2">
            <div v-for="(alt, idx) in observation.rejected" :key="idx" class="flex items-start gap-2 text-sm text-slate-400">
              <i class="fas fa-times text-red-400/50 mt-0.5 flex-shrink-0 text-xs" />
              <span>{{ alt }}</span>
            </div>
          </div>
        </div>

        <!-- Concepts -->
        <div v-if="observation.concepts?.length > 0" class="p-4 rounded-xl border-2 border-slate-700/50 bg-slate-800/30">
          <h2 class="text-xs text-slate-500 uppercase tracking-wide mb-3">Concepts</h2>
          <div class="flex flex-wrap gap-1.5">
            <Badge
              v-for="concept in observation.concepts"
              :key="concept"
              :icon="getConceptConfig(concept).icon"
              :color-class="getConceptConfig(concept).colorClass"
              :bg-class="getConceptConfig(concept).bgClass"
              :border-class="getConceptConfig(concept).borderClass"
            >
              {{ concept }}
            </Badge>
          </div>
        </div>

        <!-- Files -->
        <div v-if="(observation.files_read?.length > 0) || (observation.files_modified?.length > 0)" class="p-4 rounded-xl border-2 border-slate-700/50 bg-slate-800/30">
          <h2 class="text-xs text-slate-500 uppercase tracking-wide mb-3">Files</h2>
          <div class="space-y-2 text-xs">
            <div v-if="observation.files_read?.length > 0">
              <div class="flex items-start gap-1.5">
                <i class="fas fa-eye text-slate-600 mt-0.5" />
                <span class="text-slate-600">Read:</span>
                <div class="flex flex-wrap gap-x-2 gap-y-0.5">
                  <span
                    v-for="(file, idx) in observation.files_read"
                    :key="file"
                    :title="file"
                    class="cursor-default font-mono"
                  >
                    <span class="text-amber-600">{{ splitPath(file).root }}</span><span v-if="splitPath(file).root && splitPath(file).path">/</span><span class="text-slate-400 hover:text-slate-300">{{ splitPath(file).path }}</span><span v-if="idx < observation.files_read.length - 1" class="text-slate-600">,</span>
                  </span>
                </div>
              </div>
            </div>
            <div v-if="observation.files_modified?.length > 0">
              <div class="flex items-start gap-1.5">
                <i class="fas fa-pen text-slate-600 mt-0.5" />
                <span class="text-slate-600">Modified:</span>
                <div class="flex flex-wrap gap-x-2 gap-y-0.5">
                  <span
                    v-for="(file, idx) in observation.files_modified"
                    :key="file"
                    :title="file"
                    class="cursor-default font-mono"
                  >
                    <span class="text-amber-600">{{ splitPath(file).root }}</span><span v-if="splitPath(file).root && splitPath(file).path">/</span><span class="text-slate-400 hover:text-slate-300">{{ splitPath(file).path }}</span><span v-if="idx < observation.files_modified.length - 1" class="text-slate-600">,</span>
                  </span>
                </div>
              </div>
            </div>
          </div>
        </div>

        <!-- Metadata -->
        <div class="p-4 rounded-xl border-2 border-slate-700/50 bg-slate-800/30">
          <h2 class="text-xs text-slate-500 uppercase tracking-wide mb-3">Metadata</h2>
          <div class="grid grid-cols-2 md:grid-cols-4 gap-4 text-sm">
            <div>
              <span class="text-slate-600 text-xs block">Scope</span>
              <span class="text-slate-300">
                <i :class="['fas mr-1', observation.scope === 'global' ? 'fa-globe' : 'fa-folder']" />
                {{ observation.scope || 'project' }}
              </span>
            </div>
            <div>
              <span class="text-slate-600 text-xs block">Score</span>
              <span class="text-slate-300 font-mono">{{ (observation.importance_score || 0).toFixed(4) }}</span>
            </div>
            <div>
              <span class="text-slate-600 text-xs block">Retrievals</span>
              <span class="text-slate-300 font-mono">{{ observation.retrieval_count || 0 }}</span>
            </div>
            <div>
              <span class="text-slate-600 text-xs block">Feedback</span>
              <span :class="[
                'font-mono',
                observation.user_feedback === 1 ? 'text-green-400' :
                observation.user_feedback === -1 ? 'text-red-400' :
                'text-slate-500'
              ]">
                {{ observation.user_feedback === 1 ? '+1' : observation.user_feedback === -1 ? '-1' : '0' }}
              </span>
            </div>
          </div>
        </div>

        <!-- Feedback buttons -->
        <div class="flex items-center gap-3">
          <span class="text-xs text-slate-500">Rate this observation:</span>
          <button
            @click="handleFeedback(1)"
            :disabled="feedbackSubmitting"
            :class="[
              'p-2 rounded-lg transition-all duration-200 cursor-pointer',
              observation.user_feedback === 1
                ? 'bg-green-500/30 text-green-300'
                : 'text-blue-400 hover:text-green-400 hover:bg-green-500/10',
            ]"
            title="Rate as useful — improves future ranking"
          >
            <i class="fas fa-thumbs-up" />
          </button>
          <button
            @click="handleFeedback(-1)"
            :disabled="feedbackSubmitting"
            :class="[
              'p-2 rounded-lg transition-all duration-200 cursor-pointer',
              observation.user_feedback === -1
                ? 'bg-red-500/30 text-red-300'
                : 'text-blue-400 hover:text-red-400 hover:bg-red-500/10',
            ]"
            title="Rate as not useful — lowers future ranking"
          >
            <i class="fas fa-thumbs-down" />
          </button>
        </div>
      </div>
    </div>

    <!-- Archive Confirmation -->
    <ConfirmDialog
      :show="showArchiveConfirm"
      title="Archive Observation"
      message="This observation will be archived and no longer appear in search results. This action can be undone."
      confirm-label="Archive"
      :danger="true"
      @confirm="handleArchive"
      @cancel="showArchiveConfirm = false"
    />
  </div>
</template>
