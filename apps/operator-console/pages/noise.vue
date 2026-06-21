<script setup lang="ts">
const { t } = useI18n()
const {
  analyticsState,
  retrievalState,
  vnextState,
  noiseMetrics,
  pending,
  error,
  refresh,
  noiseTrendGap,
} = useOperatorSearchNoise()

const ratio = computed(() => {
  if (vnextState.value.kind !== 'live') return 0
  return Math.max(0, Math.min(1, vnextState.value.data.noise_ratio || 0))
})
</script>

<template>
  <div class="noise-page">
    <header class="head">
      <div>
        <h1>{{ t('noisePage.title') }}</h1>
        <p>{{ t('noisePage.subtitle') }}</p>
      </div>
      <button class="btn" type="button" :disabled="pending" @click="refresh">{{ t('noisePage.refresh') }}</button>
    </header>

    <div v-if="pending" class="state pending">{{ t('noisePage.state.pending') }}</div>
    <div v-if="error" class="state error">{{ t('noisePage.state.error', { message: error }) }}</div>

    <section class="hero">
      <div class="gauge" :style="`--p:${ratio * 100}`">
        <div class="gv">{{ vnextState.kind === 'live' ? (vnextState.data.noise_ratio ?? 0).toFixed(2) : '—' }}</div>
        <div class="gl">{{ t('noisePage.gauge.label') }}</div>
      </div>
      <div class="metrics">
        <article v-for="metric in noiseMetrics" :key="metric.label" class="metric">
          <span>{{ t(`noisePage.metrics.${metric.label}`) }}</span>
          <strong>{{ metric.value }}</strong>
          <code>{{ metric.endpoint }}</code>
        </article>
      </div>
    </section>

    <section class="grid">
      <article class="card">
        <div class="card-head">
          <h2>{{ t('noisePage.analytics.title') }}</h2>
          <code>{{ analyticsState.evidence.endpoint }}</code>
        </div>
        <dl class="stats">
          <div>
            <dt>{{ t('noisePage.analytics.total') }}</dt>
            <dd>{{ analyticsState.kind === 'live' ? analyticsState.data.total_searches ?? 0 : '—' }}</dd>
          </div>
          <div>
            <dt>{{ t('noisePage.analytics.today') }}</dt>
            <dd>{{ analyticsState.kind === 'live' ? analyticsState.data.searches_today ?? 0 : '—' }}</dd>
          </div>
          <div>
            <dt>{{ t('noisePage.analytics.latency') }}</dt>
            <dd>{{ analyticsState.kind === 'live' ? analyticsState.data.avg_latency_ms ?? 0 : '—' }}</dd>
          </div>
          <div>
            <dt>{{ t('noisePage.analytics.errors') }}</dt>
            <dd>{{ analyticsState.kind === 'live' ? analyticsState.data.search_errors ?? 0 : '—' }}</dd>
          </div>
        </dl>
      </article>

      <article class="card">
        <div class="card-head">
          <h2>{{ t('noisePage.retrieval.title') }}</h2>
          <code>{{ retrievalState.evidence.endpoint }}</code>
        </div>
        <dl class="stats">
          <div>
            <dt>{{ t('noisePage.retrieval.requests') }}</dt>
            <dd>{{ retrievalState.kind === 'live' ? retrievalState.data.total_requests ?? 0 : '—' }}</dd>
          </div>
          <div>
            <dt>{{ t('noisePage.retrieval.served') }}</dt>
            <dd>{{ retrievalState.kind === 'live' ? retrievalState.data.observations_served ?? 0 : '—' }}</dd>
          </div>
          <div>
            <dt>{{ t('noisePage.retrieval.stale') }}</dt>
            <dd>{{ retrievalState.kind === 'live' ? retrievalState.data.stale_excluded ?? 0 : '—' }}</dd>
          </div>
          <div>
            <dt>{{ t('noisePage.retrieval.duplicates') }}</dt>
            <dd>{{ retrievalState.kind === 'live' ? retrievalState.data.duplicates_removed ?? 0 : '—' }}</dd>
          </div>
        </dl>
      </article>

      <article class="card">
        <div class="card-head">
          <h2>{{ t('noisePage.unrecorded.title') }}</h2>
          <code>{{ vnextState.evidence.endpoint }}</code>
        </div>
        <p>{{ t('noisePage.unrecorded.body') }}</p>
        <strong class="big">{{ vnextState.kind === 'live' && vnextState.data.outcomes ? `${Math.round((vnextState.data.outcomes.unrecorded_fraction || 0) * 100)}%` : '—' }}</strong>
      </article>

      <article class="card gap">
        <div class="card-head">
          <h2>{{ t('noisePage.trend.title') }}</h2>
          <HonestyBadge cls="mustbuild" :evidence="noiseTrendGap.evidence.endpoint" />
        </div>
        <p>{{ t('noisePage.trend.body') }}</p>
        <code>{{ noiseTrendGap.evidence.endpoint }}</code>
      </article>
    </section>
  </div>
