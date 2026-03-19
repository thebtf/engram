<script setup lang="ts">
import { useStats, useTimeline, useHealth } from '@/composables'
import StatsCards from '@/components/StatsCards.vue'
import Sidebar from '@/components/Sidebar.vue'
import FilterTabs from '@/components/FilterTabs.vue'
import Timeline from '@/components/Timeline.vue'
import { useSSE } from '@/composables'

const { queueDepth } = useSSE()
const { health } = useHealth()

const {
  filteredItems,
  loading,
  observationCount,
  promptCount,
  summaryCount,
  currentFilter,
  currentProject,
  currentTypeFilter,
  currentConceptFilter,
  setFilter,
  setProject,
  setTypeFilter,
  setConceptFilter,
} = useTimeline()

const { stats } = useStats(currentProject)
</script>

<template>
  <div>
    <!-- Stats Cards -->
    <StatsCards :stats="stats" :queue-depth="queueDepth" />

    <!-- Two Column Layout -->
    <div class="flex gap-6">
      <!-- Info Sidebar -->
      <Sidebar
        :stats="stats"
        :observation-count="observationCount"
        :prompt-count="promptCount"
        :summary-count="summaryCount"
        :current-project="currentProject"
        :health="health"
        @update:project="setProject"
      />

      <!-- Activity Timeline Section -->
      <section class="flex-1 min-w-0">
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
  </div>
</template>
