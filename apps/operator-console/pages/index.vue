<script setup lang="ts">
import { computed } from 'vue'
import { useOperatorOverview } from '../composables/useOperatorOverview'

const { t } = useI18n()

const {
  memories,
  issues,
  creds,
  models,
  info,
  memoryActive,
  memoryNoise,
  openIssues,
  workIssues,
  resolvedIssues,
  closedIssues,
  ruleCount,
  projectCount,
  memoryPending,
  issuesPending,
  rulesPending,
  projectsPending,
  modelsPending,
  queuePending,
  queueGated,
  queueCount,
  modelsError,
  modelOK,
  modelStandby,
  modelDegraded,
  accessGap,
  graphFlagState,
} = useOperatorOverview()

function displayCount(value: number, pending = false) {
  return pending ? t('common.loadingShort') : String(value)
}

const memoryTiers = computed(() => [
  { label: 'semantic', count: memories.filter((memory) => memory.tier === 'semantic').length, color: 'var(--accent)' },
  { label: 'procedural', count: memories.filter((memory) => memory.tier === 'procedural').length, color: 'var(--class-live)' },
  { label: 'episodic', count: memories.filter((memory) => memory.tier === 'episodic').length, color: 'var(--state-warn)' },
])

const memoryCountDisplay = computed(() => displayCount(memories.length, memoryPending.value))
const memoryActiveDisplay = computed(() => displayCount(memoryActive.value, memoryPending.value))
const memoryNoiseDisplay = computed(() => displayCount(memoryNoise.value, memoryPending.value))
const issueCountDisplay = computed(() => displayCount(issues.length, issuesPending.value))
const openIssuesDisplay = computed(() => displayCount(openIssues.value, issuesPending.value))
const workIssuesDisplay = computed(() => displayCount(workIssues.value, issuesPending.value))
const resolvedIssuesDisplay = computed(() => displayCount(resolvedIssues.value, issuesPending.value))
const closedIssuesDisplay = computed(() => displayCount(closedIssues.value, issuesPending.value))
const ruleCountDisplay = computed(() => displayCount(ruleCount.value, rulesPending.value))
const projectCountDisplay = computed(() => displayCount(projectCount.value, projectsPending.value))
const modelCountDisplay = computed(() => displayCount(models.value.length, modelsPending.value))
const modelOKDisplay = computed(() => displayCount(modelOK.value, modelsPending.value))
const modelDegradedDisplay = computed(() => displayCount(modelDegraded.value, modelsPending.value))
const queueCountDisplay = computed(() => displayCount(queueCount.value, queuePending.value))
const graphFlagBadge = computed(() => {
  switch (graphFlagState.value) {
    case 'enabled':
      return { cls: 'live', text: t('common.enabled') }
    case 'disabled':
      return { cls: 'gate', text: t('overview.cards.graph.disabled') }
    case 'error':
      return { cls: 'gate', text: t('overview.cards.graph.error') }
    default:
      return { cls: 'status', text: t('overview.cards.graph.pending') }
  }
})

const memoryStops = computed(() => {
  const total = Math.max(1, memories.length)
  let acc = 0
  return memoryTiers.value.map((tier) => {
    acc += (tier.count / total) * 100
    return acc
  })
})

const taskBars = computed(() => {
  const rows = [
    { label: t('overview.tasks.open'), count: openIssues.value, display: openIssuesDisplay.value, color: 'var(--state-warn)' },
    { label: t('overview.tasks.work'), count: workIssues.value, display: workIssuesDisplay.value, color: 'var(--accent)' },
    { label: t('overview.tasks.resolved'), count: resolvedIssues.value, display: resolvedIssuesDisplay.value, color: 'var(--class-live)' },
    { label: t('overview.tasks.closed'), count: closedIssues.value, display: closedIssuesDisplay.value, color: 'var(--muted)' },
  ]
  const max = Math.max(1, ...rows.map((row) => row.count))
  return rows.map((row) => ({ ...row, width: Math.round((row.count / max) * 100) }))
})

