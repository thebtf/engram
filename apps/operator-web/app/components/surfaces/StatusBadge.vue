<script setup lang="ts">
import { computed } from 'vue'
import type { SurfaceTone } from '~/utils/navigation'

const props = withDefaults(defineProps<{
  tone: SurfaceTone
  label?: string
  compact?: boolean
}>(), {
  label: undefined,
  compact: false,
})

const resolvedLabel = computed(() => {
  if (props.label) return props.label

  switch (props.tone) {
    case 'live':
      return 'live'
    case 'must-build':
      return 'must-build'
    case 'dormant':
      return 'dormant'
    case 'stale':
      return 'stale'
  }
})

const classes = computed(() => {
  switch (props.tone) {
    case 'live':
      return 'border-[color:rgba(52,211,153,0.4)] bg-[color:rgba(52,211,153,0.12)] text-[var(--success)]'
    case 'must-build':
      return 'border-[color:rgba(167,139,250,0.4)] bg-[color:rgba(167,139,250,0.12)] text-[var(--must-build)]'
    case 'dormant':
      return 'border-[color:rgba(251,191,36,0.4)] bg-[color:rgba(251,191,36,0.12)] text-[var(--warn)]'
    case 'stale':
      return 'border-[color:rgba(107,114,128,0.4)] bg-[color:rgba(107,114,128,0.12)] text-[var(--stale)]'
  }
})
</script>

<template>
  <span
    class="inline-flex items-center rounded-full border font-semibold uppercase tracking-[0.04em]"
    :class="[
      classes,
      compact ? 'px-2 py-0.5 text-[10px]' : 'px-2.5 py-1 text-[10px]',
    ]"
  >
    {{ resolvedLabel }}
  </span>
</template>
