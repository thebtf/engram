<script setup lang="ts">
import { computed } from 'vue'
import { useHealthQuery, useMaintenanceStatsQuery, useStatsQuery, useUpdateCheckQuery } from '~/composables/useOperatorQueries'
import { formatAbsoluteDate } from '~/utils/formatters'

const statsQuery = useStatsQuery()
const healthQuery = useHealthQuery()
const maintenanceStatsQuery = useMaintenanceStatsQuery()
const updateQuery = useUpdateCheckQuery()

const uptime = computed(() => statsQuery.data.value?.uptime || '—')
const version = computed(() => healthQuery.data.value?.version || '—')

function healthTone(status: string): string {
  switch (status) {
    case 'healthy':
      return 'border-[color:rgba(52,211,153,0.4)] bg-[color:rgba(52,211,153,0.12)] text-[var(--success)]'
    case 'degraded':
      return 'border-[color:rgba(251,191,36,0.4)] bg-[color:rgba(251,191,36,0.12)] text-[var(--warn)]'
    default:
      return 'border-[color:rgba(248,113,113,0.4)] bg-[color:rgba(248,113,113,0.12)] text-[var(--danger)]'
  }
}
</script>

<template>
  <section class="space-y-5">
    <div class="surface-panel p-5">
      <div class="mb-4">
        <h1 class="text-xl font-semibold">System & Health</h1>
        <p class="mt-2 text-sm text-[var(--fg-2)]">
          Live server status, health snapshot, maintenance markers and update availability from the current Go runtime.
        </p>
      </div>

      <div class="grid gap-4 md:grid-cols-2 xl:grid-cols-4">
        <div class="surface-panel-warm p-4">
          <div class="text-xs uppercase tracking-[0.08em] text-[var(--muted)]">Version</div>
          <div class="mono-data mt-3 text-xl font-semibold">{{ version }}</div>
        </div>
        <div class="surface-panel-warm p-4">
          <div class="text-xs uppercase tracking-[0.08em] text-[var(--muted)]">Uptime</div>
          <div class="mono-data mt-3 text-xl font-semibold">{{ uptime }}</div>
        </div>
        <div class="surface-panel-warm p-4">
          <div class="text-xs uppercase tracking-[0.08em] text-[var(--muted)]">Sessions Today</div>
          <div class="mono-data mt-3 text-xl font-semibold">{{ statsQuery.data.value?.sessionsToday ?? 0 }}</div>
        </div>
        <div class="surface-panel-warm p-4">
          <div class="text-xs uppercase tracking-[0.08em] text-[var(--muted)]">Connected Clients</div>
          <div class="mono-data mt-3 text-xl font-semibold">{{ statsQuery.data.value?.connectedClients ?? 0 }}</div>
        </div>
      </div>

      <div class="mt-4 grid gap-4 xl:grid-cols-[minmax(0,1fr),360px]">
        <div class="surface-panel-warm p-4">
          <div class="mb-3 text-xs uppercase tracking-[0.08em] text-[var(--muted)]">Health Components</div>
          <div v-if="healthQuery.isPending.value" class="text-sm text-[var(--muted)]">Загрузка health...</div>
          <div v-else-if="healthQuery.isError.value" class="text-sm text-[var(--danger)]">
            {{ healthQuery.error.value?.message || 'Не удалось загрузить health' }}
          </div>
          <div v-else class="space-y-3">
            <div
              v-for="component in healthQuery.data.value?.components || []"
              :key="component.name"
              class="flex items-center justify-between gap-3 rounded-xl border border-[var(--border)] bg-[var(--surface)] px-3 py-3"
            >
              <div>
                <div class="text-sm font-semibold">{{ component.name }}</div>
                <div v-if="component.message" class="mt-1 text-xs text-[var(--muted)]">{{ component.message }}</div>
              </div>
              <span class="rounded-full border px-2 py-1 text-[10px] font-semibold uppercase tracking-[0.04em]" :class="healthTone(component.status)">
                {{ component.status }}
              </span>
            </div>
          </div>
        </div>

        <div class="space-y-4">
          <div class="surface-panel-warm p-4">
            <div class="mb-3 text-xs uppercase tracking-[0.08em] text-[var(--muted)]">Retrieval</div>
            <div class="space-y-2 text-sm text-[var(--fg-2)]">
              <div class="flex items-center justify-between gap-3">
                <span>Total requests</span>
                <span class="mono-data">{{ statsQuery.data.value?.retrieval?.total_requests ?? 0 }}</span>
              </div>
              <div class="flex items-center justify-between gap-3">
                <span>Observations served</span>
                <span class="mono-data">{{ statsQuery.data.value?.retrieval?.observations_served ?? 0 }}</span>
              </div>
              <div class="flex items-center justify-between gap-3">
                <span>Context injections</span>
                <span class="mono-data">{{ statsQuery.data.value?.retrieval?.context_injections ?? 0 }}</span>
              </div>
            </div>
          </div>

          <div class="surface-panel-warm p-4">
            <div class="mb-3 text-xs uppercase tracking-[0.08em] text-[var(--muted)]">Maintenance</div>
            <div class="space-y-2 text-sm text-[var(--fg-2)]">
              <div class="flex items-center justify-between gap-3">
                <span>Last maintenance</span>
                <span class="mono-data">
                  {{ maintenanceStatsQuery.data.value?.last_maintenance ? formatAbsoluteDate(maintenanceStatsQuery.data.value.last_maintenance) : '—' }}
                </span>
              </div>
              <div class="flex items-center justify-between gap-3">
                <span>Last consolidation</span>
                <span class="mono-data">
                  {{ maintenanceStatsQuery.data.value?.last_consolidation ? formatAbsoluteDate(maintenanceStatsQuery.data.value.last_consolidation) : '—' }}
                </span>
              </div>
            </div>
          </div>

          <div class="surface-panel-warm p-4">
            <div class="mb-3 text-xs uppercase tracking-[0.08em] text-[var(--muted)]">Updates</div>
            <div v-if="updateQuery.isPending.value" class="text-sm text-[var(--muted)]">Проверка обновлений...</div>
            <div v-else-if="updateQuery.data.value?.available" class="space-y-2 text-sm">
              <div class="text-[var(--fg)]">
                Доступно обновление до <span class="mono-data">{{ updateQuery.data.value.latest_version }}</span>
              </div>
              <div class="text-[var(--muted)]">
                current <span class="mono-data">{{ updateQuery.data.value.current_version }}</span>
              </div>
            </div>
            <div v-else class="text-sm text-[var(--fg-2)]">
              Up to date.
            </div>
          </div>
        </div>
      </div>
    </div>
  </section>
</template>
