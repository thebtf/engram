<script setup lang="ts">
import { DOMAIN_OWNER_KINDS, DOMAIN_OWNER_MODES, useOperatorDomainRegistry } from '../composables/useOperatorDomainRegistry'
import type { DomainRegistryDraft, OperatorMemoryDomain } from '../composables/useOperatorDomainRegistry'
import { computed, onBeforeUnmount, ref, watch } from 'vue'

type SettingsTabKind = 'general' | 'runtime' | 'actions' | 'domains' | 'client' | 'dead' | 'mustbuild'
type SettingsTabClass = 'live' | 'mustbuild' | 'stale'

interface SettingsTab {
  id: string
  groupKey: string
  labelKey: string
  titleKey: string
  descKey: string
  kind: SettingsTabKind
  cls: SettingsTabClass
  evidence?: string
}

const open = defineModel<boolean>('open', { default: false })
const activeTab = defineModel<string>('activeTab', { default: 'general' })

const { t, locale, locales, setLocale } = useI18n()
const colorMode = useColorMode()
const density = useState<'comfortable' | 'compact'>('density', () => 'compact')
const shell = useOperatorShellStatus()
const info = shell.info
const {
  configState,
  flagsState,
  updateStatusState,
  updateCheckState,
  configMetrics,
  flagItems,
  configPendingRestart,
  restartRequired,
  pending,
  error,
  refresh,
  saveConfig,
  restartServer,
  restartAfterUpdate,
  configSaveEvidence,
} = useOperatorHealthSettings()
const {
  domainState,
  domains,
  count: domainCount,
  pending: domainsPending,
  error: domainsError,
  refreshDomains,
  upsertDomain,
  deleteDomain,
  listEvidence: domainListEvidence,
} = useOperatorDomainRegistry()

const restartConfirm = ref(false)
const updateRestartConfirm = ref(false)
const restartInFlight = ref(false)
const updateRestartInFlight = ref(false)
const previousBodyOverflow = ref<string | null>(null)
const configSaveInFlight = ref(false)
const domainSaveInFlight = ref(false)
const domainDeleteInFlight = ref<string | null>(null)
const domainDeleteConfirm = ref<string | null>(null)
const editingDomain = ref<string | null>(null)
const configSaveResult = ref<Awaited<ReturnType<typeof saveConfig>> | null>(null)
const domainSaveResult = ref<Awaited<ReturnType<typeof upsertDomain>> | null>(null)
const domainDeleteResult = ref<Awaited<ReturnType<typeof deleteDomain>> | null>(null)
const configDraftTouched = ref(false)
const draftInjectUnified = ref(false)
const draftSourceProject = ref(false)
const domainDraft = ref<DomainRegistryDraft>({
  domain: '',
  ownerPrincipal: '',
  ownerPrincipalKind: 'agent',
  mode: 'warn',
})

const tabs = computed<SettingsTab[]>(() => [
  { id: 'general', groupKey: 'basic', labelKey: 'general', titleKey: 'general', descKey: 'general', kind: 'general', cls: 'live' },
  { id: 'models', groupKey: 'models', labelKey: 'models', titleKey: 'models', descKey: 'models', kind: 'mustbuild', cls: 'mustbuild', evidence: 'GET /api/models' },
  { id: 'credentials', groupKey: 'models', labelKey: 'credentials', titleKey: 'credentials', descKey: 'credentials', kind: 'mustbuild', cls: 'mustbuild', evidence: 'GET /api/model-credentials' },
  { id: 'bindings', groupKey: 'models', labelKey: 'bindings', titleKey: 'bindings', descKey: 'bindings', kind: 'mustbuild', cls: 'mustbuild', evidence: 'GET /api/model-bindings' },
  { id: 'addModel', groupKey: 'models', labelKey: 'addModel', titleKey: 'addModel', descKey: 'addModel', kind: 'mustbuild', cls: 'mustbuild', evidence: 'POST /api/models' },
  { id: 'acProviders', groupKey: 'access', labelKey: 'acProviders', titleKey: 'acProviders', descKey: 'acProviders', kind: 'mustbuild', cls: 'mustbuild', evidence: 'GET /api/access/providers' },
  { id: 'acPolicy', groupKey: 'access', labelKey: 'acPolicy', titleKey: 'acPolicy', descKey: 'acPolicy', kind: 'mustbuild', cls: 'mustbuild', evidence: 'GET /api/access/policy' },
  { id: 'acUsers', groupKey: 'access', labelKey: 'acUsers', titleKey: 'acUsers', descKey: 'acUsers', kind: 'mustbuild', cls: 'mustbuild', evidence: 'GET /api/access/users' },
  { id: 'acInvites', groupKey: 'access', labelKey: 'acInvites', titleKey: 'acInvites', descKey: 'acInvites', kind: 'mustbuild', cls: 'mustbuild', evidence: 'GET /api/access/invites' },
  { id: 'acRoles', groupKey: 'access', labelKey: 'acRoles', titleKey: 'acRoles', descKey: 'acRoles', kind: 'mustbuild', cls: 'mustbuild', evidence: 'GET /api/access/roles' },
  { id: 'acSessions', groupKey: 'access', labelKey: 'acSessions', titleKey: 'acSessions', descKey: 'acSessions', kind: 'mustbuild', cls: 'mustbuild', evidence: 'GET /api/access/sessions' },
  { id: 'acAudit', groupKey: 'access', labelKey: 'acAudit', titleKey: 'acAudit', descKey: 'acAudit', kind: 'mustbuild', cls: 'mustbuild', evidence: 'GET /api/access/audit' },
  { id: 'runtime', groupKey: 'server', labelKey: 'runtime', titleKey: 'runtime', descKey: 'runtime', kind: 'runtime', cls: 'live' },
  { id: 'actions', groupKey: 'server', labelKey: 'actions', titleKey: 'actions', descKey: 'actions', kind: 'actions', cls: 'live' },
  { id: 'domains', groupKey: 'server', labelKey: 'domains', titleKey: 'domains', descKey: 'domains', kind: 'domains', cls: 'live' },
  { id: 'client', groupKey: 'server', labelKey: 'client', titleKey: 'client', descKey: 'client', kind: 'client', cls: 'live' },
  { id: 'dead', groupKey: 'server', labelKey: 'dead', titleKey: 'dead', descKey: 'dead', kind: 'dead', cls: 'stale' },
])

