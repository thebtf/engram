<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useRoute } from 'vue-router'
import { useAuth } from '@/composables/useAuth'
import { useSSE } from '@/composables/useSSE'
import { useUpdate } from '@/composables/useUpdate'
import { useColorMode } from '@/composables/useColorMode'
import { useConsoleDensity } from '@/composables/useConsoleDensity'
import { useStats } from '@/composables/useStats'
import { useHealth } from '@/composables/useHealth'
import { useUiI18n } from '@/composables/useUiI18n'
import AppSidebar from '@/components/layout/AppSidebar.vue'
import { SidebarProvider, SidebarInset, SidebarTrigger } from '@/components/ui/sidebar'
import { Separator } from '@/components/ui/separator'
import { Toaster } from '@/components/ui/sonner'
import { Loader2, RefreshCw, ArrowUpCircle, CheckCircle, AlertCircle, Search, Sun, Moon, Monitor } from 'lucide-vue-next'

const route = useRoute()
const { authenticated, loading, checkAuth, user } = useAuth()
const { isReconnecting, reconnectCountdown, isConnected } = useSSE()
const { updateInfo, updateStatus, isUpdating, applyUpdate } = useUpdate()
const { preference, cycleColorMode } = useColorMode()
const { density, setDensity } = useConsoleDensity()
const { stats } = useStats()
const { health } = useHealth()
const { t } = useUiI18n()

const isPublicRoute = computed(() => !!route.meta.public)
const isRestarting = ref(false)
const globalSearch = ref('')

const routeMeta = computed<Record<string, { group: string; label: string }>>(() => ({
  home: t.value.app.routeMeta.home,
  memories: t.value.app.routeMeta.memories,
  rules: t.value.app.routeMeta.rules,
  issues: t.value.app.routeMeta.issues,
  'issue-detail': t.value.app.routeMeta.issueDetail,
  projects: t.value.app.routeMeta.projects,
  vault: t.value.app.routeMeta.vault,
  tokens: t.value.app.routeMeta.tokens,
  system: t.value.app.routeMeta.system,
  settings: t.value.app.routeMeta.settings,
  admin: t.value.app.routeMeta.admin,
}))

const currentRouteMeta = computed(() => {
  const name = String(route.name || '')
  return routeMeta.value[name] || t.value.app.routeMeta.fallback
})

const healthLabel = computed(() => {
  const overall = health.value?.overall
  if (overall === 'healthy') return t.value.app.healthLabel.healthy
  if (overall === 'degraded') return t.value.app.healthLabel.degraded
  if (overall === 'unhealthy') return t.value.app.healthLabel.unhealthy
  return t.value.app.healthLabel.unknown
})

const userInitials = computed(() => {
  const email = user.value?.email || 'EG'
  return email.slice(0, 2).toUpperCase()
})

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

onMounted(() => {
  checkAuth()
})
</script>

