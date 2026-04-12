import { ref, onUnmounted } from 'vue'
import type { Observation } from '@/types'
import {
  fetchObservationById,
  updateObservation,
  archiveObservations,
  submitObservationFeedback,
} from '@/utils/api'

export function useObservation(observationId?: number) {
  const observation = ref<Observation | null>(null)
  const loading = ref(false)
  const saving = ref(false)
  const error = ref<string | null>(null)

  let abortController: AbortController | null = null

  async function load(id?: number) {
    const targetId = id ?? observationId
    if (!targetId) return

    abortController?.abort()
    abortController = new AbortController()

    loading.value = true
    error.value = null

    try {
      observation.value = await fetchObservationById(targetId, abortController.signal)
    } catch (err) {
      if (err instanceof Error && err.name === 'AbortError') return
      error.value = err instanceof Error ? err.message : 'Failed to load observation'
      console.error('[useObservation] Load error:', err)
    } finally {
      loading.value = false
    }
  }

  async function save(updates: {
    title?: string
    subtitle?: string
    narrative?: string
    scope?: string
    facts?: string[]
    concepts?: string[]
  }) {
    const id = observation.value?.id
    if (!id) return

    saving.value = true
    error.value = null

    try {
      const result = await updateObservation(id, updates)
      observation.value = result.observation
      return result.observation
    } catch (err) {
      error.value = err instanceof Error ? err.message : 'Failed to save observation'
      console.error('[useObservation] Save error:', err)
      throw err
    } finally {
      saving.value = false
    }
  }

  async function archive(reason?: string) {
    const id = observation.value?.id
    if (!id) return

    saving.value = true
    error.value = null

    try {
      const result = await archiveObservations([id], reason)
      if (result.failed?.length > 0) {
        throw new Error(`Failed to archive observation ${id}`)
      }
      return true
    } catch (err) {
      error.value = err instanceof Error ? err.message : 'Failed to archive observation'
      console.error('[useObservation] Archive error:', err)
      throw err
    } finally {
      saving.value = false
    }
  }

  async function feedback(value: number) {
    const id = observation.value?.id
    if (!id) return

    try {
      const result = await submitObservationFeedback(id, value)
      if (observation.value && result.score !== undefined) {
        observation.value = {
          ...observation.value,
          user_feedback: value,
          importance_score: result.score,
        }
      }
      return result
    } catch (err) {
      console.error('[useObservation] Feedback error:', err)
      throw err
    }
  }

  onUnmounted(() => {
    abortController?.abort()
  })

  return {
    observation,
    loading,
    saving,
    error,
    load,
    save,
    archive,
    feedback,
  }
}
