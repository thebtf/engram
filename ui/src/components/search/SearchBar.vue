<script setup lang="ts">
import { ref, onMounted, onUnmounted } from 'vue'

const props = defineProps<{
  modelValue?: string
  compact?: boolean
  placeholder?: string
}>()

const emit = defineEmits<{
  'update:modelValue': [value: string]
  submit: [query: string]
}>()

const inputRef = ref<HTMLInputElement | null>(null)
const isFocused = ref(false)
const localValue = ref(props.modelValue || '')

function handleSubmit() {
  const q = localValue.value.trim()
  if (q) {
    emit('submit', q)
    emit('update:modelValue', q)
  }
}

function handleInput(event: Event) {
  const value = (event.target as HTMLInputElement).value
  localValue.value = value
  emit('update:modelValue', value)
}

// Global "/" hotkey to focus search
function handleKeydown(e: KeyboardEvent) {
  if (e.key === '/' && !isFocused.value) {
    const target = e.target as HTMLElement
    // Don't intercept if user is typing in an input/textarea
    if (target.tagName === 'INPUT' || target.tagName === 'TEXTAREA' || target.isContentEditable) {
      return
    }
    e.preventDefault()
    inputRef.value?.focus()
  }
  // Escape to blur
  if (e.key === 'Escape' && isFocused.value) {
    inputRef.value?.blur()
  }
}

onMounted(() => {
  document.addEventListener('keydown', handleKeydown)
})

onUnmounted(() => {
  document.removeEventListener('keydown', handleKeydown)
})
</script>

<template>
  <form @submit.prevent="handleSubmit" :class="compact ? 'max-w-lg' : 'w-full'">
    <div class="relative">
      <i class="fas fa-magnifying-glass absolute left-3 top-1/2 -translate-y-1/2 text-slate-500 text-sm" />
      <input
        ref="inputRef"
        :value="localValue"
        @input="handleInput"
        @focus="isFocused = true"
        @blur="isFocused = false"
        type="text"
        :placeholder="placeholder || 'Search observations...  /'"
        :class="[
          'w-full pl-9 pr-4 rounded-lg bg-slate-800/50 border text-white placeholder-slate-500 text-sm focus:outline-none focus:ring-2 focus:ring-claude-500/50 focus:border-claude-500 transition-colors',
          compact ? 'py-2 border-slate-700/50' : 'py-3 border-slate-600/50',
        ]"
      />
      <kbd
        v-if="!isFocused && !localValue"
        class="absolute right-3 top-1/2 -translate-y-1/2 px-1.5 py-0.5 text-[10px] font-mono text-slate-600 bg-slate-800 border border-slate-700 rounded"
      >
        /
      </kbd>
    </div>
  </form>
</template>
