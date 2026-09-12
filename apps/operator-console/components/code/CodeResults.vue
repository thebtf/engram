<script setup lang="ts">
import { computed, ref } from 'vue'
import type { CodeEntityRef, CodeEnvelope, CodeGraphOptions, CodeItem, CodePresentationState, CodeSafeContext, CodeSourceDescriptor, CodeStatus } from '~/composables/useOperatorCode'

const { t } = useI18n()

const props = defineProps<{
  pinned: CodeSafeContext | null
  status: CodeStatus | null
  search: CodeEnvelope | null
  graph: CodeEnvelope | null
  source: CodeEnvelope | null
  searchState: CodePresentationState
  graphState: CodePresentationState
  sourceState: CodePresentationState
  searchContinuationNotice: 'denied' | 'unavailable' | null
  pending: boolean
}>()

const emit = defineEmits<{
  search: [query: string]
  continueSearch: []
  explore: [item: CodeItem, options: CodeGraphOptions]
  continueGraph: [target: CodeEntityRef | null]
  source: [descriptor: CodeSourceDescriptor]
  requestIndex: []
}>()

const query = ref('')
const sourceItem = computed(() => props.source?.items[0] ?? null)
const indexEmpty = computed(() => props.status !== null && props.status.totalChunks === 0)
const copyNotice = ref<'copied' | 'unavailable' | null>(null)

function submitSearch() {
  if (query.value.trim() !== '') emit('search', query.value)
}


async function copy(value: string): Promise<void> {
  try {
    await navigator.clipboard.writeText(value)
    copyNotice.value = 'copied'
  } catch {
    copyNotice.value = 'unavailable'
  }
}
</script>

