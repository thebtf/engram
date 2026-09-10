<script setup lang="ts">
import { computed, ref } from 'vue'
import type { CodeEnvelope, CodeItem, CodePresentationState, CodeSafeContext, CodeStatus } from '~/composables/useOperatorCode'

const props = defineProps<{
  pinned: CodeSafeContext | null
  status: CodeStatus | null
  search: CodeEnvelope | null
  graph: CodeEnvelope | null
  source: CodeEnvelope | null
  searchState: CodePresentationState
  graphState: CodePresentationState
  sourceState: CodePresentationState
  pending: boolean
}>()

const emit = defineEmits<{
  search: [query: string]
  explore: [item: CodeItem]
  source: [item: CodeItem]
}>()

const query = ref('')
const sourceItem = computed(() => props.source?.items[0] ?? null)

function submitSearch() {
  if (query.value.trim() !== '') emit('search', query.value)
}
</script>

<template>
  <section class="results" aria-labelledby="code-results-heading">
    <header class="section-head">
      <div>
        <h2 id="code-results-heading">Explore the pinned view</h2>
        <p v-if="pinned === null">Search, graph, and source stay unavailable until the server confirms a pin.</p>
        <p v-else>Every result below is released separately for {{ pinned.view }}.</p>
      </div>
      <dl v-if="status !== null && pinned !== null" class="status" data-testid="code-status">
        <div><dt>Coverage</dt><dd>{{ status.coverage }}</dd></div>
        <div><dt>Indexed</dt><dd>{{ status.embeddedChunks }} / {{ status.totalChunks }}</dd></div>
        <div><dt>Freshness</dt><dd>{{ status.freshnessState ?? 'unknown' }}</dd></div>
        <div><dt>Engine/version</dt><dd>not released</dd></div>
      </dl>
    </header>

    <form v-if="pinned !== null" class="search-form" @submit.prevent="submitSearch">
      <label for="code-query">Search this immutable view</label>
      <div>
        <input id="code-query" v-model="query" name="code-query" autocomplete="off" placeholder="Describe a code concept" :disabled="pending" data-testid="code-query-input">
        <button class="btn primary" type="submit" :disabled="pending || query.trim() === ''" data-testid="code-search-submit">Search</button>
      </div>
    </form>

    <div v-else class="unselected" data-testid="code-results-unselected">
      <strong>No contextual result is rendered.</strong>
      <p>Pin one server-authorized view to start a released code exploration.</p>
    </div>

    <div v-if="pinned !== null" class="result-grid">
      <article class="panel search-panel" aria-live="polite">
        <div class="panel-head"><h3>Search</h3><span :data-state="searchState.kind">{{ searchState.kind }}</span></div>
        <p class="state-message">{{ searchState.message }}</p>
        <ul v-if="search !== null && search.items.length > 0" class="items" data-testid="code-search-results">
          <li v-for="item in search.items" :key="`${item.ref.entityKey}:${item.span.byteStart}`">
            <div class="item-copy">
              <strong>{{ item.ref.entityKey }}</strong>
              <p><code>{{ item.path }}:{{ item.span.lineStart }}–{{ item.span.lineEnd }}</code> · {{ item.language }} · {{ item.matchSources.join(', ') }}</p>
              <pre>{{ item.excerpt }}</pre>
            </div>
            <div class="item-actions">
              <button class="btn" type="button" :disabled="pending" @click="emit('explore', item)">Explore graph</button>
              <button class="btn" type="button" :disabled="pending" @click="emit('source', item)">Read source</button>
            </div>
          </li>
        </ul>
        <p v-else-if="search !== null && searchState.kind === 'ready'" class="state-message">The released response has no displayable matches.</p>
        <ul v-if="search !== null && search.warnings.length > 0" class="warnings"><li v-for="warning in search.warnings" :key="warning">{{ warning }}</li></ul>
        <p v-if="search !== null" class="cursor">Cursor: {{ search.hasContinuation ? 'released but continuation navigation is not exposed' : 'none released' }}</p>
      </article>

      <article class="panel" aria-live="polite">
        <div class="panel-head"><h3>Graph evidence</h3><span :data-state="graphState.kind">{{ graphState.kind }}</span></div>
        <p class="state-message">{{ graphState.message }}</p>
        <ol v-if="graph?.graph !== null" class="edges" data-testid="code-graph-results">
          <li v-for="edge in graph.graph.edges" :key="`${edge.from.entityKey}:${edge.relation}:${edge.to.entityKey}`">
            <code>{{ edge.from.entityKey }}</code><span>{{ edge.relation }}</span><code>{{ edge.to.entityKey }}</code>
            <small>{{ edge.evidenceKind }}<template v-if="edge.explanation !== null"> — {{ edge.explanation }}</template></small>
          </li>
        </ol>
        <p v-if="graph?.graph !== null" class="stop">Traversal: {{ graph.graph.stopReason }}</p>
        <p v-if="graph !== null" class="cursor">Cursor: {{ graph.hasContinuation ? 'released but continuation navigation is not exposed' : 'none released' }}</p>
      </article>

      <article class="panel source" aria-live="polite">
        <div class="panel-head"><h3>Exact source</h3><span :data-state="sourceState.kind">{{ sourceState.kind }}</span></div>
        <p class="state-message">{{ sourceState.message }}</p>
        <template v-if="sourceItem !== null">
          <p class="source-meta"><code>{{ sourceItem.path }}:{{ sourceItem.span.lineStart }}–{{ sourceItem.span.lineEnd }}</code> · {{ sourceItem.language }} · exact persisted span</p>
          <pre data-testid="code-source-result">{{ sourceItem.excerpt }}</pre>
        </template>
      </article>
    </div>
  </section>
