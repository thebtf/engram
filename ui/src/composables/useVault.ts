import { ref, onMounted, onUnmounted } from 'vue'
import type { VaultCredential, VaultStatus } from '@/utils/api'
import { fetchCredentials, fetchCredential, deleteCredential, fetchVaultStatus } from '@/utils/api'

export function useVault() {
  const credentials = ref<VaultCredential[]>([])
  const vaultStatus = ref<VaultStatus | null>(null)
  const loading = ref(false)
  const error = ref<string | null>(null)
  const actionError = ref<string | null>(null)

  // Track revealed credentials: name -> { value, expiresAt }
  const revealedValues = ref<Record<string, { value: string; expiresAt: number }>>({})
  const revealTimers = new Map<string, ReturnType<typeof setTimeout>>()

  let abortController: AbortController | null = null

  async function loadCredentials() {
    abortController?.abort()
    abortController = new AbortController()

    loading.value = true
    error.value = null

    try {
      const [creds, status] = await Promise.all([
        fetchCredentials(abortController.signal),
        fetchVaultStatus(abortController.signal),
      ])
      credentials.value = creds || []
      vaultStatus.value = status
    } catch (err) {
      if (err instanceof Error && err.name === 'AbortError') return
      error.value = err instanceof Error ? err.message : 'Failed to load vault'
    } finally {
      loading.value = false
    }
  }

  async function revealCredential(name: string) {
    actionError.value = null
    try {
      const result = await fetchCredential(name)
      const expiresAt = Date.now() + 30000

      revealedValues.value = {
        ...revealedValues.value,
        [name]: { value: result.value, expiresAt },
      }

      const existingTimer = revealTimers.get(name)
      if (existingTimer) clearTimeout(existingTimer)

      const timer = setTimeout(() => {
        hideCredential(name)
      }, 30000)
      revealTimers.set(name, timer)
    } catch (err) {
      actionError.value = err instanceof Error ? err.message : 'Failed to reveal credential'
    }
  }

  function hideCredential(name: string) {
    const { [name]: _, ...rest } = revealedValues.value
    revealedValues.value = rest
    const timer = revealTimers.get(name)
    if (timer) {
      clearTimeout(timer)
      revealTimers.delete(name)
    }
  }

  async function removeCredential(name: string) {
    actionError.value = null
    try {
      await deleteCredential(name)
      hideCredential(name)
      credentials.value = credentials.value.filter(c => c.name !== name)
      if (vaultStatus.value) {
        vaultStatus.value = {
          ...vaultStatus.value,
          credential_count: Math.max(0, vaultStatus.value.credential_count - 1),
        }
      }
    } catch (err) {
      actionError.value = err instanceof Error ? err.message : 'Failed to delete credential'
      throw err
    }
  }

  onMounted(() => {
    loadCredentials()
  })

  onUnmounted(() => {
    abortController?.abort()
    for (const timer of revealTimers.values()) {
      clearTimeout(timer)
    }
    revealTimers.clear()
  })

  return {
    credentials,
    vaultStatus,
    loading,
    error,
    actionError,
    revealedValues,
    loadCredentials,
    revealCredential,
    hideCredential,
    removeCredential,
  }
}
