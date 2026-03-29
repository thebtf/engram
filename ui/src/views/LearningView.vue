<script setup lang="ts">
import { ref, computed, onMounted, onUnmounted } from 'vue'
import {
  fetchLearningCurve,
  fetchStrategies,
  fetchEffectivenessDistribution,
  type LearningCurvePoint,
  type StrategyRow,
} from '@/utils/api'

const loading = ref(false)
const error = ref<string | null>(null)

const curvePoints = ref<LearningCurvePoint[]>([])
const strategies = ref<StrategyRow[]>([])
const selectedDays = ref(30)

// Effectiveness distribution counters (populated from server-side aggregation)
const effGreen = ref(0)
const effYellow = ref(0)
const effRed = ref(0)
const effGray = ref(0)
const effTotal = ref(0)

let abortController: AbortController | null = null

async function loadAll() {
  abortController?.abort()
  abortController = new AbortController()
  loading.value = true
  error.value = null

  try {
    const [curveData, strategiesData, distData] = await Promise.all([
      fetchLearningCurve(selectedDays.value, undefined, abortController.signal).catch(() => ({ data_points: [] })),
      fetchStrategies(abortController.signal).catch(() => ({ strategies: [] })),
      fetchEffectivenessDistribution(abortController.signal).catch(() => ({ high: 0, medium: 0, low: 0, insufficient: 0, total: 0 })),
    ])

    curvePoints.value = curveData.data_points || []
    strategies.value = strategiesData.strategies || []

    effGreen.value = distData.high
    effYellow.value = distData.medium
    effRed.value = distData.low
    effGray.value = distData.insufficient
    effTotal.value = distData.total
  } catch (err) {
    if (err instanceof Error && err.name === 'AbortError') return
    error.value = err instanceof Error ? err.message : 'Failed to load learning data'
  } finally {
    loading.value = false
  }
}

onMounted(() => loadAll())
onUnmounted(() => abortController?.abort())

function effPct(n: number): number {
  if (effTotal.value === 0) return 0
  return Math.round((n / effTotal.value) * 100)
}

// Learning curve: compute max for bar scaling
const curveMax = computed(() => {
  return curvePoints.value.reduce((max, p) => Math.max(max, p.sessions), 1)
})

function outcomeColor(rate: number): string {
  if (rate >= 0.7) return 'text-green-400'
  if (rate >= 0.4) return 'text-yellow-400'
  return 'text-red-400'
}
</script>

