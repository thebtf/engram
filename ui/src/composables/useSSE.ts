import { ref, onMounted, onUnmounted } from 'vue'
import type { SSEEvent } from '@/types'

// Module-level singleton: one EventSource shared across all component instances.
// This avoids N parallel SSE connections when multiple views mount simultaneously.
const isConnected = ref(false)
const isReconnecting = ref(false)
const reconnectCountdown = ref(0)
const isProcessing = ref(false)
const queueDepth = ref(0)
const lastEvent = ref<SSEEvent | null>(null)

let eventSource: EventSource | null = null
let reconnectTimeout: number | null = null
let countdownInterval: number | null = null
// Tracks how many components are currently subscribed so we know when to
// tear down the connection (on last unmount) and re-establish it (on first mount).
let subscriberCount = 0
let reconnectAttempt = 0

// Exponential backoff: 1s base, doubles each failure, caps at 30s, plus 20%
// random jitter to prevent thundering-herd after a server restart.
const BACKOFF_MIN_MS = 1000
const BACKOFF_MAX_MS = 30_000
const BACKOFF_FACTOR = 2
const JITTER_RATIO = 0.2

function computeBackoffDelay(): number {
  const base = Math.min(BACKOFF_MIN_MS * Math.pow(BACKOFF_FACTOR, reconnectAttempt), BACKOFF_MAX_MS)
  const jitter = base * JITTER_RATIO * Math.random()
  return Math.floor(base + jitter)
}

// Ticks the visible countdown displayed in reconnect UI.
function startCountdown(delayMs: number): void {
  reconnectCountdown.value = Math.ceil(delayMs / 1000)
  if (countdownInterval !== null) clearInterval(countdownInterval)
  countdownInterval = window.setInterval(() => {
    reconnectCountdown.value = Math.max(0, reconnectCountdown.value - 1)
    if (reconnectCountdown.value === 0 && countdownInterval !== null) {
      clearInterval(countdownInterval)
      countdownInterval = null
    }
  }, 1000)
}

function stopCountdown(): void {
  if (countdownInterval !== null) {
    clearInterval(countdownInterval)
    countdownInterval = null
  }
  reconnectCountdown.value = 0
}

function connect(): void {
  // Guard: don't stack connections if one is already open or pending.
  if (eventSource !== null) return

  eventSource = new EventSource('/api/events')

  eventSource.onopen = () => {
    isConnected.value = true
    isReconnecting.value = false
    // A successful open resets the backoff counter so the next failure
    // starts fresh from the minimum delay rather than the last high value.
    reconnectAttempt = 0
    stopCountdown()
    console.log('[SSE] Connected')
  }

  eventSource.onmessage = (event) => {
    try {
      const data: SSEEvent = JSON.parse(event.data)
      // processing_status fires at high frequency; skip the noisy log line
      // for it so the console stays readable during heavy activity.
      if (data.type !== 'processing_status') {
        console.log('[SSE] Event:', data.type, data)
      }
      lastEvent.value = data
      if (data.type === 'processing_status') {
        isProcessing.value = data.isProcessing ?? false
        queueDepth.value = data.queueDepth ?? 0
      }
    } catch (err) {
      console.error('[SSE] Parse error:', err)
    }
  }

  eventSource.onerror = () => {
    // Tear down the broken source before scheduling a reconnect so the
    // browser doesn't hold a zombie connection open in the background.
    isConnected.value = false
    isReconnecting.value = true
    eventSource?.close()
    eventSource = null

    const delay = computeBackoffDelay()
    reconnectAttempt++
    console.log(`[SSE] Reconnecting in ${Math.round(delay / 1000)}s (attempt ${reconnectAttempt})`)
    startCountdown(delay)
    reconnectTimeout = window.setTimeout(connect, delay)
  }
}

function disconnect(): void {
  if (reconnectTimeout !== null) {
    clearTimeout(reconnectTimeout)
    reconnectTimeout = null
  }
  stopCountdown()
  if (eventSource !== null) {
    eventSource.close()
    eventSource = null
  }
  isConnected.value = false
  isReconnecting.value = false
}

// Page lifecycle handlers: disconnect on hide/unload, reconnect when a
// bfcache-restored page becomes visible again.
function handleBeforeUnload(): void {
  disconnect()
}

function handlePageHide(event: PageTransitionEvent): void {
  // persisted=true means the page is entering bfcache, not being destroyed;
  // disconnect so the server-side SSE slot is freed while we're suspended.
  if (event.persisted) disconnect()
}

function handlePageShow(event: PageTransitionEvent): void {
  // persisted=true means we were restored from bfcache; reconnect.
  if (event.persisted && eventSource === null) connect()
}

export function useSSE() {
  onMounted(() => {
    subscriberCount++
    if (subscriberCount === 1) {
      // First subscriber: register page lifecycle listeners and open the connection.
      window.addEventListener('beforeunload', handleBeforeUnload)
      window.addEventListener('pagehide', handlePageHide)
      window.addEventListener('pageshow', handlePageShow)
      connect()
    }
  })

  onUnmounted(() => {
    subscriberCount--
    if (subscriberCount === 0) {
      // Last subscriber gone: clean up listeners and close the connection.
      window.removeEventListener('beforeunload', handleBeforeUnload)
      window.removeEventListener('pagehide', handlePageHide)
      window.removeEventListener('pageshow', handlePageShow)
      disconnect()
    }
  })

  return {
    isConnected,
    isReconnecting,
    reconnectCountdown,
    isProcessing,
    queueDepth,
    lastEvent,
    // Exposed as `reconnect` so callers can force a retry (e.g. a "Retry" button).
    reconnect: connect,
  }
}