const groupedTabs = computed(() => {
  const groups: { key: string; label: string; tabs: SettingsTab[] }[] = []
  for (const tab of tabs.value) {
    let group = groups.find((item) => item.key === tab.groupKey)
    if (!group) {
      group = { key: tab.groupKey, label: t(`settings.modal.groups.${tab.groupKey}`), tabs: [] }
      groups.push(group)
    }
    group.tabs.push(tab)
  }
  return groups
})

const selectedTab = computed(() => tabs.value.find((tab) => tab.id === activeTab.value) || tabs.value[0])
const config = computed(() => configState.value.kind === 'live' ? configState.value.data : {})
const configAvailable = computed(() => configState.value.kind === 'live')
const domainDraftValid = computed(() => Boolean(domainDraft.value.domain.trim() && domainDraft.value.ownerPrincipal.trim()))
const currentInjectUnified = computed(() => Boolean(config.value?.memory?.inject_unified))
const currentSourceProject = computed(() => Boolean(config.value?.features?.enforce_source_project))
const configDraftDirty = computed(() => configAvailable.value && (
  draftInjectUnified.value !== currentInjectUnified.value ||
  draftSourceProject.value !== currentSourceProject.value
))
const switches = computed(() => [
  {
    key: 'injectUnified',
    title: t('settings.switches.injectUnified.title'),
    desc: t('settings.switches.injectUnified.desc'),
    value: draftInjectUnified.value,
    set: (value: boolean) => {
      configDraftTouched.value = true
      draftInjectUnified.value = value
    },
    evidence: 'memory.inject_unified',
    reload: true,
    disabled: !configAvailable.value,
  },
  {
    key: 'telemetry',
    title: t('settings.switches.telemetry.title'),
    desc: t('settings.switches.telemetry.desc'),
    value: Boolean(config.value?.features?.telemetry_enabled),
    set: () => {},
    evidence: 'features.telemetry_enabled',
    reload: false,
    disabled: true,
  },
  {
    key: 'sourceProject',
    title: t('settings.switches.sourceProject.title'),
    desc: t('settings.switches.sourceProject.desc'),
    value: draftSourceProject.value,
    set: (value: boolean) => {
      configDraftTouched.value = true
      draftSourceProject.value = value
    },
    evidence: 'features.enforce_source_project',
    reload: false,
    disabled: !configAvailable.value,
  },
])

function resetConfigDraft() {
  draftInjectUnified.value = currentInjectUnified.value
  draftSourceProject.value = currentSourceProject.value
  configDraftTouched.value = false
  configSaveResult.value = null
}

function buildConfigPatch() {
  const patch: {
    memory?: { inject_unified?: boolean }
    features?: { enforce_source_project?: boolean }
  } = {}
  if (draftInjectUnified.value !== currentInjectUnified.value) {
    patch.memory = { inject_unified: draftInjectUnified.value }
  }
  if (draftSourceProject.value !== currentSourceProject.value) {
    patch.features = { enforce_source_project: draftSourceProject.value }
  }
  return patch
}

function formatChanged(fields?: string[]) {
  return fields && fields.length ? fields.join(', ') : t('settings.save.noEffectiveChanges')
}

function formatLifecycleValue(value: unknown) {
  if (value === null || value === undefined) return '—'
  if (typeof value === 'boolean') return value ? t('common.yes') : t('common.no')
  return String(value)
}

function resetDomainDraft() {
  domainDraft.value = {
    domain: '',
    ownerPrincipal: '',
    ownerPrincipalKind: 'agent',
    mode: 'warn',
  }
  domainSaveResult.value = null
  editingDomain.value = null
}

function editDomain(row: OperatorMemoryDomain) {
  domainDraft.value = {
    domain: row.domain,
    ownerPrincipal: row.ownerPrincipal,
    ownerPrincipalKind: row.ownerPrincipalKind,
    mode: row.mode,
  }
  domainSaveResult.value = null
  domainDeleteResult.value = null
  domainDeleteConfirm.value = null
  editingDomain.value = row.domain
}

function formatDomainDate(value: string) {
  return value ? value.slice(0, 19).replace('T', ' ') : '—'
}

function domainDeleteTestId(domain: string) {
  return `domain-registry-delete-${domain.replace(/[^a-z0-9_-]+/gi, '-')}`
}

async function saveRuntimeConfig() {
  if (!configDraftDirty.value || configSaveInFlight.value) return
  configSaveInFlight.value = true
  try {
    configSaveResult.value = await saveConfig(buildConfigPatch())
    if (configSaveResult.value.kind === 'success' && configSaveResult.value.data.config) {
      draftInjectUnified.value = Boolean(configSaveResult.value.data.config.memory?.inject_unified)
      draftSourceProject.value = Boolean(configSaveResult.value.data.config.features?.enforce_source_project)
      configDraftTouched.value = false
    }
  } finally {
    configSaveInFlight.value = false
  }
}

