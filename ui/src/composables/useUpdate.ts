import { ref, onMounted, onUnmounted } from 'vue'

export interface UpdateInfo {
  available: boolean
  current_version: string
  latest_version: string
  release_notes?: string
  published_at?: string
}

export interface UpdateStatus {
  state: 'idle' | 'checking' | 'downloading' | 'verifying' | 'applying' | 'done' | 'error'
  progress: number
  message: string
  error?: string
}

// Background check runs every 30 minutes so the UI can surface available
// updates without the user having to refresh the page manually.
const CHECK_INTERVAL_MS = 30 * 60 * 1000

export function useUpdate() {
  const updateInfo = ref<UpdateInfo | null>(null)
  const updateStatus = ref<UpdateStatus>({ state: 'idle', progress: 0, message: '' })
  const isChecking = ref(false)
  const isUpdating = ref(false)

  // Two separate intervals: one for background availability checks,
  // one for polling in-progress update status at 1 Hz.
  let statusPollId: ReturnType<typeof setInterval> | null = null
  let periodicCheckId: ReturnType<typeof setInterval> | null = null

  // Ask the server whether a newer release is available.
  const checkForUpdate = async (): Promise<void> => {
    isChecking.value = true
    try {
      const res = await fetch('/api/update/check')
      if (res.ok) updateInfo.value = await res.json()
    } catch (err) {
      console.error('[Update] Check failed:', err)
    } finally {
      isChecking.value = false
    }
  }

  // Trigger a server-side download + apply cycle, then start watching progress.
  const applyUpdate = async (): Promise<void> => {
    if (!updateInfo.value?.available) return
    isUpdating.value = true
    try {
      const res = await fetch('/api/update/apply', { method: 'POST' })
      if (res.ok) startStatusPolling()
    } catch (err) {
      console.error('[Update] Apply failed:', err)
      isUpdating.value = false
    }
  }

  // Fetch the current update state (downloading / verifying / done / error).
  const fetchStatus = async (): Promise<void> => {
    try {
      const res = await fetch('/api/update/status')
      if (!res.ok) return
      updateStatus.value = await res.json()
      // Terminal states: stop polling so we don't hammer the endpoint once
      // the update has finished (or failed).
      const { state } = updateStatus.value
      if (state === 'done' || state === 'error') {
        stopStatusPolling()
        isUpdating.value = false
      }
    } catch (err) {
      console.error('[Update] Status fetch failed:', err)
    }
  }

  function startStatusPolling(): void {
    if (statusPollId !== null) return
    // Fetch immediately so the UI reacts without waiting a full second.
    fetchStatus()
    statusPollId = setInterval(fetchStatus, 1000)
  }

  function stopStatusPolling(): void {
    if (statusPollId !== null) {
      clearInterval(statusPollId)
      statusPollId = null
    }
  }

  function startPeriodicCheck(): void {
    if (periodicCheckId !== null) return
    periodicCheckId = setInterval(checkForUpdate, CHECK_INTERVAL_MS)
  }

  function stopPeriodicCheck(): void {
    if (periodicCheckId !== null) {
      clearInterval(periodicCheckId)
      periodicCheckId = null
    }
  }

  onMounted(() => {
    // Check immediately on mount so the user sees update state right away,
    // then schedule recurring background checks.
    checkForUpdate()
    startPeriodicCheck()
  })

  onUnmounted(() => {
    stopStatusPolling()
    stopPeriodicCheck()
  })

  return {
    updateInfo,
    updateStatus,
    isChecking,
    isUpdating,
    checkForUpdate,
    applyUpdate,
  }
}