<template>
  <section class="results" aria-labelledby="code-results-heading">
    <header class="section-head">
      <div>
        <h2 id="code-results-heading">{{ t('codeExplorer.results.title') }}</h2>
        <p v-if="pinned === null">{{ t('codeExplorer.results.unpinned') }}</p>
        <p v-else>{{ t('codeExplorer.results.pinned', { view: pinned.view }) }}</p>
      </div>
      <dl v-if="status !== null && pinned !== null" class="status" data-testid="code-status">
        <div><dt>{{ t('codeExplorer.status.coverage') }}</dt><dd>{{ status.coverage }}</dd></div>
        <div><dt>{{ t('codeExplorer.status.indexed') }}</dt><dd>{{ status.embeddedChunks }} / {{ status.totalChunks }}</dd></div>
        <div><dt>{{ t('codeExplorer.status.freshness') }}</dt><dd>{{ status.freshnessState ?? t('codeExplorer.status.unknown') }}</dd></div>
      </dl>
    </header>

    <aside v-if="indexEmpty" class="empty-index" role="status">
      <div><strong>{{ t('codeExplorer.emptyIndex.title') }}</strong><p>{{ t('codeExplorer.emptyIndex.body') }}</p></div>
      <button class="btn primary" type="button" :disabled="pending" @click="emit('requestIndex')">{{ t('codeExplorer.emptyIndex.action') }}</button>
    </aside>

    <form v-if="pinned !== null" class="search-form" @submit.prevent="submitSearch">
      <label for="code-query">{{ t('codeExplorer.search.label') }}</label>
      <div>
        <input id="code-query" v-model="query" name="code-query" autocomplete="off" :placeholder="t('codeExplorer.search.placeholder')" :disabled="pending" data-testid="code-query-input">
        <button class="btn primary" type="submit" :disabled="pending || query.trim() === ''" data-testid="code-search-submit">{{ t('codeExplorer.search.action') }}</button>
      </div>
    </form>

    <div v-else class="unselected" data-testid="code-results-unselected">
      <strong>{{ t('codeExplorer.unselected.title') }}</strong>
      <p>{{ t('codeExplorer.unselected.body') }}</p>
    </div>

    <div v-if="pinned !== null" class="result-grid">
      <article class="panel search-panel" aria-live="polite">
        <div class="panel-head"><h3>{{ t('codeExplorer.search.title') }}</h3><span :data-state="searchState.kind">{{ t(`codeExplorer.states.${searchState.kind}.label`) }}</span></div>
        <p class="state-message">{{ t(`codeExplorer.states.${searchState.kind}.search`) }}</p>
        <ul v-if="search !== null && search.items.length > 0" class="items" data-testid="code-search-results">
          <li v-for="item in search.items" :key="`${item.ref.entityKey}:${item.span.byteStart}`">
            <div class="item-copy">
              <strong>{{ item.ref.entityKey }}</strong>
              <p><code>{{ item.path }}:{{ item.span.lineStart }}–{{ item.span.lineEnd }}</code> · {{ item.language }} · {{ item.matchSources.join(', ') }}</p>
              <pre>{{ item.excerpt }}</pre>
              <p class="result-evidence">{{ t('codeExplorer.search.evidence', { mode: search.retrievalMode ?? t('codeExplorer.status.unknown'), freshness: search.freshnessState ?? t('codeExplorer.status.unknown'), score: item.score ?? '—' }) }}</p>
            </div>
            <div class="item-actions">
              <button class="btn" type="button" :disabled="pending" data-testid="code-search-explore" @click="emit('explore', item, { direction: 'both', relations: [] })">{{ t('codeExplorer.search.explore') }}</button>
              <button class="btn" type="button" :disabled="pending" data-testid="code-search-source" @click="emit('source', { entityKey: item.ref.entityKey, span: item.span, contentDigest: item.contentDigest })">{{ t('codeExplorer.search.source') }}</button>
            </div>
          </li>
        </ul>
        <p v-else-if="search !== null && searchState.kind === 'ready'" class="state-message">{{ t('codeExplorer.search.noMatches') }}</p>
        <ul v-if="search !== null && search.warnings.length > 0" class="warnings"><li v-for="warning in search.warnings" :key="warning">{{ warning }}</li></ul>
        <button v-if="search !== null && search.continuation !== null" class="btn" type="button" :disabled="pending" data-testid="code-search-next" @click="emit('continueSearch')">{{ t('codeExplorer.continuation.next') }}</button>
        <p v-if="searchContinuationNotice !== null" class="continuation-gap" role="status">{{ t(`codeExplorer.continuation.search.${searchContinuationNotice}`) }}</p>
      </article>

      <CodeGraph
        :graph="graph"
        :search="search"
        :state="graphState"
        :pending="pending"
        @explore="(item, options) => emit('explore', item, options)"
        @continue="(target) => emit('continueGraph', target)"
        @source="(descriptor) => emit('source', descriptor)"
      />

      <article class="panel source" aria-live="polite">
        <div class="panel-head"><h3>{{ t('codeExplorer.source.title') }}</h3><span :data-state="sourceState.kind">{{ t(`codeExplorer.states.${sourceState.kind}.label`) }}</span></div>
        <p class="state-message">{{ t(`codeExplorer.states.${sourceState.kind}.source`) }}</p>
        <template v-if="sourceItem !== null">
          <p class="source-meta"><code>{{ sourceItem.path }}:{{ sourceItem.span.lineStart }}–{{ sourceItem.span.lineEnd }} · {{ sourceItem.span.byteStart }}–{{ sourceItem.span.byteEnd }} · {{ sourceItem.contentDigest }}</code></p>
          <p class="source-meta">{{ sourceItem.language }} · {{ t('codeExplorer.source.exactPublished') }}</p>
          <div class="item-actions">
            <button class="btn" type="button" :disabled="pending" @click="copy(sourceItem.path)">{{ t('codeExplorer.source.copyPath') }}</button>
            <button class="btn" type="button" :disabled="pending" @click="copy(sourceItem.contentDigest)">{{ t('codeExplorer.source.copyDigest') }}</button>
            <button class="btn" type="button" :disabled="pending" @click="copy(sourceItem.excerpt)">{{ t('codeExplorer.source.copyContent') }}</button>
          </div>
          <p v-if="copyNotice !== null" class="state-message" role="status">{{ t(`codeExplorer.source.copy.${copyNotice}`) }}</p>
          <pre data-testid="code-source-result">{{ sourceItem.excerpt }}</pre>
        </template>
      </article>
    </div>
  </section>
</template>

