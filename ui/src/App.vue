<script setup lang="ts">
import { onMounted } from 'vue'
import { useAuth } from '@/composables/useAuth'
import { useSSE } from '@/composables/useSSE'
import AppSidebar from '@/components/layout/AppSidebar.vue'
import AppHeader from '@/components/layout/AppHeader.vue'

const { authenticated, loading, checkAuth } = useAuth()
const { isReconnecting, reconnectCountdown } = useSSE()

onMounted(() => {
  checkAuth()
})
</script>

<template>
  <div class="min-h-screen bg-slate-950 text-white">
    <!-- Loading spinner while checking auth -->
    <div v-if="loading" class="min-h-screen flex items-center justify-center">
      <i class="fas fa-spinner fa-spin text-2xl text-slate-500" />
    </div>

    <!-- Login page (no sidebar, no header) -->
    <router-view v-else-if="!authenticated" />

    <!-- Authenticated layout -->
    <div v-else class="flex min-h-screen">
      <AppSidebar />

      <div class="flex-1 min-w-0 flex flex-col">
        <!-- Reconnection Banner -->
        <Transition name="slide">
          <div
            v-if="isReconnecting"
            class="bg-amber-500/90 backdrop-blur-sm text-black px-4 py-2 text-center text-sm font-medium shadow-lg"
          >
            <i class="fas fa-sync-alt fa-spin mr-2" />
            Connection lost. Reconnecting<span v-if="reconnectCountdown > 0">
              in {{ reconnectCountdown }}s</span
            >...
          </div>
        </Transition>

        <!-- Main content area -->
        <main class="flex-1 p-6 min-h-0 overflow-auto">
          <AppHeader />
          <router-view />
        </main>
      </div>
    </div>
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
