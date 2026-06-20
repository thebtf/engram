<script setup lang="ts">
/** Settings — read truth from GET /api/config; writes stay honest until /api/flags exists. */
import { computed } from 'vue'
import { useServerConfigSnapshot } from '../composables/useMockData'

const { t } = useI18n()
const config = useServerConfigSnapshot()

const injectUnified = computed({
  get: () => config.snapshot.value.injectUnified,
  set: () => {},
})

const telemetryEnabled = computed({
  get: () => config.snapshot.value.telemetryEnabled,
  set: () => {},
})

const enforceSourceProject = computed({
  get: () => config.snapshot.value.enforceSourceProject,
  set: () => {},
})
</script>

<template>
  <div class="wrap">
    <header class="head">
      <h1>{{ t('settings.title') }}</h1>
      <p>{{ t('settings.subtitle') }}</p>
    </header>

    <p v-if="config.error" class="msg err">{{ config.error }}</p>
    <p v-else-if="config.pending" class="msg note">{{ t('settings.loading') }}</p>

    <section class="card">
      <div class="card-head">
        <h3 class="card-t">{{ t('settings.runtimeTitle') }}</h3>
        <HonestyBadge cls="live" evidence="GET /api/config" />
      </div>

      <SwitchRow
        v-model="injectUnified"
        cls="live"
        :title="t('settings.rows.injectUnified.title')"
        :desc="t('settings.rows.injectUnified.desc')"
        evidence="memory.inject_unified"
        disabled
      />
      <SwitchRow
        v-model="telemetryEnabled"
        cls="live"
        :title="t('settings.rows.telemetry.title')"
        :desc="t('settings.rows.telemetry.desc')"
        evidence="features.telemetry_enabled"
        disabled
      />
      <SwitchRow
        v-model="enforceSourceProject"
        cls="live"
        :title="t('settings.rows.enforceSourceProject.title')"
        :desc="t('settings.rows.enforceSourceProject.desc')"
        evidence="features.enforce_source_project"
        disabled
      />
    </section>

    <section class="card facts">
      <h3 class="card-t">{{ t('settings.snapshotTitle') }}</h3>
      <dl class="fact-grid">
        <div class="fact"><dt>{{ t('settings.facts.contextObservations') }}</dt><dd>{{ config.snapshot.contextObservations }}</dd></div>
        <div class="fact"><dt>{{ t('settings.facts.contextMaxTokens') }}</dt><dd>{{ config.snapshot.contextMaxTokens }}</dd></div>
        <div class="fact"><dt>{{ t('settings.facts.contextSessionCount') }}</dt><dd>{{ config.snapshot.contextSessionCount }}</dd></div>
        <div class="fact"><dt>{{ t('settings.facts.vectorStrategy') }}</dt><dd>{{ config.snapshot.vectorStrategy }}</dd></div>
        <div class="fact"><dt>{{ t('settings.facts.databaseMaxConns') }}</dt><dd>{{ config.snapshot.databaseMaxConns }}</dd></div>
        <div class="fact"><dt>{{ t('settings.facts.logBufferSize') }}</dt><dd>{{ config.snapshot.logBufferSize }}</dd></div>
      </dl>
    </section>

    <section class="card mb">
      <div class="card-head">
        <h3 class="card-t">{{ t('settings.mustBuildTitle') }}</h3>
        <HonestyBadge cls="mustbuild" evidence="GET /api/flags" />
      </div>
      <p class="mbody">{{ t('settings.mustBuildBody') }}</p>
    </section>
  </div>
</template>

<style scoped>
.wrap { max-width:920px; }
.head h1 { margin:0 0 4px; font-size:var(--text-xl); font-weight:700; }
.head p { margin:0 0 16px; font-size:var(--text-sm); color:var(--muted); }
.msg { margin:0 0 14px; font-size:var(--text-sm); }
.msg.note { color:var(--muted); }
.msg.err { color:var(--state-danger); }
.card { border:1px solid var(--border); border-radius:var(--r-md); background:var(--surface); padding:12px 20px 16px; margin-bottom:14px; }
.card-head { display:flex; align-items:center; justify-content:space-between; gap:12px; margin-bottom:6px; }
.card-t { margin:0; font-size:var(--text-sm); font-weight:700; color:var(--fg); }
.facts { padding-top:16px; }
.fact-grid { display:grid; grid-template-columns:repeat(auto-fit, minmax(220px, 1fr)); gap:12px 16px; margin:0; }
.fact { border:1px solid var(--border-soft); border-radius:var(--r-sm); padding:12px 14px; background:var(--bg); }
.fact dt { margin:0 0 6px; font-size:var(--text-xs); color:var(--muted); }
.fact dd { margin:0; font-family:var(--font-mono); font-size:var(--text-sm); color:var(--fg); }
.mb { border-color:color-mix(in oklab,var(--class-mustbuild),transparent 55%); }
.mbody { margin:0; font-size:var(--text-sm); color:var(--fg-2); line-height:1.5; }
</style>
