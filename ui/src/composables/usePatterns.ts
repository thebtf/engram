import { ref, onMounted, onUnmounted } from 'vue'
import type { Pattern, PatternInsightResult } from '@/utils/api'
import { fetchPatterns, generatePatternInsight, deprecatePattern, deletePattern } from '@/utils/api'

export function usePatterns() {
  const patterns = ref<Pattern[]>([])
  const total = ref(0)
  const loading = ref(false)
  const error = ref<string | null>(null)

  // Track loaded insights by pattern id — now uses the richer PatternInsightResult
  const insights = ref<Record<number, PatternInsightResult>>({})
  const insightLoading = ref<Record<number, boolean>>({})

  // Legacy PatternInsight removed — now uses PatternInsightResult exclusively

  let abortController: AbortController | null = null

  async function loadPatterns(params?: { limit?: number; offset?: number; sort?: string }) {
    abortController?.abort()
    abortController = new AbortController()

    loading.value = true
    error.value = null

    try {
      const response = await fetchPatterns(params, abortController.signal)
      patterns.value = response.patterns || []
      total.value = response.total ?? 0
    } catch (err) {
      if (err instanceof Error && err.name === 'AbortError') return
      error.value = err instanceof Error ? err.message : 'Failed to load patterns'
    } finally {
      loading.value = false
    }
  }

  // loadInsight: triggers LLM generation via POST, caches result
  async function loadInsight(id: number) {
    if (insights.value[id]) return // Already loaded

    insightLoading.value = { ...insightLoading.value, [id]: true }
    try {
      const result = await generatePatternInsight(id)
      insights.value = { ...insights.value, [id]: result }
    } catch (err) {
      error.value = err instanceof Error ? err.message : 'Failed to load insight'
    } finally {
      insightLoading.value = { ...insightLoading.value, [id]: false }
    }
  }

  // refreshInsight: forces re-generation even if already cached in frontend
  async function refreshInsight(id: number) {
    insightLoading.value = { ...insightLoading.value, [id]: true }
    try {
      const result = await generatePatternInsight(id)
      insights.value = { ...insights.value, [id]: result }
    } catch (err) {
      error.value = err instanceof Error ? err.message : 'Failed to refresh insight'
    } finally {
      insightLoading.value = { ...insightLoading.value, [id]: false }
    }
  }

  async function deprecate(id: number) {
    try {
      await deprecatePattern(id)
      patterns.value = patterns.value.filter(p => p.id !== id)
      total.value = Math.max(0, total.value - 1)
    } catch (err) {
      error.value = err instanceof Error ? err.message : 'Failed to deprecate pattern'
      throw err
    }
  }

  async function remove(id: number) {
    try {
      await deletePattern(id)
      patterns.value = patterns.value.filter(p => p.id !== id)
      total.value = Math.max(0, total.value - 1)
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
    total,
    loading,
    error,
    insights,
    insightLoading,
    loadPatterns,
    loadInsight,
    refreshInsight,
    deprecate,
    remove,
  }
}
