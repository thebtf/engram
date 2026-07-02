<script setup lang="ts">
import { computed, reactive, ref, watch } from 'vue'
import {
  GRAPH_DEFAULT_PATH_DEPTH,
  GRAPH_DEFAULT_TRAVERSE_DEPTH,
  GRAPH_EDGE_TYPES,
  GRAPH_NODE_TYPES,
  useOperatorGraph,
  type GraphMutationError,
  type OperatorGraphEdge,
  type OperatorGraphNode,
} from '../composables/useOperatorGraph'

const { t } = useI18n()
const {
  projects,
  selectedProject,
  nodes,
  connectedEdges,
  selectedNodeID,
  nodesState,
  edgesState,
  traverseResults,
  traverseBusy,
  traverseError,
  pathResult,
  pathBusy,
  pathError,
  lastMutationError,
  refresh,
  refreshConnectedEdges,
  createNode,
  createEdge,
  deleteEdge,
  deleteNode,
  traverseMemory,
  findPath,
} = useOperatorGraph()

const nodeForm = reactive({
  nodeType: GRAPH_NODE_TYPES[2],
  externalRef: '',
  privacyScope: 'project',
})

const edgeForm = reactive({
  sourceNodeID: '',
  targetNodeID: '',
  edgeType: GRAPH_EDGE_TYPES[0],
  reasoning: '',
})

const traverseForm = reactive({
  memoryID: '',
  depth: GRAPH_DEFAULT_TRAVERSE_DEPTH,
})

const pathForm = reactive({
  sourceID: '',
  targetID: '',
  maxDepth: GRAPH_DEFAULT_PATH_DEPTH,
})

const notice = ref<{ kind: 'success' | 'error'; text: string } | null>(null)
const confirmDeleteEdgeID = ref<string | null>(null)
const confirmDeleteNode = ref(false)
const deleteCascade = ref(false)

const selectedNode = computed(() => nodes.find((node) => node.id === selectedNodeID.value) || null)
const nodeIndex = computed<Record<string, OperatorGraphNode>>(() => Object.fromEntries(nodes.map((node) => [node.id, node])))
const graphHonesty = computed(() => nodesState.value.kind === 'gated' ? 'dormant' : 'live')
const graphDisabled = computed(() => nodesState.value.kind === 'gated' || nodesState.value.kind === 'pending')
const writeDisabled = graphDisabled
const selectedDegree = computed(() => connectedEdges.length)
const typedError = computed(() => lastMutationError.value)
const projectOptions = computed(() => projects.length ? projects : (selectedProject.value ? [selectedProject.value] : []))

watch(selectedProject, () => {
  confirmDeleteEdgeID.value = null
  confirmDeleteNode.value = false
  notice.value = null
  void refresh()
})

watch(selectedNodeID, (next) => {
  confirmDeleteEdgeID.value = null
  confirmDeleteNode.value = false
  deleteCascade.value = false
  if (next) {
    edgeForm.sourceNodeID = next
  }
  void refreshConnectedEdges(next)
})

function enumLabel(group: 'nodeTypes' | 'edgeTypes' | 'privacy', value: string) {
  const key = `graphPage.${group}.${value}`
  const label = t(key)
  return label === key ? value : label
}

function nodeDisplay(node: OperatorGraphNode) {
  return `${enumLabel('nodeTypes', node.nodeType)} · ${node.externalRef}`
}

function endpointLabel(edge: OperatorGraphEdge, side: 'source' | 'target') {
  const type = side === 'source' ? edge.sourceType : edge.targetType
  if (type === 'node') {
    const id = side === 'source' ? edge.nodeSourceID : edge.nodeTargetID
    const node = id ? nodeIndex.value[id] : undefined
    return node ? nodeDisplay(node) : t('graphPage.unknownNode', { id: id || '—' })
  }
  const id = side === 'source' ? edge.sourceID : edge.targetID
  return t('graphPage.memoryRef', { id: id || '—' })
}

