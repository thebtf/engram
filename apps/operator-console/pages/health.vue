<script setup lang="ts">
const { t } = useI18n()
const {
  selfcheckState,
  readyState,
  vnextState,
  vectorState,
  updateStatusState,
  updateCheckState,
  components,
  embeddingMetrics,
  pending,
  error,
  refresh,
} = useOperatorHealthSettings()

function statusClass(status?: string): 'live' | 'dormant' | 'stale' {
  if (status === 'healthy' || status === 'ok' || status === 'ready') return 'live'
  if (status === 'degraded' || status === 'initializing') return 'dormant'
  return 'stale'
}
</script>

<template>
  <div class="health-page">
    <header class="head">
      <div>
        <h1>{{ t('health.title') }}</h1>
        <p>{{ t('health.subtitle') }}</p>
      </div>
      <button class="btn" type="button" :disabled="pending" @click="refresh">{{ t('health.refresh') }}</button>
    </header>

    <div v-if="pending" class="state pending">{{ t('health.state.pending') }}</div>
    <div v-if="error" class="state error">{{ t('health.state.error', { message: error }) }}</div>

    <section class="hero">
      <div>
        <span>{{ t('health.overall') }}</span>
        <strong>{{ selfcheckState.kind === 'live' ? selfcheckState.data.overall : '—' }}</strong>
      </div>
      <div>
        <span>{{ t('health.version') }}</span>
        <strong>{{ selfcheckState.kind === 'live' ? selfcheckState.data.version : '—' }}</strong>
      </div>
      <div>
        <span>{{ t('health.uptime') }}</span>
        <strong>{{ selfcheckState.kind === 'live' ? selfcheckState.data.uptime : '—' }}</strong>
      </div>
      <div>
        <span>{{ t('health.ready') }}</span>
        <strong>{{ readyState.kind === 'live' ? t('health.readyOk') : readyState.kind === 'error' ? t('health.readyError') : '—' }}</strong>
      </div>
    </section>

    <section class="grid">
      <article class="card">
        <div class="card-head">
          <h2>{{ t('health.components') }}</h2>
          <code>/api/selfcheck</code>
        </div>
        <div v-if="!components.length" class="empty">{{ t('health.emptyComponents') }}</div>
        <div v-else class="rows">
          <div v-for="component in components" :key="component.name" class="row">
            <span>{{ component.name }}</span>
            <HonestyBadge :cls="statusClass(component.status)" :evidence="statusClass(component.status) !== 'live' ? component.status : undefined" />
          </div>
        </div>
      </article>

      <article class="card">
        <div class="card-head">
          <h2>{{ t('health.embedding') }}</h2>
          <code>/api/stats/vnext</code>
        </div>
        <div class="metrics">
          <div v-for="metric in embeddingMetrics" :key="metric.label">
            <span>{{ t(`health.metrics.${metric.label}`) }}</span>
            <strong>{{ metric.value }}</strong>
          </div>
        </div>
      </article>

      <article class="card">
        <div class="card-head">
          <h2>{{ t('health.vector') }}</h2>
          <code>/api/vector/metrics</code>
        </div>
        <p class="message">{{ vectorState.kind === 'live' ? vectorState.data.message : t('health.state.notLoaded') }}</p>
        <HonestyBadge :cls="vectorState.kind === 'live' && vectorState.data.enabled ? 'live' : 'dormant'" :evidence="vectorState.kind === 'live' && vectorState.data.enabled ? undefined : t('health.vectorUnavailable')" />
      </article>

      <article class="card">
        <div class="card-head">
          <h2>{{ t('health.update') }}</h2>
          <code>/api/update/status</code>
        </div>
        <div class="metrics">
          <div>
            <span>{{ t('health.updateState') }}</span>
            <strong>{{ updateStatusState.kind === 'live' ? updateStatusState.data.state || t('health.idle') : '—' }}</strong>
          </div>
          <div>
            <span>{{ t('health.updateAvailable') }}</span>
            <strong>{{ updateCheckState.kind === 'live' ? (updateCheckState.data.available ? t('common.yes') : t('common.no')) : '—' }}</strong>
          </div>
          <div>
            <span>{{ t('health.noise') }}</span>
            <strong>{{ vnextState.kind === 'live' ? vnextState.data.noise_ratio ?? '—' : '—' }}</strong>
          </div>
        </div>
      </article>
    </section>
  </div>
</template>

<style scoped>
.health-page { display:grid; gap:16px; }
.head { display:flex; align-items:flex-start; justify-content:space-between; gap:16px; }
.head h1 { margin:0 0 4px; font-size:var(--text-xl); font-weight:700; }
.head p { margin:0; font-size:var(--text-sm); color:var(--muted); }
.btn { border:1px solid var(--border); border-radius:var(--r-sm); background:var(--surface); color:var(--fg); padding:9px 14px; font:inherit; font-weight:700; cursor:pointer; }
.btn:disabled { opacity:.55; cursor:wait; }
.state { border:1px solid var(--border); border-radius:var(--r-md); background:var(--surface); padding:10px 12px; color:var(--fg-2); font-size:var(--text-sm); }
.state.pending { border-color:color-mix(in oklab,var(--accent),transparent 55%); }
.state.error { color:var(--state-warn); border-color:color-mix(in oklab,var(--state-warn),transparent 45%); }
.hero { display:grid; grid-template-columns:repeat(auto-fit,minmax(170px,1fr)); gap:12px; }
.hero div, .card { border:1px solid var(--border); border-radius:var(--r-md); background:var(--surface); }
.hero div { padding:14px 16px; }
.hero span, .metrics span { display:block; color:var(--muted); font-size:var(--text-xs); text-transform:uppercase; letter-spacing:.04em; }
.hero strong, .metrics strong { display:block; margin-top:7px; font-family:var(--font-mono); font-size:var(--text-xl); color:var(--fg); overflow:hidden; text-overflow:ellipsis; }
.grid { display:grid; grid-template-columns:repeat(auto-fit,minmax(280px,1fr)); gap:14px; }
.card { padding:16px; }
.card-head { display:flex; align-items:center; gap:10px; margin-bottom:14px; }
.card-head h2 { margin:0; font-size:var(--text-sm); font-weight:800; }
.card-head code { margin-left:auto; font-family:var(--font-mono); font-size:10px; color:var(--muted); }
.rows { display:grid; gap:8px; }
.row { display:flex; align-items:center; justify-content:space-between; gap:10px; border-bottom:1px solid var(--border-soft); padding:8px 0; color:var(--fg-2); }
.metrics { display:grid; grid-template-columns:repeat(auto-fit,minmax(120px,1fr)); gap:12px; }
.message, .empty { color:var(--muted); font-size:var(--text-sm); margin:0 0 12px; }
@media (max-width: 720px) { .head { display:grid; } }
</style>
