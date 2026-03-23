<script setup lang="ts">
import { useStats, useTimeline } from '@/composables'
import StatsCards from '@/components/StatsCards.vue'
import FilterTabs from '@/components/FilterTabs.vue'
import Timeline from '@/components/Timeline.vue'

const {
  filteredItems,
  loading,
  observationCount,
  promptCount,
  currentFilter,
  currentProject,
  currentTypeFilter,
  currentConceptFilter,
  setFilter,
  setTypeFilter,
  setConceptFilter,
} = useTimeline()

const { stats } = useStats(currentProject)
</script>

<template>
  <div>
    <!-- Stats Cards -->
    <StatsCards :stats="stats" :observation-count="observationCount" />

    <!-- Activity Timeline Section -->
    <section>
      <div class="flex items-center gap-3 mb-4">
        <i class="fas fa-list text-claude-400" />
        <h2 class="text-lg font-semibold text-white">Activity Timeline</h2>
      </div>

      <FilterTabs
        :current-filter="currentFilter"
        :current-type-filter="currentTypeFilter"
        :current-concept-filter="currentConceptFilter"
        :observation-count="observationCount"
        :prompt-count="promptCount"
        @update:filter="setFilter"
        @update:type-filter="setTypeFilter"
        @update:concept-filter="setConceptFilter"
      />

      <Timeline :items="filteredItems" :loading="loading" />
    </section>
  </div>
</template>
