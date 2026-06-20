import { computed, onMounted, ref, watch } from 'vue'

type ConsoleDensity = 'compact' | 'comfortable'

const STORAGE_KEY = 'engram:mvp-density'

const density = ref<ConsoleDensity>('compact')
const isInitialized = ref(false)
let watchStarted = false

export function useConsoleDensity() {
  onMounted(() => {
    if (!isInitialized.value) {
      const stored = localStorage.getItem(STORAGE_KEY)
      if (stored === 'compact' || stored === 'comfortable') {
        density.value = stored
      }
      isInitialized.value = true
    }

    if (!watchStarted) {
      watchStarted = true
      watch(density, value => {
        localStorage.setItem(STORAGE_KEY, value)
      })
    }
  })

  function setDensity(next: ConsoleDensity) {
    density.value = next
  }

  return {
    density: computed(() => density.value),
    setDensity,
  }
}
