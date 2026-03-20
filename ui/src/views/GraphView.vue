<script setup lang="ts">
import { ref, onMounted, onUnmounted, nextTick, computed } from 'vue'
import { useRouter } from 'vue-router'
import { Network } from 'vis-network'
import { DataSet } from 'vis-data'
import { TYPE_CONFIG } from '@/types/observation'
import type { ObservationType } from '@/types/observation'
import { fetchTopObservations, fetchObservationRelations, fetchProjects } from '@/utils/api'
import type { RelationWithDetails } from '@/types'
import EmptyState from '@/components/layout/EmptyState.vue'

const router = useRouter()

const graphContainer = ref<HTMLElement | null>(null)
const loading = ref(false)
const error = ref<string | null>(null)
const projects = ref<string[]>([])
const selectedProject = ref<string>('')

// Graph data
const nodeCount = ref(0)
const edgeCount = ref(0)
const relationTypeCounts = ref<Record<string, number>>({})
const hoveredNode = ref<{ id: number; title: string; type: string; score: number; x: number; y: number } | null>(null)

// Filter state
const relationTypes = ref<string[]>([])
const enabledRelationTypes = ref<Set<string>>(new Set())

let network: Network | null = null

// Type-based node colors (matching TYPE_CONFIG palette)
const TYPE_COLORS: Record<string, { background: string; border: string; highlight: { background: string; border: string } }> = {
  bugfix: { background: '#ef4444', border: '#dc2626', highlight: { background: '#f87171', border: '#ef4444' } },
  feature: { background: '#a855f7', border: '#9333ea', highlight: { background: '#c084fc', border: '#a855f7' } },
  refactor: { background: '#3b82f6', border: '#2563eb', highlight: { background: '#60a5fa', border: '#3b82f6' } },
  change: { background: '#64748b', border: '#475569', highlight: { background: '#94a3b8', border: '#64748b' } },
  discovery: { background: '#06b6d4', border: '#0891b2', highlight: { background: '#22d3ee', border: '#06b6d4' } },
  decision: { background: '#eab308', border: '#ca8a04', highlight: { background: '#facc15', border: '#eab308' } },
}

const hasGraph = computed(() => nodeCount.value > 0)

async function loadGraph() {
  loading.value = true
  error.value = null
  hoveredNode.value = null

  try {
    // Step 1: Fetch top observations
    const observations = await fetchTopObservations(selectedProject.value || undefined, 50)
    if (observations.length === 0) {
      nodeCount.value = 0
      edgeCount.value = 0
      loading.value = false
      return
    }

    // Step 2: Fetch relations for each observation (in parallel batches)
    const allRelations: RelationWithDetails[] = []
    const batchSize = 10
    for (let i = 0; i < observations.length; i += batchSize) {
      const batch = observations.slice(i, i + batchSize)
      const results = await Promise.allSettled(
        batch.map(obs => fetchObservationRelations(obs.id))
      )
      for (const result of results) {
        if (result.status === 'fulfilled' && result.value) {
          allRelations.push(...result.value)
        }
      }
    }

    // Collect all relation types for filter
    const typeSet = new Set<string>()
    const typeCounts: Record<string, number> = {}
    for (const rel of allRelations) {
      const rt = rel.relation.relation_type
      typeSet.add(rt)
      typeCounts[rt] = (typeCounts[rt] || 0) + 1
    }
    relationTypes.value = Array.from(typeSet).sort()
    relationTypeCounts.value = typeCounts

    // Enable all relation types by default
    if (enabledRelationTypes.value.size === 0) {
      enabledRelationTypes.value = new Set(typeSet)
    }

    // Step 3: Build nodes from observations + relations
    const nodeMap = new Map<number, { id: number; label: string; type: string; score: number }>()
    for (const obs of observations) {
      nodeMap.set(obs.id, {
        id: obs.id,
        label: obs.title || `#${obs.id}`,
        type: obs.type,
        score: obs.importance_score,
      })
    }

    // Add nodes from relations that might not be in the top observations
    for (const r of allRelations) {
      if (!nodeMap.has(r.relation.source_id)) {
        nodeMap.set(r.relation.source_id, {
          id: r.relation.source_id,
          label: r.source_title || `#${r.relation.source_id}`,
          type: r.source_type,
          score: 0.5,
        })
      }
      if (!nodeMap.has(r.relation.target_id)) {
        nodeMap.set(r.relation.target_id, {
          id: r.relation.target_id,
          label: r.target_title || `#${r.relation.target_id}`,
          type: r.target_type,
          score: 0.5,
        })
      }
    }

    // Filter relations by enabled types
    const filteredRelations = allRelations.filter(
      r => enabledRelationTypes.value.has(r.relation.relation_type)
    )

    // Deduplicate edges (same source->target->type)
    const edgeKey = (r: RelationWithDetails) =>
      `${r.relation.source_id}-${r.relation.target_id}-${r.relation.relation_type}`
    const seenEdges = new Set<string>()
    const uniqueRelations: RelationWithDetails[] = []
    for (const r of filteredRelations) {
      const key = edgeKey(r)
      if (!seenEdges.has(key)) {
        seenEdges.add(key)
        uniqueRelations.push(r)
      }
    }

    nodeCount.value = nodeMap.size
    edgeCount.value = uniqueRelations.length

    // Set loading false BEFORE rendering so the container gets its final dimensions
    loading.value = false
    await nextTick()

    // Double nextTick: first tick updates DOM (loading overlay removed),
    // second tick ensures layout is fully computed before vis-network measures container
    await nextTick()
    renderGraph(nodeMap, uniqueRelations)
  } catch (err) {
    error.value = err instanceof Error ? err.message : 'Failed to load graph'
    loading.value = false
  }
}

