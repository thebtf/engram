import { ref, onMounted, onUnmounted } from 'vue'
import type { ApiToken, CreateTokenResponse } from '@/utils/api'
import { fetchTokens, createToken, revokeToken } from '@/utils/api'

export function useTokens() {
  const tokens = ref<ApiToken[]>([])
  const loading = ref(false)
  const error = ref<string | null>(null)

  let abortController: AbortController | null = null

  async function loadTokens() {
    abortController?.abort()
    abortController = new AbortController()

    loading.value = true
    error.value = null

    try {
      tokens.value = await fetchTokens(abortController.signal) || []
    } catch (err) {
      if (err instanceof Error && err.name === 'AbortError') return
      error.value = err instanceof Error ? err.message : 'Failed to load tokens'
    } finally {
      loading.value = false
    }
  }

  async function create(name: string, scope: string): Promise<CreateTokenResponse> {
    error.value = null
    try {
      const result = await createToken({ name, scope })
      await loadTokens()
      return result
    } catch (err) {
      error.value = err instanceof Error ? err.message : 'Failed to create token'
      throw err
    }
  }

  async function revoke(name: string) {
    error.value = null
    try {
      await revokeToken(name)
      tokens.value = tokens.value.filter(t => t.name !== name)
    } catch (err) {
      error.value = err instanceof Error ? err.message : 'Failed to revoke token'
      throw err
    }
  }

  onMounted(() => {
    loadTokens()
  })

  onUnmounted(() => {
    abortController?.abort()
  })

  return {
    tokens,
    loading,
    error,
    loadTokens,
    create,
    revoke,
  }
}
