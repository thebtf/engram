import { watch } from 'vue'

export type OperatorDensity = 'comfortable' | 'compact'

export function useOperatorPreferences() {
  const density = useState<OperatorDensity>('operator-density', () => 'comfortable')
  const initialized = useState<boolean>('operator-preferences-init', () => false)

  if (import.meta.client && !initialized.value) {
    const storedDensity = localStorage.getItem('engram.operator.density')
    if (storedDensity === 'comfortable' || storedDensity === 'compact') {
      density.value = storedDensity
    }
    initialized.value = true
  }

  if (import.meta.client) {
    watch(density, (nextDensity) => {
      localStorage.setItem('engram.operator.density', nextDensity)
      document.documentElement.dataset.operatorDensity = nextDensity
    }, { immediate: true })
  }

  function setDensity(nextDensity: OperatorDensity) {
    density.value = nextDensity
  }

  return {
    density,
    setDensity,
  }
}
