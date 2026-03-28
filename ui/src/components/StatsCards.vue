<script setup lang="ts">
import type { Stats } from '@/types'
import { formatUptime } from '@/utils/formatters'
import { computed } from 'vue'

const props = defineProps<{
  stats: Stats | null
  observationCount: number
}>()

const uptime = computed(() => {
  if (!props.stats?.uptime) return '-'
  return formatUptime(props.stats.uptime)
})

// Use the total count from the stats API when available (reflects actual DB total,
// not the page size fetched by the timeline). Fall back to the timeline prop when
// the stats endpoint hasn't returned yet.
const displayObservationCount = computed(() =>
  props.stats?.observationCount ?? props.observationCount
)

const status = computed(() => {
  if (!props.stats) return 'Loading'
  if (props.stats.isProcessing) return 'Processing'
  if (props.stats.activeSessions > 0) return 'Active'
  return 'Idle'
})

const statusColor = computed(() => {
  if (status.value === 'Processing') return 'text-yellow-400'
  if (status.value === 'Active') return 'text-green-400'
  return 'text-slate-400'
})
</script>

<template>
  <div class="grid grid-cols-2 xl:grid-cols-4 gap-4 mb-6">
    <!-- Uptime -->
    <div class="glass rounded-xl p-4 border border-white/10">
      <div class="flex items-center justify-between">
        <div>
          <p class="text-xs text-slate-400 uppercase tracking-wide">Uptime</p>
          <p class="text-2xl font-bold text-claude-400">{{ uptime }}</p>
        </div>
        <div class="w-10 h-10 rounded-full bg-claude-500/20 flex items-center justify-center">
          <i class="fas fa-clock text-claude-400" />
        </div>
      </div>
    </div>

    <!-- Active Sessions -->
    <div class="glass rounded-xl p-4 border border-white/10">
      <div class="flex items-center justify-between">
        <div>
          <p class="text-xs text-slate-400 uppercase tracking-wide">Sessions Today</p>
          <p class="text-2xl font-bold text-blue-400">{{ stats?.sessionsToday ?? stats?.activeSessions ?? 0 }}</p>
        </div>
        <div class="w-10 h-10 rounded-full bg-blue-500/20 flex items-center justify-center">
          <i class="fas fa-terminal text-blue-400" />
        </div>
      </div>
    </div>

    <!-- Observations -->
    <div class="glass rounded-xl p-4 border border-white/10">
      <div class="flex items-center justify-between">
        <div>
          <p class="text-xs text-slate-400 uppercase tracking-wide">Observations</p>
          <p class="text-2xl font-bold text-purple-400">{{ displayObservationCount }}</p>
        </div>
        <div class="w-10 h-10 rounded-full bg-purple-500/20 flex items-center justify-center">
          <i class="fas fa-database text-purple-400" />
        </div>
      </div>
    </div>

    <!-- Status -->
    <div class="glass rounded-xl p-4 border border-white/10">
      <div class="flex items-center justify-between">
        <div>
          <p class="text-xs text-slate-400 uppercase tracking-wide">Status</p>
          <p class="text-2xl font-bold" :class="statusColor">{{ status }}</p>
        </div>
        <div class="w-10 h-10 rounded-full bg-green-500/20 flex items-center justify-center">
          <i class="fas fa-signal text-green-400" />
        </div>
      </div>
    </div>
  </div>
</template>
