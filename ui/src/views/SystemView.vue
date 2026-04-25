<script setup lang="ts">
import { ref, onMounted, computed } from 'vue'
import { useHealth, useStats, useUpdate } from '@/composables'
import { useColorMode } from '@/composables/useColorMode'
import { fetchConfig, fetchMaintenanceStats } from '@/utils/api'
import { formatUptime } from '@/utils/formatters'
import { Card, CardContent, CardHeader, CardTitle, CardDescription } from '@/components/ui/card'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Separator } from '@/components/ui/separator'
import {
  Select, SelectContent, SelectItem, SelectTrigger, SelectValue,
} from '@/components/ui/select'
import {
  Sun, Moon, Monitor as MonitorIcon, RefreshCw, Server, Database,
  Cpu, Radio, ArrowUpCircle, Loader2, CheckCircle,
} from 'lucide-vue-next'

const { health, loading: healthLoading, refresh: refreshHealth } = useHealth()
const { stats } = useStats()
const { updateInfo, updateStatus, isUpdating, applyUpdate } = useUpdate()
const { preference, cycleColorMode } = useColorMode()

const config = ref<Record<string, Record<string, unknown>> | null>(null)
const configLoading = ref(false)
const maintenanceStats = ref<{ last_maintenance?: string } | null>(null)

const uptime = computed(() => {
  if (!stats.value?.uptime) return '—'
  return formatUptime(stats.value.uptime)
})

const version = computed(() => health.value?.version || '—')

const themeOptions = [
  { value: 'light', label: 'Light', icon: Sun },
  { value: 'dark', label: 'Dark', icon: Moon },
  { value: 'auto', label: 'System', icon: MonitorIcon },
]

function setTheme(value: string) {
  while (preference.value !== value) {
    cycleColorMode()
  }
}

const isRestarting = ref(false)
const restartWorker = async () => {
  isRestarting.value = true
  try {
    await fetch('/api/update/restart', { method: 'POST' })
    for (let i = 0; i < 30; i++) {
      await new Promise(r => setTimeout(r, 500))
      try {
        const res = await fetch('/api/health', { signal: AbortSignal.timeout(2000) })
        if (res.ok) { const d = await res.json(); if (d.status === 'ready') break }
      } catch { /* not ready */ }
    }
    globalThis.location.reload()
  } catch {
    isRestarting.value = false
  }
}

onMounted(async () => {
  configLoading.value = true
  try {
    config.value = await fetchConfig()
  } catch { /* non-critical */ }
  try {
    maintenanceStats.value = await fetchMaintenanceStats()
  } catch { /* non-critical */ }
  configLoading.value = false
})

const healthIcon = (name: string) => {
  const map: Record<string, typeof Server> = {
    'Worker Service': Server,
    'PostgreSQL': Database,
    'SDK Processor': Cpu,
    'SSE Broadcaster': Radio,
  }
  return map[name] || Server
}
</script>

