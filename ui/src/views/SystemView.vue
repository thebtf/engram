<script setup lang="ts">
import { ref, onMounted, onUnmounted } from 'vue'
import {
  fetchSystemHealth,
  fetchVectorMetrics,
  fetchGraphStats,
  fetchVaultStatus,
  triggerConsolidation,
  triggerMaintenance,
  checkForUpdate,
  type SystemHealth,
} from '@/utils/api'
import type { VectorMetrics, GraphStats } from '@/types'
import { copyToClipboard } from '@/utils/clipboard'

const health = ref<SystemHealth | null>(null)
const vectorMetrics = ref<VectorMetrics | null>(null)
const graphStats = ref<GraphStats | null>(null)
const loading = ref(false)
const error = ref<string | null>(null)

// Vault setup helper
const vaultKeyConfigured = ref<boolean | null>(null)
const vaultCopyFeedback = ref(false)
const vaultCopyError = ref(false)

// Maintenance
const consolidating = ref(false)
const maintaining = ref(false)
const maintenanceMessage = ref<string | null>(null)

// Update check
const updateChecking = ref(false)
const updateResult = ref<{ available: boolean; current_version: string; latest_version: string; release_notes?: string } | null>(null)

let abortController: AbortController | null = null

async function loadAll() {
  abortController?.abort()
  abortController = new AbortController()

  loading.value = true
  error.value = null

  const signal = abortController.signal

  try {
    const [h, v, g, vaultResult] = await Promise.allSettled([
      fetchSystemHealth(signal),
      fetchVectorMetrics(signal),
      fetchGraphStats(signal),
      fetchVaultStatus(signal),
    ])

    // Bail out if this request was superseded by a newer one.
    if (signal.aborted) return

    health.value = h.status === 'fulfilled' ? h.value : null
    vectorMetrics.value = v.status === 'fulfilled' ? v.value : null
    graphStats.value = g.status === 'fulfilled' ? g.value : null
    if (vaultResult.status === 'fulfilled') {
      vaultKeyConfigured.value = vaultResult.value.encrypted
    }

    // Surface real errors (not AbortErrors for the current signal).
    const failures = [h, v, g, vaultResult]
      .filter(r => r.status === 'rejected')
      .map(r => (r as PromiseRejectedResult).reason)
      .filter(e => !(e instanceof Error && e.name === 'AbortError'))
    if (failures.length > 0) {
      error.value = failures.map((e: unknown) => e instanceof Error ? e.message : String(e)).join('; ')
    }
  } catch (err) {
    if (err instanceof Error && err.name === 'AbortError') return
    error.value = err instanceof Error ? err.message : 'Failed to load system info'
  } finally {
    if (!signal.aborted) {
      loading.value = false
    }
  }
}

async function copyVaultSetupCommand() {
  const ok = await copyToClipboard('openssl rand -hex 32 > vault.key')
  if (ok) {
    vaultCopyFeedback.value = true
    setTimeout(() => { vaultCopyFeedback.value = false }, 2000)
  } else {
    console.warn('Failed to copy vault setup command to clipboard')
    vaultCopyError.value = true
    setTimeout(() => { vaultCopyError.value = false }, 2000)
  }
}

function statusColor(status: string): string {
  switch (status) {
    case 'healthy': return 'text-green-400'
    case 'degraded': return 'text-yellow-400'
    case 'unhealthy': return 'text-red-400'
    default: return 'text-slate-400'
  }
}

function statusBg(status: string): string {
  switch (status) {
    case 'healthy': return 'bg-green-500/10 border-green-500/30'
    case 'degraded': return 'bg-yellow-500/10 border-yellow-500/30'
    case 'unhealthy': return 'bg-red-500/10 border-red-500/30'
    default: return 'bg-slate-500/10 border-slate-500/30'
  }
}

function statusIcon(status: string): string {
  switch (status) {
    case 'healthy': return 'fa-check-circle'
    case 'degraded': return 'fa-exclamation-triangle'
    case 'unhealthy': return 'fa-times-circle'
    default: return 'fa-question-circle'
  }
}

async function handleConsolidate() {
  consolidating.value = true
  maintenanceMessage.value = null
  try {
    const result = await triggerConsolidation()
    maintenanceMessage.value = result.message || 'Consolidation started'
  } catch (err) {
    maintenanceMessage.value = err instanceof Error ? err.message : 'Failed to start consolidation'
  } finally {
    consolidating.value = false
  }
}

