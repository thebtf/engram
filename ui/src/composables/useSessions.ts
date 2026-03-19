import { ref, onMounted, onUnmounted } from 'vue'
import type { IndexedSession } from '@/utils/api'
import { fetchIndexedSessions, searchIndexedSessions, fetchProjects } from '@/utils/api'

export function useSessions() {
  const sessions = ref<IndexedSession[]>([])
  const projects = ref<string[]>([])
  const loading = ref(false)
  const error = ref<string | null>(null)
  const searchQuery = ref('')
  const filterProject = ref('')
  const filterFrom = ref('')
  const filterTo = ref('')

  let abortController: AbortController | null = null

  async function loadSessions() {
    abortController?.abort()
    abortController = new AbortController()

    loading.value = true
    error.value = null

    try {
      sessions.value = await fetchIndexedSessions(
        {
          project: filterProject.value || undefined,
          from: filterFrom.value || undefined,
          to: filterTo.value || undefined,
        },
        abortController.signal,
      ) || []
    } catch (err) {
      if (err instanceof Error && err.name === 'AbortError') return
      error.value = err instanceof Error ? err.message : 'Failed to load sessions'
    } finally {
      loading.value = false
    }
  }

  async function search() {
    if (!searchQuery.value.trim()) {
      await loadSessions()
      return
    }

    abortController?.abort()
    abortController = new AbortController()

    loading.value = true
    error.value = null

    try {
      sessions.value = await searchIndexedSessions(
        searchQuery.value.trim(),
        abortController.signal,
      ) || []
    } catch (err) {
      if (err instanceof Error && err.name === 'AbortError') return
      error.value = err instanceof Error ? err.message : 'Failed to search sessions'
    } finally {
      loading.value = false
    }
  }

  async function loadProjects() {
    try {
      projects.value = await fetchProjects()
    } catch {
      // Non-critical
    }
  }

  onMounted(() => {
    loadSessions()
    loadProjects()
  })

  onUnmounted(() => {
    abortController?.abort()
  })

  return {
    sessions,
    projects,
    loading,
    error,
    searchQuery,
    filterProject,
    filterFrom,
    filterTo,
    loadSessions,
    search,
  }
}