<template>
  <div class="space-y-4">
    <h1 class="text-lg font-semibold">System</h1>

    <!-- Server Info -->
    <div class="grid grid-cols-1 md:grid-cols-2 gap-4">
      <Card>
        <CardHeader class="pb-3">
          <CardTitle class="text-sm font-medium">Server</CardTitle>
        </CardHeader>
        <CardContent class="space-y-2 text-sm">
          <div class="flex justify-between">
            <span class="text-muted-foreground">Version</span>
            <Badge variant="secondary" class="font-mono">{{ version }}</Badge>
          </div>
          <div class="flex justify-between">
            <span class="text-muted-foreground">Uptime</span>
            <span class="font-mono">{{ uptime }}</span>
          </div>
          <div class="flex justify-between">
            <span class="text-muted-foreground">Sessions today</span>
            <span class="font-mono">{{ stats?.sessionsToday ?? 0 }}</span>
          </div>
          <div class="flex justify-between">
            <span class="text-muted-foreground">Connected clients</span>
            <span class="font-mono">{{ stats?.connectedClients ?? 0 }}</span>
          </div>
          <div v-if="stats?.retrieval" class="flex justify-between">
            <span class="text-muted-foreground">Retrieval requests</span>
            <span class="font-mono">{{ stats.retrieval.total_requests }}</span>
          </div>
        </CardContent>
      </Card>

      <!-- Health -->
      <Card>
        <CardHeader class="pb-3">
          <div class="flex items-center justify-between">
            <CardTitle class="text-sm font-medium">Health</CardTitle>
            <Button variant="ghost" size="sm" @click="refreshHealth" :disabled="healthLoading">
              <RefreshCw :class="healthLoading ? 'animate-spin' : ''" :size="14" />
            </Button>
          </div>
        </CardHeader>
        <CardContent>
          <div v-if="health" class="space-y-2">
            <div
              v-for="comp in health.components"
              :key="comp.name"
              class="flex items-center justify-between text-sm"
            >
              <div class="flex items-center gap-2">
                <component :is="healthIcon(comp.name)" :size="14" class="text-muted-foreground" />
                <span>{{ comp.name }}</span>
              </div>
              <Badge
                :variant="comp.status === 'healthy' ? 'secondary' : 'destructive'"
                class="text-xs"
              >
                {{ comp.status }}
              </Badge>
            </div>
          </div>
          <div v-else class="text-sm text-muted-foreground">Loading...</div>
        </CardContent>
      </Card>
    </div>

    <Separator />

    <!-- Appearance -->
    <Card>
      <CardHeader class="pb-3">
        <CardTitle class="text-sm font-medium">Appearance</CardTitle>
        <CardDescription>Customize how the dashboard looks</CardDescription>
      </CardHeader>
      <CardContent>
        <div class="flex items-center justify-between">
          <div>
            <p class="text-sm font-medium">Theme</p>
            <p class="text-xs text-muted-foreground">Select light, dark, or follow system preference</p>
          </div>
          <Select :model-value="preference" @update:model-value="(v: any) => setTheme(String(v))">
            <SelectTrigger class="w-36">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem v-for="opt in themeOptions" :key="opt.value" :value="opt.value">
                <div class="flex items-center gap-2">
                  <component :is="opt.icon" :size="14" />
                  {{ opt.label }}
                </div>
              </SelectItem>
            </SelectContent>
          </Select>
        </div>
      </CardContent>
    </Card>

    <!-- Updates -->
    <Card>
      <CardHeader class="pb-3">
        <CardTitle class="text-sm font-medium">Updates</CardTitle>
      </CardHeader>
      <CardContent>
        <div v-if="updateInfo?.available && !isUpdating && updateStatus.state === 'idle'" class="flex items-center justify-between">
          <div>
            <p class="text-sm">Update available: <span class="font-mono font-medium">v{{ updateInfo.latest_version }}</span></p>
            <p class="text-xs text-muted-foreground">Current: v{{ updateInfo.current_version }}</p>
          </div>
          <Button size="sm" @click="applyUpdate()">
            <ArrowUpCircle :size="14" class="mr-1" />
            Update now
          </Button>
        </div>
        <div v-else-if="isUpdating" class="flex items-center gap-2 text-sm">
          <Loader2 class="animate-spin" :size="14" />
          <span>{{ updateStatus.message || 'Updating...' }} {{ Math.round(updateStatus.progress * 100) }}%</span>
        </div>
        <div v-else-if="updateStatus.state === 'done'" class="flex items-center justify-between">
          <p class="text-sm text-green-600 dark:text-green-400">Update applied successfully</p>
          <Button size="sm" variant="outline" @click="restartWorker" :disabled="isRestarting">
            <Loader2 v-if="isRestarting" class="animate-spin mr-1" :size="14" />
            <CheckCircle v-else :size="14" class="mr-1" />
            {{ isRestarting ? 'Restarting...' : 'Restart' }}
          </Button>
        </div>
        <p v-else class="text-sm text-muted-foreground">Server is up to date</p>
      </CardContent>
    </Card>

    <!-- Server Configuration (read-only for now — future: editable) -->
    <Card v-if="config">
      <CardHeader class="pb-3">
        <CardTitle class="text-sm font-medium">Configuration</CardTitle>
        <CardDescription>Current server configuration (read-only — editing coming soon)</CardDescription>
      </CardHeader>
      <CardContent>
        <div class="space-y-3">
          <div v-for="(section, key) in config" :key="key">
            <p class="text-xs font-medium text-muted-foreground uppercase tracking-wide mb-1">{{ key }}</p>
            <div class="bg-muted rounded-md p-2 text-xs font-mono space-y-0.5">
              <div v-for="(val, field) in section" :key="field" class="flex gap-2">
                <span class="text-muted-foreground min-w-[140px]">{{ field }}</span>
                <span class="break-all">{{ val }}</span>
              </div>
            </div>
          </div>
        </div>
      </CardContent>
    </Card>
  </div>
</template>
