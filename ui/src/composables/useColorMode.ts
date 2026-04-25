import { ref, watch, onMounted } from 'vue'

type ColorMode = 'light' | 'dark'

const mode = ref<ColorMode>('light')
const isInitialized = ref(false)

function applyMode(value: ColorMode) {
  if (value === 'dark') {
    document.documentElement.classList.add('dark')
  } else {
    document.documentElement.classList.remove('dark')
  }
}

export function useColorMode() {
  onMounted(() => {
    if (!isInitialized.value) {
      const stored = localStorage.getItem('theme') as ColorMode | null
      if (stored) {
        mode.value = stored
      } else if (window.matchMedia('(prefers-color-scheme: dark)').matches) {
        mode.value = 'dark'
      }
      applyMode(mode.value)
      isInitialized.value = true
    }
  })

  watch(mode, (newMode) => {
    localStorage.setItem('theme', newMode)
    applyMode(newMode)
  })

  function toggleColorMode() {
    mode.value = mode.value === 'light' ? 'dark' : 'light'
  }

  return {
    mode,
    toggleColorMode,
  }
}