<template>
  <div class="min-h-screen bg-background text-foreground">
    <Toaster rich-colors position="bottom-right" />
    <!-- Loading spinner -->
    <div v-if="loading" class="min-h-screen flex items-center justify-center">
      <Loader2 class="animate-spin text-muted-foreground" :size="24" />
    </div>

    <!-- Public pages -->
    <router-view v-else-if="!authenticated || isPublicRoute" />

    <!-- Authenticated layout -->
    <SidebarProvider v-else class="bg-sidebar">
      <AppSidebar />
      <SidebarInset class="flex min-h-screen flex-col">
        <header
          class="flex min-h-14 shrink-0 items-center gap-3 border-b border-border bg-card/80 px-4 backdrop-blur supports-[backdrop-filter]:bg-card/70"
          :class="density === 'comfortable' ? 'py-3' : 'py-2'"
        >
          <SidebarTrigger class="-ml-1" />
          <Separator orientation="vertical" class="h-5" />

          <div class="min-w-0">
            <p class="text-[10px] uppercase tracking-[0.08em] text-muted-foreground">
              {{ currentRouteMeta.group }}
            </p>
            <h1 class="truncate text-sm font-semibold text-foreground">
              {{ currentRouteMeta.label }}
            </h1>
          </div>

          <div class="hidden min-w-0 flex-1 items-center md:flex">
            <label
              class="flex h-10 w-full max-w-xl items-center gap-2 rounded-xl border border-border bg-muted/40 px-3 text-sm text-muted-foreground"
              :title="t.app.globalSearchTitle"
            >
              <Search :size="14" />
              <input
                v-model="globalSearch"
                type="text"
                :placeholder="t.app.globalSearchPlaceholder"
                disabled
                class="w-full bg-transparent outline-none placeholder:text-muted-foreground/80"
              />
              <span class="rounded border border-border px-1.5 py-0.5 font-mono text-[10px]">/</span>
            </label>
          </div>

          <div class="ml-auto flex items-center gap-2">
            <div class="hidden items-center gap-1 rounded-xl border border-border bg-muted/40 p-1 sm:flex">
              <button
                class="rounded-lg px-3 py-1.5 text-xs font-medium transition-colors"
                :class="density === 'comfortable' ? 'bg-background text-foreground shadow-sm' : 'text-muted-foreground'"
                @click="setDensity('comfortable')"
              >
                {{ t.common.density.comfortable }}
              </button>
              <button
                class="rounded-lg px-3 py-1.5 text-xs font-medium transition-colors"
                :class="density === 'compact' ? 'bg-background text-foreground shadow-sm' : 'text-muted-foreground'"
                @click="setDensity('compact')"
              >
                {{ t.common.density.compact }}
              </button>
            </div>

            <button
              class="inline-flex h-10 w-10 items-center justify-center rounded-xl border border-border bg-muted/40 text-muted-foreground transition-colors hover:text-foreground"
              :title="`${t.app.themeTitle}: ${preference}`"
              @click="cycleColorMode"
            >
              <Sun v-if="preference === 'light'" :size="16" />
              <Moon v-else-if="preference === 'dark'" :size="16" />
              <Monitor v-else :size="16" />
            </button>

            <div
              class="hidden items-center gap-2 rounded-xl border border-border bg-muted/40 px-3 py-2 sm:flex"
              :title="user?.email || t.app.identityFallback"
            >
              <span class="flex h-8 w-8 items-center justify-center rounded-full bg-primary/15 text-xs font-semibold text-primary">
                {{ userInitials }}
              </span>
              <div class="min-w-0">
                <p class="truncate text-xs font-medium text-foreground">
                  {{ user?.email || t.app.identityFallback }}
                </p>
                <p class="text-[10px] uppercase tracking-[0.06em] text-muted-foreground">
                  {{ user?.role || t.app.identityRoleFallback }}
                </p>
              </div>
            </div>
          </div>
        </header>

        <div
          v-if="isReconnecting"
          class="flex items-center gap-2 border-b border-amber-500/20 bg-amber-500/10 px-4 py-2 text-sm text-amber-700 dark:text-amber-300"
        >
          <RefreshCw class="animate-spin" :size="14" />
          <span>
            {{ t.app.reconnecting }}
            <span v-if="reconnectCountdown > 0"> {{ t.app.reconnectInSeconds.replace('{seconds}', String(reconnectCountdown)) }}</span>
            ...
          </span>
        </div>

        <div
          v-if="updateInfo?.available && !isUpdating && updateStatus.state === 'idle'"
          class="flex items-center justify-between gap-3 border-b border-amber-500/20 bg-amber-500/10 px-4 py-2 text-sm"
        >
          <div class="text-amber-700 dark:text-amber-300">
            {{ t.app.updateAvailable }} <span class="font-mono">v{{ updateInfo.latest_version }}</span>
          </div>
          <button
            class="inline-flex items-center gap-1.5 rounded-lg bg-amber-600 px-3 py-1.5 text-xs font-medium text-white transition-colors hover:bg-amber-700"
            @click="applyUpdate()"
          >
            <ArrowUpCircle :size="14" />
            {{ t.app.updateAction }}
          </button>
        </div>

        <div
          v-else-if="isUpdating"
          class="flex items-center gap-2 border-b border-amber-500/20 bg-amber-500/10 px-4 py-2 text-sm text-amber-700 dark:text-amber-300"
        >
          <Loader2 class="animate-spin" :size="14" />
          <span>{{ updateStatus.message || t.app.updatingFallback }} {{ Math.round(updateStatus.progress * 100) }}%</span>
        </div>

        <div
          v-else-if="updateStatus.state === 'done'"
          class="flex items-center justify-between gap-3 border-b border-emerald-500/20 bg-emerald-500/10 px-4 py-2 text-sm"
        >
          <div class="text-emerald-700 dark:text-emerald-300">{{ t.app.updateApplied }}</div>
          <button
            class="inline-flex items-center gap-1.5 rounded-lg bg-emerald-600 px-3 py-1.5 text-xs font-medium text-white transition-colors hover:bg-emerald-700"
            :disabled="isRestarting"
            @click="restartWorker"
          >
            <Loader2 v-if="isRestarting" class="animate-spin" :size="14" />
            <CheckCircle v-else :size="14" />
            {{ isRestarting ? t.app.restarting : t.app.restart }}
          </button>
        </div>

        <div
          v-else-if="updateStatus.state === 'error'"
          class="flex items-center gap-2 border-b border-destructive/20 bg-destructive/10 px-4 py-2 text-sm text-destructive"
        >
          <AlertCircle :size="14" />
          <span>{{ t.app.updateFailed }}</span>
        </div>

        <main
          class="flex-1 overflow-auto"
          :class="density === 'comfortable' ? 'px-6 pb-6 pt-4 lg:px-8' : 'px-4 pb-4 pt-4 lg:px-6'"
        >
          <router-view />
        </main>

        <footer class="flex min-h-8 shrink-0 items-center gap-3 border-t border-border bg-card/90 px-4 text-xs text-muted-foreground">
          <span class="inline-flex items-center gap-1.5">
            <span :class="['h-2 w-2 rounded-full', isConnected ? 'bg-emerald-500' : 'bg-amber-500']" />
            {{ isConnected ? t.common.online : t.common.offline }}
          </span>
          <span class="font-mono text-foreground/80">{{ health?.version || '—' }}</span>
          <span class="hidden sm:inline">•</span>
          <span class="hidden sm:inline">{{ currentRouteMeta.label }}</span>
          <span class="ml-auto" />
          <span class="hidden md:inline">{{ t.app.footerSessionsToday }} {{ stats?.sessionsToday ?? '—' }}</span>
          <span class="hidden md:inline">{{ t.app.footerClients }} {{ stats?.connectedClients ?? '—' }}</span>
          <span class="inline-flex items-center gap-1.5">
            <span :class="['h-2 w-2 rounded-full', health?.overall === 'healthy' ? 'bg-emerald-500' : health?.overall === 'degraded' ? 'bg-amber-500' : 'bg-red-500']" />
            {{ healthLabel }}
          </span>
        </footer>
      </SidebarInset>
    </SidebarProvider>
  </div>
</template>

<style scoped>
.slide-enter-active,
.slide-leave-active {
  transition: transform 0.3s ease, opacity 0.3s ease;
}

.slide-enter-from,
.slide-leave-to {
  transform: translateY(-100%);
  opacity: 0;
}
</style>
