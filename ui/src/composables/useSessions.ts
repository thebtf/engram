import { ref, onMounted, onUnmounted } from 'vue'
import type { IndexedSession } from '@/utils/api'
import { fetchSDKSessions, searchIndexedSessions, fetchProjects } from '@/utils/api'

function resolveSessionError(err: unknown, fallback: string): string {
  if (!(err instanceof Error)) return fallback
  if (err.message.startsWith('HTTP 503')) {
    return 'Session indexing is not configured on the server. Sessions will appear once transcript indexing is enabled.'
  }
  return err.message || fallback
}

export function useSessions() {
  const sessions = ref<IndexedSession[]>([])
  const totalSessions = ref(0)
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
      const fromEpoch = filterFrom.value ? new Date(filterFrom.value).getTime() : undefined
      const toEpoch = filterTo.value ? new Date(filterTo.value + 'T23:59:59').getTime() : undefined
      const result = await fetchSDKSessions(
        {
          project: filterProject.value || undefined,
          limit: 50,
          offset: 0,
          min_prompts: 1,
          from: fromEpoch,
          to: toEpoch,
        },
        abortController.signal,
      )
      totalSessions.value = result.total || 0
      sessions.value = (result.sessions || []).map(s => ({
        id: s.claude_session_id,
        workstation: '',
        project: s.project,
        date: s.started_at ? s.started_at.slice(0, 10) : '',
        message_count: s.prompt_counter,
        created_at: s.started_at,
        // Extra SDK fields exposed as-is for SessionsView compatibility
        status: s.status,
        user_prompt: s.user_prompt,
        completed_at: s.completed_at,
      } as any))
    } catch (err) {
      if (err instanceof Error && err.name === 'AbortError') return
      error.value = resolveSessionError(err, 'Failed to load sessions')
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
      error.value = resolveSessionError(err, 'Failed to search sessions')
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
    totalSessions,
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
