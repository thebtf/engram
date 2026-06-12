import { ref, onMounted, onUnmounted, watch, type Ref } from 'vue'
import type { Stats } from '@/types'
import { fetchStats } from '@/utils/api'
import { useSSE } from './useSSE'

// When SSE is disconnected we fall back to polling so the stats panel stays
// fresh even without a live event stream.
const FALLBACK_POLL_MS = 10_000

export function useStats(projectRef?: Ref<string | null>) {
  const stats = ref<Stats | null>(null)
  const loading = ref(false)
  const error = ref<string | null>(null)

  // SSE-driven updates are the primary path. The fallback poll only runs
  // while the SSE connection is down.
  const { lastEvent, isConnected } = useSSE()

  let fallbackPollId: number | null = null

  const refresh = async (): Promise<void> => {
    loading.value = true
    error.value = null
    try {
      stats.value = await fetchStats(projectRef?.value ?? null)
    } catch (err) {
      error.value = err instanceof Error ? err.message : 'Failed to fetch stats'
      console.error('[Stats] Error:', err)
    } finally {
      loading.value = false
    }
  }

  function startFallbackPolling(): void {
    if (fallbackPollId !== null) return
    console.log('[Stats] SSE down — starting fallback poll')
    fallbackPollId = window.setInterval(refresh, FALLBACK_POLL_MS)
  }

  function stopFallbackPolling(): void {
    if (fallbackPollId !== null) {
      console.log('[Stats] SSE up — stopping fallback poll')
      clearInterval(fallbackPollId)
      fallbackPollId = null
    }
  }

  // SSE events that carry session or processing data are the signal to pull
  // fresh stats. We react to the event here rather than having the SSE layer
  // push stats directly, keeping concerns separated.
  watch(lastEvent, (event) => {
    if (event && (event.type === 'session' || event.type === 'processing_status')) {
      if (event.type === 'session') {
        console.log('[Stats] Session event — refreshing:', event.action)
      }
      refresh()
    }
  })

  // A project filter change means the previous stats are for the wrong scope;
  // refresh immediately rather than waiting for the next SSE event.
  if (projectRef) {
    watch(projectRef, () => {
      console.log('[Stats] Project filter changed — refreshing')
      refresh()
    })
  }

  // Switch between SSE-driven and polled modes as the connection comes and goes.
  watch(isConnected, (connected) => {
    if (connected) {
      stopFallbackPolling()
      // Refresh on reconnect to catch any events we missed while disconnected.
      refresh()
    } else {
      startFallbackPolling()
    }
  })

  onMounted(() => {
    refresh()
    // Only start polling if SSE is already down at mount time; the watcher
    // above handles the transition if SSE drops later.
    if (!isConnected.value) startFallbackPolling()
  })

  onUnmounted(() => {
    stopFallbackPolling()
  })

  return {
    stats,
    loading,
    error,
    refresh,
  }
}