const modelHealth = computed(() => {
  if (modelsPending.value && models.value.length === 0) {
    return [{ label: t('overview.modelHealth.loading'), count: 1, color: 'var(--muted)', width: 100 }]
  }

  if (modelsError.value) {
    return [{ label: t('overview.modelHealth.error'), count: 1, color: 'var(--state-warn)', width: 100 }]
  }

  const rows = [
    { label: t('overview.modelHealth.ok'), count: modelOK.value, color: 'var(--class-live)' },
    { label: t('overview.modelHealth.standby'), count: modelStandby.value, color: 'var(--muted)' },
    { label: t('overview.modelHealth.degraded'), count: modelDegraded.value, color: 'var(--state-warn)' },
  ]
  const total = Math.max(1, rows.reduce((sum, row) => sum + row.count, 0))
  return rows.map((row) => ({ ...row, width: Math.round((row.count / total) * 100) }))
})

const attention = computed(() => [
  {
    color: modelsError.value || modelDegraded.value > 0 ? 'var(--state-warn)' : 'var(--class-live)',
    text: modelsError.value
      ? t('overview.attention.modelHealthError')
      : modelDegraded.value > 0
        ? t('overview.attention.modelHealthDegraded', { n: modelDegradedDisplay.value })
        : t('overview.attention.modelHealthOk', { n: modelCountDisplay.value }),
    to: '/health',
    label: t('overview.attention.stateLink'),
  },
  { color: 'var(--state-warn)', text: t('overview.attention.search'), to: '/health', label: t('overview.attention.stateLink') },
  {
    color: queueGated.value ? 'var(--class-dormant)' : (queueCount.value > 0 ? 'var(--state-warn)' : 'var(--class-live)'),
    text: queueGated.value
      ? t('overview.attention.queueGated', { flag: 'VNEXT_F' })
      : t('overview.attention.queueLive', { n: queueCountDisplay.value }),
    to: '/queue',
    label: t('overview.attention.queueLink'),
  },
  { color: 'var(--class-mustbuild)', text: t('overview.attention.books'), to: '/books', label: t('overview.attention.booksLink') },
  { color: 'var(--state-warn)', text: t('overview.attention.noise', { noise: info.value.noise, n: memoryNoiseDisplay.value }), to: '/noise', label: t('overview.attention.noiseLink') },
])

const memoryCards = computed(() => [
  {
    to: '/memory',
    icon: 'memory',
    name: t('nav.items.memory'),
    big: memoryCountDisplay.value,
    sub: t('overview.cards.memory.sub', { active: memoryActiveDisplay.value, noise: memoryNoiseDisplay.value }),
    meta: [
      { cls: 'live', text: t('overview.badges.semanticCount', { n: displayCount(memoryTiers.value[0]?.count ?? 0, memoryPending.value) }) },
      { cls: 'status', text: t('overview.badges.episodicCount', { n: displayCount(memoryTiers.value[2]?.count ?? 0, memoryPending.value) }) },
    ],
  },
  {
    to: '/queue',
    icon: 'queue',
    name: t('nav.items.queue'),
    big: queueGated.value ? null : queueCountDisplay.value,
    sub: t('overview.cards.queue.sub'),
    meta: [{ cls: queueGated.value ? 'gate' : 'live', text: queueGated.value ? t('overview.badges.vnext') : t('overview.badges.liveEndpoint') }],
  },
  {
    to: '/noise',
    icon: 'noise',
    name: t('nav.items.noise'),
    big: String(info.value.noise),
    sub: t('overview.cards.noise.sub'),
    meta: [{ cls: 'gate', text: t('overview.badges.aboveNorm') }],
  },
  {
    to: '/graph',
    icon: 'graph',
    name: t('nav.items.graph'),
    big: '—',
    sub: t('overview.cards.graph.sub'),
    meta: [graphFlagBadge.value],
  },
  {
    to: '/books',
    icon: 'books',
    name: t('nav.items.books'),
    big: null,
    sub: t('overview.cards.books.sub'),
    meta: [{ cls: 'mb', text: t('overview.badges.mustBuild') }],
  },
])

