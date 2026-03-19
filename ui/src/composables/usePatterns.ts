import { ref, onMounted, onUnmounted } from 'vue'
import type { Pattern, PatternInsight } from '@/utils/api'
import { fetchPatterns, fetchPatternInsight, deprecatePattern, deletePattern } from '@/utils/api'

export function usePatterns() {
  const patterns = ref<Pattern[]>([])
  const loading = ref(false)
  const error = ref<string | null>(null)

  // Track loaded insights by pattern id
  const insights = ref<Record<number, PatternInsight>>({})
  const insightLoading = ref<Record<number, boolean>>({})

  let abortController: AbortController | null = null

  async function loadPatterns() {
    abortController?.abort()
    abortController = new AbortController()

    loading.value = true
    error.value = null

    try {
      patterns.value = await fetchPatterns(abortController.signal) || []
    } catch (err) {
      if (err instanceof Error && err.name === 'AbortError') return
      error.value = err instanceof Error ? err.message : 'Failed to load patterns'
    } finally {
      loading.value = false
    }
  }

  async function loadInsight(id: number) {
    if (insights.value[id]) return // Already loaded

    insightLoading.value = { ...insightLoading.value, [id]: true }
    try {
      const insight = await fetchPatternInsight(id)
      insights.value = { ...insights.value, [id]: insight }
    } catch (err) {
      error.value = err instanceof Error ? err.message : 'Failed to load insight'
    } finally {
      insightLoading.value = { ...insightLoading.value, [id]: false }
    }
  }

  async function deprecate(id: number) {
    try {
      await deprecatePattern(id)
      patterns.value = patterns.value.filter(p => p.id !== id)
    } catch (err) {
      error.value = err instanceof Error ? err.message : 'Failed to deprecate pattern'
      throw err
    }
  }

  async function remove(id: number) {
    try {
      await deletePattern(id)
      patterns.value = patterns.value.filter(p => p.id !== id)
      const { [id]: _, ...rest } = insights.value
      insights.value = rest
    } catch (err) {
      error.value = err instanceof Error ? err.message : 'Failed to delete pattern'
      throw err
    }
  }

  onMounted(() => {
    loadPatterns()
  })

  onUnmounted(() => {
    abortController?.abort()
  })

  return {
    patterns,
    loading,
    error,
    insights,
    insightLoading,
    loadPatterns,
    loadInsight,
    deprecate,
    remove,
  }
}