async function saveDomainDraft() {
  if (!domainDraftValid.value || domainSaveInFlight.value) return
  domainSaveInFlight.value = true
  domainDeleteResult.value = null
  try {
    domainSaveResult.value = await upsertDomain(domainDraft.value)
    if (domainSaveResult.value.kind === 'success') {
      domainDraft.value = {
        domain: domainSaveResult.value.data.domain,
        ownerPrincipal: domainSaveResult.value.data.ownerPrincipal,
        ownerPrincipalKind: domainSaveResult.value.data.ownerPrincipalKind,
        mode: domainSaveResult.value.data.mode,
      }
    }
  } finally {
    domainSaveInFlight.value = false
  }
}

async function confirmDeleteDomain(domain: string) {
  if (domainDeleteInFlight.value) return
  if (domainDeleteConfirm.value !== domain) {
    domainDeleteConfirm.value = domain
    return
  }
  domainDeleteInFlight.value = domain
  domainSaveResult.value = null
  try {
    domainDeleteResult.value = await deleteDomain(domain)
    if (domainDeleteResult.value.kind === 'success' && domainDraft.value.domain === domain) {
      resetDomainDraft()
    }
  } finally {
    domainDeleteInFlight.value = null
    domainDeleteConfirm.value = null
  }
}

function selectTab(id: string) {
  activeTab.value = id
  restartConfirm.value = false
  updateRestartConfirm.value = false
  domainDeleteConfirm.value = null
}

function closeModal() {
  open.value = false
}

function cycleLocaleTo(code: string) {
  setLocale(code)
}

function setTheme(value: 'dark' | 'light') {
  colorMode.preference = value
}

async function confirmRestartServer() {
  if (restartInFlight.value) return
  if (!restartConfirm.value) {
    restartConfirm.value = true
    return
  }
  restartInFlight.value = true
  try {
    await restartServer()
  } finally {
    restartInFlight.value = false
    restartConfirm.value = false
  }
}

async function confirmUpdateRestart() {
  if (updateRestartInFlight.value) return
  if (!updateRestartConfirm.value) {
    updateRestartConfirm.value = true
    return
  }
  updateRestartInFlight.value = true
  try {
    await restartAfterUpdate()
  } finally {
    updateRestartInFlight.value = false
    updateRestartConfirm.value = false
  }
}

function onKeydown(event: KeyboardEvent) {
  if (event.key === 'Escape' && open.value) closeModal()
}

watch(open, (isOpen) => {
  if (!import.meta.client) return
  if (isOpen) {
    if (previousBodyOverflow.value === null) {
      previousBodyOverflow.value = document.body.style.overflow
    }
    document.body.style.overflow = 'hidden'
    window.addEventListener('keydown', onKeydown)
  } else {
    if (previousBodyOverflow.value !== null) {
      document.body.style.overflow = previousBodyOverflow.value
      previousBodyOverflow.value = null
    }
    window.removeEventListener('keydown', onKeydown)
  }
}, { immediate: true })

watch(configState, () => {
  if (!configDraftTouched.value) resetConfigDraft()
}, { immediate: true })

onBeforeUnmount(() => {
  if (!import.meta.client) return
  window.removeEventListener('keydown', onKeydown)
  if (previousBodyOverflow.value !== null) {
    document.body.style.overflow = previousBodyOverflow.value
    previousBodyOverflow.value = null
  }
})
</script>

