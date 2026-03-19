<script setup lang="ts">
import { ref, onMounted, onUnmounted, nextTick } from 'vue'
import { useRouter } from 'vue-router'
import { Network } from 'vis-network'
import { DataSet } from 'vis-data'
import { TYPE_CONFIG } from '@/types/observation'
import type { ObservationType } from '@/types'
import { fetchObservationGraph, fetchGraphStats, fetchProjects } from '@/utils/api'
import type { GraphStats, RelationGraph } from '@/types'
import EmptyState from '@/components/layout/EmptyState.vue'

const router = useRouter()

const graphContainer = ref<HTMLElement | null>(null)
const graphStats = ref<GraphStats | null>(null)
const loading = ref(false)
const error = ref<string | null>(null)
const observationIdInput = ref('')
const projects = ref<string[]>([])

let network: Network | null = null

// Type-based node colors
const TYPE_COLORS: Record<string, { background: string; border: string }> = {
  bugfix: { background: '#ef4444', border: '#dc2626' },
  feature: { background: '#a855f7', border: '#9333ea' },
  refactor: { background: '#3b82f6', border: '#2563eb' },
  change: { background: '#64748b', border: '#475569' },
  discovery: { background: '#06b6d4', border: '#0891b2' },
  decision: { background: '#eab308', border: '#ca8a04' },
}

async function loadGraphStats() {
  loading.value = true
  error.value = null
  try {
    graphStats.value = await fetchGraphStats()
  } catch (err) {
    error.value = err instanceof Error ? err.message : 'Failed to load graph stats'
  } finally {
    loading.value = false
  }
}

async function loadGraph(observationId: number) {
  loading.value = true
  error.value = null

  try {
    const graph: RelationGraph = await fetchObservationGraph(observationId, 2)
    await nextTick()
    renderGraph(graph, observationId)
  } catch (err) {
    error.value = err instanceof Error ? err.message : 'Failed to load graph'
  } finally {
    loading.value = false
  }
}

function renderGraph(graph: RelationGraph, centerId: number) {
  if (!graphContainer.value) return

  // Collect unique nodes from relations
  const nodeMap = new Map<number, { id: number; label: string; type: string }>()
  for (const r of graph.relations) {
    if (!nodeMap.has(r.relation.source_id)) {
      nodeMap.set(r.relation.source_id, {
        id: r.relation.source_id,
        label: r.source_title || `#${r.relation.source_id}`,
        type: r.source_type,
      })
    }
    if (!nodeMap.has(r.relation.target_id)) {
      nodeMap.set(r.relation.target_id, {
        id: r.relation.target_id,
        label: r.target_title || `#${r.relation.target_id}`,
        type: r.target_type,
      })
    }
  }

  // Ensure center node exists
  if (!nodeMap.has(centerId)) {
    nodeMap.set(centerId, { id: centerId, label: `#${centerId}`, type: 'change' })
  }

  const nodes = new DataSet(
    Array.from(nodeMap.values()).map(n => ({
      id: n.id,
      label: n.label.length > 30 ? n.label.slice(0, 27) + '...' : n.label,
      title: n.label,
      color: {
        background: TYPE_COLORS[n.type]?.background || '#64748b',
        border: TYPE_COLORS[n.type]?.border || '#475569',
        highlight: {
          background: '#d4a0ff',
          border: '#a855f7',
        },
      },
      borderWidth: n.id === centerId ? 3 : 1,
      size: n.id === centerId ? 30 : 20,
      font: { color: '#e2e8f0', size: 11 },
    }))
  )

  const edges = new DataSet(
    graph.relations.map((r, i) => ({
      id: i,
      from: r.relation.source_id,
      to: r.relation.target_id,
      label: r.relation.relation_type.replace(/_/g, ' '),
      arrows: 'to',
      color: { color: '#475569', highlight: '#a855f7' },
      font: { color: '#64748b', size: 9, strokeWidth: 0 },
      width: Math.max(1, r.relation.confidence * 3),
    }))
  )

  // Destroy existing network
  if (network) {
    network.destroy()
    network = null
  }

  network = new Network(graphContainer.value, { nodes, edges }, {
    physics: {
      stabilization: { iterations: 100 },
      barnesHut: { gravitationalConstant: -3000, springLength: 150 },
    },
    interaction: {
      hover: true,
      tooltipDelay: 200,
    },
    nodes: {
      shape: 'dot',
    },
    edges: {
      smooth: { type: 'continuous' },
    },
  })

  // Click handler: navigate to observation detail
  network.on('click', (params) => {
    if (params.nodes.length > 0) {
      const nodeId = params.nodes[0]
      router.push({ name: 'observation-detail', params: { id: nodeId } })
    }
  })
}

