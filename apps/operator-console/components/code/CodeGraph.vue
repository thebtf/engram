<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import type { CodeEntityRef, CodeEnvelope, CodeGraphEdge, CodeGraphOptions, CodeItem, CodePresentationState, CodeSourceDescriptor } from '~/composables/useOperatorCode'

const { t } = useI18n()

const props = defineProps<{
  graph: CodeEnvelope | null
  search: CodeEnvelope | null
  state: CodePresentationState
  pending: boolean
}>()

const emit = defineEmits<{
  explore: [item: CodeItem, options: CodeGraphOptions]
  continue: [target: CodeEntityRef | null]
  source: [descriptor: CodeSourceDescriptor]
}>()

const direction = ref<CodeGraphOptions['direction']>('both')
const relations = ref<string[]>([])
const mode = ref<'graph' | 'list'>('graph')
const selectedNode = ref<string | null>(null)
const selectedEdge = ref<CodeGraphEdge | null>(null)

const relationTypes = ['contains', 'imports', 'exports', 'references', 'calls', 'may_call', 'inherits', 'implements', 'documents', 'mentions', 'configures', 'schema_references', 'tests', 'depends_on']
const graph = computed(() => props.graph?.graph ?? null)
const visibleContinuation = computed(() => props.graph?.continuation !== null)
const edges = computed(() => graph.value?.edges ?? [])
const nodes = computed(() => graph.value?.nodes.map((ref, index) => {
  const count = Math.max(graph.value?.nodes.length ?? 0, 1)
  const angle = (Math.PI * 2 * index) / count - Math.PI / 2
  return { ref, x: 160 + Math.cos(angle) * 112, y: 130 + Math.sin(angle) * 82 }
}) ?? [])
const selectedTarget = computed(() => nodes.value.find((node) => node.ref.entityKey === selectedNode.value)?.ref ?? null)
const selectedSource = computed(() => selectedTarget.value === null ? null : props.graph?.navigation?.nodes.find((node) => node.ref.entityKey === selectedTarget.value?.entityKey)?.source ?? null)

function point(ref: CodeEntityRef): { x: number; y: number } | null {
  return nodes.value.find((node) => node.ref.entityKey === ref.entityKey) ?? null
}

function edgePath(edge: CodeGraphEdge): string {
  const from = point(edge.from)
  const to = point(edge.to)
  if (from === null || to === null) return ''
  const middleX = (from.x + to.x) / 2
  const middleY = (from.y + to.y) / 2 - 14
  return `M ${from.x} ${from.y} Q ${middleX} ${middleY} ${to.x} ${to.y}`
}

function selected(edge: CodeGraphEdge): boolean {
  return selectedEdge.value === edge
}

function toggleRelation(relation: string) {
  relations.value = relations.value.includes(relation)
    ? relations.value.filter((value) => value !== relation)
    : [...relations.value, relation]
}

function submit(item: CodeItem) {
  selectedNode.value = item.ref.entityKey
  selectedEdge.value = null
  emit('explore', item, { direction: direction.value, relations: relations.value })
}

function selectTarget(event: Event) {
  const selected = Number((event.target as HTMLSelectElement).value)
  const item = props.search?.items[selected]
  if (item !== undefined) submit(item)
}

function chooseNode(key: string) {
  selectedNode.value = key
  selectedEdge.value = null
}

function chooseEdge(edge: CodeGraphEdge) {
  selectedEdge.value = edge
  selectedNode.value = edge.to.entityKey
}

watch(() => props.graph, () => {
  if (selectedNode.value !== null && !nodes.value.some((node) => node.ref.entityKey === selectedNode.value)) selectedNode.value = null
  if (selectedEdge.value !== null && !edges.value.includes(selectedEdge.value)) selectedEdge.value = null
})
</script>

