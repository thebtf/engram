import { defineNuxtPlugin } from '#app'

const RELOAD_KEY = 'engram:operator-console:chunk-error-reload'
const RELOAD_TTL_MS = 30_000
const RELOAD_QUERY_PARAM = 'engram_chunk_reload'

const CHUNK_ERROR_PATTERNS = [
  'failed to fetch dynamically imported module',
  'error loading dynamically imported module',
  'importing a module script failed',
  'failed to load module script',
  'chunkloaderror',
  'loading chunk',
]

function readReloadState() {
  try {
    return JSON.parse(sessionStorage.getItem(RELOAD_KEY) || 'null') as { count?: number; at?: number } | null
  } catch {
    return null
  }
}

function canUseSessionStorage() {
  try {
    sessionStorage.setItem(`${RELOAD_KEY}:probe`, '1')
    sessionStorage.removeItem(`${RELOAD_KEY}:probe`)
    return true
  } catch {
    return false
  }
}

function writeReloadState(state: { count: number; at: number }) {
  try {
    sessionStorage.setItem(RELOAD_KEY, JSON.stringify(state))
  } catch {
    // Storage can be disabled; URL replacement below is still safer than staying on the error page.
  }
}

function shouldReloadWithSessionStorage() {
  const now = Date.now()
  const state = readReloadState()

  if (!state?.at || now - state.at > RELOAD_TTL_MS) {
    writeReloadState({ count: 1, at: now })
    return true
  }

  if ((state.count || 0) < 1) {
    writeReloadState({ count: (state.count || 0) + 1, at: state.at })
    return true
  }

  return false
}

function replaceWithFreshURL() {
  const url = new URL(window.location.href)
  url.searchParams.set(RELOAD_QUERY_PARAM, String(Date.now()))
  window.location.replace(url.toString())
}

function reloadWithURLGuard(reason: unknown) {
  const url = new URL(window.location.href)
  if (url.searchParams.has(RELOAD_QUERY_PARAM)) {
    // Without sessionStorage we cannot bound retries by timestamp, so the URL marker is a one-shot loop guard.
    console.error('Engram operator console chunk recovery guard stopped a repeated URL replacement without storage.', reason)
    return
  }

  replaceWithFreshURL()
}

function messageFromReason(reason: unknown) {
  if (reason instanceof Error) return reason.message
  if (typeof reason === 'string') return reason
  try {
    return JSON.stringify(reason)
  } catch {
    return String(reason)
  }
}

function isChunkError(reason: unknown) {
  const message = messageFromReason(reason).toLowerCase()
  return CHUNK_ERROR_PATTERNS.some((pattern) => message.includes(pattern))
}

function isNuxtModuleScriptFailure(event: ErrorEvent) {
  return event.error == null && typeof event.filename === 'string' && /\/_nuxt\/.+\.js(?:$|\?)/.test(event.filename)
}

function reloadForChunkError(reason: unknown) {
  if (!canUseSessionStorage()) {
    reloadWithURLGuard(reason)
    return
  }

  if (!shouldReloadWithSessionStorage()) {
    console.error('Engram operator console chunk recovery guard stopped a repeated URL replacement.', reason)
    return
  }

  replaceWithFreshURL()
}

export default defineNuxtPlugin((nuxtApp) => {
  nuxtApp.hook('app:chunkError', (payload) => {
    reloadForChunkError(payload)
  })

  window.addEventListener('vite:preloadError', (event) => {
    event.preventDefault()
    reloadForChunkError(event)
  })

  window.addEventListener('unhandledrejection', (event) => {
    if (!isChunkError(event.reason)) return
    event.preventDefault()
    reloadForChunkError(event.reason)
  })

  window.addEventListener('error', (event) => {
    const reason = event.error || event.message
    const filenameOnlyChunkFailure = isNuxtModuleScriptFailure(event)
    if (!filenameOnlyChunkFailure && !isChunkError(reason)) return
    event.preventDefault()
    reloadForChunkError(reason || event.filename || event)
  }, true)
})
