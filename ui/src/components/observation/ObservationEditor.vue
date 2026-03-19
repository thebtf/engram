<script setup lang="ts">
import { ref, watch, computed } from 'vue'
import type { Observation, ObservationScope } from '@/types'

const props = defineProps<{
  observation: Observation
}>()

const emit = defineEmits<{
  save: [updates: {
    title?: string
    subtitle?: string
    narrative?: string
    scope?: string
    concepts?: string[]
  }]
  cancel: []
}>()

// Editable fields (local copies)
const title = ref('')
const subtitle = ref('')
const narrative = ref('')
const scope = ref<ObservationScope>('project')
const concepts = ref<string[]>([])

// Initialize from observation
function resetForm() {
  title.value = props.observation.title || ''
  subtitle.value = props.observation.subtitle || ''
  narrative.value = props.observation.narrative || ''
  scope.value = props.observation.scope || 'project'
  concepts.value = [...(props.observation.concepts || [])]
}

watch(() => props.observation, () => {
  resetForm()
}, { immediate: true })

// Track changes
const hasChanges = computed(() => {
  return title.value !== (props.observation.title || '') ||
    subtitle.value !== (props.observation.subtitle || '') ||
    narrative.value !== (props.observation.narrative || '') ||
    scope.value !== (props.observation.scope || 'project') ||
    JSON.stringify([...concepts.value].sort()) !== JSON.stringify([...(props.observation.concepts || [])].sort())
})

function handleSave() {
  const updates: Record<string, unknown> = {}
  if (title.value !== (props.observation.title || '')) updates.title = title.value
  if (subtitle.value !== (props.observation.subtitle || '')) updates.subtitle = subtitle.value
  if (narrative.value !== (props.observation.narrative || '')) updates.narrative = narrative.value
  if (scope.value !== (props.observation.scope || 'project')) updates.scope = scope.value
  if (JSON.stringify([...concepts.value].sort()) !== JSON.stringify([...(props.observation.concepts || [])].sort())) {
    updates.concepts = concepts.value
  }
  emit('save', updates)
}

function handleCancel() {
  resetForm()
  emit('cancel')
}

// Concept management
const newConcept = ref('')

function addConcept() {
  const c = newConcept.value.trim().toLowerCase()
  if (c && !concepts.value.includes(c)) {
    concepts.value = [...concepts.value, c]
  }
  newConcept.value = ''
}

function removeConcept(concept: string) {
  concepts.value = concepts.value.filter(c => c !== concept)
}

defineExpose({ hasChanges })
</script>

<template>
  <div class="space-y-4">
    <!-- Title -->
    <div>
      <label class="block text-xs text-slate-500 uppercase tracking-wide mb-1">Title</label>
      <input
        v-model="title"
        type="text"
        class="w-full px-3 py-2 rounded-lg bg-slate-800/50 border border-slate-700/50 text-white text-sm focus:outline-none focus:ring-2 focus:ring-claude-500/50 focus:border-claude-500 transition-colors"
        placeholder="Observation title"
      />
    </div>

    <!-- Subtitle -->
    <div>
      <label class="block text-xs text-slate-500 uppercase tracking-wide mb-1">Subtitle</label>
      <input
        v-model="subtitle"
        type="text"
        class="w-full px-3 py-2 rounded-lg bg-slate-800/50 border border-slate-700/50 text-white text-sm focus:outline-none focus:ring-2 focus:ring-claude-500/50 focus:border-claude-500 transition-colors"
        placeholder="Brief summary"
      />
    </div>

    <!-- Narrative -->
    <div>
      <label class="block text-xs text-slate-500 uppercase tracking-wide mb-1">Narrative</label>
      <textarea
        v-model="narrative"
        rows="6"
        class="w-full px-3 py-2 rounded-lg bg-slate-800/50 border border-slate-700/50 text-white text-sm focus:outline-none focus:ring-2 focus:ring-claude-500/50 focus:border-claude-500 transition-colors resize-y"
        placeholder="Detailed narrative..."
      />
    </div>

    <!-- Scope -->
    <div>
      <label class="block text-xs text-slate-500 uppercase tracking-wide mb-1">Scope</label>
      <div class="flex items-center gap-3">
        <button
          v-for="s in ['project', 'global'] as ObservationScope[]"
          :key="s"
          @click="scope = s"
          :class="[
            'px-3 py-1.5 rounded-lg text-sm font-medium border transition-colors',
            scope === s
              ? 'bg-claude-500/20 text-claude-400 border-claude-500/50'
              : 'bg-slate-800/30 text-slate-500 border-slate-700/50 hover:text-slate-300',
          ]"
        >
          <i :class="['fas mr-1.5', s === 'global' ? 'fa-globe' : 'fa-folder']" />
          {{ s }}
        </button>
      </div>
    </div>

    <!-- Concepts -->
    <div>
      <label class="block text-xs text-slate-500 uppercase tracking-wide mb-1">Concepts</label>
      <div class="flex flex-wrap gap-1.5 mb-2">
        <span
          v-for="concept in concepts"
          :key="concept"
          class="inline-flex items-center gap-1 px-2 py-0.5 text-xs font-medium rounded-full bg-slate-700/50 text-slate-300 border border-slate-600/50"
        >
          {{ concept }}
          <button
            @click="removeConcept(concept)"
            class="text-slate-500 hover:text-red-400 transition-colors ml-0.5"
          >
            <i class="fas fa-times text-[10px]" />
          </button>
        </span>
      </div>
      <div class="flex items-center gap-2">
        <input
          v-model="newConcept"
          type="text"
          class="flex-1 px-3 py-1.5 rounded-lg bg-slate-800/50 border border-slate-700/50 text-white text-xs focus:outline-none focus:ring-1 focus:ring-claude-500/50 transition-colors"
          placeholder="Add concept..."
          @keydown.enter.prevent="addConcept"
        />
        <button
          @click="addConcept"
          class="px-3 py-1.5 rounded-lg text-xs font-medium bg-slate-700/50 text-slate-300 hover:bg-slate-700 transition-colors"
        >
          Add
        </button>
      </div>
    </div>

    <!-- Actions -->
    <div class="flex items-center justify-end gap-3 pt-2 border-t border-slate-700/50">
      <button
        @click="handleCancel"
        class="px-4 py-2 rounded-lg text-sm text-slate-400 hover:text-white hover:bg-slate-800/50 transition-colors"
      >
        Discard
      </button>
      <button
        @click="handleSave"
        :disabled="!hasChanges"
        :class="[
          'px-4 py-2 rounded-lg text-sm font-medium transition-colors',
          hasChanges
            ? 'bg-claude-500 text-white hover:bg-claude-400'
            : 'bg-slate-700/50 text-slate-500 cursor-not-allowed',
        ]"
      >
        Save Changes
      </button>
    </div>
  </div>
</template>