async function handleMaintenance() {
  maintaining.value = true
  maintenanceMessage.value = null
  try {
    const result = await triggerMaintenance()
    maintenanceMessage.value = result.message || 'Maintenance started'
  } catch (err) {
    maintenanceMessage.value = err instanceof Error ? err.message : 'Failed to start maintenance'
  } finally {
    maintaining.value = false
  }
}

async function handleCheckUpdate() {
  updateChecking.value = true
  try {
    updateResult.value = await checkForUpdate()
  } catch (err) {
    updateResult.value = null
    maintenanceMessage.value = err instanceof Error ? err.message : 'Failed to check for updates'
  } finally {
    updateChecking.value = false
  }
}

onMounted(() => {
  loadAll()
})

onUnmounted(() => {
  abortController?.abort()
})
</script>

<template>
  <div>
    <!-- Header -->
    <div class="flex items-center justify-between mb-6">
      <div class="flex items-center gap-3">
        <i class="fas fa-server text-claude-400 text-xl" />
        <h1 class="text-2xl font-bold text-white">System</h1>
        <span v-if="health" :class="['text-sm font-medium', statusColor(health.status)]">
          {{ health.status }}
        </span>
      </div>
      <button
        @click="loadAll()"
        :disabled="loading"
        class="px-3 py-1.5 rounded-lg text-sm bg-slate-800/50 border border-slate-700/50 text-slate-300 hover:text-white hover:border-claude-500/50 transition-colors disabled:opacity-50"
      >
        <i :class="['fas fa-sync-alt mr-1.5', loading && 'fa-spin']" />
        Refresh
      </button>
    </div>

    <!-- Loading -->
    <div v-if="loading && !health" class="flex items-center justify-center py-20">
      <i class="fas fa-circle-notch fa-spin text-claude-400 text-2xl" />
    </div>

    <!-- Error -->
    <div v-else-if="error && !health" class="text-center py-16">
      <i class="fas fa-exclamation-triangle text-red-400 text-3xl mb-3 block" />
      <p class="text-red-400 mb-2">{{ error }}</p>
      <button @click="loadAll()" class="text-sm text-slate-400 hover:text-white transition-colors">
        Try again
      </button>
    </div>

    <template v-else>
      <!-- Overall Health -->
      <div v-if="health" class="mb-6">
        <div class="flex items-center gap-4 mb-4">
          <span class="text-xs text-slate-500">Version: <span class="text-slate-300 font-mono">{{ health.version }}</span></span>
        </div>

        <!-- Component Health Cards -->
        <div class="grid grid-cols-2 md:grid-cols-3 lg:grid-cols-4 gap-3">
          <div
            v-for="comp in health.components"
            :key="comp.name"
            :class="[
              'p-3 rounded-xl border-2',
              statusBg(comp.status)
            ]"
          >
            <div class="flex items-center gap-2 mb-1">
              <i :class="['fas', statusIcon(comp.status), statusColor(comp.status)]" />
              <span class="text-sm font-medium text-white">{{ comp.name }}</span>
            </div>
            <div :class="['text-xs', statusColor(comp.status)]">{{ comp.status }}</div>
            <div v-if="comp.message" class="text-[10px] text-slate-500 mt-1 truncate" :title="comp.message">
              {{ comp.message }}
            </div>
            <div v-if="comp.latency_ms" class="text-[10px] text-slate-600 mt-0.5">
              {{ comp.latency_ms.toFixed(1) }}ms
            </div>
          </div>
        </div>

        <!-- Warnings -->
        <div v-if="health.warnings?.length" class="mt-4 p-3 rounded-lg bg-amber-500/10 border border-amber-500/30">
          <div v-for="(warn, idx) in health.warnings" :key="idx" class="text-xs text-amber-400">
            <i class="fas fa-triangle-exclamation mr-1" />
            {{ warn }}
          </div>
        </div>
      </div>

      <!-- Vector Metrics -->
      <div v-if="vectorMetrics" class="p-4 rounded-xl border-2 border-slate-700/50 bg-slate-800/30 mb-6">
        <h2 class="text-xs text-slate-500 uppercase tracking-wide mb-3">Vector Metrics</h2>
        <div v-if="!vectorMetrics.enabled" class="text-xs text-slate-500">
          <i class="fas fa-circle-xmark mr-1 text-slate-600" />
          {{ vectorMetrics.message || 'Vector database not available' }}
        </div>
        <template v-else>
          <div class="grid grid-cols-2 md:grid-cols-4 gap-4 text-sm">
            <div>
              <span class="text-slate-600 text-xs block">Total Queries</span>
              <span class="text-slate-300 font-mono">{{ vectorMetrics.query_count }}</span>
            </div>
            <div>
              <span class="text-slate-600 text-xs block">Avg Latency</span>
              <span class="text-slate-300 font-mono">{{ vectorMetrics.avg_latency_ms.toFixed(1) }}ms</span>
            </div>
            <div>
              <span class="text-slate-600 text-xs block">Total Documents</span>
              <span class="text-slate-300 font-mono">{{ vectorMetrics.total_documents }}</span>
            </div>
            <div>
              <span class="text-slate-600 text-xs block">Uptime</span>
              <span class="text-slate-300 font-mono">{{ vectorMetrics.uptime }}</span>
            </div>
          </div>
          <div class="text-[10px] text-slate-500 uppercase mt-3 mb-1">Latency Percentiles</div>
          <div class="grid grid-cols-2 md:grid-cols-4 gap-4 text-sm">
            <div>
              <span class="text-slate-600 text-xs block">P50 Latency</span>
              <span class="text-slate-300 font-mono">{{ vectorMetrics.p50_latency_ms.toFixed(1) }}ms</span>
            </div>
            <div>
              <span class="text-slate-600 text-xs block">P95 Latency</span>
              <span class="text-slate-300 font-mono">{{ vectorMetrics.p95_latency_ms.toFixed(1) }}ms</span>
            </div>
            <div>
              <span class="text-slate-600 text-xs block">P99 Latency</span>
              <span class="text-slate-300 font-mono">{{ vectorMetrics.p99_latency_ms.toFixed(1) }}ms</span>
            </div>
          </div>
        </template>
      </div>

      <!-- Graph Stats -->
      <div v-if="graphStats" class="p-4 rounded-xl border-2 border-slate-700/50 bg-slate-800/30 mb-6">
        <h2 class="text-xs text-slate-500 uppercase tracking-wide mb-3">Graph Stats</h2>
        <div class="grid grid-cols-2 md:grid-cols-4 gap-4 text-sm">
          <div>
            <span class="text-slate-600 text-xs block">Nodes</span>
            <span class="text-slate-300 font-mono">{{ graphStats.nodeCount }}</span>
          </div>
          <div>
            <span class="text-slate-600 text-xs block">Edges</span>
            <span class="text-slate-300 font-mono">{{ graphStats.edgeCount }}</span>
          </div>
          <div>
            <span class="text-slate-600 text-xs block">Avg Degree</span>
            <span class="text-slate-300 font-mono">{{ graphStats.avgDegree.toFixed(1) }}</span>
          </div>
          <div>
            <span class="text-slate-600 text-xs block">Max Degree</span>
            <span class="text-slate-300 font-mono">{{ graphStats.maxDegree }}</span>
          </div>
        </div>
        <div v-if="graphStats.edgeTypes" class="mt-3">
          <span class="text-[10px] text-slate-600 uppercase">Edge Types:</span>
          <div class="flex flex-wrap gap-2 mt-1">
            <span
              v-for="(count, type) in graphStats.edgeTypes"
              :key="type"
              class="text-[10px] px-2 py-0.5 rounded-full bg-slate-700/50 text-slate-400 border border-slate-600/50"
            >
              {{ type }}: {{ count }}
            </span>
          </div>
        </div>
      </div>

      <!-- Vault Encryption Setup (shown when key is not configured) -->
      <div
        v-if="vaultKeyConfigured === false"
        class="p-4 rounded-xl border-2 border-yellow-500/30 bg-yellow-500/5 mb-6"
      >
        <h2 class="text-xs text-yellow-500 uppercase tracking-wide mb-1 flex items-center gap-1.5">
          <i class="fas fa-triangle-exclamation" />
          Vault Encryption Setup
        </h2>
        <p class="text-xs text-slate-400 mb-3">
          Vault encryption is not configured. Credentials are stored unencrypted.
          Set up a master key to enable AES-256-GCM encryption.
        </p>
        <div class="space-y-2 mb-3">
          <div>
            <span class="text-[10px] text-slate-500 uppercase tracking-wide block mb-1">1. Generate a key</span>
            <code class="block px-3 py-1.5 rounded bg-slate-900/70 border border-slate-700/50 text-xs font-mono text-green-400 select-all">
              openssl rand -hex 32 &gt; vault.key
            </code>
          </div>
          <div>
            <span class="text-[10px] text-slate-500 uppercase tracking-wide block mb-1">2. Set environment variable</span>
            <code class="block px-3 py-1.5 rounded bg-slate-900/70 border border-slate-700/50 text-xs font-mono text-slate-300 select-all">
              ENGRAM_ENCRYPTION_KEY_FILE=/path/to/vault.key
            </code>
          </div>
        </div>
        <button
          @click="copyVaultSetupCommand"
          class="px-3 py-1.5 rounded-lg text-xs bg-slate-800/50 border border-slate-700/50 text-slate-300 hover:text-white hover:border-yellow-500/50 transition-colors"
        >
          <i :class="['fas mr-1.5', vaultCopyFeedback ? 'fa-check text-green-400' : vaultCopyError ? 'fa-times text-red-400' : 'fa-copy']" />
          {{ vaultCopyFeedback ? 'Copied!' : vaultCopyError ? 'Copy failed' : 'Copy command' }}
        </button>
      </div>

      <!-- Maintenance Controls -->
      <div class="p-4 rounded-xl border-2 border-slate-700/50 bg-slate-800/30 mb-6">
        <h2 class="text-xs text-slate-500 uppercase tracking-wide mb-3">Maintenance</h2>
        <p class="text-xs text-slate-500 mb-3">Background processes to optimize storage and knowledge quality.</p>
        <div class="flex flex-wrap items-center gap-3">
          <button
            @click="handleConsolidate"
            :disabled="consolidating"
            title="Runs decay scoring, association discovery, and forgetting cycles to consolidate knowledge"
            class="px-4 py-2 rounded-lg text-sm bg-slate-800/50 border border-slate-700/50 text-slate-300 hover:text-white hover:border-claude-500/50 transition-colors disabled:opacity-50"
          >
            <i :class="['fas mr-1.5', consolidating ? 'fa-circle-notch fa-spin' : 'fa-compress']" />
            Run Consolidation
          </button>
          <button
            @click="handleMaintenance"
            :disabled="maintaining"
            title="Runs cleanup of old observations, database optimization, prompt pruning, and pattern decay"
            class="px-4 py-2 rounded-lg text-sm bg-slate-800/50 border border-slate-700/50 text-slate-300 hover:text-white hover:border-claude-500/50 transition-colors disabled:opacity-50"
          >
            <i :class="['fas mr-1.5', maintaining ? 'fa-circle-notch fa-spin' : 'fa-wrench']" />
            Run Maintenance
          </button>
          <button
            @click="handleCheckUpdate"
            :disabled="updateChecking"
            class="px-4 py-2 rounded-lg text-sm bg-slate-800/50 border border-slate-700/50 text-slate-300 hover:text-white hover:border-claude-500/50 transition-colors disabled:opacity-50"
          >
            <i :class="['fas mr-1.5', updateChecking ? 'fa-circle-notch fa-spin' : 'fa-cloud-arrow-down']" />
            Check for Updates
          </button>
        </div>

        <!-- Maintenance message -->
        <div v-if="maintenanceMessage" class="mt-3 p-2 rounded-lg bg-slate-900/50 text-xs text-slate-400">
          <i class="fas fa-info-circle mr-1 text-claude-400" />
          {{ maintenanceMessage }}
        </div>

        <!-- Update result -->
        <div v-if="updateResult" class="mt-3 p-3 rounded-lg border" :class="updateResult.available ? 'bg-green-500/10 border-green-500/30' : 'bg-slate-900/50 border-slate-700/50'">
          <div v-if="updateResult.available" class="text-sm">
            <span class="text-green-400 font-medium">
              <i class="fas fa-arrow-up mr-1" />
              Update available: {{ updateResult.latest_version }}
            </span>
            <span class="text-slate-500 ml-2">(current: {{ updateResult.current_version }})</span>
            <p v-if="updateResult.release_notes" class="text-xs text-slate-400 mt-2 whitespace-pre-wrap">
              {{ updateResult.release_notes }}
            </p>
          </div>
          <div v-else class="text-sm text-slate-400">
            <i class="fas fa-check mr-1 text-green-400" />
            You are on the latest version ({{ updateResult.current_version }})
          </div>
        </div>
      </div>
    </template>
  </div>
</template>