<template>
  <article class="panel graph-panel" aria-live="polite">
    <div class="panel-head">
      <div>
        <h3>{{ t('codeExplorer.graph.title') }}</h3>
        <p>{{ t(`codeExplorer.states.${state.kind}.graph`) }}</p>
      </div>
      <span :data-state="state.kind">{{ t(`codeExplorer.states.${state.kind}.label`) }}</span>
    </div>

    <div v-if="search !== null && search.items.length > 0" class="graph-controls">
      <label>
        <span>{{ t('codeExplorer.graph.direction') }}</span>
        <select v-model="direction" :disabled="pending">
          <option value="both">{{ t('codeExplorer.graph.directions.both') }}</option>
          <option value="outgoing">{{ t('codeExplorer.graph.directions.outgoing') }}</option>
          <option value="incoming">{{ t('codeExplorer.graph.directions.incoming') }}</option>
        </select>
      </label>
      <fieldset>
        <legend>{{ t('codeExplorer.graph.relations') }}</legend>
        <label v-for="relation in relationTypes" :key="relation" class="relation">
          <input type="checkbox" :checked="relations.includes(relation)" :disabled="pending" @change="toggleRelation(relation)">
          <span>{{ relation }}</span>
        </label>
      </fieldset>
      <label class="target">
        <span>{{ t('codeExplorer.graph.target') }}</span>
        <select :disabled="pending" @change="selectTarget">
          <option value="" selected disabled>{{ t('codeExplorer.graph.chooseTarget') }}</option>
          <option v-for="(item, index) in search.items" :key="item.ref.entityKey" :value="index">{{ item.ref.entityKey }}</option>
        </select>
      </label>
    </div>

    <div v-if="graph !== null" class="graph-toolbar">
      <button class="btn" type="button" :aria-pressed="mode === 'graph'" :disabled="pending" @click="mode = 'graph'">{{ t('codeExplorer.graph.visual') }}</button>
      <button class="btn" type="button" :aria-pressed="mode === 'list'" :disabled="pending" @click="mode = 'list'">{{ t('codeExplorer.graph.list') }}</button>
      <button v-if="selectedTarget !== null || visibleContinuation" class="btn" type="button" :disabled="pending" @click="emit('continue', selectedTarget)">{{ t('codeExplorer.graph.continue') }}</button>
    </div>

    <svg v-if="graph !== null && mode === 'graph'" class="graph-canvas" viewBox="0 0 320 260" role="img" data-testid="code-graph-results" :aria-label="t('codeExplorer.graph.canvasLabel', { count: nodes.length })">
      <desc>{{ edges.map((edge) => `${edge.from.entityKey} ${edge.relation} ${edge.to.entityKey} ${edge.explanation ?? ''}`).join(' ') }}</desc>
      <defs><marker id="code-arrow" markerWidth="8" markerHeight="8" refX="6" refY="3" orient="auto"><path d="M0,0 L0,6 L6,3 z" /></marker></defs>
      <path
        v-for="edge in edges"
        :key="`${edge.from.entityKey}:${edge.relation}:${edge.to.entityKey}`"
        class="edge"
        :class="{ selected: selected(edge) }"
        :d="edgePath(edge)"
        marker-end="url(#code-arrow)"
        tabindex="0"
        role="button"
        :aria-label="t('codeExplorer.graph.edgeLabel', { from: edge.from.entityKey, relation: edge.relation, to: edge.to.entityKey })"
        @click="chooseEdge(edge)"
        @keydown.enter.prevent="chooseEdge(edge)"
        @keydown.space.prevent="chooseEdge(edge)"
      />
      <g
        v-for="node in nodes"
        :key="node.ref.entityKey"
        class="node"
        :class="{ selected: selectedNode === node.ref.entityKey }"
        tabindex="0"
        role="button"
        :aria-label="t('codeExplorer.graph.nodeLabel', { node: node.ref.entityKey })"
        @click="chooseNode(node.ref.entityKey)"
        @keydown.enter.prevent="chooseNode(node.ref.entityKey)"
        @keydown.space.prevent="chooseNode(node.ref.entityKey)"
      >
        <circle :cx="node.x" :cy="node.y" r="14" />
        <text :x="node.x" :y="node.y + 28" text-anchor="middle">{{ node.ref.entityKey }}</text>
      </g>
    </svg>

    <ol v-if="graph !== null && mode === 'list'" class="edges" data-testid="code-graph-results">
      <li v-for="edge in edges" :key="`${edge.from.entityKey}:${edge.relation}:${edge.to.entityKey}`">
        <button type="button" class="edge-list" :aria-pressed="selected(edge)" @click="chooseEdge(edge)">
          <code>{{ edge.from.entityKey }}</code><span>{{ edge.relation }}</span><code>{{ edge.to.entityKey }}</code>
          <small>{{ edge.evidenceKind }}<template v-if="edge.explanation !== null"> — {{ edge.explanation }}</template></small>
        </button>
      </li>
    </ol>

    <section v-if="selectedNode !== null" class="selection" :aria-label="t('codeExplorer.graph.selection')">
      <strong>{{ selectedNode }}</strong>
      <p v-if="selectedEdge !== null">{{ selectedEdge.relation }} · {{ selectedEdge.evidenceKind }}</p>
      <button v-if="selectedSource !== null" class="btn" type="button" :disabled="pending" @click="emit('source', selectedSource)">{{ t('codeExplorer.graph.inspectSource') }}</button>
      <p v-else>{{ t('codeExplorer.graph.noPublishedSource') }}</p>
    </section>

    <p v-if="graph !== null" class="stop">{{ t('codeExplorer.graph.traversal', { stop: graph.stopReason }) }}</p>
  </article>