function edgeDisplay(edge: OperatorGraphEdge) {
  return `${endpointLabel(edge, 'source')} → ${enumLabel('edgeTypes', edge.edgeType)} → ${endpointLabel(edge, 'target')}`
}

function stateMessage() {
  switch (nodesState.value.kind) {
    case 'pending':
      return t('graphPage.state.pending')
    case 'error':
      return t('graphPage.state.error', { message: nodesState.value.error.message })
    case 'empty':
      return t('graphPage.state.empty')
    case 'gated':
      return t('graphPage.state.gated', { flag: nodesState.value.flag })
    default:
      return t('graphPage.state.live', { count: nodes.length })
  }
}

function mutationErrorText(error: GraphMutationError) {
  const key = `graphPage.errors.codes.${error.code}`
  const label = t(key, { message: error.message })
  return label === key ? error.message : label
}

function clearNotice() {
  notice.value = null
}

async function submitCreateNode() {
  if (!selectedProject.value || !nodeForm.externalRef.trim()) {
    notice.value = { kind: 'error', text: t('graphPage.notices.invalidNode') }
    return
  }
  const result = await createNode({
    nodeType: nodeForm.nodeType,
    externalRef: nodeForm.externalRef.trim(),
    project: selectedProject.value,
    privacyScope: nodeForm.privacyScope,
  })
  if (result.ok) {
    nodeForm.externalRef = ''
    notice.value = { kind: 'success', text: t('graphPage.notices.nodeCreated') }
    return
  }
  notice.value = { kind: 'error', text: mutationErrorText(result.error!) }
}

async function submitCreateEdge() {
  if (!edgeForm.sourceNodeID || !edgeForm.targetNodeID) {
    notice.value = { kind: 'error', text: t('graphPage.notices.invalidEdge') }
    return
  }
  const result = await createEdge({
    sourceNodeID: edgeForm.sourceNodeID,
    targetNodeID: edgeForm.targetNodeID,
    edgeType: edgeForm.edgeType,
    reasoning: edgeForm.reasoning.trim(),
  })
  if (result.ok) {
    edgeForm.targetNodeID = ''
    edgeForm.reasoning = ''
    notice.value = { kind: 'success', text: t('graphPage.notices.edgeCreated') }
    return
  }
  notice.value = { kind: 'error', text: mutationErrorText(result.error!) }
}

async function requestDeleteEdge(edgeID: string) {
  if (confirmDeleteEdgeID.value !== edgeID) {
    confirmDeleteEdgeID.value = edgeID
    notice.value = null
    return
  }
  const result = await deleteEdge(edgeID)
  confirmDeleteEdgeID.value = null
  if (result.ok) {
    notice.value = { kind: 'success', text: t('graphPage.notices.edgeDeleted') }
    return
  }
  notice.value = { kind: 'error', text: mutationErrorText(result.error!) }
}

async function requestDeleteNode() {
  if (!selectedNode.value) return
  if (!confirmDeleteNode.value) {
    confirmDeleteNode.value = true
    notice.value = null
    return
  }
  const result = await deleteNode({ nodeID: selectedNode.value.id, cascade: deleteCascade.value })
  confirmDeleteNode.value = false
  if (result.ok) {
    notice.value = { kind: 'success', text: t('graphPage.notices.nodeDeleted') }
    return
  }
  notice.value = { kind: 'error', text: mutationErrorText(result.error!) }
}

async function runTraverse() {
  if (!traverseForm.memoryID.trim()) {
    notice.value = { kind: 'error', text: t('graphPage.notices.invalidTraverse') }
    return
  }
  clearNotice()
  await traverseMemory(traverseForm.memoryID.trim(), traverseForm.depth)
}

async function runFindPath() {
  if (!pathForm.sourceID.trim() || !pathForm.targetID.trim()) {
    notice.value = { kind: 'error', text: t('graphPage.notices.invalidPath') }
    return
  }
  clearNotice()
  await findPath(pathForm.sourceID.trim(), pathForm.targetID.trim(), pathForm.maxDepth)
}
</script>

