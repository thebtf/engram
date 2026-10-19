import { ref, onMounted, onUnmounted } from 'vue'
import { useUiI18n } from '@/composables/useUiI18n'
import type { VaultCredential, VaultStatus } from '@/utils/api'
import { fetchCredentials, fetchCredential, deleteCredential, fetchVaultStatus } from '@/utils/api'

export function useVault() {
  const { t } = useUiI18n()
  const credentials = ref<VaultCredential[]>([])
  const vaultStatus = ref<VaultStatus | null>(null)
  const loading = ref(false)
  const error = ref<string | null>(null)
  const actionError = ref<string | null>(null)

  const revealedValues = ref<Record<number, { value: string; expiresAt: number }>>({})
  const revealTimers = new Map<number, ReturnType<typeof setTimeout>>()

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
      error.value = err instanceof Error ? err.message : t.value.vault.errors.loadFailed
    } finally {
      loading.value = false
    }
  }

  async function revealCredential(credential: VaultCredential) {
    actionError.value = null
    try {
      const result = await fetchCredential(credential)
      const expiresAt = Date.now() + 30000

      revealedValues.value = {
        ...revealedValues.value,
        [credential.id]: { value: result.value, expiresAt },
      }

      const existingTimer = revealTimers.get(credential.id)
      if (existingTimer) clearTimeout(existingTimer)

      const timer = setTimeout(() => {
        hideCredential(credential.id)
      }, 30000)
      revealTimers.set(credential.id, timer)
    } catch (err) {
      const msg = err instanceof Error ? err.message : ''
      const normalized = msg.toLowerCase()
      if (/\b409\b/.test(normalized) || normalized.includes('key mismatch') || normalized.includes('encryption') || normalized.includes('decrypt')) {
        actionError.value = t.value.vault.errors.decryptMismatch
      } else {
        actionError.value = msg || t.value.vault.errors.revealFailed
      }
    }
  }

  function hideCredential(id: number) {
    const { [id]: _, ...rest } = revealedValues.value
    revealedValues.value = rest
    const timer = revealTimers.get(id)
    if (timer) {
      clearTimeout(timer)
      revealTimers.delete(id)
    }
  }

  async function removeCredential(credential: VaultCredential) {
    actionError.value = null
    try {
      await deleteCredential(credential)
      hideCredential(credential.id)
      credentials.value = credentials.value.filter(c => c.id !== credential.id)
      if (vaultStatus.value) {
        vaultStatus.value = {
          ...vaultStatus.value,
          credential_count: Math.max(0, vaultStatus.value.credential_count - 1),
        }
      }
    } catch (err) {
      actionError.value = err instanceof Error ? err.message : t.value.vault.errors.deleteFailed
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
