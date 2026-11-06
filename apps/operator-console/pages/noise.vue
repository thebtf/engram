<script setup lang="ts">
const { t } = useI18n()
const {
  analyticsState,
  retrievalState,
  vnextState,
  pending,
  error,
  refresh,
} = useOperatorSearchNoise()

</script>

<template>
  <div class="noise-page">
    <header class="head">
      <div>
        <h1>{{ t('noisePage.title') }}</h1>
        <p>{{ t('knowledge.serverScope') }}</p>
      </div>
      <button class="btn" type="button" :disabled="pending" @click="refresh">{{ t('noisePage.refresh') }}</button>
    </header>

    <div v-if="pending" class="state pending">{{ t('noisePage.state.pending') }}</div>
    <div v-if="error" class="state error">{{ t('noisePage.state.error', { message: error }) }}</div>

    <section class="citation-summary" aria-labelledby="citation-share-title">
      <div class="snapshot-value">
        <h2 id="citation-share-title">{{ t('noisePage.gauge.label') }}</h2>
        <div class="gv">{{ vnextState.kind === 'live' && typeof vnextState.data.noise_ratio === 'number' ?
          `${Math.round(vnextState.data.noise_ratio * 100)}%` : '—' }}</div>
      </div>
      <dl class="stats citation-counts">
        <div>
          <dt>{{ t('noisePage.citations.cited') }}</dt>
          <dd>{{ vnextState.kind === 'live' ? vnextState.data.citation_count ?? '—' : '—' }}</dd>
        </div>
        <div>
          <dt>{{ t('noisePage.citations.uncited') }}</dt>
          <dd>{{ vnextState.kind === 'live' ? vnextState.data.uncited_count ?? '—' : '—' }}</dd>
        </div>
      </dl>
    </section>

    <section class="grid">
      <article class="card">
        <div class="card-head">
          <h2>{{ t('noisePage.analytics.title') }}</h2>
        </div>
        <dl class="stats">
          <div>
            <dt>{{ t('noisePage.analytics.total') }}</dt>
            <dd>{{ analyticsState.kind === 'live' ? analyticsState.data.total_searches ?? '—' : '—' }}</dd>
          </div>
          <div>
            <dt>{{ t('noisePage.analytics.today') }}</dt>
            <dd>{{ analyticsState.kind === 'live' ? analyticsState.data.searches_today ?? '—' : '—' }}</dd>
          </div>
          <div>
            <dt>{{ t('noisePage.analytics.latency') }}</dt>
            <dd>{{ analyticsState.kind === 'live' ? analyticsState.data.avg_latency_ms ?? '—' : '—' }}</dd>
          </div>
          <div>
            <dt>{{ t('noisePage.analytics.errors') }}</dt>
            <dd>{{ analyticsState.kind === 'live' ? analyticsState.data.search_errors ?? '—' : '—' }}</dd>
          </div>
        </dl>
      </article>

      <article class="card">
        <div class="card-head">
          <h2>{{ t('noisePage.retrieval.title') }}</h2>
        </div>
        <dl class="stats">
          <div>
            <dt>{{ t('noisePage.retrieval.requests') }}</dt>
            <dd>{{ retrievalState.kind === 'live' ? retrievalState.data.total_requests ?? '—' : '—' }}</dd>
          </div>
          <div>
            <dt>{{ t('noisePage.retrieval.served') }}</dt>
            <dd>{{ retrievalState.kind === 'live' ? retrievalState.data.observations_served ?? '—' : '—' }}</dd>
          </div>
          <div>
            <dt>{{ t('noisePage.retrieval.stale') }}</dt>
            <dd>{{ retrievalState.kind === 'live' ? retrievalState.data.stale_excluded ?? '—' : '—' }}</dd>
          </div>
          <div>
            <dt>{{ t('noisePage.retrieval.duplicates') }}</dt>
            <dd>{{ retrievalState.kind === 'live' ? retrievalState.data.duplicates_removed ?? '—' : '—' }}</dd>
          </div>
          <div>
            <dt>{{ t('noisePage.retrieval.searchRequests') }}</dt>
            <dd>{{ retrievalState.kind === 'live' ? retrievalState.data.search_requests ?? '—' : '—' }}</dd>
          </div>
        </dl>
      </article>

      <article class="card">
        <div class="card-head">
          <h2>{{ t('noisePage.unrecorded.title') }}</h2>
        </div>
        <p>{{ t('noisePage.unrecorded.body') }}</p>
        <strong class="big">{{ vnextState.kind === 'live' && typeof vnextState.data.outcomes?.unrecorded_fraction ===
          'number' ? `${Math.round(vnextState.data.outcomes.unrecorded_fraction * 100)}%` : '—' }}</strong>
      </article>

    </section>
    <details class="sources">
      <summary>{{ t('noisePage.sources.title') }}</summary>
      <p>{{ t('noisePage.sources.citations') }}</p>
      <code>uncited_count / (citation_count + uncited_count)</code>
      <p>{{ t('noisePage.sources.unknown') }}</p>
      <ul>
        <li><code>{{ vnextState.evidence.endpoint }}</code></li>
        <li><code>{{ analyticsState.evidence.endpoint }}</code></li>
        <li><code>{{ retrievalState.evidence.endpoint }}</code></li>
      </ul>
    </details>
  </div>
