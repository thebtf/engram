import { ref, watch } from 'vue'

export const density = ref<'comfortable' | 'compact'>('compact')
export const startedLoads = new Set<string>()
let savedMode: string | null = null
try { savedMode = localStorage.getItem('nuxt-color-mode') } catch { }
export const theme = ref<'dark' | 'light' | 'system'>(savedMode === 'light' || savedMode === 'system' ? savedMode : 'dark')
const systemMode = window.matchMedia('(prefers-color-scheme: dark)')

function applyTheme() {
  const value = theme.value === 'system' ? systemMode.matches ? 'dark' : 'light' : theme.value
  document.documentElement.dataset.theme = value
  document.documentElement.classList.toggle('dark', value === 'dark')
  document.documentElement.classList.toggle('light', value === 'light')
}
watch(theme, () => {
  try { localStorage.setItem('nuxt-color-mode', theme.value) } catch { }
  applyTheme()
}, { immediate: true })
systemMode.addEventListener('change', applyTheme)
