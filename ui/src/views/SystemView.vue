<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useHealth, useStats, useUpdate } from '@/composables'
import { useUiI18n } from '@/composables/useUiI18n'
import { fetchMaintenanceStats } from '@/utils/api'
import { safeAbsoluteDate, formatUptime } from '@/utils/formatters'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import {
  RefreshCw,
  Server,
  Database,
  Cpu,
  Radio,
  ArrowUpCircle,
  Loader2,
  CheckCircle,
} from 'lucide-vue-next'

const { health, loading: healthLoading, refresh: refreshHealth } = useHealth()
const { stats } = useStats()
const { updateInfo, updateStatus, isUpdating, applyUpdate } = useUpdate()
const { t } = useUiI18n()

const maintenanceStats = ref<{ last_maintenance?: string } | null>(null)
const isRestarting = ref(false)

const uptime = computed(() => {
  if (!stats.value?.uptime) return t.value.common.none
  return formatUptime(stats.value.uptime)
})

const version = computed(() => health.value?.version || t.value.common.none)

const restartWorker = async () => {
  isRestarting.value = true
  try {
    await fetch('/api/update/restart', { method: 'POST' })
    for (let i = 0; i < 30; i++) {
      await new Promise(r => setTimeout(r, 500))
      try {
        const res = await fetch('/api/health', { signal: AbortSignal.timeout(2000) })
        if (res.ok) {
          const data = await res.json()
          if (data.status === 'ready') break
        }
      } catch {
        // worker not ready yet
      }
    }
    globalThis.location.reload()
  } catch {
    isRestarting.value = false
  }
}

onMounted(async () => {
  try {
    maintenanceStats.value = await fetchMaintenanceStats()
  } catch {
    maintenanceStats.value = null
  }
})

const healthIcon = (name: string) => {
  const map: Record<string, typeof Server> = {
    'Worker Service': Server,
    PostgreSQL: Database,
    'SDK Processor': Cpu,
    'SSE Broadcaster': Radio,
  }
  return map[name] || Server
}
</script>

<template>
  <div class="space-y-4 pt-4">
    <h1 class="text-lg font-semibold">{{ t.system.title }}</h1>

    <div class="grid grid-cols-1 md:grid-cols-2 gap-4">
      <Card>
        <CardHeader class="pb-3">
          <CardTitle class="text-sm font-medium">{{ t.system.serverTitle }}</CardTitle>
        </CardHeader>
        <CardContent class="space-y-2 text-sm">
          <div class="flex justify-between">
            <span class="text-muted-foreground">{{ t.system.fields.version }}</span>
            <Badge variant="secondary" class="font-mono">{{ version }}</Badge>
          </div>
          <div class="flex justify-between">
            <span class="text-muted-foreground">{{ t.system.fields.uptime }}</span>
            <span class="font-mono">{{ uptime }}</span>
          </div>
          <div class="flex justify-between">
            <span class="text-muted-foreground">{{ t.system.fields.sessionsToday }}</span>
            <span class="font-mono">{{ stats?.sessionsToday ?? 0 }}</span>
          </div>
          <div class="flex justify-between">
            <span class="text-muted-foreground">{{ t.system.fields.connectedClients }}</span>
            <span class="font-mono">{{ stats?.connectedClients ?? 0 }}</span>
          </div>
          <div v-if="stats?.retrieval" class="flex justify-between">
            <span class="text-muted-foreground">{{ t.system.fields.retrievalRequests }}</span>
            <span class="font-mono">{{ stats.retrieval.total_requests }}</span>
          </div>
          <div v-if="maintenanceStats?.last_maintenance" class="flex justify-between">
            <span class="text-muted-foreground">{{ t.system.fields.lastMaintenance }}</span>
            <span class="font-mono">{{ safeAbsoluteDate(maintenanceStats.last_maintenance) }}</span>
          </div>
        </CardContent>
      </Card>

      <Card>
        <CardHeader class="pb-3">
          <div class="flex items-center justify-between">
            <CardTitle class="text-sm font-medium">{{ t.system.healthTitle }}</CardTitle>
            <Button variant="ghost" size="sm" @click="refreshHealth" :disabled="healthLoading">
              <RefreshCw :class="healthLoading ? 'animate-spin' : ''" :size="14" />
            </Button>
          </div>
        </CardHeader>
        <CardContent>
          <div v-if="health" class="space-y-2">
            <div
              v-for="component in health.components"
              :key="component.name"
              class="flex items-center justify-between text-sm"
            >
              <div class="flex items-center gap-2">
                <component :is="healthIcon(component.name)" :size="14" class="text-muted-foreground" />
                <span>{{ component.name }}</span>
              </div>
              <Badge
                :variant="component.status === 'healthy' ? 'secondary' : 'destructive'"
                class="text-xs"
              >
                {{ component.status }}
              </Badge>
            </div>
          </div>
          <div v-else class="text-sm text-muted-foreground">{{ t.system.loadingHealth }}</div>
        </CardContent>
      </Card>
    </div>

    <Card>
      <CardHeader class="pb-3">
        <CardTitle class="text-sm font-medium">{{ t.system.updatesTitle }}</CardTitle>
      </CardHeader>
      <CardContent>
        <div v-if="updateInfo?.available && !isUpdating && updateStatus.state === 'idle'" class="flex items-center justify-between">
          <div>
            <p class="text-sm">{{ t.system.updateAvailable }}: <span class="font-mono font-medium">v{{ updateInfo.latest_version }}</span></p>
            <p class="text-xs text-muted-foreground">{{ t.system.currentVersion }}: v{{ updateInfo.current_version }}</p>
          </div>
          <Button size="sm" @click="applyUpdate()">
            <ArrowUpCircle :size="14" class="mr-1" />
            {{ t.system.updateNow }}
          </Button>
        </div>
        <div v-else-if="isUpdating" class="flex items-center gap-2 text-sm">
          <Loader2 class="animate-spin" :size="14" />
          <span>{{ updateStatus.message || t.system.updatingFallback }} {{ Math.round(updateStatus.progress * 100) }}%</span>
        </div>
        <div v-else-if="updateStatus.state === 'done'" class="flex items-center justify-between">
          <p class="text-sm text-green-600 dark:text-green-400">{{ t.system.updateApplied }}</p>
          <Button size="sm" variant="outline" @click="restartWorker" :disabled="isRestarting">
            <Loader2 v-if="isRestarting" class="animate-spin mr-1" :size="14" />
            <CheckCircle v-else :size="14" class="mr-1" />
            {{ isRestarting ? t.system.restarting : t.system.restart }}
          </Button>
        </div>
        <p v-else-if="updateStatus.state === 'error'" class="text-sm text-destructive">
          {{ t.system.updateFailed }}
        </p>
        <p v-else class="text-sm text-muted-foreground">{{ t.system.upToDate }}</p>
      </CardContent>
    </Card>
  </div>
</template>