const workCards = computed(() => [
  {
    to: '/rules',
    icon: 'rules',
    name: t('nav.items.rules'),
    big: ruleCountDisplay.value,
    sub: t('overview.cards.rules.sub'),
    meta: [],
  },
  {
    to: '/issues',
    icon: 'issues',
    name: t('nav.items.issues'),
    big: openIssuesDisplay.value,
    sub: t('overview.cards.issues.sub', { open: openIssuesDisplay.value, work: workIssuesDisplay.value, total: issueCountDisplay.value }),
    meta: [{ cls: 'pri-high', text: t('overview.cards.issues.openBadge', { n: openIssuesDisplay.value }) }],
  },
  {
    to: '/projects',
    icon: 'projects',
    name: t('nav.items.projects'),
    big: projectCountDisplay.value,
    sub: t('overview.cards.projects.sub', { n: projectCountDisplay.value }),
    meta: [],
  },
])

const serviceCards = computed(() => [
  {
    to: '/health',
    icon: 'health',
    name: t('nav.items.health'),
    big: String(info.value.health),
    sub: t('overview.cards.health.sub', { ok: modelOKDisplay.value, degraded: modelDegradedDisplay.value }),
    meta: [{ cls: 'live', text: t('overview.badges.liveEndpoint') }],
  },
  {
    to: '/secrets',
    icon: 'secrets',
    name: t('nav.items.secrets'),
    big: String(creds.length),
    sub: t('overview.cards.secrets.sub'),
    meta: [{ cls: 'live', text: t('overview.badges.vaultLive') }],
  },
  {
    to: '/access',
    icon: 'access',
    name: t('nav.items.access'),
    big: null,
    sub: t('overview.cards.access.sub'),
    meta: [{ cls: 'mb', text: t('overview.badges.mustBuild') }],
  },
])

function iconPath(icon: string) {
  const icons: Record<string, string> = {
    memory: '<path d="M3 4.5c0-1.1 2.2-2 5-2s5 .9 5 2-2.2 2-5 2-5-.9-5-2Z"/><path d="M3 4.5v7c0 1.1 2.2 2 5 2s5-.9 5-2v-7"/><path d="M3 8c0 1.1 2.2 2 5 2s5-.9 5-2"/>',
    queue: '<path d="M3 4h10M3 8h10M3 12h6"/>',
    noise: '<circle cx="8" cy="8" r="6"/><path d="M8 5v3l2 2"/>',
    graph: '<circle cx="4" cy="4" r="2"/><circle cx="12" cy="6" r="2"/><circle cx="7" cy="12" r="2"/><path d="M5.5 5.2 10.5 5 M5.4 5.6 6.4 10.2 M8.6 11 10.8 7.6"/>',
    books: '<path d="M3 2h8a2 2 0 0 1 2 2v10H5a2 2 0 0 1-2-2Z"/><path d="M5 2v12"/>',
    rules: '<path d="M3 3h10v10H3z"/><path d="M5.5 6.5h5M5.5 9.5h3"/>',
    issues: '<circle cx="8" cy="8" r="6"/><path d="M8 5v4M8 11h.01"/>',
    projects: '<path d="M2 4h5l1.5 2H14v6H2z"/>',
    health: '<path d="M2 8.2H5L6.8 4l2.4 8.4L11 8.2h3"/>',
    secrets: '<rect x="3" y="7" width="10" height="7" rx="1.5"/><path d="M5 7V5a3 3 0 0 1 6 0v2"/>',
    access: '<circle cx="8" cy="5.5" r="2.5"/><path d="M3.5 13c0-2.5 2-4 4.5-4s4.5 1.5 4.5 4"/>',
  }
  return icons[icon] || icons.memory
}
</script>