</template>

<style scoped>
.noise-page {
  display: grid;
  gap: 16px;
}

.head {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  gap: 16px;
}

.head h1 {
  margin: 0 0 4px;
  font-size: var(--text-xl);
  font-weight: 700;
}

.head p {
  margin: 0;
  font-size: var(--text-sm);
  color: var(--muted);
}

.btn {
  border: 1px solid var(--border);
  border-radius: var(--r-sm);
  background: var(--surface);
  color: var(--fg);
  padding: 9px 14px;
  font: inherit;
  font-weight: 700;
  cursor: pointer;
}

.btn:disabled {
  opacity: .55;
  cursor: wait;
}

.state {
  border: 1px solid var(--border);
  border-radius: var(--r-md);
  background: var(--surface);
  padding: 10px 12px;
  color: var(--fg-2);
  font-size: var(--text-sm);
}

.state.pending {
  border-color: color-mix(in oklab, var(--accent), transparent 55%);
}

.state.error {
  color: var(--state-warn);
  border-color: color-mix(in oklab, var(--state-warn), transparent 45%);
}

.citation-summary {
  border: 1px solid var(--border);
  border-radius: var(--r-md);
  background: var(--surface);
  padding: 16px;
  display: grid;
  grid-template-columns: minmax(0, 1fr) minmax(0, 1fr);
  gap: 16px;
  align-items: center;
}

.snapshot-value h2 {
  margin: 0 0 8px;
  font-size: var(--text-sm);
  font-weight: 700;
}

.gv {
  font-weight: 800;
  font-size: var(--text-2xl);
  font-variant-numeric: tabular-nums;
}

.stats dt {
  color: var(--muted);
  font-size: var(--text-sm);
}

.stats dd,
.big {
  display: block;
  margin: 6px 0 0;
  font-size: var(--text-xl);
  font-weight: 800;
  color: var(--fg);
  font-variant-numeric: tabular-nums;
}

.grid {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(280px, 1fr));
  gap: 14px;
}

.card {
  border: 1px solid var(--border);
  border-radius: var(--r-md);
  background: var(--surface);
  padding: 16px;
}

.card-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  margin-bottom: 12px;
}

.card-head h2 {
  margin: 0;
  font-size: var(--text-sm);
  font-weight: 800;
}

.stats {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: 12px;
  margin: 0;
}

.stats dd {
  margin-bottom: 0;
  overflow-wrap: anywhere;
}

.card p {
  color: var(--muted);
  font-size: var(--text-sm);
  margin: 0 0 10px;
}

.sources {
  border-top: 1px solid var(--border);
  padding-top: 12px;
  color: var(--fg-2);
  font-size: var(--text-sm);
}

.sources summary {
  cursor: pointer;
  min-height: 44px;
  display: list-item;
  padding: 10px 0;
  box-sizing: border-box;
  color: var(--fg);
  font-weight: 700;
}

.sources p {
  max-width: 72ch;
  line-height: 1.5;
}

.sources code {
  overflow-wrap: anywhere;
}

.btn:focus-visible,
.sources summary:focus-visible {
  outline: 2px solid var(--accent);
  outline-offset: 3px;
}

@media (max-width:820px) {

  .head,
  .citation-summary {
    display: grid;
    grid-template-columns: 1fr;
  }
}
</style>
