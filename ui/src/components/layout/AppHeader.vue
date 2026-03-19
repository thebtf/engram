<script setup lang="ts">
import { ref } from 'vue'
import { useRouter } from 'vue-router'
import { useSSE, useUpdate } from '@/composables'
import SearchBar from '@/components/search/SearchBar.vue'

const router = useRouter()
const { isConnected, isProcessing } = useSSE()
const { updateInfo, updateStatus, isUpdating, applyUpdate } = useUpdate()

const showUpdateModal = ref(false)
const isRestarting = ref(false)
const searchQuery = ref('')

function handleSearch(q: string) {
  if (q.trim()) {
    router.push({ name: 'search', query: { q: q.trim() } })
    searchQuery.value = ''
  }
}

const restartWorker = async () => {
  isRestarting.value = true
  try {
    await fetch('/api/update/restart', { method: 'POST' })
    await waitForWorker()
    globalThis.location.reload()
  } catch (error) {
    console.error('Failed to restart:', error)
    isRestarting.value = false
  }
}

const waitForWorker = async (maxAttempts = 30, delayMs = 500): Promise<void> => {
  for (let i = 0; i < maxAttempts; i++) {
    await new Promise(resolve => setTimeout(resolve, delayMs))
    try {
      const response = await fetch('/api/health', {
        signal: AbortSignal.timeout(2000),
      })
      if (response.ok) {
        const data = await response.json()
        if (data.status === 'ready') {
          return
        }
      }
    } catch {
      // Worker not ready yet, continue polling
    }
  }
}
</script>

<template>
  <header class="flex items-center justify-between gap-4 mb-6">
    <!-- Search bar -->
    <SearchBar
      v-model="searchQuery"
      :compact="true"
      class="flex-1"
      @submit="handleSearch"
    />

    <!-- Right actions -->
    <div class="flex items-center gap-3">
      <!-- Update Available -->
      <div v-if="updateInfo?.available && !isUpdating && updateStatus.state === 'idle'">
        <button
          class="flex items-center gap-2 px-3 py-1.5 rounded-lg bg-amber-500/20 border border-amber-500/50 text-amber-400 hover:bg-amber-500/30 transition-colors text-sm"
          @click="showUpdateModal = true"
        >
          <i class="fas fa-arrow-circle-up" />
          <span>v{{ updateInfo.latest_version }}</span>
        </button>
      </div>

      <!-- Update In Progress -->
      <div v-else-if="isUpdating" class="flex items-center gap-2 text-amber-400 text-sm">
        <i class="fas fa-spinner animate-spin" />
        <span>{{ updateStatus.message || 'Updating...' }}</span>
        <span class="text-slate-500">{{ Math.round(updateStatus.progress * 100) }}%</span>
      </div>

      <!-- Update Complete -->
      <div v-else-if="updateStatus.state === 'done'">
        <button
          class="flex items-center gap-2 px-3 py-1.5 rounded-lg bg-green-500/20 border border-green-500/50 text-green-400 hover:bg-green-500/30 transition-colors text-sm"
          :disabled="isRestarting"
          @click="restartWorker"
        >
          <i :class="isRestarting ? 'fas fa-spinner animate-spin' : 'fas fa-check-circle'" />
          <span>{{ isRestarting ? 'Restarting...' : 'Restart' }}</span>
        </button>
      </div>

      <!-- Update Error -->
      <div
        v-else-if="updateStatus.state === 'error'"
        class="flex items-center gap-2 text-red-400 text-sm"
        :title="updateStatus.error"
      >
        <i class="fas fa-exclamation-circle" />
        <span>Update failed</span>
      </div>

      <!-- Connection Status -->
      <div class="flex items-center gap-2">
        <span
          class="w-2 h-2 rounded-full"
          :class="[
            isConnected ? 'bg-green-500' : 'bg-red-500',
            isProcessing ? 'animate-pulse' : '',
          ]"
        />
        <span class="text-sm text-slate-400">
          {{ isConnected ? (isProcessing ? 'Processing' : 'Connected') : 'Disconnected' }}
        </span>
      </div>
    </div>

    <!-- Update Modal -->
    <Teleport to="body">
      <div v-if="showUpdateModal" class="fixed inset-0 z-50 flex items-center justify-center p-4">
        <div class="absolute inset-0 bg-black/60 backdrop-blur-sm" @click="showUpdateModal = false" />
        <div class="relative glass border border-white/10 rounded-2xl p-6 max-w-md w-full shadow-2xl">
          <button
            class="absolute top-4 right-4 text-slate-400 hover:text-white"
            @click="showUpdateModal = false"
          >
            <i class="fas fa-times" />
          </button>

          <div class="text-center mb-4">
            <div
              class="w-16 h-16 mx-auto mb-4 rounded-2xl bg-gradient-to-br from-amber-500 to-orange-600 flex items-center justify-center"
            >
              <i class="fas fa-arrow-circle-up text-3xl text-white" />
            </div>
            <h3 class="text-xl font-bold text-white mb-1">Update Available</h3>
            <p class="text-slate-400 text-sm">
              v{{ updateInfo?.current_version }} &rarr; v{{ updateInfo?.latest_version }}
            </p>
          </div>

          <div v-if="updateInfo?.release_notes" class="mb-4 max-h-48 overflow-y-auto">
            <p class="text-slate-500 text-xs uppercase tracking-wide mb-2">What's new</p>
            <div
              class="bg-slate-800/50 rounded-lg p-3 text-sm text-slate-300 whitespace-pre-wrap leading-relaxed"
            >
              {{ updateInfo.release_notes }}
            </div>
          </div>

          <div class="space-y-3">
            <button
              class="w-full py-3 rounded-xl bg-amber-500 text-slate-900 font-semibold hover:bg-amber-400 transition-colors"
              @click="applyUpdate(); showUpdateModal = false"
            >
              Update Now
            </button>
            <button
              class="w-full py-3 rounded-xl bg-white/5 text-slate-400 hover:bg-white/10 hover:text-white transition-colors"
              @click="showUpdateModal = false"
            >
              Later
            </button>
          </div>

          <p class="text-center text-slate-500 text-xs mt-4">
            Updates are verified with cosign signatures
          </p>
        </div>
      </div>
    </Teleport>
  </header>
</template>
