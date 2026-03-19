<script setup lang="ts">
import { ref, watch, nextTick, onMounted } from 'vue'
import { useLogs, type LogLevel } from '@/composables/useLogs'

const {
  filteredEntries,
  connected,
  paused,
  enabledLevels,
  searchText,
  connect,
  togglePause,
  toggleLevel,
  clearEntries,
  LOG_LEVELS,
} = useLogs()

const logContainer = ref<HTMLElement | null>(null)
const autoScroll = ref(true)

const LEVEL_COLORS: Record<LogLevel, { text: string; bg: string; border: string }> = {
  trace: { text: 'text-slate-500', bg: 'bg-slate-500/10', border: 'border-slate-500/30' },
  debug: { text: 'text-blue-400', bg: 'bg-blue-500/10', border: 'border-blue-500/30' },
  info: { text: 'text-green-400', bg: 'bg-green-500/10', border: 'border-green-500/30' },
  warn: { text: 'text-yellow-400', bg: 'bg-yellow-500/10', border: 'border-yellow-500/30' },
  error: { text: 'text-red-400', bg: 'bg-red-500/10', border: 'border-red-500/30' },
  fatal: { text: 'text-red-300', bg: 'bg-red-500/20', border: 'border-red-500/50' },
}

function scrollToBottom() {
  if (logContainer.value) {
    logContainer.value.scrollTop = logContainer.value.scrollHeight
  }
}

function handleScroll() {
  if (!logContainer.value) return
  const { scrollTop, scrollHeight, clientHeight } = logContainer.value
  autoScroll.value = scrollHeight - scrollTop - clientHeight < 50
}

// Auto-scroll when new entries arrive
watch(
  () => filteredEntries.value.length,
  () => {
    if (autoScroll.value) {
      nextTick(scrollToBottom)
    }
  }
)

function formatTimestamp(ts: string): string {
  try {
    const d = new Date(ts)
    return d.toLocaleTimeString('en-US', { hour12: false, hour: '2-digit', minute: '2-digit', second: '2-digit' })
      + '.' + String(d.getMilliseconds()).padStart(3, '0')
  } catch {
    return ts
  }
}

onMounted(() => {
  connect()
})
</script>

<template>
  <div class="flex flex-col h-full">
    <!-- Header -->
    <div class="flex items-center justify-between mb-4">
      <div class="flex items-center gap-3">
        <i class="fas fa-terminal text-claude-400 text-xl" />
        <h1 class="text-2xl font-bold text-white">Logs</h1>
        <span :class="[
          'flex items-center gap-1.5 px-2 py-0.5 rounded-full text-[10px] font-medium border',
          connected
            ? 'bg-green-500/10 text-green-400 border-green-500/30'
            : 'bg-red-500/10 text-red-400 border-red-500/30'
        ]">
          <span :class="['w-1.5 h-1.5 rounded-full', connected ? 'bg-green-400' : 'bg-red-400']" />
          {{ connected ? 'Connected' : 'Disconnected' }}
        </span>
      </div>

      <div class="flex items-center gap-2">
        <button
          @click="togglePause()"
          :class="[
            'px-3 py-1.5 rounded-lg text-sm border transition-colors',
            paused
              ? 'bg-amber-500/20 text-amber-300 border-amber-500/30'
              : 'bg-slate-800/50 text-slate-300 border-slate-700/50 hover:text-white hover:border-claude-500/50'
          ]"
        >
          <i :class="['fas mr-1.5', paused ? 'fa-play' : 'fa-pause']" />
          {{ paused ? 'Resume' : 'Pause' }}
        </button>
        <button
          v-if="!autoScroll"
          @click="autoScroll = true; scrollToBottom()"
          class="px-3 py-1.5 rounded-lg text-sm bg-claude-500/20 text-claude-300 border border-claude-500/30 hover:bg-claude-500/30 transition-colors"
        >
          <i class="fas fa-arrow-down mr-1.5" />
          Jump to latest
        </button>
        <button
          @click="clearEntries()"
          class="px-3 py-1.5 rounded-lg text-sm bg-slate-800/50 border border-slate-700/50 text-slate-400 hover:text-white hover:border-slate-600 transition-colors"
        >
          <i class="fas fa-eraser mr-1.5" />
          Clear
        </button>
      </div>
    </div>

    <!-- Filters -->
    <div class="flex items-center gap-3 mb-4">
      <!-- Level filter buttons -->
      <div class="flex items-center gap-1">
        <button
          v-for="level in LOG_LEVELS"
          :key="level"
          @click="toggleLevel(level)"
          :class="[
            'px-2 py-1 rounded text-[10px] font-medium uppercase border transition-colors',
            enabledLevels.has(level)
              ? `${LEVEL_COLORS[level].bg} ${LEVEL_COLORS[level].text} ${LEVEL_COLORS[level].border}`
              : 'bg-slate-800/20 text-slate-600 border-slate-700/30 hover:text-slate-400'
          ]"
        >
          {{ level }}
        </button>
      </div>

      <!-- Search -->
      <div class="relative flex-1 max-w-xs">
        <i class="fas fa-search absolute left-2.5 top-1/2 -translate-y-1/2 text-slate-600 text-xs" />
        <input
          v-model="searchText"
          type="text"
          placeholder="Filter logs..."
          class="w-full pl-8 pr-3 py-1.5 rounded-lg bg-slate-800/50 border border-slate-700/50 text-xs text-slate-200 placeholder-slate-600 focus:outline-none focus:ring-1 focus:ring-claude-500/50"
        />
      </div>

      <span class="text-xs text-slate-600">{{ filteredEntries.length }} entries</span>
    </div>

    <!-- Log entries -->
    <div
      ref="logContainer"
      @scroll="handleScroll"
      class="flex-1 overflow-y-auto rounded-xl border-2 border-slate-700/50 bg-slate-950/80 font-mono text-xs p-3 min-h-[400px]"
    >
      <div v-if="filteredEntries.length === 0" class="flex items-center justify-center h-full text-slate-600">
        <span v-if="connected">Waiting for log entries...</span>
        <span v-else>Not connected to log stream</span>
      </div>
      <div
        v-for="entry in filteredEntries"
        :key="entry.id"
        class="flex items-start gap-2 py-0.5 hover:bg-slate-800/30 px-1 rounded"
      >
        <span class="text-slate-600 whitespace-nowrap flex-shrink-0">{{ formatTimestamp(entry.timestamp) }}</span>
        <span
          :class="[
            'px-1.5 py-0 rounded text-[10px] font-bold uppercase flex-shrink-0 min-w-[3rem] text-center',
            LEVEL_COLORS[entry.level]?.bg || 'bg-slate-500/10',
            LEVEL_COLORS[entry.level]?.text || 'text-slate-400',
          ]"
        >
          {{ entry.level }}
        </span>
        <span class="text-slate-300 break-all">{{ entry.message }}</span>
      </div>
    </div>
  </div>
</template>
