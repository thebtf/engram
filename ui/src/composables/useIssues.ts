import { ref, watch } from 'vue'
import { useUiI18n } from '@/composables/useUiI18n'
import { fetchIssues, type Issue } from '@/utils/api'
import { useProjectCatalog } from './useProjectCatalog'

export function useIssues() {
  const { t } = useUiI18n()
  const issues = ref<Issue[]>([])
  const total = ref(0)
  const loading = ref(false)
  const error = ref<string | null>(null)
  const statusFilter = ref('open,acknowledged,resolved,reopened')
  const catalog = useProjectCatalog('all')
  const projectFilter = catalog.selectedProject
  const sourceProjectFilter = ref('')
  const sourceAgentFilter = ref('')
  const typeFilter = ref('')
  /** Map from project ID to human-readable display name, populated by last fetch. */
  const projectNames = ref<Record<string, string>>({})

  let abortController: AbortController | null = null

  async function load() {
    abortController?.abort()
    const project = projectFilter.value
    if (catalog.selectionPending.value || catalog.selectionError.value || !project || (sourceAgentFilter.value === 'human' && !catalog.projects.value.some(row => row.project_id === project))) { issues.value = []; total.value = 0; loading.value = false; return }
    abortController = new AbortController()
    const signal = abortController.signal

    loading.value = true
    error.value = null

    try {
      const result = await fetchIssues(
        projectFilter.value === 'all' || sourceAgentFilter.value === 'human' ? undefined : projectFilter.value,
        statusFilter.value || undefined,
        50,
        0,
        signal,
        typeFilter.value || undefined,
        sourceAgentFilter.value === 'human' ? project : sourceProjectFilter.value || undefined,
        sourceAgentFilter.value || undefined
      )
      if (catalog.selectionPending.value || projectFilter.value !== project || signal.aborted) return
      issues.value = result.issues || []
      total.value = result.total || 0
      projectNames.value = result.project_names || {}
    } catch (err: any) {
      if (!signal.aborted && err.name !== 'AbortError') {
        error.value = err.message || t.value.issues.errors.loadFailed
        issues.value = []
        total.value = 0
      }
    } finally {
      loading.value = false
    }
  }

  // Auto-reload when filters change
  watch([statusFilter, projectFilter, sourceProjectFilter, sourceAgentFilter, typeFilter, catalog.selectionPending], () => {
    load()
  })

  return {
    ...catalog,
    issues,
    total,
    loading,
    error,
    statusFilter,
    projectFilter,
    sourceProjectFilter,
    sourceAgentFilter,
    typeFilter,
    projectNames,
    load,
  }
}
