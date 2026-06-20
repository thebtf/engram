<script setup lang="ts">
import StatusBadge from '~/components/surfaces/StatusBadge.vue'
import type { SurfaceTone } from '~/utils/navigation'

defineProps<{
  title: string
  tone: SurfaceTone
  summary: string
  nextStep: string
  seams?: string[]
}>()
</script>

<template>
  <section class="surface-panel p-5">
    <div class="mb-3 flex flex-wrap items-center gap-3">
      <h1 class="text-xl font-semibold">{{ title }}</h1>
      <StatusBadge :tone="tone" />
    </div>

    <p class="mb-4 max-w-3xl text-sm leading-6 text-[var(--fg-2)]">
      {{ summary }}
    </p>

    <div class="surface-panel-warm p-4">
      <div class="mb-2 text-xs uppercase tracking-[0.08em] text-[var(--muted)]">
        Next seam
      </div>
      <div class="text-sm text-[var(--fg)]">
        {{ nextStep }}
      </div>
    </div>

    <div v-if="seams?.length" class="mt-4">
      <div class="mb-2 text-xs uppercase tracking-[0.08em] text-[var(--muted)]">
        Runtime seams
      </div>
      <ul class="space-y-2 text-sm text-[var(--fg-2)]">
        <li v-for="seam in seams" :key="seam" class="surface-panel-warm px-3 py-2">
          {{ seam }}
        </li>
      </ul>
    </div>
  </section>
</template>
