<script setup lang="ts">
import { computed } from 'vue'

const props = defineProps<{
  total: number
  limit: number
  offset: number
}>()

const emit = defineEmits<{
  'update:offset': [offset: number]
}>()

const totalPages = computed(() => Math.max(1, Math.ceil(props.total / props.limit)))
const currentPage = computed(() => Math.floor(props.offset / props.limit) + 1)

const showingFrom = computed(() => (props.total === 0 ? 0 : props.offset + 1))
const showingTo = computed(() => Math.min(props.offset + props.limit, props.total))

// Generate visible page numbers with ellipsis
const visiblePages = computed(() => {
  const pages: (number | '...')[] = []
  const total = totalPages.value
  const current = currentPage.value

  if (total <= 7) {
    for (let i = 1; i <= total; i++) pages.push(i)
    return pages
  }

  pages.push(1)

  if (current > 3) {
    pages.push('...')
  }

  const start = Math.max(2, current - 1)
  const end = Math.min(total - 1, current + 1)

  for (let i = start; i <= end; i++) {
    pages.push(i)
  }

  if (current < total - 2) {
    pages.push('...')
  }

  pages.push(total)

  return pages
})

function goToPage(page: number) {
  const newOffset = (page - 1) * props.limit
  emit('update:offset', newOffset)
}
</script>

<template>
  <div v-if="total > limit" class="flex items-center justify-between gap-4 text-sm">
    <!-- Showing X of Y -->
    <span class="text-slate-500">
      Showing {{ showingFrom }}&ndash;{{ showingTo }} of {{ total }}
    </span>

    <!-- Page controls -->
    <div class="flex items-center gap-1">
      <!-- Previous -->
      <button
        class="px-2 py-1 rounded text-slate-400 hover:text-white hover:bg-slate-800/50 transition-colors disabled:opacity-30 disabled:cursor-not-allowed"
        :disabled="currentPage <= 1"
        @click="goToPage(currentPage - 1)"
      >
        <i class="fas fa-chevron-left text-xs" />
      </button>

      <!-- Page numbers -->
      <template v-for="(page, idx) in visiblePages" :key="idx">
        <span v-if="page === '...'" class="px-2 py-1 text-slate-600">...</span>
        <button
          v-else
          :class="[
            'px-2.5 py-1 rounded transition-colors',
            page === currentPage
              ? 'bg-claude-500/20 text-claude-400 font-medium'
              : 'text-slate-400 hover:text-white hover:bg-slate-800/50',
          ]"
          @click="goToPage(page)"
        >
          {{ page }}
        </button>
      </template>

      <!-- Next -->
      <button
        class="px-2 py-1 rounded text-slate-400 hover:text-white hover:bg-slate-800/50 transition-colors disabled:opacity-30 disabled:cursor-not-allowed"
        :disabled="currentPage >= totalPages"
        @click="goToPage(currentPage + 1)"
      >
        <i class="fas fa-chevron-right text-xs" />
      </button>
    </div>
  </div>
</template>