</template>

<style scoped>
.graph-panel { min-width:0; }
.panel-head { display:flex; align-items:flex-start; justify-content:space-between; gap:8px; }
h3 { margin:0; color:var(--fg); font-size:var(--text-sm); font-weight:800; }.panel-head p, .stop, .selection p { margin:4px 0 0; color:var(--muted); font-size:var(--text-sm); }
.panel-head span { border:1px solid var(--border); border-radius:var(--radius-pill); padding:3px 7px; color:var(--muted); font-size:var(--text-xs); }.panel-head span[data-state='partial'], .panel-head span[data-state='stale'], .panel-head span[data-state='timeout'] { color:var(--warn); border-color:color-mix(in oklab,var(--warn),transparent 35%); }.panel-head span[data-state='denied'], .panel-head span[data-state='error'], .panel-head span[data-state='offline'] { color:var(--danger); border-color:color-mix(in oklab,var(--danger),transparent 35%); }
.graph-controls { display:grid; gap:10px; margin-top:14px; }.graph-controls > label, fieldset { display:grid; gap:5px; min-width:0; border:0; margin:0; padding:0; }label > span, legend { color:var(--muted); font-size:var(--text-xs); font-weight:700; letter-spacing:.04em; text-transform:uppercase; }select { min-height:38px; min-width:0; border:1px solid var(--border); border-radius:var(--r-sm); background:var(--bg); color:var(--fg); padding:8px; }.relation { display:inline-flex; align-items:center; gap:6px; margin:2px 10px 2px 0; color:var(--fg-2); font-family:var(--font-mono); font-size:var(--text-xs); }.relation input { accent-color:var(--accent); }
.graph-toolbar { display:flex; flex-wrap:wrap; gap:7px; margin-top:14px; }.btn { min-height:36px; border:1px solid var(--border); border-radius:var(--r-sm); background:var(--surface); color:var(--fg); padding:8px 12px; font:inherit; font-size:var(--text-sm); font-weight:700; cursor:pointer; }.btn[aria-pressed='true'] { border-color:var(--accent); box-shadow:inset 0 0 0 1px var(--accent); }.btn:disabled { cursor:not-allowed; opacity:.55; }
.graph-canvas { display:block; width:100%; min-height:260px; margin-top:14px; border:1px solid var(--border-soft); border-radius:var(--r-sm); background:var(--bg); }.edge { fill:none; stroke:var(--border); stroke-width:2; cursor:pointer; }.edge.selected { stroke:var(--accent); stroke-width:3; }.edge:focus-visible, .node:focus-visible { outline:none; }.edge:focus-visible { stroke:var(--accent); stroke-width:4; }.node { cursor:pointer; }.node circle { fill:var(--surface-warm); stroke:var(--border); stroke-width:2; }.node.selected circle, .node:focus-visible circle { stroke:var(--accent); stroke-width:3; }.node text { fill:var(--fg-2); font-family:var(--font-mono); font-size:8px; pointer-events:none; }.graph-canvas marker path { fill:var(--border); }
.edges { display:grid; gap:8px; margin:14px 0 0; padding:0; list-style:none; }.edge-list { display:grid; width:100%; gap:4px; border:1px solid var(--border-soft); border-radius:var(--r-sm); background:var(--bg); padding:10px; color:var(--fg); text-align:left; cursor:pointer; }.edge-list[aria-pressed='true'] { border-color:var(--accent); }.edge-list span, .edge-list small { color:var(--muted); font-size:var(--text-xs); }.edge-list code { overflow-wrap:anywhere; }.selection { display:grid; gap:6px; margin-top:14px; border-top:1px solid var(--border-soft); padding-top:12px; }.selection strong { color:var(--fg); font-family:var(--font-mono); font-size:var(--text-sm); }.selection p { margin:0; }
@media (max-width:720px) { .panel-head { display:grid; }.panel-head span { justify-self:start; white-space:normal; } }
</style>