<template>
  <div class="overview-page">
    <header class="page-head">
      <h1>{{ t('overview.pageTitle') }}</h1>
      <p>{{ t('overview.pageSubtitle') }}</p>
    </header>

    <section class="ov-hero">
      <div class="ov-hi">
        <h2>{{ t('overview.heroTitle') }}</h2>
        <p>{{ info.host }} · {{ info.version }} · {{ t('overview.uptimePrefix') }} {{ info.uptime }}</p>
      </div>
      <div class="ov-id">
        <span class="health-pill" data-h="degraded"><span class="hd" />{{ t('shell.statusDegradation') }} · {{ info.health }}</span>
        <span class="bdg gate">{{ t('shell.statusNoise') }} {{ info.noise }}</span>
      </div>
    </section>

    <section class="ov-attention">
      <h3>
        <svg class="ov-ico" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.7" stroke-linecap="round" stroke-linejoin="round">
          <path d="M8 1.5 15 14H1Z" /><path d="M8 6.5v3" /><circle cx="8" cy="11.6" r=".4" />
        </svg>
        {{ t('overview.attentionTitle') }}
      </h3>
      <div class="ov-att-list">
        <NuxtLink v-for="item in attention" :key="item.text" :to="item.to" class="ov-att-row">
          <span class="oa-dot" :style="{ background: item.color }" />
          <span>{{ item.text }}</span>
          <span class="oa-go">{{ item.label }}</span>
        </NuxtLink>
      </div>
    </section>

    <section class="ov-viz">
      <div class="vz">
        <h4>{{ t('overview.viz.memoryComposition') }}<span class="src">{{ t('overview.recordsCount', { n: memoryCountDisplay }) }}</span></h4>
        <div class="donut-row">
          <div
            class="donut"
            :style="`--c1:${memoryTiers[0].color};--s1:${memoryStops[0]};--c2:${memoryTiers[1].color};--s2:${memoryStops[1]};--c3:${memoryTiers[2].color};--s3:${memoryStops[2]};--c4:var(--muted)`"
          >
            <span class="dn">{{ memoryCountDisplay }}</span>
            <span class="dl">{{ t('overview.total') }}</span>
          </div>
          <div class="donut-legend">
            <div v-for="tier in memoryTiers" :key="tier.label" class="dlg">
              <span class="sw" :style="{ background: tier.color }" />
              <span class="dn2">{{ tier.label }}</span>
              <span class="dv">{{ displayCount(tier.count, memoryPending) }}</span>
            </div>
          </div>
        </div>
        <div class="vz-trend">
          <svg viewBox="0 0 120 30" preserveAspectRatio="none" aria-hidden="true"><line x1="0" y1="15" x2="120" y2="15" stroke="currentColor" stroke-width="1.5" stroke-dasharray="3 5" opacity=".6" /></svg>
          <span class="tt">{{ t('overview.viz.memoryTrend') }} <b>{{ t('overview.badges.mustBuildParen') }}</b>; {{ t('overview.snapshotOnly') }}</span>
        </div>
      </div>

      <div class="vz">
        <h4>{{ t('overview.viz.tasksByStatus') }}<span class="src">{{ t('overview.totalIssues', { n: issueCountDisplay }) }}</span></h4>
        <div class="hbars">
          <div v-for="bar in taskBars" :key="bar.label" class="hbar">
            <span class="hl">{{ bar.label }}</span>
            <span class="ht"><i :style="{ width: `${bar.width}%`, background: bar.color }" /></span>
            <span class="hv">{{ bar.display }}</span>
          </div>
        </div>
      </div>

      <div class="vz">
        <h4>{{ t('overview.viz.modelHealth') }}<span class="src">/api/model-health</span></h4>
        <div class="segbar"><i v-for="row in modelHealth" :key="row.label" :style="{ width: `${row.width}%`, background: row.color }" /></div>
        <div class="seg-legend">
          <span v-for="row in modelHealth" :key="row.label"><span class="sw" :style="{ background: row.color }" />{{ row.label }} · {{ row.count }}</span>
        </div>
        <div class="vz-trend">
          <svg viewBox="0 0 120 30" preserveAspectRatio="none" aria-hidden="true"><line x1="0" y1="15" x2="120" y2="15" stroke="currentColor" stroke-width="1.5" stroke-dasharray="3 5" opacity=".6" /></svg>
          <span class="tt">
            <template v-if="modelsError">{{ t('overview.modelHealth.errorShort', { message: modelsError }) }}</template>
            <template v-else>{{ t('overview.viz.modelTrend') }} <b>{{ t('overview.badges.mustBuild') }}</b>; {{ t('overview.snapshotOnly') }}</template>
          </span>
        </div>
      </div>
    </section>

    <div class="ov-section-t">{{ t('overview.groups.memoryProduct') }}</div>
    <div class="ov-grid">
      <NuxtLink v-for="card in memoryCards" :key="card.to" :to="card.to" class="ov-card">
        <div class="ov-top">
          <svg class="ov-ico" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.7" stroke-linecap="round" stroke-linejoin="round" v-html="iconPath(card.icon)" />
          <span class="ov-name">{{ card.name }}</span>
          <span class="ov-arr">→</span>
        </div>
        <div v-if="card.big !== null" class="ov-big">{{ card.big }}</div>
        <div class="ov-sub">{{ card.sub }}</div>
        <div v-if="card.meta.length" class="ov-meta">
          <span v-for="meta in card.meta" :key="meta.text" class="bdg" :class="meta.cls">{{ meta.text }}</span>
        </div>
      </NuxtLink>
    </div>

    <div class="ov-section-t">{{ t('overview.groups.behaviorWork') }}</div>
    <div class="ov-grid">
      <NuxtLink v-for="card in workCards" :key="card.to" :to="card.to" class="ov-card">
        <div class="ov-top">
          <svg class="ov-ico" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.7" stroke-linecap="round" stroke-linejoin="round" v-html="iconPath(card.icon)" />
          <span class="ov-name">{{ card.name }}</span>
          <span class="ov-arr">→</span>
        </div>
        <div class="ov-big">{{ card.big }}</div>
        <div class="ov-sub">{{ card.sub }}</div>
        <div v-if="card.meta.length" class="ov-meta">
          <span v-for="meta in card.meta" :key="meta.text" class="bdg" :class="meta.cls">{{ meta.text }}</span>
        </div>
      </NuxtLink>
    </div>

    <div class="ov-section-t">{{ t('overview.groups.service') }}</div>
    <div class="ov-grid">
      <NuxtLink v-for="card in serviceCards" :key="card.to" :to="card.to" class="ov-card">
        <div class="ov-top">
          <svg class="ov-ico" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.7" stroke-linecap="round" stroke-linejoin="round" v-html="iconPath(card.icon)" />
          <span class="ov-name">{{ card.name }}</span>
          <span class="ov-arr">→</span>
        </div>
        <div class="ov-big">{{ card.big }}</div>
        <div class="ov-sub">{{ card.sub }}</div>
        <div v-if="card.meta.length" class="ov-meta">
          <span v-for="meta in card.meta" :key="meta.text" class="bdg" :class="meta.cls">{{ meta.text }}</span>
        </div>
      </NuxtLink>
    </div>
  </div>
