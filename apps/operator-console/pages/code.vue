<script setup lang="ts">
import { computed, onMounted } from 'vue'
import { useOperatorCode } from '../composables/useOperatorCode'

const { t } = useI18n()
const {
  bootstrapPhase,
  bootstrapEvidence,
  contextCatalog,
  contextState,
  contextCandidate,
  pinnedContext,
  status,
  searchEnvelope,
  graphEnvelope,
  sourceEnvelope,
  searchContinuationNotice,
  searchState,
  graphState,
  sourceState,
  pending,
  indexIntentState,
  indexIntentPending,
  initialize,
  discoverContext,
  selectContext,
  pinContext,
  refreshStatus,
  submitIndexIntent,
  retryIndexIntent,
  refreshIndexIntent,
  search,
  continueSearch,
  explore,
  readSource,
  continueGraph,
} = useOperatorCode()

const resultMode = computed(() => pinnedContext.value === null ? 'unselected' : searchState.value.kind)

onMounted(() => {
  void initialize()
})
</script>

<template>
  <main class="code-page">
    <header class="head">
      <div>
        <h1>{{ t('codeExplorer.title') }}</h1>
        <p>{{ t('codeExplorer.subtitle') }}</p>
      </div>
      <button class="btn" type="button" :disabled="pending || pinnedContext === null" @click="refreshStatus">{{ t('codeExplorer.refresh') }}</button>
    </header>

    <CodeContextPicker
      :phase="bootstrapPhase"
      :state="contextState"
      :catalog="contextCatalog"
      :candidate="contextCandidate"
      :pinned="pinnedContext"
      :pending="pending"
      :evidence="bootstrapEvidence"
      @refresh="discoverContext"
      @select="selectContext"
      @pin="pinContext"
      @retry="initialize"
    />

    <CodeIndexIntentStatus
      :pinned="pinnedContext"
      :state="indexIntentState"
      :busy="indexIntentPending"
      :pending="pending"
      @submit="submitIndexIntent"
      @retry="retryIndexIntent"
      @refresh="refreshIndexIntent"
    />

    <CodeResults
      :pinned="pinnedContext"
      :status="status"
      :search="searchEnvelope"
      :graph="graphEnvelope"
      :source="sourceEnvelope"
      :search-continuation-notice="searchContinuationNotice"
      :search-state="searchState"
      :graph-state="graphState"
      :source-state="sourceState"
      :pending="pending"
      @search="search"
      @continue-search="continueSearch"
      @explore="explore"
      @continue-graph="continueGraph"
      @source="readSource"
      @request-index="submitIndexIntent('reindex')"
    />

    <details class="release-note" :data-state="resultMode" data-testid="code-release-state">
      <summary>{{ t('codeExplorer.evidence.title') }}</summary>
      <span v-if="pinnedContext === null">{{ t('codeExplorer.evidence.unselected') }}</span>
      <span v-else>{{ t('codeExplorer.evidence.pinned', { view: pinnedContext.view }) }}</span>
    </details>
  </main>
</template>

<style scoped>
.code-page { display:grid; gap:16px; min-width:0; }
.head { display:flex; align-items:flex-start; justify-content:space-between; gap:16px; }
h1 { margin:0 0 4px; color:var(--fg); font-size:var(--text-xl); font-weight:700; letter-spacing:var(--tracking-display); }
.head p { max-width:78ch; margin:0; color:var(--muted); font-size:var(--text-sm); }
.btn { min-height:36px; border:1px solid var(--border); border-radius:var(--r-sm); background:var(--surface); color:var(--fg); padding:8px 12px; font:inherit; font-size:var(--text-sm); font-weight:700; cursor:pointer; white-space:nowrap; }
.btn:disabled { cursor:not-allowed; opacity:.55; }
.release-note { display:grid; gap:6px; border:1px solid var(--border); border-radius:var(--r-sm); background:var(--surface-warm); padding:10px 12px; color:var(--fg-2); font-size:var(--text-sm); }.release-note summary { color:var(--fg); cursor:pointer; font-weight:700; }.release-note[data-state='partial'], .release-note[data-state='stale'] { border-color:color-mix(in oklab,var(--warn),transparent 35%); }
@media (max-width: 720px) { .head { display:grid; }.btn { justify-self:start; white-space:normal; } }
</style>