<template>
  <div class="graph-page">
    <header class="page-head">
      <div>
        <h1>{{ t('graphPage.title') }}</h1>
        <p>{{ t('graphPage.subtitle') }}</p>
      </div>
      <HonestyBadge :cls="graphHonesty" :evidence="nodesState.kind === 'gated' ? 'ENGRAM_GRAPH_ENABLED' : '/api/graph/*'" />
    </header>

    <section class="graph-brief">
      <article class="metric">
        <span>{{ t('graphPage.metrics.nodes') }}</span>
        <b>{{ nodes.length }}</b>
      </article>
      <article class="metric">
        <span>{{ t('graphPage.metrics.edges') }}</span>
        <b>{{ connectedEdges.length }}</b>
      </article>
      <article class="metric">
        <span>{{ t('graphPage.metrics.selectedDegree') }}</span>
        <b>{{ selectedDegree }}</b>
      </article>
      <article class="brief-copy">
        <strong>{{ t('graphPage.brief.title') }}</strong>
        <span>{{ t('graphPage.brief.body') }}</span>
      </article>
    </section>

    <div class="ops">
      <div class="ops-left">
        <label>
          <span>{{ t('graphPage.filters.project') }}</span>
          <select v-model="selectedProject" class="select">
            <option v-for="project in projectOptions" :key="project" :value="project">{{ project }}</option>
          </select>
        </label>
      </div>
      <div class="ops-right">
        <button class="tbtn" @click="refresh">{{ t('graphPage.actions.refresh') }}</button>
      </div>
    </div>

    <div class="statebar" :data-state="nodesState.kind">
      <span>{{ stateMessage() }}</span>
      <button v-if="nodesState.kind === 'error'" class="tbtn" @click="refresh">{{ t('graphPage.state.retry') }}</button>
    </div>

    <div v-if="notice" class="notice" :data-kind="notice.kind">
      <span>{{ notice.text }}</span>
      <button class="notice-close" @click="notice = null">×</button>
    </div>

    <div v-if="typedError" class="typed-error">
      <HonestyBadge cls="mustbuild" :evidence="typedError.code" />
      <div>
        <strong>{{ t('graphPage.errors.title') }}</strong>
        <p>{{ mutationErrorText(typedError) }}</p>
      </div>
    </div>

    <section class="forms-grid">
      <article class="panel form-panel">
        <header class="panel-head">
          <h2>{{ t('graphPage.forms.node.title') }}</h2>
        </header>
        <label>
          <span>{{ t('graphPage.forms.node.type') }}</span>
          <select v-model="nodeForm.nodeType" class="select" :disabled="writeDisabled">
            <option v-for="nodeType in GRAPH_NODE_TYPES" :key="nodeType" :value="nodeType">{{ enumLabel('nodeTypes', nodeType) }}</option>
          </select>
        </label>
        <label>
          <span>{{ t('graphPage.forms.node.externalRef') }}</span>
          <input v-model="nodeForm.externalRef" class="input" :disabled="writeDisabled" :placeholder="t('graphPage.forms.node.externalRefPlaceholder')" />
        </label>
        <label>
          <span>{{ t('graphPage.forms.node.privacy') }}</span>
          <select v-model="nodeForm.privacyScope" class="select" :disabled="writeDisabled">
            <option value="project">{{ enumLabel('privacy', 'project') }}</option>
            <option value="private">{{ enumLabel('privacy', 'private') }}</option>
            <option value="shared">{{ enumLabel('privacy', 'shared') }}</option>
            <option value="global">{{ enumLabel('privacy', 'global') }}</option>
          </select>
        </label>
        <button class="act primary" :disabled="writeDisabled" @click="submitCreateNode">{{ t('graphPage.actions.createNode') }}</button>
      </article>

      <article class="panel form-panel">
        <header class="panel-head">
          <h2>{{ t('graphPage.forms.edge.title') }}</h2>
        </header>
        <label>
          <span>{{ t('graphPage.forms.edge.source') }}</span>
          <input v-model="edgeForm.sourceNodeID" class="input" :disabled="writeDisabled" :placeholder="t('graphPage.forms.edge.nodeIdPlaceholder')" list="graph-node-ids" />
        </label>
        <label>
          <span>{{ t('graphPage.forms.edge.target') }}</span>
          <input v-model="edgeForm.targetNodeID" class="input" :disabled="writeDisabled" :placeholder="t('graphPage.forms.edge.nodeIdPlaceholder')" list="graph-node-ids" />
        </label>
        <label>
          <span>{{ t('graphPage.forms.edge.type') }}</span>
          <select v-model="edgeForm.edgeType" class="select" :disabled="writeDisabled">
            <option v-for="edgeType in GRAPH_EDGE_TYPES" :key="edgeType" :value="edgeType">{{ enumLabel('edgeTypes', edgeType) }}</option>
          </select>
        </label>
        <label>
          <span>{{ t('graphPage.forms.edge.reasoning') }}</span>
          <input v-model="edgeForm.reasoning" class="input" :disabled="writeDisabled" :placeholder="t('graphPage.forms.edge.reasoningPlaceholder')" />
        </label>
        <button class="act primary" :disabled="writeDisabled" @click="submitCreateEdge">{{ t('graphPage.actions.createEdge') }}</button>
        <datalist id="graph-node-ids">
          <option v-for="node in nodes" :key="node.id" :value="node.id">{{ nodeDisplay(node) }}</option>
        </datalist>
      </article>

      <article class="panel form-panel">
        <header class="panel-head">
          <h2>{{ t('graphPage.forms.traverse.title') }}</h2>
        </header>
        <label>
          <span>{{ t('graphPage.forms.traverse.memoryId') }}</span>
          <input v-model="traverseForm.memoryID" class="input" :disabled="graphDisabled" :placeholder="t('graphPage.forms.traverse.memoryPlaceholder')" />
        </label>
        <label>
          <span>{{ t('graphPage.forms.traverse.depth') }}</span>
          <input v-model.number="traverseForm.depth" class="input" :disabled="graphDisabled" type="number" min="1" max="3" />
        </label>
        <button class="act primary" :disabled="traverseBusy || graphDisabled" @click="runTraverse">{{ traverseBusy ? t('graphPage.actions.running') : t('graphPage.actions.runTraverse') }}</button>
      </article>

      <article class="panel form-panel">
        <header class="panel-head">
          <h2>{{ t('graphPage.forms.path.title') }}</h2>
        </header>
        <label>
          <span>{{ t('graphPage.forms.path.sourceId') }}</span>
          <input v-model="pathForm.sourceID" class="input" :disabled="graphDisabled" :placeholder="t('graphPage.forms.path.memoryPlaceholder')" />
        </label>
        <label>
          <span>{{ t('graphPage.forms.path.targetId') }}</span>
          <input v-model="pathForm.targetID" class="input" :disabled="graphDisabled" :placeholder="t('graphPage.forms.path.memoryPlaceholder')" />
        </label>
        <label>
          <span>{{ t('graphPage.forms.path.maxDepth') }}</span>
          <input v-model.number="pathForm.maxDepth" class="input" :disabled="graphDisabled" type="number" min="1" max="3" />
        </label>
        <button class="act primary" :disabled="pathBusy || graphDisabled" @click="runFindPath">{{ pathBusy ? t('graphPage.actions.running') : t('graphPage.actions.findPath') }}</button>
      </article>
    </section>

    <section class="topology-grid">
      <article class="panel list-panel">
        <header class="panel-head">
          <h2>{{ t('graphPage.sections.nodes') }}</h2>
          <span class="count">{{ nodes.length }}</span>
        </header>
        <div v-if="nodes.length" class="node-list">
          <button
            v-for="node in nodes"
            :key="node.id"
            class="node-row"
            :class="{ selected: selectedNodeID === node.id }"
            @click="selectedNodeID = node.id"
          >
            <b>{{ nodeDisplay(node) }}</b>
            <span>{{ enumLabel('privacy', node.privacyScope) }} · #{{ node.id }}</span>
          </button>
        </div>
        <div v-else class="empty-card">
          <b>{{ t('graphPage.empty.nodesTitle') }}</b>
          <p>{{ t('graphPage.empty.nodesBody') }}</p>
        </div>
      </article>

      <article class="panel list-panel">
        <header class="panel-head">
          <h2>{{ t('graphPage.sections.edges') }}</h2>
          <span class="count">{{ connectedEdges.length }}</span>
        </header>
        <div v-if="selectedNode" class="selection-note">{{ t('graphPage.selectedNode', { node: nodeDisplay(selectedNode) }) }}</div>
        <div v-if="edgesState.kind === 'error'" class="empty-card">
          <b>{{ t('graphPage.empty.edgesTitle') }}</b>
          <p>{{ edgesState.error.message }}</p>
        </div>
        <div v-else-if="connectedEdges.length" class="edge-list">
          <div v-for="edge in connectedEdges" :key="edge.id" class="edge-row">
            <div class="edge-copy">
              <b>{{ edgeDisplay(edge) }}</b>
              <span>{{ edge.reasoning || t('graphPage.edgeNoReasoning') }}</span>
            </div>
            <button class="act muted" :disabled="writeDisabled" @click="requestDeleteEdge(edge.id)">
              {{ confirmDeleteEdgeID === edge.id ? t('graphPage.actions.confirmDeleteEdge') : t('graphPage.actions.deleteEdge') }}
            </button>
          </div>
        </div>
        <div v-else class="empty-card">
          <b>{{ t('graphPage.empty.edgesTitle') }}</b>
          <p>{{ t('graphPage.empty.edgesBody') }}</p>
        </div>
      </article>

      <article v-if="selectedNode" class="panel detail-panel">
        <header class="panel-head">
          <h2>{{ t('graphPage.sections.selected') }}</h2>
          <HonestyBadge cls="live" />
        </header>
        <div class="detail-copy">
          <b>{{ nodeDisplay(selectedNode) }}</b>
          <span>#{{ selectedNode.id }} · {{ selectedNode.project }}</span>
        </div>
        <dl class="fields">
          <dt>{{ t('graphPage.detail.nodeType') }}</dt>
          <dd>{{ enumLabel('nodeTypes', selectedNode.nodeType) }}</dd>
          <dt>{{ t('graphPage.detail.privacy') }}</dt>
          <dd>{{ enumLabel('privacy', selectedNode.privacyScope) }}</dd>
          <dt>{{ t('graphPage.detail.updated') }}</dt>
          <dd>{{ selectedNode.updatedAt || '—' }}</dd>
        </dl>
        <label class="cascade-toggle">
          <input v-model="deleteCascade" type="checkbox" :disabled="writeDisabled" />
          <span>{{ t('graphPage.actions.deleteNodeCascade') }}</span>
        </label>
        <button class="act danger" :disabled="writeDisabled" @click="requestDeleteNode">
          {{ confirmDeleteNode ? t('graphPage.actions.confirmDeleteNode') : t('graphPage.actions.deleteNode') }}
        </button>
      </article>
    </section>

    <section class="analysis-grid">
      <article class="panel">
        <header class="panel-head">
          <h2>{{ t('graphPage.sections.traverse') }}</h2>
          <span class="count">{{ traverseResults.length }}</span>
        </header>
        <div v-if="traverseError" class="empty-card">
          <b>{{ t('graphPage.empty.traverseTitle') }}</b>
          <p>{{ traverseError }}</p>
        </div>
        <ol v-else-if="traverseResults.length" class="trace-list">
          <li v-for="step in traverseResults" :key="step.edgeID + ':' + step.depth">
            <b>{{ t('graphPage.trace.depth', { depth: step.depth }) }}</b>
            <span>{{ t('graphPage.trace.edge', { source: step.sourceID, type: enumLabel('edgeTypes', step.edgeType), target: step.targetID }) }}</span>
          </li>
        </ol>
        <div v-else class="empty-card">
          <b>{{ t('graphPage.empty.traverseTitle') }}</b>
          <p>{{ t('graphPage.empty.traverseBody') }}</p>
        </div>
      </article>

      <article class="panel">
        <header class="panel-head">
          <h2>{{ t('graphPage.sections.path') }}</h2>
          <span class="count">{{ pathResult?.hops || 0 }}</span>
        </header>
        <div v-if="pathError" class="empty-card">
          <b>{{ t('graphPage.empty.pathTitle') }}</b>
          <p>{{ pathError }}</p>
        </div>
        <div v-else-if="pathResult && pathResult.found" class="path-found">
          <p>{{ t('graphPage.pathFound', { hops: pathResult.hops }) }}</p>
          <ol class="trace-list">
            <li v-for="step in pathResult.path" :key="step.edgeID + ':' + step.depth">
              <b>{{ t('graphPage.trace.depth', { depth: step.depth }) }}</b>
              <span>{{ t('graphPage.trace.edge', { source: step.sourceID, type: enumLabel('edgeTypes', step.edgeType), target: step.targetID }) }}</span>
            </li>
          </ol>
        </div>
        <div v-else class="empty-card">
          <b>{{ t('graphPage.empty.pathTitle') }}</b>
          <p>{{ t('graphPage.empty.pathBody') }}</p>
        </div>
      </article>
    </section>
  </div>