<template>
  <Teleport to="body">
    <div v-if="open" class="settings-overlay" @click.self="closeModal">
      <section class="modal settings-modal" role="dialog" aria-modal="true" :aria-label="t('settings.modal.aria')">
        <div class="settings-shell">
          <aside class="settings-rail">
            <div class="settings-kicker">{{ t('settings.modal.kicker') }}</div>
            <div v-for="group in groupedTabs" :key="group.key" class="settings-group">
              <div class="sr-label">{{ group.label }}</div>
              <button
                v-for="tab in group.tabs"
                :key="tab.id"
                class="settings-tab"
                type="button"
                :aria-selected="tab.id === selectedTab.id"
                @click="selectTab(tab.id)"
              >
                <span class="tab-mark" :data-cls="tab.cls" />
                <span class="tab-label">{{ t(`settings.modal.tabs.${tab.labelKey}.label`) }}</span>
                <HonestyBadge v-if="tab.cls === 'mustbuild'" cls="mustbuild" :evidence="tab.evidence" :label="t('honesty.mustbuild')" />
                <HonestyBadge v-else-if="tab.cls === 'stale'" cls="stale" :label="t('honesty.stale')" />
              </button>
            </div>
          </aside>

          <section class="settings-stage">
            <div class="settings-head">
              <div>
                <h3>{{ t(`settings.modal.tabs.${selectedTab.titleKey}.title`) }}</h3>
                <p>{{ t(`settings.modal.tabs.${selectedTab.descKey}.desc`) }}</p>
              </div>
              <button class="tbtn close" type="button" :aria-label="t('settings.modal.close')" @click="closeModal">×</button>
            </div>

            <div class="settings-body">
              <template v-if="selectedTab.kind === 'general'">
                <section class="settings-section">
                  <div class="settings-section-title">{{ t('settings.modal.sections.appearance') }}</div>
                  <div class="settings-row">
                    <div class="setting-copy">
                      <b>{{ t('settings.modal.general.theme.title') }}</b>
                      <p>{{ t('settings.modal.general.theme.desc') }}</p>
                    </div>
                    <div class="setting-control">
                      <div class="seg">
                        <button type="button" :aria-pressed="colorMode.value === 'dark'" @click="setTheme('dark')">{{ t('settings.modal.general.theme.dark') }}</button>
                        <button type="button" :aria-pressed="colorMode.value === 'light'" @click="setTheme('light')">{{ t('settings.modal.general.theme.light') }}</button>
                      </div>
                    </div>
                  </div>
                  <div class="settings-row">
                    <div class="setting-copy">
                      <b>{{ t('settings.modal.general.density.title') }}</b>
                      <p>{{ t('settings.modal.general.density.desc') }}</p>
                    </div>
                    <div class="setting-control">
                      <div class="seg">
                        <button type="button" :aria-pressed="density === 'comfortable'" @click="density = 'comfortable'">{{ t('shell.densityComfortable') }}</button>
                        <button type="button" :aria-pressed="density === 'compact'" @click="density = 'compact'">{{ t('shell.densityCompact') }}</button>
                      </div>
                    </div>
                  </div>
                  <div class="settings-row">
                    <div class="setting-copy">
                      <b>{{ t('settings.modal.general.language.title') }}</b>
                      <p>{{ t('settings.modal.general.language.desc') }}</p>
                    </div>
                    <div class="setting-control">
                      <div class="seg">
                        <button
                          v-for="item in locales"
                          :key="item.code"
                          type="button"
                          :aria-pressed="locale === item.code"
                          @click="cycleLocaleTo(String(item.code))"
                        >
                          {{ item.name }}
                        </button>
                      </div>
                    </div>
                  </div>
                </section>

                <section class="settings-section">
                  <div class="settings-section-title">{{ t('settings.modal.sections.workspace') }}</div>
                  <div class="settings-row">
                    <div class="setting-copy">
                      <b>{{ t('settings.modal.general.server.title') }}</b>
                      <p>{{ t('settings.modal.general.server.desc') }}</p>
                    </div>
                    <div class="setting-control">
                      <span class="tag">{{ info.host }}</span>
                      <span class="tag">{{ info.version }}</span>
                    </div>
                  </div>
                  <div class="settings-row">
                    <div class="setting-copy">
                      <b>{{ t('settings.modal.general.overlay.title') }}</b>
                      <p>{{ t('settings.modal.general.overlay.desc') }}</p>
                    </div>
                    <div class="setting-control">
                      <span class="bdg live">{{ t('settings.modal.general.overlay.badge') }}</span>
                    </div>
                  </div>
                </section>
              </template>

              <template v-else-if="selectedTab.kind === 'runtime'">
                <div v-if="pending" class="state pending">{{ t('settings.state.pending') }}</div>
                <div v-if="error" class="state error">{{ t('settings.state.error', { message: error }) }}</div>
                <div v-if="restartRequired" class="state restart">{{ t('settings.state.restartRequired') }}</div>
                <div v-if="configPendingRestart.length" class="pending-restart-list">
                  <div v-for="item in configPendingRestart" :key="item.field" class="pending-restart-row">
                    <code>{{ item.field }}</code>
                    <span>{{ t('settings.lifecycle.effective') }}: <b>{{ formatLifecycleValue(item.effective) }}</b></span>
                    <span>{{ t('settings.lifecycle.desired') }}: <b>{{ formatLifecycleValue(item.desired) }}</b></span>
                  </div>
                </div>

                <section class="settings-section">
                  <div class="settings-section-title">{{ t('settings.runtime') }}</div>
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
                      :disabled="item.disabled"
                      @update:model-value="item.set"
                    />
                  </div>
                  <div class="settings-actions config-actions">
                    <div class="left">
                      <button class="tbtn primary" type="button" :disabled="!configDraftDirty || configSaveInFlight" @click="saveRuntimeConfig">
                        {{ configSaveInFlight ? t('settings.save.pending') : t('settings.save.action') }}
                      </button>
                      <button class="tbtn" type="button" :disabled="!configDraftDirty || configSaveInFlight" @click="resetConfigDraft">
                        {{ t('settings.save.reset') }}
                      </button>
                    </div>
                    <HonestyBadge cls="live" :evidence="configSaveEvidence.endpoint" />
                  </div>
                  <div v-if="configSaveResult?.kind === 'success'" class="state" :class="configSaveResult.data.restart_required ? 'restart' : 'ok'">
                    {{ t('settings.save.success', {
                      changed: formatChanged(configSaveResult.data.changed),
                      restart: configSaveResult.data.restart_required ? t('common.yes') : t('common.no'),
                    }) }}
                    <span v-if="configSaveResult.data.restart_required_fields?.length">
                      {{ t('settings.save.restartFields', { fields: configSaveResult.data.restart_required_fields.join(', ') }) }}
                    </span>
                  </div>
                  <div v-else-if="configSaveResult?.kind === 'rollback'" class="state error">
                    {{ t('settings.save.error', { message: configSaveResult.error.message }) }}
                  </div>
                </section>

                <section class="settings-section">
                  <div class="settings-section-title">{{ t('settings.flags.title') }}</div>
                  <p class="plain-help">{{ t('settings.flags.body') }}</p>
                  <div v-if="flagsState.kind === 'pending'" class="state pending">{{ t('settings.flags.pending') }}</div>
                  <div v-else-if="flagsState.kind === 'error'" class="state error">{{ t('settings.flags.error', { message: flagsState.error.message }) }}</div>
                  <div v-else class="flag-list">
                    <div v-for="item in flagItems" :key="item.name" class="flag-row">
                      <div class="flag-copy">
                        <code>{{ item.name }}</code>
                        <span>{{ item.category }} · {{ item.source }}</span>
                      </div>
                      <div class="flag-status">
                        <span class="bdg" :class="item.enabled ? 'live' : 'off'">
                          {{ item.enabled ? t('settings.flags.enabled') : t('settings.flags.disabled') }}
                        </span>
                        <span v-if="item.restart_required_to_change" class="tag">{{ t('settings.flags.restartRequired') }}</span>
                      </div>
                    </div>
                  </div>
                </section>

                <section class="settings-section">
                  <div class="settings-section-title">{{ t('settings.configSnapshot') }}</div>
                  <div class="metrics">
                    <div v-for="metric in configMetrics" :key="metric.label">
                      <span>{{ metric.label }}</span>
                      <strong>{{ metric.value }}</strong>
                    </div>
                  </div>
                </section>
              </template>

              <template v-else-if="selectedTab.kind === 'actions'">
                <section class="settings-section">
                  <div class="settings-section-title">{{ t('settings.restart.title') }}</div>
                  <p class="plain-help">{{ t('settings.restart.body') }}</p>
                  <div class="settings-actions">
                    <div class="left">
                      <button class="danger" type="button" :disabled="restartInFlight" @click="confirmRestartServer">
                        {{ restartConfirm ? t('settings.restart.confirm') : t('settings.restart.action') }}
                      </button>
                      <button class="tbtn" type="button" :disabled="updateRestartInFlight || (updateStatusState.kind === 'live' && updateStatusState.data?.state !== 'done')" @click="confirmUpdateRestart">
                        {{ updateRestartConfirm ? t('settings.restart.confirmUpdate') : t('settings.restart.updateAction') }}
                      </button>
                    </div>
                    <button class="tbtn" type="button" :disabled="pending" @click="refresh">{{ t('settings.refresh') }}</button>
                  </div>
                  <p class="muted">
                    {{ t('settings.restart.updateState') }}:
                    <code>{{ updateStatusState.kind === 'live' ? updateStatusState.data?.state || t('health.idle') : '—' }}</code>
                    · {{ t('settings.restart.updateAvailable') }}:
                    <code>{{ updateCheckState.kind === 'live' ? (updateCheckState.data?.available ? t('common.yes') : t('common.no')) : '—' }}</code>
                  </p>
                </section>
              </template>

              <template v-else-if="selectedTab.kind === 'domains'">
                <section class="settings-section">
                  <div class="settings-section-title">{{ t('settings.domains.title') }}</div>
                  <p class="plain-help">{{ t('settings.domains.body') }}</p>
                  <div class="settings-actions domain-toolbar">
                    <div class="left">
                      <span class="tag">{{ t('settings.domains.count', domainCount) }}</span>
                      <HonestyBadge cls="live" :evidence="domainListEvidence.endpoint" />
                      <code class="endpoint-pill">GET {{ domainListEvidence.endpoint }}</code>
                    </div>
                    <button class="tbtn" type="button" :disabled="domainsPending" @click="refreshDomains">{{ t('settings.refresh') }}</button>
                  </div>
                  <div v-if="domainsPending" class="state pending">{{ t('settings.domains.pending') }}</div>
                  <div v-if="domainsError" class="state error">{{ t('settings.domains.error', { message: domainsError }) }}</div>
                  <div v-if="domainSaveResult?.kind === 'success'" class="state ok">
                    {{ t('settings.domains.notice.saved', { domain: domainSaveResult.data.domain }) }}
                  </div>
                  <div v-else-if="domainSaveResult?.kind === 'rollback'" class="state error">
                    {{ t('settings.domains.notice.error', { message: domainSaveResult.error.message }) }}
                  </div>
                  <div v-if="domainDeleteResult?.kind === 'success'" class="state ok">
                    {{ t('settings.domains.notice.deleted', { domain: domainDeleteResult.data.domain }) }}
                  </div>
                  <div v-else-if="domainDeleteResult?.kind === 'rollback'" class="state error">
                    {{ t('settings.domains.notice.error', { message: domainDeleteResult.error.message }) }}
                  </div>
                </section>

                <section class="settings-section domain-form">
                  <div class="settings-section-title">{{ t('settings.domains.form.title') }}</div>
                  <div class="domain-grid">
                    <label>
                      <span>{{ t('settings.domains.form.domain') }}</span>
                      <input
                        v-model.trim="domainDraft.domain"
                        data-testid="domain-registry-domain"
                        :disabled="Boolean(editingDomain)"
                        :placeholder="t('settings.domains.form.domainPlaceholder')"
                      />
                    </label>
                    <label>
                      <span>{{ t('settings.domains.form.owner') }}</span>
                      <input v-model.trim="domainDraft.ownerPrincipal" data-testid="domain-registry-owner" :placeholder="t('settings.domains.form.ownerPlaceholder')" />
                    </label>
                    <label>
                      <span>{{ t('settings.domains.form.kind') }}</span>
                      <select v-model="domainDraft.ownerPrincipalKind" data-testid="domain-registry-kind">
                        <option v-for="kind in DOMAIN_OWNER_KINDS" :key="kind" :value="kind">{{ t(`settings.domains.kind.${kind}`) }}</option>
                      </select>
                    </label>
                    <label>
                      <span>{{ t('settings.domains.form.mode') }}</span>
                      <select v-model="domainDraft.mode" data-testid="domain-registry-mode">
                        <option v-for="mode in DOMAIN_OWNER_MODES" :key="mode" :value="mode">{{ t(`settings.domains.mode.${mode}`) }}</option>
                      </select>
                    </label>
                  </div>
                  <div class="settings-actions">
                    <div class="left">
                      <button
                        class="tbtn primary"
                        type="button"
                        data-testid="domain-registry-save"
                        :disabled="!domainDraftValid || domainSaveInFlight"
                        @click="saveDomainDraft"
                      >
                        {{ domainSaveInFlight ? t('settings.domains.form.saving') : t('settings.domains.form.save') }}
                      </button>
                      <button class="tbtn" type="button" :disabled="domainSaveInFlight" @click="resetDomainDraft">
                        {{ t('settings.domains.form.reset') }}
                      </button>
                    </div>
                    <span class="muted">{{ t('settings.domains.form.evidence') }} <code>PUT /api/memory-domains/{domain}</code></span>
                  </div>
                </section>

                <section class="settings-section">
                  <div class="settings-section-title">{{ t('settings.domains.rows.title') }}</div>
                  <div v-if="domainState.kind === 'empty'" class="operator-note">
                    <span class="mark">i</span>
                    <div>
                      <b>{{ t('settings.domains.empty.title') }}</b>
                      <p>{{ t('settings.domains.empty.body') }}</p>
                    </div>
                  </div>
                  <div v-else class="domain-list">
                    <div v-for="row in domains" :key="row.domain" class="domain-row">
                      <div class="domain-main">
                        <code>{{ row.domain }}</code>
                        <span>{{ row.ownerPrincipal }} · {{ t(`settings.domains.kind.${row.ownerPrincipalKind}`) }}</span>
                      </div>
                      <div class="domain-meta">
                        <span class="bdg" :class="row.mode === 'reject' ? 'danger-soft' : row.mode === 'warn' ? 'warn-soft' : 'off'">
                          {{ t(`settings.domains.mode.${row.mode}`) }}
                        </span>
                        <span class="tag">{{ formatDomainDate(row.updatedAt) }}</span>
                      </div>
                      <div class="domain-actions">
                        <button class="tbtn" type="button" :aria-label="t('settings.domains.aria.edit', { domain: row.domain })" @click="editDomain(row)">
                          {{ t('common.change') }}
                        </button>
                        <button
                          class="danger"
                          type="button"
                          :data-testid="domainDeleteTestId(row.domain)"
                          :disabled="Boolean(domainDeleteInFlight)"
                          :aria-label="t('settings.domains.aria.delete', { domain: row.domain })"
                          @click="confirmDeleteDomain(row.domain)"
                        >
                          {{ domainDeleteConfirm === row.domain ? t('settings.domains.rows.confirmDelete') : t('settings.domains.rows.delete') }}
                        </button>
                      </div>
                    </div>
                  </div>
                  <p class="plain-help">{{ t('settings.domains.rows.deleteHelp') }}</p>
                </section>
              </template>

              <template v-else-if="selectedTab.kind === 'client'">
                <section class="settings-section">
                  <div class="settings-section-title">{{ t('settings.modal.sections.client') }}</div>
                  <div class="settings-note-grid">
                    <div class="surface-card">
                      <b>{{ t('settings.modal.client.theme.title') }}</b>
                      <p>{{ t('settings.modal.client.theme.desc') }}</p>
                      <div class="route">{{ colorMode.value }}</div>
                    </div>
                    <div class="surface-card">
                      <b>{{ t('settings.modal.client.density.title') }}</b>
                      <p>{{ t('settings.modal.client.density.desc') }}</p>
                      <div class="route">{{ density }}</div>
                    </div>
                    <div class="surface-card">
                      <b>{{ t('settings.modal.client.locale.title') }}</b>
                      <p>{{ t('settings.modal.client.locale.desc') }}</p>
                      <div class="route">{{ locale }}</div>
                    </div>
                  </div>
                </section>
              </template>

              <template v-else-if="selectedTab.kind === 'dead'">
                <section class="settings-section">
                  <div class="settings-section-title">{{ t('settings.modal.sections.tombstones') }}</div>
                  <div class="operator-note">
                    <span class="mark">!</span>
                    <div>
                      <b>{{ t('settings.modal.dead.title') }}</b>
                      <p>{{ t('settings.modal.dead.body') }}</p>
                      <code>v5 demolition guard</code>
                    </div>
                  </div>
                </section>
              </template>

              <template v-else>
                <section class="settings-section">
                  <div class="settings-section-title">{{ t('settings.modal.sections.honesty') }}</div>
                  <div class="operator-note">
                    <span class="mark">i</span>
                    <div>
                      <b>{{ t('settings.modal.mustbuild.title') }}</b>
                      <p>{{ t('settings.modal.mustbuild.body') }}</p>
                      <HonestyBadge cls="mustbuild" :evidence="selectedTab.evidence" />
                    </div>
                  </div>
                </section>
              </template>
            </div>
          </section>
        </div>
      </section>
    </div>
  </Teleport>