function handleExplore() {
  const id = parseInt(observationIdInput.value, 10)
  if (isNaN(id) || id <= 0) return
  loadGraph(id)
}

async function loadProjects() {
  try {
    projects.value = await fetchProjects()
  } catch {
    // Non-critical
  }
}

onMounted(() => {
  loadGraphStats()
  loadProjects()
})

onUnmounted(() => {
  if (network) {
    network.destroy()
    network = null
  }
})
</script>

<template>
  <div class="flex flex-col h-full">
    <!-- Header -->
    <div class="flex items-center justify-between mb-4">
      <div class="flex items-center gap-3">
        <i class="fas fa-diagram-project text-claude-400 text-xl" />
        <h1 class="text-2xl font-bold text-white">Knowledge Graph</h1>
      </div>

      <!-- Observation ID input -->
      <div class="flex items-center gap-2">
        <input
          v-model="observationIdInput"
          type="number"
          placeholder="Observation ID"
          class="px-3 py-1.5 rounded-lg bg-slate-800/50 border border-slate-700/50 text-sm text-white placeholder-slate-600 focus:outline-none focus:ring-1 focus:ring-claude-500/50 w-36"
          @keydown.enter="handleExplore"
        />
        <button
          @click="handleExplore"
          :disabled="loading || !observationIdInput"
          class="px-3 py-1.5 rounded-lg text-sm bg-claude-500 text-white hover:bg-claude-400 transition-colors disabled:opacity-50"
        >
          <i class="fas fa-search mr-1.5" />
          Explore
        </button>
      </div>
    </div>

    <!-- Graph Stats (when no graph loaded) -->
    <div v-if="graphStats && !graphContainer?.querySelector('canvas')" class="grid grid-cols-2 md:grid-cols-4 gap-4 mb-4">
      <div class="p-3 rounded-xl border-2 border-slate-700/50 bg-gradient-to-br from-slate-800/50 to-slate-900/50">
        <span class="text-xs text-slate-500 block">Nodes</span>
        <span class="text-lg font-bold text-white font-mono">{{ graphStats.nodeCount }}</span>
      </div>
      <div class="p-3 rounded-xl border-2 border-slate-700/50 bg-gradient-to-br from-slate-800/50 to-slate-900/50">
        <span class="text-xs text-slate-500 block">Edges</span>
        <span class="text-lg font-bold text-white font-mono">{{ graphStats.edgeCount }}</span>
      </div>
      <div class="p-3 rounded-xl border-2 border-slate-700/50 bg-gradient-to-br from-slate-800/50 to-slate-900/50">
        <span class="text-xs text-slate-500 block">Avg Degree</span>
        <span class="text-lg font-bold text-white font-mono">{{ graphStats.avgDegree.toFixed(1) }}</span>
      </div>
      <div class="p-3 rounded-xl border-2 border-slate-700/50 bg-gradient-to-br from-slate-800/50 to-slate-900/50">
        <span class="text-xs text-slate-500 block">Max Degree</span>
        <span class="text-lg font-bold text-white font-mono">{{ graphStats.maxDegree }}</span>
      </div>
    </div>

    <!-- Type Legend -->
    <div class="flex items-center gap-3 mb-4 flex-wrap">
      <span class="text-[10px] text-slate-600 uppercase">Legend:</span>
      <div v-for="(config, type) in TYPE_CONFIG" :key="type" class="flex items-center gap-1">
        <span class="w-2.5 h-2.5 rounded-full" :style="{ backgroundColor: TYPE_COLORS[type]?.background || '#64748b' }" />
        <span class="text-[10px] text-slate-500">{{ type }}</span>
      </div>
    </div>

    <!-- Loading -->
    <div v-if="loading" class="flex items-center justify-center py-20">
      <i class="fas fa-circle-notch fa-spin text-claude-400 text-2xl" />
    </div>

    <!-- Error -->
    <div v-else-if="error" class="text-center py-16">
      <i class="fas fa-exclamation-triangle text-red-400 text-3xl mb-3 block" />
      <p class="text-red-400 mb-2">{{ error }}</p>
    </div>

    <!-- Graph container -->
    <div
      ref="graphContainer"
      class="flex-1 rounded-xl border-2 border-slate-700/50 bg-slate-950/50 min-h-[500px]"
    >
      <EmptyState
        v-if="!loading && !error"
        icon="fa-diagram-project"
        title="Select an observation to explore"
        description="Enter an observation ID above to visualize its relationship graph."
      />
    </div>
  </div>
</template>
