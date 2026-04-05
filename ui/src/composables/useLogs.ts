import { ref, computed, onUnmounted } from 'vue'

export type LogLevel = 'trace' | 'debug' | 'info' | 'warn' | 'error' | 'fatal'

export interface LogEntry {
  id: number
  timestamp: string
  level: LogLevel
  message: string
}

const LOG_LEVELS: LogLevel[] = ['trace', 'debug', 'info', 'warn', 'error', 'fatal']
const MAX_BUFFER = 1000

let entryIdCounter = 0

export function useLogs() {
  const entries = ref<LogEntry[]>([])
  const connected = ref(false)
  const paused = ref(false)
  const enabledLevels = ref<Set<LogLevel>>(new Set(LOG_LEVELS))
  const searchText = ref('')

  let eventSource: EventSource | null = null
  let reconnectTimer: ReturnType<typeof setTimeout> | null = null
  let allowAutoReconnect = true
  // Buffer entries while paused
  let pauseBuffer: LogEntry[] = []

  const filteredEntries = computed(() => {
    let items = entries.value.filter(e => enabledLevels.value.has(e.level))
    if (searchText.value) {
      const lower = searchText.value.toLowerCase()
      items = items.filter(e => e.message.toLowerCase().includes(lower))
    }
    return items
  })

  function parseLogLine(line: string): LogEntry | null {
    // Try to parse JSON log format
    try {
      const parsed = JSON.parse(line)
      return {
        id: ++entryIdCounter,
        timestamp: parsed.time || parsed.timestamp || parsed.ts || new Date().toISOString(),
        level: (parsed.level || parsed.lvl || 'info').toLowerCase() as LogLevel,
        message: parsed.msg || parsed.message || line,
      }
    } catch {
      // Fall back to plain text with level detection
      const levelMatch = line.match(/\b(TRACE|DEBUG|INFO|WARN|ERROR|FATAL)\b/i)
      const tsMatch = line.match(/^(\d{4}-\d{2}-\d{2}[T ]\d{2}:\d{2}:\d{2}[^\s]*)/)
      return {
        id: ++entryIdCounter,
        timestamp: tsMatch?.[1] || new Date().toISOString(),
        level: (levelMatch?.[1]?.toLowerCase() || 'info') as LogLevel,
        message: line,
      }
    }
  }

  function addEntry(entry: LogEntry) {
    if (paused.value) {
      pauseBuffer.push(entry)
      // Cap pause buffer too
      if (pauseBuffer.length > MAX_BUFFER) {
        pauseBuffer = pauseBuffer.slice(-MAX_BUFFER)
      }
      return
    }

    const updated = [...entries.value, entry]
    entries.value = updated.length > MAX_BUFFER ? updated.slice(-MAX_BUFFER) : updated
  }

  function connect() {
    if (eventSource) return
    allowAutoReconnect = true

    eventSource = new EventSource('/api/logs?follow=true')

    eventSource.onopen = () => {
      connected.value = true
    }

    eventSource.onmessage = (event) => {
      const entry = parseLogLine(event.data)
      if (entry) {
        addEntry(entry)
      }
    }

    eventSource.onerror = () => {
      connected.value = false
      eventSource?.close()
      eventSource = null

      // Reconnect after delay, only if not explicitly disconnected/unmounted.
      if (allowAutoReconnect) {
        reconnectTimer = setTimeout(() => {
          reconnectTimer = null
          if (allowAutoReconnect && !eventSource) connect()
        }, 3000)
      }
    }
  }

  function disconnect() {
    allowAutoReconnect = false
    if (reconnectTimer !== null) {
      clearTimeout(reconnectTimer)
      reconnectTimer = null
    }
    if (eventSource) {
      eventSource.close()
      eventSource = null
    }
    connected.value = false
  }

  function togglePause() {
    if (paused.value) {
      // Resume: flush buffer
      paused.value = false
      if (pauseBuffer.length > 0) {
        const updated = [...entries.value, ...pauseBuffer]
        entries.value = updated.length > MAX_BUFFER ? updated.slice(-MAX_BUFFER) : updated
        pauseBuffer = []
      }
    } else {
      paused.value = true
    }
  }

  function toggleLevel(level: LogLevel) {
    const updated = new Set(enabledLevels.value)
    if (updated.has(level)) {
      updated.delete(level)
    } else {
      updated.add(level)
    }
    enabledLevels.value = updated
  }

  function clearEntries() {
    entries.value = []
    pauseBuffer = []
  }

  onUnmounted(() => {
    disconnect()
  })

  return {
    entries,
    filteredEntries,
    connected,
    paused,
    enabledLevels,
    searchText,
    connect,
    disconnect,
    togglePause,
    toggleLevel,
    clearEntries,
    LOG_LEVELS,
  }
}