function renderGraph(
  nodeMap: Map<number, { id: number; label: string; type: string; score: number }>,
  relations: RelationWithDetails[]
) {
  if (!graphContainer.value) return

  // Ensure container has dimensions — vis-network needs non-zero size
  const rect = graphContainer.value.getBoundingClientRect()
  if (rect.width === 0 || rect.height === 0) {
    console.warn('[GraphView] Container has zero dimensions, forcing min size', rect)
    graphContainer.value.style.minHeight = '500px'
    graphContainer.value.style.minWidth = '400px'
  }

  const nodes = new DataSet(
    Array.from(nodeMap.values()).map(n => {
      const colors = TYPE_COLORS[n.type] || TYPE_COLORS.change
      // Scale node size by importance score (min 15, max 40)
      const size = 15 + Math.round(n.score * 25)
      return {
        id: n.id,
        label: n.label.length > 25 ? n.label.slice(0, 22) + '...' : n.label,
        title: `${n.label}\nType: ${n.type}\nScore: ${n.score.toFixed(2)}`,
        color: {
          background: colors.background,
          border: colors.border,
          highlight: colors.highlight,
        },
        size,
        font: { color: '#e2e8f0', size: 11 },
        // Store extra data for hover tooltip
        _type: n.type,
        _score: n.score,
        _fullTitle: n.label,
      }
    })
  )

  const edges = new DataSet(
    relations.map((r, i) => ({
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
      stabilization: { iterations: 150 },
      barnesHut: { gravitationalConstant: -4000, springLength: 180, springConstant: 0.04 },
    },
    interaction: {
      hover: true,
      tooltipDelay: 200,
    },
    nodes: {
      shape: 'dot',
    },
    edges: {
      smooth: { enabled: true, type: 'curvedCW', roundness: 0.2 },
    },
  })

  // Click handler: navigate to observation detail
  network.on('click', (params) => {
    if (params.nodes.length > 0) {
      const nodeId = params.nodes[0]
      router.push({ name: 'observation-detail', params: { id: nodeId } })
    }
  })

  // Hover handler: show floating tooltip
  network.on('hoverNode', (params) => {
    const nodeId = params.node
    const node = nodeMap.get(nodeId)
    if (node && network) {
      const canvasPos = network.getPosition(nodeId)
      const domPos = network.canvasToDOM(canvasPos)
      hoveredNode.value = {
        id: node.id,
        title: node.label,
        type: node.type,
        score: node.score,
        x: domPos.x,
        y: domPos.y,
      }
    }
  })

  network.on('blurNode', () => {
    hoveredNode.value = null
  })
}

function toggleRelationType(type: string) {
  const updated = new Set(enabledRelationTypes.value)
  if (updated.has(type)) {
    updated.delete(type)
  } else {
    updated.add(type)
  }
  enabledRelationTypes.value = updated
  loadGraph()
}

function handleProjectChange() {
  enabledRelationTypes.value = new Set()
  loadGraph()
}

async function loadProjects() {
  try {
    projects.value = await fetchProjects()
  } catch {
    // Non-critical
  }
}

onMounted(() => {
  loadProjects()
  loadGraph()
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

      <!-- Project filter -->
      <div class="flex items-center gap-2">
        <select
          v-model="selectedProject"
          @change="handleProjectChange"
          class="px-3 py-1.5 rounded-lg bg-slate-800/50 border border-slate-700/50 text-sm text-white focus:outline-none focus:ring-1 focus:ring-claude-500/50"
        >
          <option value="">All projects</option>
          <option v-for="p in projects" :key="p" :value="p">{{ p }}</option>
        </select>
        <button
          @click="loadGraph"
          :disabled="loading"
          class="px-3 py-1.5 rounded-lg text-sm bg-claude-500 text-white hover:bg-claude-400 transition-colors disabled:opacity-50"
        >
          <i :class="['fas fa-sync-alt mr-1.5', loading && 'fa-spin']" />
          Refresh
        </button>
      </div>
    </div>

    <!-- Graph stats summary -->
    <div v-if="hasGraph" class="grid grid-cols-2 md:grid-cols-4 gap-4 mb-4">
      <div class="p-3 rounded-xl border-2 border-slate-700/50 bg-gradient-to-br from-slate-800/50 to-slate-900/50">
        <span class="text-xs text-slate-500 block">Nodes</span>
        <span class="text-lg font-bold text-white font-mono">{{ nodeCount }}</span>
      </div>
      <div class="p-3 rounded-xl border-2 border-slate-700/50 bg-gradient-to-br from-slate-800/50 to-slate-900/50">
        <span class="text-xs text-slate-500 block">Edges</span>
        <span class="text-lg font-bold text-white font-mono">{{ edgeCount }}</span>
      </div>
      <div
        v-for="(count, rtype) in relationTypeCounts"
        :key="rtype"
        class="p-3 rounded-xl border-2 border-slate-700/50 bg-gradient-to-br from-slate-800/50 to-slate-900/50"
      >
        <span class="text-xs text-slate-500 block capitalize">{{ String(rtype).replace(/_/g, ' ') }}</span>
        <span class="text-lg font-bold text-white font-mono">{{ count }}</span>
      </div>
    </div>

    <div class="flex gap-4 flex-1 min-h-0">
      <!-- Filter sidebar -->
      <div v-if="relationTypes.length > 0" class="w-48 flex-shrink-0 space-y-4">
        <!-- Type Legend -->
        <div class="rounded-xl border-2 border-slate-700/50 bg-gradient-to-br from-slate-800/50 to-slate-900/50 p-3">
          <span class="text-[10px] text-slate-500 uppercase block mb-2">Node Types</span>
          <div class="space-y-1">
            <div v-for="(_config, type) in TYPE_CONFIG" :key="type" class="flex items-center gap-1.5">
              <span
                class="w-2.5 h-2.5 rounded-full flex-shrink-0"
                :style="{ backgroundColor: TYPE_COLORS[type as string]?.background || '#64748b' }"
              />
              <span class="text-[11px] text-slate-400 capitalize">{{ type }}</span>
            </div>
          </div>
        </div>

        <!-- Relation type filter -->
        <div class="rounded-xl border-2 border-slate-700/50 bg-gradient-to-br from-slate-800/50 to-slate-900/50 p-3">
          <span class="text-[10px] text-slate-500 uppercase block mb-2">Relation Types</span>
          <div class="space-y-1">
            <label
              v-for="rt in relationTypes"
              :key="rt"
              class="flex items-center gap-1.5 cursor-pointer group"
            >
              <input
                type="checkbox"
                :checked="enabledRelationTypes.has(rt)"
                @change="toggleRelationType(rt)"
                class="w-3 h-3 rounded border-slate-600 bg-slate-800 text-claude-500 focus:ring-0"
              />
              <span class="text-[11px] text-slate-400 group-hover:text-white transition-colors capitalize">
                {{ rt.replace(/_/g, ' ') }}
              </span>
            </label>
          </div>
        </div>
      </div>

      <!-- Main graph area -->
      <div class="flex-1 relative">
        <!-- Loading -->
        <div v-if="loading" class="absolute inset-0 flex items-center justify-center z-10 bg-slate-950/50 rounded-xl">
          <i class="fas fa-circle-notch fa-spin text-claude-400 text-2xl" />
        </div>

        <!-- Error -->
        <div v-else-if="error" class="text-center py-16">
          <i class="fas fa-exclamation-triangle text-red-400 text-3xl mb-3 block" />
          <p class="text-red-400 mb-2">{{ error }}</p>
          <button @click="loadGraph" class="text-sm text-slate-400 hover:text-white transition-colors">
            Try again
          </button>
        </div>

        <!-- Empty state -->
        <EmptyState
          v-else-if="!hasGraph && !loading"
          icon="fa-diagram-project"
          title="No relations found"
          description="New observations will build the graph automatically."
        />

        <!-- Graph container -->
        <div
          v-show="hasGraph && !loading && !error"
          ref="graphContainer"
          class="w-full h-full rounded-xl border-2 border-slate-700/50 bg-slate-950/50 min-h-[500px]"
        />

        <!-- Floating tooltip on hover -->
        <div
          v-if="hoveredNode"
          class="absolute z-20 pointer-events-none px-3 py-2 rounded-lg border border-slate-600 bg-slate-800/95 shadow-xl"
          :style="{ left: hoveredNode.x + 'px', top: (hoveredNode.y - 60) + 'px', transform: 'translateX(-50%)' }"
        >
          <div class="text-xs font-medium text-white truncate max-w-[200px]">{{ hoveredNode.title }}</div>
          <div class="flex items-center gap-2 mt-0.5">
            <span
              class="inline-flex items-center gap-1 px-1.5 py-0.5 rounded text-[10px]"
              :class="[
                TYPE_CONFIG[hoveredNode.type as ObservationType]?.bgClass || 'bg-slate-500/20',
                TYPE_CONFIG[hoveredNode.type as ObservationType]?.colorClass || 'text-slate-300'
              ]"
            >
              <i :class="['fas', TYPE_CONFIG[hoveredNode.type as ObservationType]?.icon || 'fa-pen']" />
              {{ hoveredNode.type }}
            </span>
            <span class="text-[10px] text-slate-500">Score: {{ hoveredNode.score.toFixed(2) }}</span>
          </div>
        </div>
      </div>
    </div>
  </div>
</template>
