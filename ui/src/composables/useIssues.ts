import { ref, watch } from 'vue'
import { fetchIssues, type Issue } from '@/utils/api'

export function useIssues() {
  const issues = ref<Issue[]>([])
  const total = ref(0)
  const loading = ref(false)
  const error = ref<string | null>(null)
  const statusFilter = ref('open,acknowledged,resolved,reopened')
  const projectFilter = ref('')
  const typeFilter = ref('')

  let abortController: AbortController | null = null

  async function load() {
    if (abortController) {
      abortController.abort()
    }
    abortController = new AbortController()

    loading.value = true
    error.value = null

    try {
      const result = await fetchIssues(
        projectFilter.value || undefined,
        statusFilter.value || undefined,
        50,
        0,
        abortController.signal,
        typeFilter.value || undefined
      )
      issues.value = result.issues || []
      total.value = result.total || 0
    } catch (err: any) {
      if (err.name !== 'AbortError') {
        error.value = err.message || 'Failed to load issues'
        issues.value = []
        total.value = 0
      }
    } finally {
      loading.value = false
    }
  }

  // Auto-reload when filters change
  watch([statusFilter, projectFilter, typeFilter], () => {
    load()
  })

  return {
    issues,
    total,
    loading,
    error,
    statusFilter,
    projectFilter,
    typeFilter,
    load,
  }
}
