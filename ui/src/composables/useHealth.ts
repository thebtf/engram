import { ref, onMounted, onUnmounted } from 'vue'
import type { SelfCheckResponse } from '@/types'

// Poll the self-check endpoint every 30 seconds. This gives the health panel
// a fresh snapshot without polling so aggressively that it adds noticeable
// load to the server.
const POLL_INTERVAL_MS = 30_000

export function useHealth() {
  const health = ref<SelfCheckResponse | null>(null)
  const loading = ref(false)
  const error = ref<string | null>(null)

  let pollId: ReturnType<typeof setInterval> | null = null

  async function fetchHealth(): Promise<void> {
    loading.value = true
    error.value = null
    try {
      const res = await fetch('/api/selfcheck')
      if (!res.ok) throw new Error(`HTTP ${res.status}`)
      health.value = await res.json()
    } catch (e) {
      // Surface the error message for display but clear stale health data
      // so the UI shows degraded state rather than an outdated snapshot.
      error.value = e instanceof Error ? e.message : 'Unknown error'
      health.value = null
    } finally {
      loading.value = false
    }
  }

  function startPolling(): void {
    // Fetch immediately so the panel isn't blank for the first 30 seconds.
    fetchHealth()
    pollId = setInterval(fetchHealth, POLL_INTERVAL_MS)
  }

  function stopPolling(): void {
    if (pollId !== null) {
      clearInterval(pollId)
      pollId = null
    }
  }

  onMounted(startPolling)
  onUnmounted(stopPolling)

  return {
    health,
    loading,
    error,
    // Expose as `refresh` so a "Retry" button can force an immediate re-check
    // without waiting for the next poll cycle.
    refresh: fetchHealth,
  }
}
