<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useRoute } from 'vue-router'
import { useAuth } from '@/composables/useAuth'
import { useSSE } from '@/composables/useSSE'
import { useUpdate } from '@/composables/useUpdate'
import { useColorMode } from '@/composables/useColorMode'
import AppSidebar from '@/components/layout/AppSidebar.vue'
import { SidebarProvider, SidebarInset, SidebarTrigger } from '@/components/ui/sidebar'
import { Separator } from '@/components/ui/separator'
import { Loader2, RefreshCw, ArrowUpCircle, CheckCircle, AlertCircle } from 'lucide-vue-next'

const route = useRoute()
const { authenticated, loading, checkAuth } = useAuth()
const { isReconnecting, reconnectCountdown } = useSSE()
const { updateInfo, updateStatus, isUpdating, applyUpdate } = useUpdate()
useColorMode()

const isPublicRoute = computed(() => !!route.meta.public)
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

onMounted(() => {
  checkAuth()
})
</script>

<template>
  <div class="min-h-screen bg-background text-foreground">
    <!-- Loading spinner -->
    <div v-if="loading" class="min-h-screen flex items-center justify-center">
      <Loader2 class="animate-spin text-muted-foreground" :size="24" />
    </div>

    <!-- Public pages -->
    <router-view v-else-if="!authenticated || isPublicRoute" />

    <!-- Authenticated layout -->
    <SidebarProvider v-else>
      <AppSidebar />
      <SidebarInset>
        <!-- Top bar: sidebar trigger + update banner -->
        <header class="flex h-10 shrink-0 items-center gap-2 border-b border-border px-3">
          <SidebarTrigger class="-ml-1" />
          <Separator orientation="vertical" class="mr-2 h-4" />

          <!-- Reconnection banner (inline) -->
          <div v-if="isReconnecting" class="flex items-center gap-2 text-amber-600 dark:text-amber-400 text-sm">
            <RefreshCw class="animate-spin" :size="14" />
            <span>Reconnecting<span v-if="reconnectCountdown > 0"> in {{ reconnectCountdown }}s</span>...</span>
          </div>

          <div class="flex-1" />

          <!-- Update widget (right side, inline) -->
          <div v-if="updateInfo?.available && !isUpdating && updateStatus.state === 'idle'"
            class="flex items-center gap-2 text-sm">
            <button
              class="flex items-center gap-1.5 px-2.5 py-1 rounded-md bg-amber-100 dark:bg-amber-500/20 text-amber-700 dark:text-amber-400 hover:bg-amber-200 dark:hover:bg-amber-500/30 transition-colors"
              @click="applyUpdate()"
            >
              <ArrowUpCircle :size="14" />
              <span>Update to v{{ updateInfo.latest_version }}</span>
            </button>
          </div>

          <div v-else-if="isUpdating" class="flex items-center gap-2 text-amber-600 dark:text-amber-400 text-sm">
            <Loader2 class="animate-spin" :size="14" />
            <span>{{ updateStatus.message || 'Updating...' }} {{ Math.round(updateStatus.progress * 100) }}%</span>
          </div>

          <div v-else-if="updateStatus.state === 'done'">
            <button
              class="flex items-center gap-1.5 px-2.5 py-1 rounded-md bg-green-100 dark:bg-green-500/20 text-green-700 dark:text-green-400 hover:bg-green-200 dark:hover:bg-green-500/30 transition-colors text-sm"
              :disabled="isRestarting"
              @click="restartWorker"
            >
              <Loader2 v-if="isRestarting" class="animate-spin" :size="14" />
              <CheckCircle v-else :size="14" />
              <span>{{ isRestarting ? 'Restarting...' : 'Restart to apply' }}</span>
            </button>
          </div>

          <div v-else-if="updateStatus.state === 'error'" class="flex items-center gap-2 text-destructive text-sm">
            <AlertCircle :size="14" />
            <span>Update failed</span>
          </div>
        </header>

        <!-- Main content -->
        <main class="flex-1 overflow-auto px-4 py-4 lg:px-6">
          <router-view />
        </main>
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