</template>

<style scoped>
.overview-page { display:flex; flex-direction:column; gap:18px; }
.page-head { margin-bottom:2px; padding-bottom:14px; border-bottom:1px solid var(--border); }
.page-head h1 { margin:0 0 4px; font-size:var(--text-xl); font-weight:800; letter-spacing:var(--tracking-display); }
.page-head p { margin:0; color:var(--muted); font-size:var(--text-sm); }
.ov-hero { display:flex; align-items:flex-start; justify-content:space-between; gap:18px; padding:18px 20px; border:1px solid var(--border); border-radius:var(--r-md); background:var(--surface); }
.ov-hi h2 { margin:0 0 3px; font-size:var(--text-2xl); font-weight:800; letter-spacing:var(--tracking-display); }
.ov-hi p { margin:0; color:var(--muted); font-size:var(--text-sm); }
.ov-id { display:flex; align-items:center; gap:8px; flex-wrap:wrap; justify-content:flex-end; }
.health-pill { display:inline-flex; align-items:center; gap:7px; padding:5px 11px 5px 9px; border-radius:var(--radius-pill); font-size:var(--text-xs); font-weight:700; border:1px solid color-mix(in oklab,var(--state-warn),transparent 60%); color:var(--state-warn); background:color-mix(in oklab,var(--state-warn),transparent 89%); }
.health-pill .hd { width:8px; height:8px; border-radius:50%; background:var(--state-warn); }
.bdg { display:inline-flex; align-items:center; gap:4px; border:1px solid var(--border); border-radius:var(--radius-pill); padding:3px 8px; font-size:10px; font-weight:800; text-transform:uppercase; letter-spacing:.03em; color:var(--fg-2); background:var(--surface-warm); }
.bdg.live { color:var(--class-live); border-color:color-mix(in oklab,var(--class-live),transparent 58%); background:color-mix(in oklab,var(--class-live),transparent 90%); }
.bdg.gate, .bdg.pri-high { color:var(--class-dormant); border-color:color-mix(in oklab,var(--class-dormant),transparent 58%); background:color-mix(in oklab,var(--class-dormant),transparent 90%); }
.bdg.mb { color:var(--class-mustbuild); border-color:color-mix(in oklab,var(--class-mustbuild),transparent 58%); background:color-mix(in oklab,var(--class-mustbuild),transparent 90%); }
.bdg.status { color:var(--fg-2); }
.ov-attention { border:1px solid color-mix(in oklab,var(--state-warn),transparent 55%); border-radius:var(--r-md); background:color-mix(in oklab,var(--state-warn),transparent 91%); padding:15px 18px; }
.ov-attention h3 { display:flex; align-items:center; gap:8px; margin:0 0 9px; color:var(--state-warn); font-size:var(--text-sm); font-weight:800; }
.ov-ico { width:16px; height:16px; flex:none; }
.ov-att-list { display:flex; flex-direction:column; gap:2px; }
.ov-att-row { display:grid; grid-template-columns:8px minmax(0,1fr) auto; align-items:center; gap:10px; padding:7px 0; color:var(--fg-2); text-decoration:none; font-size:var(--text-sm); }
.ov-att-row:hover { color:var(--fg); }
.oa-dot { width:7px; height:7px; border-radius:50%; }
.oa-go { color:var(--muted); font-size:var(--text-xs); }
.ov-viz { display:grid; grid-template-columns:repeat(3,minmax(0,1fr)); gap:14px; }
.vz { min-width:0; border:1px solid var(--border); border-radius:var(--r-md); background:var(--surface); padding:16px 18px; }
.vz h4 { display:flex; align-items:center; gap:8px; margin:0 0 14px; font-size:var(--text-sm); font-weight:800; }
.vz h4 .src { margin-left:auto; font-family:var(--font-mono); font-size:10px; color:var(--muted); font-weight:500; }
.donut-row { display:flex; align-items:center; gap:18px; }
.donut { width:118px; height:118px; border-radius:50%; flex:none; position:relative; display:grid; place-items:center; background:conic-gradient(var(--c1) calc(var(--s1)*1%), var(--c2) 0 calc(var(--s2)*1%), var(--c3) 0 calc(var(--s3)*1%), var(--c4) 0); }
.donut::after { content:""; position:absolute; inset:15px; border-radius:50%; background:var(--surface); box-shadow:inset 0 0 0 1px var(--border-soft); }
.donut .dn { position:relative; z-index:1; font-family:var(--font-mono); font-size:var(--text-xl); font-weight:800; line-height:1; }
.donut .dl { position:relative; z-index:1; margin-top:24px; margin-left:-28px; font-size:9px; text-transform:uppercase; letter-spacing:.08em; color:var(--muted); }
.donut-legend { display:flex; flex-direction:column; gap:7px; flex:1; min-width:0; }
.dlg { display:flex; align-items:center; gap:8px; color:var(--fg-2); font-size:var(--text-sm); }
.dlg .sw, .seg-legend .sw { width:10px; height:10px; border-radius:3px; flex:none; }
.dlg .dv { margin-left:auto; font-family:var(--font-mono); font-weight:700; color:var(--fg); }
.hbars { display:flex; flex-direction:column; gap:11px; }
.hbar { display:grid; grid-template-columns:96px 1fr 34px; align-items:center; gap:10px; font-size:var(--text-sm); }
.hl { color:var(--fg-2); }
.ht { height:9px; border-radius:var(--radius-pill); overflow:hidden; background:var(--border-soft); }
.ht i { display:block; height:100%; border-radius:var(--radius-pill); }
.hv { font-family:var(--font-mono); font-weight:700; text-align:right; }
.segbar { display:flex; height:14px; border-radius:var(--radius-pill); overflow:hidden; background:var(--border-soft); margin-bottom:12px; }
.segbar i { min-width:3px; }
.seg-legend { display:flex; gap:16px; flex-wrap:wrap; color:var(--muted); font-size:var(--text-xs); }
.seg-legend span { display:inline-flex; align-items:center; gap:6px; }
.vz-trend { display:flex; align-items:center; gap:12px; margin-top:13px; padding-top:12px; border-top:1px solid var(--border-soft); }
.vz-trend svg { width:120px; height:30px; flex:none; color:var(--class-mustbuild); }
.vz-trend .tt { color:var(--muted); font-size:var(--text-xs); line-height:1.35; }
.vz-trend b { color:var(--class-mustbuild); }
.ov-section-t { margin:3px 0 -5px; color:var(--muted); font-size:var(--text-xs); font-weight:800; text-transform:uppercase; letter-spacing:.08em; }
.ov-grid { display:grid; grid-template-columns:repeat(4,minmax(180px,1fr)); gap:14px; }
.ov-card { min-height:118px; display:flex; flex-direction:column; gap:8px; border:1px solid var(--border); border-radius:var(--r-md); background:var(--surface); padding:15px 17px; color:var(--fg); text-decoration:none; }
.ov-card:hover { border-color:var(--accent); background:var(--surface-warm); }
.ov-top { display:flex; align-items:center; gap:8px; min-width:0; }
.ov-top .ov-ico { color:var(--accent); }
.ov-name { font-weight:800; font-size:var(--text-sm); white-space:nowrap; overflow:hidden; text-overflow:ellipsis; }
.ov-arr { margin-left:auto; color:var(--muted); }
.ov-big { margin-top:4px; font-family:var(--font-mono); font-size:var(--text-2xl); font-weight:800; line-height:1; }
.ov-sub { color:var(--muted); font-size:var(--text-xs); line-height:1.4; }
.ov-meta { display:flex; align-items:center; gap:7px; flex-wrap:wrap; margin-top:auto; }
@media (max-width:1180px) {
  .ov-viz { grid-template-columns:1fr; }
  .ov-grid { grid-template-columns:repeat(2,minmax(180px,1fr)); }
}
@media (max-width:760px) {
  .ov-hero { flex-direction:column; }
  .ov-grid { grid-template-columns:1fr; }
  .ov-att-row { grid-template-columns:8px 1fr; }
  .oa-go { grid-column:2; }
}
</style>