</template>

<style scoped>
.graph-page { display:flex; flex-direction:column; gap:14px; }
.page-head { display:flex; align-items:flex-start; justify-content:space-between; gap:18px; padding-bottom:14px; border-bottom:1px solid var(--border); }
.page-head h1 { margin:0 0 4px; font-size:var(--text-xl); font-weight:800; letter-spacing:var(--tracking-display); }
.page-head p { margin:0; color:var(--muted); font-size:var(--text-sm); }
.graph-brief { display:grid; grid-template-columns:repeat(3, minmax(120px, 180px)) minmax(260px, 1fr); gap:12px; }
.metric, .brief-copy, .panel { border:1px solid var(--border); border-radius:var(--r-md); background:var(--surface); }
.metric, .brief-copy { padding:14px; }
.metric { display:flex; flex-direction:column; gap:3px; }
.metric b { font-family:var(--font-mono); font-size:var(--text-xl); line-height:1; color:var(--fg); }
.metric span, .brief-copy span { color:var(--muted); font-size:var(--text-xs); }
.brief-copy { display:flex; flex-direction:column; justify-content:center; gap:5px; }
.brief-copy strong { color:var(--fg-2); font-size:var(--text-sm); }
.ops { display:flex; align-items:center; justify-content:space-between; gap:12px; flex-wrap:wrap; }
.ops-left, .ops-right { display:flex; align-items:center; gap:10px; flex-wrap:wrap; }
.ops label { display:flex; align-items:center; gap:10px; color:var(--muted); font-size:var(--text-xs); }
.select, .input { min-height:34px; border:1px solid var(--border); border-radius:var(--r-sm); background:var(--surface); color:var(--fg); padding:0 10px; font-size:var(--text-sm); }
.input { width:100%; }
.tbtn, .act { display:inline-flex; align-items:center; justify-content:center; min-height:32px; padding:6px 10px; border:1px solid var(--border); border-radius:var(--r-sm); background:var(--surface); color:var(--fg-2); font-size:var(--text-xs); font-weight:800; cursor:pointer; }
.tbtn:hover:not(:disabled), .act:hover:not(:disabled) { border-color:var(--accent); color:var(--fg); }
.tbtn:disabled, .act:disabled { opacity:.45; cursor:not-allowed; }
.act.primary { border-color:color-mix(in oklab,var(--class-live),transparent 45%); color:var(--class-live); }
.act.danger { border-color:color-mix(in oklab,var(--state-warn),transparent 35%); color:var(--state-warn); }
.act.muted { color:var(--muted); }
.statebar, .notice, .typed-error { display:flex; align-items:flex-start; justify-content:space-between; gap:12px; padding:10px 12px; border:1px solid var(--border); border-radius:var(--r-md); background:var(--surface); font-size:var(--text-sm); }
.statebar[data-state="pending"] { border-color:color-mix(in oklab,var(--accent),transparent 55%); }
.statebar[data-state="error"], .notice[data-kind="error"], .typed-error { border-color:color-mix(in oklab,var(--state-warn),transparent 45%); }
.statebar[data-state="gated"] { border-color:color-mix(in oklab,var(--class-dormant),transparent 45%); }
.notice[data-kind="success"] { border-color:color-mix(in oklab,var(--class-live),transparent 45%); }
.notice-close { border:0; background:transparent; color:var(--muted); cursor:pointer; font-size:18px; line-height:1; }
.typed-error p { margin:2px 0 0; color:var(--muted); }
.forms-grid { display:grid; grid-template-columns:repeat(4, minmax(0, 1fr)); gap:12px; }
.panel { padding:14px; display:flex; flex-direction:column; gap:10px; }
.panel-head { display:flex; align-items:center; justify-content:space-between; gap:10px; }
.panel-head h2 { margin:0; font-size:var(--text-sm); font-weight:900; letter-spacing:.04em; text-transform:uppercase; }
.count { color:var(--muted); font-family:var(--font-mono); font-size:var(--text-xs); }
.form-panel label { display:flex; flex-direction:column; gap:6px; color:var(--muted); font-size:var(--text-xs); }
.topology-grid { display:grid; grid-template-columns:minmax(280px, 1fr) minmax(320px, 1.2fr) minmax(280px, .9fr); gap:12px; align-items:start; }
.list-panel { min-height:340px; }
.node-list, .edge-list, .trace-list { display:flex; flex-direction:column; gap:8px; }
.node-row { display:flex; flex-direction:column; align-items:flex-start; gap:3px; padding:10px 12px; border:1px solid var(--border); border-radius:var(--r-sm); background:var(--surface-warm); color:var(--fg); text-align:left; cursor:pointer; }
.node-row.selected { box-shadow:inset 3px 0 0 var(--accent); border-color:color-mix(in oklab,var(--accent),transparent 45%); }
.node-row b, .edge-copy b, .detail-copy b { color:var(--fg); font-size:var(--text-sm); }
.node-row span, .edge-copy span, .detail-copy span, .selection-note { color:var(--muted); font-size:var(--text-xs); }
.edge-row { display:flex; align-items:center; justify-content:space-between; gap:10px; padding:10px 12px; border:1px solid var(--border); border-radius:var(--r-sm); background:var(--surface-warm); }
.edge-copy { min-width:0; display:flex; flex-direction:column; gap:3px; }
.empty-card { display:flex; flex-direction:column; justify-content:center; gap:5px; min-height:160px; color:var(--muted); }
.empty-card b { color:var(--fg-2); font-size:var(--text-lg); }
.detail-panel { position:sticky; top:0; }
.detail-copy { display:flex; flex-direction:column; gap:3px; }
.fields { display:grid; grid-template-columns:110px minmax(0,1fr); gap:6px 12px; margin:0; font-size:var(--text-sm); }
.fields dt { color:var(--muted); }
.fields dd { margin:0; color:var(--fg); font-family:var(--font-mono); }
.cascade-toggle { display:flex; align-items:center; gap:8px; color:var(--muted); font-size:var(--text-xs); }
.trace-list { margin:0; padding-left:18px; }
.trace-list li { display:flex; flex-direction:column; gap:2px; }
.trace-list span { color:var(--fg-2); font-size:var(--text-sm); }
.analysis-grid { display:grid; grid-template-columns:repeat(2, minmax(0, 1fr)); gap:12px; }
.path-found p { margin:0; color:var(--fg-2); font-size:var(--text-sm); }
@media (max-width:1260px) {
  .graph-brief, .forms-grid, .topology-grid, .analysis-grid { grid-template-columns:1fr 1fr; }
  .brief-copy { grid-column:1 / -1; }
  .topology-grid .detail-panel { grid-column:1 / -1; position:static; }
}
@media (max-width:760px) {
  .page-head, .ops, .edge-row { flex-direction:column; align-items:stretch; }
  .graph-brief, .forms-grid, .topology-grid, .analysis-grid { grid-template-columns:1fr; }
}
</style>