</template>

<style scoped>
.noise-page { display:grid; gap:16px; }
.head { display:flex; align-items:flex-start; justify-content:space-between; gap:16px; }
.head h1 { margin:0 0 4px; font-size:var(--text-xl); font-weight:700; }
.head p { margin:0; font-size:var(--text-sm); color:var(--muted); }
.btn { border:1px solid var(--border); border-radius:var(--r-sm); background:var(--surface); color:var(--fg); padding:9px 14px; font:inherit; font-weight:700; cursor:pointer; }
.btn:disabled { opacity:.55; cursor:wait; }
.state { border:1px solid var(--border); border-radius:var(--r-md); background:var(--surface); padding:10px 12px; color:var(--fg-2); font-size:var(--text-sm); }
.state.pending { border-color:color-mix(in oklab,var(--accent),transparent 55%); }
.state.error { color:var(--state-warn); border-color:color-mix(in oklab,var(--state-warn),transparent 45%); }
.hero { border:1px solid var(--border); border-radius:var(--r-md); background:var(--surface); padding:22px; display:grid; grid-template-columns:170px 1fr; gap:24px; align-items:center; }
.gauge { --p:0; width:150px; height:150px; border-radius:50%; flex:none; position:relative; display:grid; place-items:center; background:conic-gradient(var(--state-warn) calc(var(--p)*1%), color-mix(in oklab,var(--class-live),transparent 60%) 0); }
.gauge::after { content:""; position:absolute; inset:14px; border-radius:50%; background:var(--surface); }
.gv { position:relative; font-family:var(--font-mono); font-weight:800; font-size:var(--text-2xl); }
.gl { position:relative; font-size:10px; text-transform:uppercase; letter-spacing:.08em; color:var(--muted); }
.metrics { display:grid; grid-template-columns:repeat(auto-fit,minmax(150px,1fr)); gap:10px; }
.metric { border:1px solid var(--border-soft); border-radius:var(--r-sm); padding:11px; }
.metric span, .stats dt { display:block; color:var(--muted); font-size:var(--text-xs); text-transform:uppercase; letter-spacing:.04em; }
.metric strong, .stats dd, .big { display:block; margin:6px 0 0; font-family:var(--font-mono); font-size:var(--text-xl); font-weight:800; color:var(--fg); }
.metric code, .card-head code, .gap code { display:block; margin-top:8px; font-family:var(--font-mono); font-size:10px; color:var(--muted); }
.grid { display:grid; grid-template-columns:repeat(auto-fit,minmax(280px,1fr)); gap:14px; }
.card { border:1px solid var(--border); border-radius:var(--r-md); background:var(--surface); padding:16px; }
.card-head { display:flex; align-items:center; justify-content:space-between; gap:12px; margin-bottom:12px; }
.card-head h2 { margin:0; font-size:var(--text-sm); font-weight:800; }
.stats { display:grid; grid-template-columns:repeat(2,minmax(0,1fr)); gap:12px; margin:0; }
.stats dd { margin-bottom:0; }
.card p { color:var(--muted); font-size:var(--text-sm); margin:0 0 10px; }
.gap { border-color:color-mix(in oklab,var(--class-mustbuild),transparent 50%); }
@media (max-width: 820px) {
  .head, .hero { display:grid; grid-template-columns:1fr; }
  .gauge { margin:auto; }
}
</style>