<style scoped>
.results { display:grid; gap:14px; }.section-head { display:flex; align-items:flex-start; justify-content:space-between; gap:16px; border:1px solid var(--border); border-radius:var(--r-md); background:var(--surface); padding:16px; }h2, h3 { margin:0; color:var(--fg); }h2, h3 { font-size:var(--text-sm); font-weight:800; }.section-head p, .state-message, .source-meta, .result-evidence, .continuation-gap { margin:4px 0 0; color:var(--muted); font-size:var(--text-sm); }.status { display:grid; grid-template-columns:repeat(3,minmax(0,1fr)); gap:10px; }.status div { min-width:82px; }dt { color:var(--muted); font-size:var(--text-xs); letter-spacing:.04em; text-transform:uppercase; }dd { margin:4px 0 0; color:var(--fg); font-family:var(--font-mono); font-size:var(--text-xs); font-variant-numeric:tabular-nums; }.empty-index { display:flex; align-items:center; justify-content:space-between; gap:14px; border:1px solid color-mix(in oklab,var(--warn),transparent 45%); border-radius:var(--r-md); background:var(--surface-warm); padding:12px 14px; }.empty-index strong { color:var(--fg); }.empty-index p { max-width:68ch; margin:4px 0 0; color:var(--muted); font-size:var(--text-sm); }.search-form { display:grid; gap:6px; border:1px solid var(--border); border-radius:var(--r-md); background:var(--surface); padding:14px; }.search-form label { color:var(--muted); font-size:var(--text-xs); font-weight:700; letter-spacing:.04em; text-transform:uppercase; }.search-form div { display:grid; grid-template-columns:minmax(0,1fr) auto; gap:8px; }input { min-width:0; min-height:38px; border:1px solid var(--border); border-radius:var(--r-sm); background:var(--bg); color:var(--fg); padding:8px 10px; }.btn { min-height:36px; border:1px solid var(--border); border-radius:var(--r-sm); background:var(--surface); color:var(--fg); padding:8px 12px; font:inherit; font-size:var(--text-sm); font-weight:700; cursor:pointer; }.btn.primary { border-color:var(--accent); background:var(--accent); color:var(--accent-on); }.btn:disabled { cursor:not-allowed; opacity:.55; }.unselected { border:1px dashed var(--border); border-radius:var(--r-md); background:var(--bg); padding:24px; text-align:center; }.unselected strong { color:var(--fg); }.unselected p { margin:5px 0 0; color:var(--muted); font-size:var(--text-sm); }.result-grid { display:grid; grid-template-columns:repeat(3,minmax(0,1fr)); gap:14px; align-items:start; }.panel { min-width:0; border:1px solid var(--border); border-radius:var(--r-md); background:var(--surface); padding:14px; }.panel-head { display:flex; align-items:flex-start; justify-content:space-between; gap:8px; }.panel-head span { border:1px solid var(--border); border-radius:var(--radius-pill); padding:3px 7px; color:var(--muted); font-size:var(--text-xs); }.panel-head span[data-state='partial'], .panel-head span[data-state='stale'], .panel-head span[data-state='timeout'] { color:var(--warn); border-color:color-mix(in oklab,var(--warn),transparent 35%); }.panel-head span[data-state='denied'], .panel-head span[data-state='error'], .panel-head span[data-state='offline'] { color:var(--danger); border-color:color-mix(in oklab,var(--danger),transparent 35%); }.items, .warnings { display:grid; gap:10px; margin:14px 0 0; padding:0; list-style:none; }.items li { display:grid; gap:10px; border-top:1px solid var(--border-soft); padding-top:12px; }.item-copy strong { color:var(--fg); font-size:var(--text-sm); }.item-copy p { margin:4px 0; color:var(--muted); font-size:var(--text-xs); }pre { overflow:auto; margin:8px 0 0; border:1px solid var(--border-soft); border-radius:var(--r-sm); background:var(--bg); padding:10px; color:var(--fg); font-family:var(--font-mono); font-size:var(--text-xs); line-height:1.5; white-space:pre-wrap; overflow-wrap:anywhere; }.item-actions { display:flex; flex-wrap:wrap; gap:7px; }.warnings { color:var(--warn); font-size:var(--text-xs); list-style:disc; padding-left:18px; }.source-meta { font-family:var(--font-mono); font-size:var(--text-xs); }.continuation-gap { border-top:1px solid var(--border-soft); padding-top:10px; }
@media (max-width:1120px) { .result-grid { grid-template-columns:repeat(2,minmax(0,1fr)); }.source { grid-column:1 / -1; } }@media (max-width:720px) { .section-head, .search-form div, .result-grid, .status, .empty-index { display:grid; grid-template-columns:1fr; }.source { grid-column:auto; }.empty-index .btn { justify-self:start; } }
</style>
