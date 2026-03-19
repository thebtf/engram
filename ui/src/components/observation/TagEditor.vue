<script setup lang="ts">
import { ref, computed } from 'vue'
import { CONCEPT_CONFIG, type ConceptType } from '@/types/observation'
import { updateObservationTags } from '@/utils/api'
import Badge from '@/components/Badge.vue'

const props = defineProps<{
  observationId: number
  concepts: string[]
}>()

const emit = defineEmits<{
  update: [concepts: string[]]
}>()

const newTag = ref('')
const updating = ref(false)
const error = ref<string | null>(null)

function getConceptConfig(concept: string) {
  return CONCEPT_CONFIG[concept as ConceptType] || { icon: 'fa-tag', colorClass: 'text-slate-300', bgClass: 'bg-slate-500/20', borderClass: 'border-slate-500/40' }
}

// Pre-compute concept configs to avoid repeated calls per render cycle.
const conceptConfigs = computed(() =>
  Object.fromEntries(props.concepts.map(c => [c, getConceptConfig(c)]))
)

async function addTag() {
  const tag = newTag.value.trim().toLowerCase()
  if (!tag || props.concepts.includes(tag)) {
    newTag.value = ''
    return
  }

  updating.value = true
  error.value = null

  try {
    const result = await updateObservationTags(props.observationId, 'add', tag)
    emit('update', result.concepts)
    newTag.value = ''
  } catch (err) {
    error.value = err instanceof Error ? err.message : 'Failed to add tag'
  } finally {
    updating.value = false
  }
}

async function removeTag(tag: string) {
  updating.value = true
  error.value = null

  try {
    const result = await updateObservationTags(props.observationId, 'remove', tag)
    emit('update', result.concepts)
  } catch (err) {
    error.value = err instanceof Error ? err.message : 'Failed to remove tag'
  } finally {
    updating.value = false
  }
}
</script>

<template>
  <div>
    <h2 class="text-xs text-slate-500 uppercase tracking-wide mb-3">Concepts</h2>

    <!-- Existing tags -->
    <div class="flex flex-wrap gap-1.5 mb-3">
      <span
        v-for="concept in concepts"
        :key="concept"
        class="inline-flex items-center gap-1 group"
      >
        <Badge
          :icon="conceptConfigs[concept].icon"
          :color-class="conceptConfigs[concept].colorClass"
          :bg-class="conceptConfigs[concept].bgClass"
          :border-class="conceptConfigs[concept].borderClass"
        >
          {{ concept }}
          <button
            @click="removeTag(concept)"
            :disabled="updating"
            class="ml-0.5 opacity-0 group-hover:opacity-100 hover:text-red-400 transition-all"
            title="Remove"
          >
            <i class="fas fa-times text-[8px]" />
          </button>
        </Badge>
      </span>
      <span v-if="concepts.length === 0" class="text-xs text-slate-600">No concepts</span>
    </div>

    <!-- Add tag input -->
    <div class="flex items-center gap-2">
      <input
        v-model="newTag"
        type="text"
        placeholder="Add concept..."
        :disabled="updating"
        class="px-2 py-1 rounded-lg bg-slate-900/50 border border-slate-700/50 text-xs text-white placeholder-slate-600 focus:outline-none focus:ring-1 focus:ring-claude-500/50 w-40"
        @keydown.enter="addTag"
      />
      <button
        @click="addTag"
        :disabled="updating || !newTag.trim()"
        class="px-2 py-1 rounded-lg text-xs bg-slate-800/50 border border-slate-700/50 text-slate-400 hover:text-white hover:border-claude-500/50 transition-colors disabled:opacity-50"
      >
        <i class="fas fa-plus mr-0.5" />
        Add
      </button>
    </div>

    <div v-if="error" class="mt-2 text-xs text-red-400">{{ error }}</div>
  </div>
</template>
