<script setup lang="ts">
const { t } = useI18n()
const { openSettingsModal } = useSettingsModal()
</script>

<template>
  <div class="settings-page">
    <header class="head">
      <h1>{{ t('settings.title') }}</h1>
      <p>{{ t('settings.subtitle') }}</p>
    </header>

    <section class="settings-launch">
      <h2>{{ t('settings.launch.title') }}</h2>
      <p>{{ t('settings.launch.body') }}</p>
      <div class="dactions">
        <button class="tbtn primary" type="button" @click="openSettingsModal('general')">
          {{ t('settings.launch.action') }}
        </button>
      </div>
    </section>
  </div>
</template>

<style scoped>
.settings-page { max-width: 960px; }
.head { margin-bottom: var(--space-4); }
.head h1 { margin: 0 0 4px; font-size: var(--text-xl); font-weight: 700; }
.head p { margin: 0; font-size: var(--text-sm); color: var(--muted); }
.settings-launch {
  max-width: 760px;
  margin: var(--space-8) auto;
  background: var(--surface);
  border: 1px solid var(--border);
  border-radius: var(--r-lg);
  padding: var(--space-6);
  box-shadow: var(--elev-ring);
}
.settings-launch h2 { margin: 0 0 8px; font-size: var(--text-xl); }
.settings-launch p { margin: 0; color: var(--fg-2); font-size: var(--text-sm); line-height: 1.5; }
.dactions { display: flex; gap: var(--space-2); margin-top: var(--space-5); }
.tbtn {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  min-height: 34px;
  border: 1px solid var(--border);
  border-radius: var(--r-sm);
  background: var(--surface);
  color: var(--fg);
  padding: 7px 12px;
  font: inherit;
  font-size: var(--text-sm);
  font-weight: 800;
  cursor: pointer;
}
.tbtn.primary { background: var(--accent); border-color: var(--accent); color: var(--accent-on); }
</style>