</template>

<style scoped>
.settings-overlay {
  position: fixed;
  inset: 0;
  z-index: 120;
  display: grid;
  place-items: center;
  padding: 24px;
  background: color-mix(in oklab, #000, transparent 34%);
}
.modal.settings-modal {
  width: min(1040px, calc(100vw - 48px));
  max-height: min(760px, 88vh);
  overflow: hidden;
  border: 1px solid var(--border);
  border-radius: var(--r-lg);
  background: var(--surface);
  box-shadow: var(--elev-raised);
  color: var(--fg);
}
.settings-shell { display: grid; grid-template-columns: 232px minmax(0, 1fr); height: min(760px, 88vh); min-height: 620px; }
.settings-rail { border-right: 1px solid var(--border); background: color-mix(in oklab, var(--surface), var(--bg) 28%); padding: 26px 8px 18px; overflow-y: auto; overflow-x: hidden; }
.settings-kicker { color: var(--muted); font-size: 12px; text-transform: uppercase; letter-spacing: .08em; font-weight: 700; padding: 0 12px 8px; }
.settings-group { display: grid; gap: 4px; }
.sr-label { margin: 18px 12px 5px; color: var(--muted); font-size: 10px; text-transform: uppercase; letter-spacing: .07em; font-weight: 800; }
.settings-tab { width: 100%; min-height: 36px; border: 0; background: transparent; color: var(--fg-2); text-align: left; border-radius: var(--r-sm); padding: 8px 10px; font-weight: 650; display: grid; grid-template-columns: 10px minmax(0, 1fr); align-items: center; gap: 6px 8px; cursor: pointer; }
.settings-tab:hover { background: color-mix(in oklab, var(--surface), var(--fg) 4%); color: var(--fg); }
.settings-tab[aria-selected="true"] { background: color-mix(in oklab, var(--accent), transparent 88%); color: var(--fg); }
.settings-tab .tab-label { min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.settings-tab :deep(.hb) { grid-column: 2; justify-self: start; max-width: 100%; min-width: 0; }
.settings-tab :deep(.hb-lbl) { white-space: nowrap; }
.settings-tab :deep(.hb-ev) { max-width: 116px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.tab-mark { width: 7px; height: 7px; border-radius: 50%; background: var(--class-live); }
.tab-mark[data-cls="mustbuild"] { background: var(--class-mustbuild); }
.tab-mark[data-cls="stale"] { background: transparent; border: 1.5px solid var(--class-stale); }
.settings-stage { min-width: 0; display: flex; flex-direction: column; max-height: min(760px, 88vh); background: var(--surface); }
.settings-head { min-height: 70px; padding: 18px 24px 14px; border-bottom: 1px solid var(--border); display: flex; align-items: flex-start; justify-content: space-between; gap: var(--space-4); }
.settings-head h3 { margin: 0 0 4px; font-size: var(--text-lg); letter-spacing: 0; }
.settings-head p { margin: 0; color: var(--muted); font-size: var(--text-sm); }
.settings-body { padding: 18px 24px 28px; overflow: auto; display: flex; flex-direction: column; gap: 22px; }
.settings-section { display: flex; flex-direction: column; gap: 0; }
.settings-section-title { color: var(--muted); font-size: 12px; text-transform: uppercase; letter-spacing: .07em; font-weight: 800; margin: 0 0 10px; }
.settings-row { display: grid; grid-template-columns: minmax(220px, 1fr) minmax(260px, 360px); gap: 20px; align-items: start; padding: 14px 0; border-top: 1px solid var(--border-soft); }
.settings-row:first-of-type { border-top: 0; }
.setting-copy b { display: block; color: var(--fg); font-size: var(--text-sm); margin-bottom: 3px; }
.setting-copy p { margin: 0; color: var(--muted); font-size: var(--text-sm); line-height: 1.38; }
.setting-control { display: flex; align-items: center; justify-content: flex-end; gap: 8px; flex-wrap: wrap; min-width: 0; }
.seg { display: inline-flex; background: var(--surface-warm); border: 1px solid var(--border); border-radius: var(--r-sm); overflow: hidden; }
.seg button { border: 0; background: transparent; padding: 6px 10px; font-size: var(--text-xs); font-weight: 700; color: var(--muted); cursor: pointer; }
.seg button[aria-pressed="true"] { background: var(--surface); color: var(--fg); }
.tbtn, .danger { border: 1px solid var(--border); border-radius: var(--r-sm); background: var(--surface); color: var(--fg); padding: 8px 12px; font: inherit; font-size: var(--text-xs); font-weight: 700; cursor: pointer; }
.tbtn.primary { border-color: color-mix(in oklab, var(--accent), transparent 52%); background: color-mix(in oklab, var(--accent), transparent 88%); color: var(--fg); }
.tbtn:disabled { opacity: .55; cursor: not-allowed; }
.tbtn.close { width: 32px; height: 32px; padding: 0; font-size: 18px; line-height: 1; }
.danger { border-color: color-mix(in oklab, var(--state-danger), transparent 55%); color: var(--state-danger); }
.bdg { display: inline-flex; align-items: center; border-radius: var(--radius-pill); border: 1px solid var(--border); padding: 2px 8px; font-size: 10px; font-weight: 800; text-transform: uppercase; letter-spacing: .04em; }
.bdg.live { color: var(--class-live); background: color-mix(in oklab, var(--class-live), transparent 90%); border-color: color-mix(in oklab, var(--class-live), transparent 65%); }
.bdg.off { color: var(--muted); background: color-mix(in oklab, var(--surface), var(--fg) 3%); border-color: var(--border); }
.bdg.warn-soft { color: var(--state-warn); background: color-mix(in oklab, var(--state-warn), transparent 88%); border-color: color-mix(in oklab, var(--state-warn), transparent 55%); }
.bdg.danger-soft { color: var(--state-danger); background: color-mix(in oklab, var(--state-danger), transparent 90%); border-color: color-mix(in oklab, var(--state-danger), transparent 55%); }
.tag { display: inline-flex; align-items: center; padding: 2px 7px; border: 1px solid var(--border); border-radius: var(--radius-pill); color: var(--fg-2); font-size: 10px; font-family: var(--font-mono); }
.endpoint-pill { display: inline-flex; align-items: center; max-width: 100%; padding: 2px 7px; border: 1px solid var(--border-soft); border-radius: var(--radius-pill); color: var(--muted); background: color-mix(in oklab, var(--surface), var(--fg) 2%); font-family: var(--font-mono); font-size: 10px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.state { border: 1px solid var(--border); border-radius: var(--r-md); background: var(--surface); padding: 10px 12px; color: var(--fg-2); font-size: var(--text-sm); }
.state.pending { border-color: color-mix(in oklab, var(--accent), transparent 55%); }
.state.ok { color: var(--class-live); border-color: color-mix(in oklab, var(--class-live), transparent 45%); }
.state.restart { color: var(--state-warn); border-color: color-mix(in oklab, var(--state-warn), transparent 45%); }
.state.error { color: var(--state-danger); border-color: color-mix(in oklab, var(--state-danger), transparent 45%); }
.pending-restart-list { display: grid; gap: 8px; }
.pending-restart-row { display: grid; grid-template-columns: minmax(180px, 1fr) auto auto; gap: 10px; align-items: center; border: 1px solid color-mix(in oklab, var(--state-warn), transparent 62%); border-radius: var(--r-sm); background: color-mix(in oklab, var(--state-warn), transparent 92%); padding: 9px 10px; color: var(--fg-2); font-size: var(--text-xs); }
.pending-restart-row code { min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; font-family: var(--font-mono); color: var(--fg); }
.pending-restart-row b { color: var(--fg); font-family: var(--font-mono); }
.switches { padding: 0 4px; }
.config-actions { margin-top: 14px; }
.gaps { display: grid; gap: 10px; margin-top: 14px; }
.gap { display: flex; align-items: center; justify-content: space-between; gap: 12px; border: 1px dashed var(--border); border-radius: var(--r-sm); padding: 10px 12px; color: var(--muted); font-size: var(--text-xs); }
.metrics { display: grid; grid-template-columns: repeat(auto-fit, minmax(180px, 1fr)); gap: 10px; }
.metrics div { border: 1px solid var(--border-soft); border-radius: var(--r-sm); padding: 10px; }
.metrics span { display: block; color: var(--muted); font-size: var(--text-xs); }
.metrics strong { display: block; margin-top: 6px; font-family: var(--font-mono); color: var(--fg); }
.flag-list { display: grid; gap: 8px; }
.flag-row { display: grid; grid-template-columns: minmax(0, 1fr) auto; gap: 12px; align-items: center; border: 1px solid var(--border-soft); border-radius: var(--r-sm); padding: 9px 10px; background: color-mix(in oklab, var(--surface), var(--fg) 1.5%); }
.flag-copy { display: grid; gap: 3px; min-width: 0; }
.flag-copy code { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; font-family: var(--font-mono); color: var(--fg); font-size: var(--text-xs); }
.flag-copy span { color: var(--muted); font-size: 10px; text-transform: uppercase; letter-spacing: .04em; }
.flag-status { display: flex; justify-content: flex-end; align-items: center; gap: 6px; flex-wrap: wrap; }
.settings-actions { display: flex; flex-wrap: wrap; gap: var(--space-2); align-items: center; justify-content: space-between; }
.settings-actions .left { display: flex; flex-wrap: wrap; gap: var(--space-2); align-items: center; }
.domain-toolbar { margin-top: 12px; }
.domain-form { border: 1px solid var(--border-soft); border-radius: var(--r-md); padding: 14px; background: color-mix(in oklab, var(--surface), var(--fg) 1.5%); }
.domain-grid { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 12px; margin-bottom: 14px; }
.domain-grid label { display: grid; gap: 5px; color: var(--muted); font-size: var(--text-xs); font-weight: 700; }
.domain-grid input, .domain-grid select { width: 100%; min-width: 0; border: 1px solid var(--border); border-radius: var(--r-sm); background: var(--bg); color: var(--fg); padding: 8px 10px; font: inherit; font-size: var(--text-sm); }
.domain-list { display: grid; gap: 8px; }
.domain-row { display: grid; grid-template-columns: minmax(0, 1fr) auto auto; gap: 12px; align-items: center; border: 1px solid var(--border-soft); border-radius: var(--r-md); padding: 10px 12px; background: color-mix(in oklab, var(--surface), var(--fg) 1.5%); }
.domain-main { display: grid; gap: 3px; min-width: 0; }
.domain-main code { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; color: var(--fg); font-family: var(--font-mono); }
.domain-main span { color: var(--muted); font-size: var(--text-xs); }
.domain-meta, .domain-actions { display: flex; align-items: center; justify-content: flex-end; gap: 8px; flex-wrap: wrap; }
.plain-help, .muted { color: var(--muted); font-size: var(--text-sm); line-height: 1.45; }
.settings-note-grid { display: grid; grid-template-columns: repeat(auto-fit, minmax(220px, 1fr)); gap: var(--space-3); }
.surface-card { background: var(--surface); border: 1px solid var(--border); border-radius: var(--r-md); padding: var(--space-4); }
.surface-card b { display: block; color: var(--fg); font-size: var(--text-sm); margin-bottom: 4px; }
.surface-card p { margin: 0; color: var(--muted); font-size: var(--text-sm); line-height: 1.42; }
.surface-card .route { font-family: var(--font-mono); font-size: var(--text-xs); color: var(--fg-2); margin-top: 8px; }
.operator-note { display: flex; gap: 10px; align-items: flex-start; padding: 11px 12px; border: 1px solid var(--border); border-radius: var(--r-md); background: var(--surface-warm); color: var(--fg-2); font-size: var(--text-sm); line-height: 1.42; }
.operator-note b { color: var(--fg); }
.operator-note p { margin: 4px 0 8px; color: var(--fg-2); }
.operator-note .mark { width: 20px; height: 20px; border-radius: 50%; display: grid; place-items: center; flex: 0 0 auto; background: var(--accent); color: var(--accent-on); font-size: 12px; font-weight: 800; }
.operator-note code { font-family: var(--font-mono); color: var(--muted); font-size: var(--text-xs); }
@media (max-width: 1100px) {
  .settings-shell { grid-template-columns: 1fr; min-height: min(760px, 88vh); }
  .settings-rail { border-right: 0; border-bottom: 1px solid var(--border); max-height: 220px; }
  .settings-row { grid-template-columns: 1fr; }
  .setting-control { justify-content: flex-start; }
  .pending-restart-row { grid-template-columns: 1fr; }
  .domain-grid { grid-template-columns: 1fr; }
  .domain-row { grid-template-columns: 1fr; align-items: stretch; }
  .domain-meta, .domain-actions { justify-content: flex-start; }
}
@media (max-width: 720px) {
  .settings-overlay { padding: 12px; place-items: stretch; }
  .modal.settings-modal { width: 100%; max-height: calc(100vh - 24px); }
}
</style>