</template>

<style scoped>
.results { display:grid; gap:14px; }
.section-head { display:flex; align-items:flex-start; justify-content:space-between; gap:16px; border:1px solid var(--border); border-radius:var(--r-md); background:var(--surface); padding:16px; }
h2, h3 { margin:0; color:var(--fg); }
h2 { font-size:var(--text-sm); font-weight:800; }
h3 { font-size:var(--text-sm); font-weight:800; }
.section-head p, .state-message, .source-meta, .stop, .cursor { margin:4px 0 0; color:var(--muted); font-size:var(--text-sm); }
.status div { min-width:82px; }
dt { color:var(--muted); font-size:var(--text-xs); letter-spacing:.04em; text-transform:uppercase; }
dd { margin:4px 0 0; color:var(--fg); font-family:var(--font-mono); font-size:var(--text-xs); }
.search-form { display:grid; gap:6px; border:1px solid var(--border); border-radius:var(--r-md); background:var(--surface); padding:14px; }
.search-form label { color:var(--muted); font-size:var(--text-xs); font-weight:700; letter-spacing:.04em; text-transform:uppercase; }
.search-form div { display:grid; grid-template-columns:minmax(0,1fr) auto; gap:8px; }
input { min-width:0; min-height:38px; border:1px solid var(--border); border-radius:var(--r-sm); background:var(--bg); color:var(--fg); padding:8px 10px; }
.btn { min-height:36px; border:1px solid var(--border); border-radius:var(--r-sm); background:var(--surface); color:var(--fg); padding:8px 12px; font:inherit; font-size:var(--text-sm); font-weight:700; cursor:pointer; }
.btn.primary { border-color:var(--accent); background:var(--accent); color:var(--accent-on); }
.btn:disabled { cursor:not-allowed; opacity:.55; }
.unselected { border:1px dashed var(--border); border-radius:var(--r-md); background:var(--bg); padding:24px; text-align:center; }
.unselected strong { color:var(--fg); }.unselected p { margin:5px 0 0; color:var(--muted); font-size:var(--text-sm); }
.result-grid { display:grid; grid-template-columns:repeat(3,minmax(0,1fr)); gap:14px; align-items:start; }
.panel { min-width:0; border:1px solid var(--border); border-radius:var(--r-md); background:var(--surface); padding:14px; }
.panel-head { display:flex; align-items:center; justify-content:space-between; gap:8px; }
.panel-head span { border:1px solid var(--border); border-radius:var(--radius-pill); padding:3px 7px; color:var(--muted); font-size:var(--text-xs); }
.panel-head span[data-state='partial'], .panel-head span[data-state='stale'], .panel-head span[data-state='timeout'] { color:var(--warn); border-color:color-mix(in oklab,var(--warn),transparent 35%); }
.panel-head span[data-state='denied'], .panel-head span[data-state='error'], .panel-head span[data-state='offline'] { color:var(--danger); border-color:color-mix(in oklab,var(--danger),transparent 35%); }
.items, .warnings, .edges { display:grid; gap:10px; margin:14px 0 0; padding:0; list-style:none; }
.items li { display:grid; gap:10px; border-top:1px solid var(--border-soft); padding-top:12px; }
.item-copy strong { color:var(--fg); font-size:var(--text-sm); }.item-copy p { margin:4px 0; color:var(--muted); font-size:var(--text-xs); }
pre { overflow:auto; margin:8px 0 0; border:1px solid var(--border-soft); border-radius:var(--r-sm); background:var(--bg); padding:10px; color:var(--fg); font-family:var(--font-mono); font-size:var(--text-xs); line-height:1.5; white-space:pre-wrap; overflow-wrap:anywhere; }
.item-actions { display:flex; flex-wrap:wrap; gap:7px; }
.edges li { display:grid; gap:4px; border-top:1px solid var(--border-soft); padding-top:10px; }.edges code { color:var(--fg); overflow-wrap:anywhere; }.edges span { color:var(--meta); font-size:var(--text-xs); }.edges small { color:var(--muted); font-size:var(--text-xs); }
.stop { font-family:var(--font-mono); font-size:var(--text-xs); }.warnings { color:var(--warn); font-size:var(--text-xs); list-style:disc; padding-left:18px; }
.source-meta { font-family:var(--font-mono); font-size:var(--text-xs); }
@media (max-width: 1120px) { .result-grid { grid-template-columns:repeat(2,minmax(0,1fr)); }.source { grid-column:1 / -1; } }
@media (max-width: 720px) { .section-head, .search-form div, .result-grid { display:grid; grid-template-columns:1fr; }.status { margin-top:4px; }.source { grid-column:auto; } }
</style>
