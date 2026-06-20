<script setup lang="ts">
/** Health — server health snapshot. Seam: /api/selfcheck, /api/stats/vnext, mock-only models. */
import { computed } from 'vue'
import { useHealthSnapshot, useModels, useServerInfo } from '../composables/useMockData'

const { t } = useI18n()
const models = useModels()
const info = useServerInfo()
const health = useHealthSnapshot()

const dot: Record<string, string> = {
  healthy: 'var(--class-live)',
  degraded: 'var(--state-warn)',
  unhealthy: 'var(--state-danger)',
}

const hcls: Record<string, 'live' | 'dormant' | 'stale'> = {
  ok: 'live',
  standby: 'live',
  degraded: 'dormant',
}

const overallCls = computed<'live' | 'dormant' | 'stale'>(() => {
  if (health.snapshot.value.overall === 'healthy') return 'live'
  if (health.snapshot.value.overall === 'degraded') return 'dormant'
  return 'stale'
})
</script>

<template>
  <div>
    <header class="head">
      <h1>{{ t('health.title') }}</h1>
      <p>{{ t('health.subtitle') }}</p>
    </header>

    <p v-if="health.error" class="msg err">{{ health.error }}</p>
    <p v-else-if="health.pending" class="msg note">{{ t('health.loading') }}</p>

    <div class="cards">
      <div class="card">
        <h3>{{ t('health.embeddingTitle') }} <span class="src">/api/stats/vnext</span></h3>
        <div class="figs">
          <div class="fig"><div class="fn">{{ health.snapshot.embedding.chunkCount }}</div><div class="fl">{{ t('health.embedding.chunkCount') }}</div></div>
          <div class="fig"><div class="fn" style="color:var(--class-live)">{{ health.snapshot.embedding.withVectors }}</div><div class="fl">{{ t('health.embedding.withVectors') }}</div></div>
          <div class="fig"><div class="fn">{{ health.snapshot.embedding.dimension }}</div><div class="fl">{{ t('health.embedding.dimension') }}</div></div>
        </div>
        <div class="coverage">
          <span>{{ t('health.embedding.coverage') }}</span>
          <strong>{{ health.snapshot.embedding.coverage }}</strong>
        </div>
      </div>

      <div class="card">
        <h3>{{ t('health.servicesTitle') }} <span class="src">/api/selfcheck</span></h3>
        <div class="svc-overall">
          <span>{{ t('health.overall') }}</span>
          <HonestyBadge :cls="overallCls" :label="health.snapshot.overall" />
        </div>
        <div class="subsys">
          <span v-for="component in health.snapshot.components" :key="component.name" class="s">
            <i :style="{ background: dot[component.status] || 'var(--muted)' }" />
            {{ component.name }}
          </span>
        </div>
      </div>

      <div class="card">
        <h3>{{ t('health.modelsTitle') }} <span class="src">{{ info.version }}</span></h3>
        <div v-for="model in models" :key="model.id" class="mrow">
          <code>{{ model.id }}</code>
          <HonestyBadge :cls="hcls[model.health]" :evidence="model.health === 'degraded' ? 'degraded' : undefined" />
          <span class="cost">{{ model.costs }}</span>
        </div>
        <p class="note">{{ t('health.modelsNote') }}</p>
      </div>

      <div class="card mb">
        <div class="card-head">
          <h3>{{ t('health.migrationsTitle') }}</h3>
          <HonestyBadge cls="mustbuild" evidence="GET /api/migrations" />
        </div>
        <p class="mid">{{ t('health.migrationsBody') }}</p>
      </div>
    </div>
  </div>
</template>

<style scoped>
.head h1 { margin:0 0 4px; font-size:var(--text-xl); font-weight:700; }
.head p { margin:0 0 16px; font-size:var(--text-sm); color:var(--muted); }
.msg { margin:0 0 14px; font-size:var(--text-sm); }
.msg.note { color:var(--muted); }
.msg.err { color:var(--state-danger); }
.cards { display:grid; grid-template-columns:repeat(auto-fit,minmax(280px,1fr)); gap:14px; }
.card { border:1px solid var(--border); border-radius:var(--r-md); background:var(--surface); padding:16px 20px; }
.card-head { display:flex; align-items:center; justify-content:space-between; gap:12px; }
.card h3 { margin:0 0 14px; font-size:var(--text-sm); font-weight:600; display:flex; }
.card h3 .src { margin-left:auto; font-family:var(--font-mono); font-size:10px; color:var(--muted); font-weight:500; }
.figs { display:flex; gap:28px; margin-bottom:14px; }
.fig .fn { font-family:var(--font-mono); font-weight:700; font-size:var(--text-2xl); }
.fig .fl { font-size:11px; color:var(--muted); margin-top:4px; }
.coverage { display:flex; align-items:center; gap:10px; font-size:var(--text-sm); color:var(--fg-2); }
.coverage strong { font-family:var(--font-mono); color:var(--fg); }
.svc-overall { display:flex; align-items:center; justify-content:space-between; gap:12px; margin-bottom:12px; font-size:var(--text-sm); color:var(--fg-2); }
.subsys { display:flex; flex-wrap:wrap; gap:10px; }
.subsys .s { display:inline-flex; align-items:center; gap:6px; font-size:var(--text-xs); color:var(--fg-2); }
.subsys .s i { width:7px; height:7px; border-radius:50%; }
.mrow { display:flex; align-items:center; gap:10px; padding:7px 0; border-bottom:1px solid var(--border-soft); font-size:var(--text-sm); }
.mrow code { font-family:var(--font-mono); font-size:var(--text-xs); }
.mrow .cost { margin-left:auto; font-family:var(--font-mono); font-size:11px; color:var(--muted); }
.note { margin:12px 0 0; font-size:var(--text-xs); color:var(--muted); }
.card.mb { border-color:color-mix(in oklab,var(--class-mustbuild),transparent 55%); }
.mid { font-size:var(--text-sm); color:var(--fg-2); margin:0; }
</style>
