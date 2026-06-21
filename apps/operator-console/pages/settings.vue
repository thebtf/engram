<script setup lang="ts">
const { t } = useI18n()
const {
  configState,
  updateStatusState,
  updateCheckState,
  configMetrics,
  restartRequired,
  pending,
  error,
  refresh,
  restartServer,
  restartAfterUpdate,
  configSaveGap,
  flagsGap,
} = useOperatorHealthSettings()

const restartConfirm = ref(false)
const updateRestartConfirm = ref(false)

const config = computed(() => configState.value.kind === 'live' ? configState.value.data : {})
const switches = computed(() => [
  {
    key: 'injectUnified',
    title: t('settings.switches.injectUnified.title'),
    desc: t('settings.switches.injectUnified.desc'),
    value: Boolean(config.value.memory?.inject_unified),
    evidence: 'memory.inject_unified',
    reload: true,
  },
  {
    key: 'telemetry',
    title: t('settings.switches.telemetry.title'),
    desc: t('settings.switches.telemetry.desc'),
    value: Boolean(config.value.features?.telemetry_enabled),
    evidence: 'features.telemetry_enabled',
    reload: false,
  },
  {
    key: 'sourceProject',
    title: t('settings.switches.sourceProject.title'),
    desc: t('settings.switches.sourceProject.desc'),
    value: Boolean(config.value.features?.enforce_source_project),
    evidence: 'features.enforce_source_project',
    reload: true,
  },
])

async function confirmRestartServer() {
  if (!restartConfirm.value) {
    restartConfirm.value = true
    return
  }
  await restartServer()
  restartConfirm.value = false
}

async function confirmUpdateRestart() {
  if (!updateRestartConfirm.value) {
    updateRestartConfirm.value = true
    return
  }
  await restartAfterUpdate()
  updateRestartConfirm.value = false
}
</script>

<template>
  <div class="settings-page">
    <header class="head">
      <div>
        <h1>{{ t('settings.title') }}</h1>
        <p>{{ t('settings.subtitle') }}</p>
      </div>
      <button class="btn" type="button" :disabled="pending" @click="refresh">{{ t('settings.refresh') }}</button>
    </header>

    <div v-if="pending" class="state pending">{{ t('settings.state.pending') }}</div>
    <div v-if="error" class="state error">{{ t('settings.state.error', { message: error }) }}</div>
    <div v-if="restartRequired" class="state restart">{{ t('settings.state.restartRequired') }}</div>

    <section class="card">
      <div class="card-head">
        <h2>{{ t('settings.runtime') }}</h2>
        <code>/api/config</code>
      </div>
      <div class="switches">
        <SwitchRow
          v-for="item in switches"
          :key="item.key"
          :model-value="item.value"
          cls="live"
          :title="item.title"
          :desc="item.desc"
          :evidence="item.evidence"
          :reload="item.reload"
          disabled
        />
      </div>
      <div class="gaps">
        <div class="gap">
          <HonestyBadge cls="mustbuild" :evidence="configSaveGap.evidence.endpoint" />
          <span>{{ t('settings.gaps.configSave') }}</span>
        </div>
        <div class="gap">
          <HonestyBadge cls="mustbuild" :evidence="flagsGap.evidence.endpoint" />
          <span>{{ t('settings.gaps.flags') }}</span>
        </div>
      </div>
    </section>

    <section class="card">
      <div class="card-head">
        <h2>{{ t('settings.configSnapshot') }}</h2>
        <code>/api/config</code>
      </div>
      <div class="metrics">
        <div v-for="metric in configMetrics" :key="metric.label">
          <span>{{ metric.label }}</span>
          <strong>{{ metric.value }}</strong>
        </div>
      </div>
    </section>

    <section class="card">
      <div class="card-head">
        <h2>{{ t('settings.restart.title') }}</h2>
        <code>/api/restart</code>
      </div>
      <p>{{ t('settings.restart.body') }}</p>
      <div class="actions">
        <button class="danger" type="button" @click="confirmRestartServer">
          {{ restartConfirm ? t('settings.restart.confirm') : t('settings.restart.action') }}
        </button>
        <button class="btn" type="button" :disabled="updateStatusState.kind === 'live' && updateStatusState.data.state !== 'done'" @click="confirmUpdateRestart">
          {{ updateRestartConfirm ? t('settings.restart.confirmUpdate') : t('settings.restart.updateAction') }}
        </button>
      </div>
      <p class="muted">
        {{ t('settings.restart.updateState') }}:
        <code>{{ updateStatusState.kind === 'live' ? updateStatusState.data.state || t('health.idle') : '—' }}</code>
        · {{ t('settings.restart.updateAvailable') }}:
        <code>{{ updateCheckState.kind === 'live' ? (updateCheckState.data.available ? t('common.yes') : t('common.no')) : '—' }}</code>
      </p>
    </section>
  </div>
</template>

<style scoped>
.settings-page { max-width:960px; display:grid; gap:16px; }
.head { display:flex; align-items:flex-start; justify-content:space-between; gap:16px; }
.head h1 { margin:0 0 4px; font-size:var(--text-xl); font-weight:700; }
.head p { margin:0; font-size:var(--text-sm); color:var(--muted); }
.btn, .danger { border:1px solid var(--border); border-radius:var(--r-sm); background:var(--surface); color:var(--fg); padding:9px 14px; font:inherit; font-weight:700; cursor:pointer; }
.btn:disabled { opacity:.55; cursor:not-allowed; }
.danger { border-color:color-mix(in oklab,var(--state-danger),transparent 55%); color:var(--state-danger); }
.state { border:1px solid var(--border); border-radius:var(--r-md); background:var(--surface); padding:10px 12px; color:var(--fg-2); font-size:var(--text-sm); }
.state.pending { border-color:color-mix(in oklab,var(--accent),transparent 55%); }
.state.error, .state.restart { color:var(--state-warn); border-color:color-mix(in oklab,var(--state-warn),transparent 45%); }
.card { border:1px solid var(--border); border-radius:var(--r-md); background:var(--surface); padding:16px; }
.card-head { display:flex; align-items:center; gap:10px; margin-bottom:12px; }
.card-head h2 { margin:0; font-size:var(--text-sm); font-weight:800; }
.card-head code { margin-left:auto; font-family:var(--font-mono); font-size:10px; color:var(--muted); }
.switches { padding:0 4px; }
.gaps { display:grid; gap:10px; margin-top:14px; }
.gap { display:flex; align-items:center; justify-content:space-between; gap:12px; border:1px dashed var(--border); border-radius:var(--r-sm); padding:10px 12px; color:var(--muted); font-size:var(--text-xs); }
.metrics { display:grid; grid-template-columns:repeat(auto-fit,minmax(180px,1fr)); gap:10px; }
.metrics div { border:1px solid var(--border-soft); border-radius:var(--r-sm); padding:10px; }
.metrics span { display:block; color:var(--muted); font-size:var(--text-xs); }
.metrics strong { display:block; margin-top:6px; font-family:var(--font-mono); color:var(--fg); }
.actions { display:flex; flex-wrap:wrap; gap:10px; }
.muted, .card p { color:var(--muted); font-size:var(--text-sm); }
@media (max-width: 720px) { .head { display:grid; } }
</style>
