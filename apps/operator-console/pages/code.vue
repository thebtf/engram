<script setup lang="ts">
import { computed, onMounted } from 'vue'
import { useOperatorCode } from '../composables/useOperatorCode'
const {
  bootstrapPhase,
  bootstrapEvidence,
  contextCandidate,
  pinnedContext,
  status,
  searchEnvelope,
  graphEnvelope,
  sourceEnvelope,
  searchState,
  graphState,
  sourceState,
  contextMessage,
  pending,
  initialize,
  discoverContext,
  pinContext,
  refreshStatus,
  search,
  explore,
  readSource,
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
        <h1>Code Explorer</h1>
        <p>Inspect one server-authorized immutable source view. Source text and graph facts appear only after their individual release.</p>
      </div>
      <button class="btn" type="button" :disabled="pending || pinnedContext === null" @click="refreshStatus">Refresh release status</button>
    </header>

    <CodeContextPicker
      :phase="bootstrapPhase"
      :candidate="contextCandidate"
      :pinned="pinnedContext"
      :pending="pending"
      :message="contextMessage"
      :evidence="bootstrapEvidence"
      @refresh="discoverContext"
      @pin="pinContext"
      @retry="initialize"
    />

    <CodeResults
      :pinned="pinnedContext"
      :status="status"
      :search="searchEnvelope"
      :graph="graphEnvelope"
      :source="sourceEnvelope"
      :search-state="searchState"
      :graph-state="graphState"
      :source-state="sourceState"
      :pending="pending"
      @search="search"
      @explore="explore"
      @source="readSource"
    />

    <footer class="release-note" :data-state="resultMode" data-testid="code-release-state">
      <strong>Release boundary:</strong>
      <span v-if="pinnedContext === null">No contextual payload is eligible for rendering before an explicit server pin.</span>
      <span v-else>Each status, search, graph, and source response must match this pinned view before rendering.</span>
    </footer>
  </main>
</template>

<style scoped>
.code-page { display:grid; gap:16px; min-width:0; }
.head { display:flex; align-items:flex-start; justify-content:space-between; gap:16px; }
h1 { margin:0 0 4px; color:var(--fg); font-size:var(--text-xl); font-weight:700; letter-spacing:var(--tracking-display); }
.head p { max-width:78ch; margin:0; color:var(--muted); font-size:var(--text-sm); }
.btn { min-height:36px; border:1px solid var(--border); border-radius:var(--r-sm); background:var(--surface); color:var(--fg); padding:8px 12px; font:inherit; font-size:var(--text-sm); font-weight:700; cursor:pointer; white-space:nowrap; }
.btn:disabled { cursor:not-allowed; opacity:.55; }
.release-note { display:flex; gap:6px; border:1px solid var(--border); border-radius:var(--r-sm); background:var(--surface-warm); padding:10px 12px; color:var(--fg-2); font-size:var(--text-sm); }
.release-note strong { color:var(--fg); }.release-note[data-state='partial'], .release-note[data-state='stale'] { border-color:color-mix(in oklab,var(--warn),transparent 35%); }
@media (max-width: 720px) { .head { display:grid; }.btn { justify-self:start; white-space:normal; } }
</style>