<template>
  <div class="space-y-6">
    <!-- Header -->
    <div class="flex items-center justify-between">
      <div>
        <h1 class="text-xl font-bold text-white">Learning Dashboard</h1>
        <p class="text-sm text-slate-400 mt-0.5">Closed-loop learning metrics — injection effectiveness and session outcomes</p>
      </div>
      <div class="flex items-center gap-3">
        <select
          v-model="selectedDays"
          @change="loadAll"
          class="bg-slate-800 border border-slate-700 text-slate-300 text-sm rounded-lg px-3 py-1.5 focus:outline-none focus:border-claude-500"
        >
          <option :value="7">Last 7 days</option>
          <option :value="14">Last 14 days</option>
          <option :value="30">Last 30 days</option>
          <option :value="90">Last 90 days</option>
        </select>
        <button
          @click="loadAll"
          :disabled="loading"
          class="px-3 py-1.5 rounded-lg text-sm bg-slate-800 border border-slate-700 text-slate-300 hover:text-white hover:bg-slate-700 transition-colors disabled:opacity-50"
        >
          <i :class="['fas fa-sync-alt text-xs mr-1.5', loading ? 'fa-spin' : '']" />
          Refresh
        </button>
      </div>
    </div>

    <!-- Error banner -->
    <div v-if="error" class="bg-red-500/10 border border-red-500/30 rounded-lg px-4 py-3 text-red-400 text-sm">
      <i class="fas fa-circle-exclamation mr-2" />{{ error }}
    </div>

    <!-- Loading skeleton -->
    <div v-if="loading && !curvePoints.length" class="flex items-center justify-center py-20 text-slate-500">
      <i class="fas fa-spinner fa-spin mr-2" />Loading learning data...
    </div>

    <template v-else>
      <!-- Section 1: Effectiveness Distribution -->
      <div class="bg-slate-900/60 border border-slate-800 rounded-xl p-5">
        <h2 class="text-sm font-semibold text-slate-300 mb-4 flex items-center gap-2">
          <i class="fas fa-circle-dot text-orange-400" />
          Effectiveness Distribution
        </h2>

        <div v-if="effTotal === 0" class="text-slate-500 text-sm">No observations with effectiveness data yet.</div>
        <div v-else class="space-y-3">
          <!-- Bar row: green -->
          <div class="flex items-center gap-3">
            <span class="w-2 h-2 rounded-full bg-green-500 flex-shrink-0" />
            <span class="text-xs text-slate-400 w-28 flex-shrink-0">High (≥ 70%)</span>
            <div class="flex-1 h-2 bg-slate-800 rounded-full overflow-hidden">
              <div
                class="h-full bg-green-500 rounded-full transition-all duration-500"
                :style="{ width: effPct(effGreen) + '%' }"
              />
            </div>
            <span class="text-xs font-mono text-slate-300 w-16 text-right flex-shrink-0">
              {{ effGreen }} <span class="text-slate-500">({{ effPct(effGreen) }}%)</span>
            </span>
          </div>

          <!-- Bar row: yellow -->
          <div class="flex items-center gap-3">
            <span class="w-2 h-2 rounded-full bg-yellow-500 flex-shrink-0" />
            <span class="text-xs text-slate-400 w-28 flex-shrink-0">Medium (≥ 40%)</span>
            <div class="flex-1 h-2 bg-slate-800 rounded-full overflow-hidden">
              <div
                class="h-full bg-yellow-500 rounded-full transition-all duration-500"
                :style="{ width: effPct(effYellow) + '%' }"
              />
            </div>
            <span class="text-xs font-mono text-slate-300 w-16 text-right flex-shrink-0">
              {{ effYellow }} <span class="text-slate-500">({{ effPct(effYellow) }}%)</span>
            </span>
          </div>

          <!-- Bar row: red -->
          <div class="flex items-center gap-3">
            <span class="w-2 h-2 rounded-full bg-red-500 flex-shrink-0" />
            <span class="text-xs text-slate-400 w-28 flex-shrink-0">Low (&lt; 40%)</span>
            <div class="flex-1 h-2 bg-slate-800 rounded-full overflow-hidden">
              <div
                class="h-full bg-red-500 rounded-full transition-all duration-500"
                :style="{ width: effPct(effRed) + '%' }"
              />
            </div>
            <span class="text-xs font-mono text-slate-300 w-16 text-right flex-shrink-0">
              {{ effRed }} <span class="text-slate-500">({{ effPct(effRed) }}%)</span>
            </span>
          </div>

          <!-- Bar row: gray -->
          <div class="flex items-center gap-3">
            <span class="w-2 h-2 rounded-full bg-slate-500 flex-shrink-0" />
            <span class="text-xs text-slate-400 w-28 flex-shrink-0">Insufficient data</span>
            <div class="flex-1 h-2 bg-slate-800 rounded-full overflow-hidden">
              <div
                class="h-full bg-slate-500 rounded-full transition-all duration-500"
                :style="{ width: effPct(effGray) + '%' }"
              />
            </div>
            <span class="text-xs font-mono text-slate-300 w-16 text-right flex-shrink-0">
              {{ effGray }} <span class="text-slate-500">({{ effPct(effGray) }}%)</span>
            </span>
          </div>

          <p class="text-xs text-slate-500 mt-2">{{ effTotal }} total observations evaluated</p>
        </div>
      </div>

      <!-- Section 2: Learning Curve -->
      <div class="bg-slate-900/60 border border-slate-800 rounded-xl p-5">
        <h2 class="text-sm font-semibold text-slate-300 mb-4 flex items-center gap-2">
          <i class="fas fa-chart-line text-orange-400" />
          Learning Curve (last {{ selectedDays }} days)
        </h2>

        <div v-if="curvePoints.length === 0" class="text-slate-500 text-sm">
          No session outcome data for this period yet.
        </div>
        <div v-else class="space-y-1.5">
          <!-- Mini bar chart: one row per day -->
          <div
            v-for="point in curvePoints"
            :key="point.date"
            class="flex items-center gap-3 group"
          >
            <span class="text-[11px] text-slate-500 w-20 flex-shrink-0 font-mono">{{ point.date }}</span>
            <div class="flex-1 h-2 bg-slate-800 rounded-full overflow-hidden">
              <div
                class="h-full rounded-full transition-all duration-500"
                :class="point.outcome_rate >= 0.7 ? 'bg-green-500' : point.outcome_rate >= 0.4 ? 'bg-yellow-500' : 'bg-orange-500'"
                :style="{ width: (point.sessions / curveMax * 100) + '%' }"
              />
            </div>
            <span class="text-[11px] font-mono w-10 flex-shrink-0 text-right" :class="outcomeColor(point.outcome_rate)">
              {{ (point.outcome_rate * 100).toFixed(0) }}%
            </span>
            <span class="text-[11px] text-slate-500 w-16 flex-shrink-0 text-right">
              {{ point.successes }}/{{ point.sessions }}
            </span>
          </div>

          <div class="flex gap-4 mt-3 pt-3 border-t border-slate-800">
            <span class="text-xs text-slate-500">Bar width = session volume</span>
            <span class="text-xs text-slate-500">Color = outcome rate</span>
          </div>
        </div>
      </div>

      <!-- Section 3: Strategy Comparison -->
      <div class="bg-slate-900/60 border border-slate-800 rounded-xl p-5">
        <h2 class="text-sm font-semibold text-slate-300 mb-4 flex items-center gap-2">
          <i class="fas fa-flask text-orange-400" />
          Strategy Comparison
        </h2>

        <div v-if="strategies.length === 0" class="text-slate-500 text-sm">
          No injection strategy data yet. Strategies are recorded when sessions complete.
        </div>
        <table v-else class="w-full text-sm">
          <thead>
            <tr class="text-left">
              <th class="text-xs font-medium text-slate-500 pb-2 pr-4">Strategy</th>
              <th class="text-xs font-medium text-slate-500 pb-2 pr-4 text-right">Sessions</th>
              <th class="text-xs font-medium text-slate-500 pb-2 pr-4 text-right">Successes</th>
              <th class="text-xs font-medium text-slate-500 pb-2 text-right">Outcome Rate</th>
            </tr>
          </thead>
          <tbody class="divide-y divide-slate-800">
            <tr
              v-for="row in strategies"
              :key="row.name"
              class="hover:bg-slate-800/30 transition-colors"
            >
              <td class="py-2 pr-4 text-slate-300 font-mono text-xs">{{ row.name }}</td>
              <td class="py-2 pr-4 text-right text-slate-400 text-xs">{{ row.sessions }}</td>
              <td class="py-2 pr-4 text-right text-slate-400 text-xs">{{ row.successes }}</td>
              <td class="py-2 text-right">
                <span
                  class="text-xs font-semibold"
                  :class="outcomeColor(row.outcome_rate)"
                >
                  {{ (row.outcome_rate * 100).toFixed(1) }}%
                </span>
              </td>
            </tr>
          </tbody>
        </table>
      </div>
    </template>
  </div>
</template>
