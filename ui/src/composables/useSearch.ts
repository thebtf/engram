import { ref, onUnmounted } from 'vue'
import type { SearchResultObservation } from '@/types'
import { searchObservations, searchDecisions } from '@/utils/api'

export function useSearch() {
  const query = ref('')
  const project = ref('')
  const results = ref<SearchResultObservation[]>([])
  const totalCount = ref(0)
  const loading = ref(false)
  const error = ref<string | null>(null)
  const decisionMode = ref(false)
  const intent = ref('')

  let abortController: AbortController | null = null

  async function search(opts?: { query?: string; project?: string; decisionMode?: boolean }) {
    const q = opts?.query ?? query.value
    const p = opts?.project ?? project.value
    const isDecision = opts?.decisionMode ?? decisionMode.value

    if (!q.trim()) {
      results.value = []
      totalCount.value = 0
      return
    }

    // For context search, project is optional; for decisions, it is required
    if (isDecision && !p) {
      error.value = 'Project is required for decision search'
      return
    }

    abortController?.abort()
    abortController = new AbortController()

    loading.value = true
    error.value = null

    try {
      if (isDecision) {
        const response = await searchDecisions(
          { query: q, project: p, limit: 50 },
          abortController.signal
        )
        results.value = response.observations
        totalCount.value = response.total_count
        intent.value = ''
      } else {
        const response = await searchObservations(
          { query: q, project: p || 'all', limit: 50 },
          abortController.signal
        )
        results.value = response.observations
        totalCount.value = response.observations.length
        intent.value = response.intent || ''
      }
    } catch (err) {
      if (err instanceof Error && err.name === 'AbortError') return
      error.value = err instanceof Error ? err.message : 'Search failed'
      console.error('[useSearch] Error:', err)
    } finally {
      loading.value = false
    }
  }

  function clear() {
    query.value = ''
    results.value = []
    totalCount.value = 0
    error.value = null
    intent.value = ''
  }

  onUnmounted(() => {
    abortController?.abort()
  })

  return {
    query,
    project,
    results,
    totalCount,
    loading,
    error,
    decisionMode,
    intent,
    search,
    clear,
  }
}
