<script setup lang="ts">
defineProps<{
  icon: string
  title: string
  description?: string
}>()
</script>

<template>
  <div class="flex flex-col items-center justify-center py-16 text-center">
    <i :class="['fas', icon, 'text-4xl text-slate-600 mb-4']" />
    <h3 class="text-lg font-medium text-slate-400 mb-1">{{ title }}</h3>
    <p v-if="description" class="text-sm text-slate-500 max-w-xs">{{ description }}</p>
    <slot />
  </div>
</template>
