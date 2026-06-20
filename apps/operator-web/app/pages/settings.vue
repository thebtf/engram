<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { useOperatorAuth } from '~/composables/useOperatorAuth'
import { useOperatorPreferences } from '~/composables/useOperatorPreferences'
import { useConfigQuery } from '~/composables/useOperatorQueries'

const { state } = useOperatorAuth()
const { density, setDensity } = useOperatorPreferences()
const configQuery = useConfigQuery()
const { locale } = useI18n()

const localeLabel = computed(() => locale.value === 'ru' ? 'Русский' : 'English')
</script>

<template>
  <section class="space-y-5">
    <div class="surface-panel p-5">
      <div class="mb-4">
        <h1 class="text-xl font-semibold">Settings</h1>
        <p class="mt-2 text-sm text-[var(--fg-2)]">
          Truthful operator preferences plus read-only config/auth state. No fake editable server settings here.
        </p>
      </div>

      <div class="grid gap-4 xl:grid-cols-[minmax(0,1fr),minmax(0,1fr)]">
        <div class="space-y-4">
          <div class="surface-panel-warm p-4">
            <div class="mb-3 text-xs uppercase tracking-[0.08em] text-[var(--muted)]">General</div>

            <div class="space-y-4">
              <div>
                <div class="mb-1 text-sm font-semibold">Theme</div>
                <div class="text-sm text-[var(--fg-2)]">Dark-first, fixed in the current migration slice.</div>
              </div>

              <div>
                <div class="mb-2 text-sm font-semibold">Density</div>
                <div class="flex gap-2">
                  <button
                    class="rounded-lg border px-3 py-2 text-sm"
                    :class="density === 'comfortable'
                      ? 'border-[var(--accent)] bg-[color:rgba(76,141,255,0.12)] text-[var(--fg)]'
                      : 'border-[var(--border)] bg-[var(--surface)] text-[var(--fg-2)]'"
                    @click="setDensity('comfortable')"
                  >
                    Comfortable
                  </button>
                  <button
                    class="rounded-lg border px-3 py-2 text-sm"
                    :class="density === 'compact'
                      ? 'border-[var(--accent)] bg-[color:rgba(76,141,255,0.12)] text-[var(--fg)]'
                      : 'border-[var(--border)] bg-[var(--surface)] text-[var(--fg-2)]'"
                    @click="setDensity('compact')"
                  >
                    Compact
                  </button>
                </div>
              </div>

              <div>
                <div class="mb-2 text-sm font-semibold">Locale</div>
                <div class="flex gap-2">
                  <button
                    class="rounded-lg border px-3 py-2 text-sm"
                    :class="locale === 'ru'
                      ? 'border-[var(--accent)] bg-[color:rgba(76,141,255,0.12)] text-[var(--fg)]'
                      : 'border-[var(--border)] bg-[var(--surface)] text-[var(--fg-2)]'"
                    @click="locale = 'ru'"
                  >
                    Русский
                  </button>
                  <button
                    class="rounded-lg border px-3 py-2 text-sm"
                    :class="locale === 'en'
                      ? 'border-[var(--accent)] bg-[color:rgba(76,141,255,0.12)] text-[var(--fg)]'
                      : 'border-[var(--border)] bg-[var(--surface)] text-[var(--fg-2)]'"
                    @click="locale = 'en'"
                  >
                    English
                  </button>
                </div>
                <div class="mt-2 text-xs text-[var(--muted)]">
                  Current: {{ localeLabel }}
                </div>
              </div>
            </div>
          </div>

          <div class="surface-panel-warm p-4">
            <div class="mb-3 text-xs uppercase tracking-[0.08em] text-[var(--muted)]">Access State</div>
            <div class="space-y-2 text-sm text-[var(--fg-2)]">
              <div class="flex items-center justify-between gap-3">
                <span>Auth state</span>
                <span class="mono-data">{{ state.phase }}</span>
              </div>
              <div class="flex items-center justify-between gap-3">
                <span>Auth disabled</span>
                <span class="mono-data">{{ state.authDisabled ? 'yes' : 'no' }}</span>
              </div>
              <div class="flex items-center justify-between gap-3">
                <span>User</span>
                <span class="mono-data">{{ state.user?.email || '—' }}</span>
              </div>
              <div class="flex items-center justify-between gap-3">
                <span>Role</span>
                <span class="mono-data">{{ state.user?.role || '—' }}</span>
              </div>
            </div>
          </div>
        </div>

        <div class="surface-panel-warm p-4">
          <div class="mb-3 text-xs uppercase tracking-[0.08em] text-[var(--muted)]">Server Config</div>
          <div class="mb-3 text-xs text-[var(--muted)]">
            Read-only config slice from the current Go API. No fake save/apply flow.
          </div>

          <div v-if="configQuery.isPending.value" class="text-sm text-[var(--muted)]">Загрузка config...</div>
          <div v-else-if="configQuery.isError.value" class="text-sm text-[var(--danger)]">
            {{ configQuery.error.value?.message || 'Не удалось загрузить config' }}
          </div>
          <div v-else-if="configQuery.data.value" class="space-y-3">
            <div
              v-for="(section, key) in configQuery.data.value"
              :key="key"
              class="rounded-xl border border-[var(--border)] bg-[var(--surface)] p-4"
            >
              <div class="mb-3 text-xs font-semibold uppercase tracking-[0.08em] text-[var(--muted)]">
                {{ key }}
              </div>
              <div class="space-y-2 text-xs">
                <div
                  v-for="(value, field) in section"
                  :key="field"
                  class="flex items-start justify-between gap-4"
                >
                  <span class="text-[var(--muted)]">{{ field }}</span>
                  <span class="mono-data break-all text-right text-[var(--fg-2)]">{{ value }}</span>
                </div>
              </div>
            </div>
          </div>
          <div v-else class="text-sm text-[var(--muted)]">
            Config empty.
          </div>
        </div>
      </div>
    </div>
  </section>
</template>
