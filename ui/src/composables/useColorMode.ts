import { ref, computed, watch, onMounted } from 'vue'

type ColorPreference = 'light' | 'dark' | 'auto'
type ResolvedMode = 'light' | 'dark'

const preference = ref<ColorPreference>('auto')
const isInitialized = ref(false)
let watchStarted = false
let mediaQuery: MediaQueryList | null = null
let mediaListener: ((e: MediaQueryListEvent) => void) | null = null

function getSystemMode(): ResolvedMode {
  return window.matchMedia('(prefers-color-scheme: dark)').matches ? 'dark' : 'light'
}

function applyMode(resolved: ResolvedMode) {
  if (resolved === 'dark') {
    document.documentElement.classList.add('dark')
  } else {
    document.documentElement.classList.remove('dark')
  }
}

function resolveMode(pref: ColorPreference): ResolvedMode {
  return pref === 'auto' ? getSystemMode() : pref
}

function syncMediaListener(pref: ColorPreference) {
  // Clean up old listener
  if (mediaQuery && mediaListener) {
    mediaQuery.removeEventListener('change', mediaListener)
    mediaListener = null
  }
  // Only listen for OS changes when preference is 'auto'
  if (pref === 'auto') {
    mediaQuery = window.matchMedia('(prefers-color-scheme: dark)')
    mediaListener = () => applyMode(resolveMode('auto'))
    mediaQuery.addEventListener('change', mediaListener)
  }
}

export function useColorMode() {
  const resolvedMode = computed<ResolvedMode>(() => resolveMode(preference.value))

  onMounted(() => {
    if (!isInitialized.value) {
      const stored = localStorage.getItem('theme') as ColorPreference | null
      if (stored && ['light', 'dark', 'auto'].includes(stored)) {
        preference.value = stored
      }
      // else default 'auto'
      applyMode(resolvedMode.value)
      syncMediaListener(preference.value)
      isInitialized.value = true
    }

    if (!watchStarted) {
      watchStarted = true
      watch(preference, (newPref) => {
        localStorage.setItem('theme', newPref)
        applyMode(resolveMode(newPref))
        syncMediaListener(newPref)
      })
    }
  })

  function cycleColorMode() {
    const order: ColorPreference[] = ['light', 'dark', 'auto']
    const idx = order.indexOf(preference.value)
    preference.value = order[(idx + 1) % order.length]
  }

  return {
    preference,
    resolvedMode,
    cycleColorMode,
  }
}
