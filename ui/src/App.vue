<script setup lang="ts">
import { computed, onMounted } from 'vue'
import { useRoute } from 'vue-router'
import { useAuth } from '@/composables/useAuth'
import { useSSE } from '@/composables/useSSE'
import { useColorMode } from '@/composables/useColorMode'
import AppSidebar from '@/components/layout/AppSidebar.vue'
import AppHeader from '@/components/layout/AppHeader.vue'
import { SidebarProvider, SidebarInset } from '@/components/ui/sidebar'

const route = useRoute()
const { authenticated, loading, checkAuth } = useAuth()
const { isReconnecting, reconnectCountdown } = useSSE()
useColorMode()

// Public routes (login, setup, register) render without sidebar/header
const isPublicRoute = computed(() => !!route.meta.public)

onMounted(() => {
  checkAuth()
})
</script>

<template>
  <div class="min-h-screen bg-background text-foreground">
    <!-- Loading spinner while checking auth -->
    <div v-if="loading" class="min-h-screen flex items-center justify-center">
      <i class="fas fa-spinner fa-spin text-2xl text-slate-500" />
    </div>

    <!-- Public pages: login, setup, register (no sidebar, no header) -->
    <router-view v-else-if="!authenticated || isPublicRoute" />

    <!-- Authenticated layout -->
    <SidebarProvider v-else>
      <AppSidebar />
      <SidebarInset>
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
        <main class="flex-1 overflow-auto p-6">
          <AppHeader />
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
