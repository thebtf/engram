<script setup lang="ts">
import { ref, computed, watch } from 'vue'

defineProps<{
  modelValue?: string
}>()

const emit = defineEmits<{
  'update:modelValue': [value: string | undefined]
}>()

type Preset = 'today' | '7d' | '30d' | 'all'

const selected = ref<Preset>('7d')

const presets: { value: Preset; label: string }[] = [
  { value: 'today', label: 'Today' },
  { value: '7d', label: '7 days' },
  { value: '30d', label: '30 days' },
  { value: 'all', label: 'All time' },
]

function computeSince(preset: Preset): string | undefined {
  const now = new Date()
  switch (preset) {
    case 'today': {
      // Start of today in user's local timezone, serialized as UTC ISO8601
      const start = new Date(now.getFullYear(), now.getMonth(), now.getDate())
      return start.toISOString()
    }
    case '7d':
      return new Date(now.getTime() - 7 * 24 * 60 * 60 * 1000).toISOString()
    case '30d':
      return new Date(now.getTime() - 30 * 24 * 60 * 60 * 1000).toISOString()
    case 'all':
      return undefined
  }
}

const sinceValue = computed(() => computeSince(selected.value))

watch(selected, () => {
  emit('update:modelValue', sinceValue.value)
}, { immediate: true })

function select(preset: Preset) {
  selected.value = preset
}
</script>

<template>
  <div class="inline-flex rounded-lg border border-slate-700/50 bg-slate-800/50 p-0.5">
    <button
      v-for="p in presets"
      :key="p.value"
      @click="select(p.value)"
      :class="[
        'px-3 py-1 rounded-md text-xs font-medium transition-colors',
        selected === p.value
          ? 'bg-claude-500 text-white'
          : 'text-slate-400 hover:text-white'
      ]"
    >
      {{ p.label }}
    </button>
  </div>
</template>
