<script setup lang="ts">
import { computed, ref } from 'vue'
import { useMutation, useQueryClient } from '@tanstack/vue-query'
import { deleteCredential, fetchCredential } from '~/composables/useOperatorApi'
import { useVaultOverviewQuery } from '~/composables/useOperatorQueries'
import { formatAbsoluteDate } from '~/utils/formatters'

const queryClient = useQueryClient()
const vaultQuery = useVaultOverviewQuery()

const revealedValues = ref<Record<string, { value: string; expiresAt: number }>>({})
const actionError = ref('')
const copyFeedback = ref('')

const credentials = computed(() => vaultQuery.data.value?.credentials ?? [])
const vaultStatus = computed(() => vaultQuery.data.value?.status ?? null)

async function revealValue(name: string, project?: string) {
  actionError.value = ''

  try {
    const result = await fetchCredential(name, project)
    revealedValues.value = {
      ...revealedValues.value,
      [name]: {
        value: result.value,
        expiresAt: Date.now() + 30_000,
      },
    }

    setTimeout(() => {
      const current = revealedValues.value[name]
      if (current && current.value === result.value) {
        const next = { ...revealedValues.value }
        delete next[name]
        revealedValues.value = next
      }
    }, 30_000)
  } catch (error) {
    actionError.value = error instanceof Error ? error.message : 'Не удалось раскрыть credential'
  }
}

async function copyValue(name: string) {
  const value = revealedValues.value[name]?.value
  if (!value) return

  await navigator.clipboard.writeText(value)
  copyFeedback.value = name
  setTimeout(() => {
    if (copyFeedback.value === name) {
      copyFeedback.value = ''
    }
  }, 2_000)
}

const deleteCredentialMutation = useMutation({
  mutationFn: ({ name, project }: { name: string; project?: string }) =>
    deleteCredential(name, project),
  onSuccess: async () => {
    await queryClient.invalidateQueries({ queryKey: ['vault-overview'] })
  },
  onError: (error) => {
    actionError.value = error instanceof Error ? error.message : 'Не удалось удалить credential'
  },
})
</script>

<template>
  <section class="space-y-5">
    <div class="surface-panel p-5">
      <div class="mb-4 flex flex-wrap items-center justify-between gap-3">
        <div>
          <h1 class="text-xl font-semibold">Vault</h1>
          <p class="mt-2 text-sm text-[var(--fg-2)]">
            Live-backed vault slice в новом shell: status, credential list, one-shot reveal и delete.
          </p>
        </div>

        <button
          class="rounded-lg border border-[var(--border)] bg-[var(--surface-warm)] px-4 py-2 text-sm text-[var(--fg-2)]"
          @click="vaultQuery.refetch()"
        >
          Refresh
        </button>
      </div>

      <div v-if="vaultStatus" class="mb-4 grid gap-4 md:grid-cols-3">
        <div class="surface-panel-warm p-4">
          <div class="text-xs uppercase tracking-[0.08em] text-[var(--muted)]">Encryption</div>
          <div class="mt-3 text-sm font-semibold" :class="vaultStatus.encrypted ? 'text-[var(--success)]' : 'text-[var(--danger)]'">
            {{ vaultStatus.encrypted ? 'enabled' : 'disabled' }}
          </div>
        </div>
        <div class="surface-panel-warm p-4">
          <div class="text-xs uppercase tracking-[0.08em] text-[var(--muted)]">Key fingerprint</div>
          <div class="mono-data mt-3 text-sm">
            {{ vaultStatus.key_fingerprint || 'n/a' }}
          </div>
        </div>
        <div class="surface-panel-warm p-4">
          <div class="text-xs uppercase tracking-[0.08em] text-[var(--muted)]">Credentials</div>
          <div class="mono-data mt-3 text-2xl font-semibold">
            {{ vaultStatus.credential_count }}
          </div>
        </div>
      </div>

      <div v-if="actionError" class="mb-4 rounded-lg border border-[color:rgba(248,113,113,0.4)] bg-[color:rgba(248,113,113,0.12)] px-3 py-2 text-sm text-[var(--danger)]">
        {{ actionError }}
      </div>

      <div v-if="vaultQuery.isPending.value" class="text-sm text-[var(--muted)]">
        Загрузка vault...
      </div>
      <div v-else-if="vaultQuery.isError.value" class="text-sm text-[var(--danger)]">
        {{ vaultQuery.error.value?.message || 'Не удалось загрузить vault' }}
      </div>
      <div v-else-if="credentials.length === 0" class="text-sm text-[var(--muted)]">
        В vault пока нет credentials.
      </div>
      <div v-else class="space-y-3">
        <article
          v-for="credential in credentials"
          :key="credential.name"
          class="surface-panel-warm p-4"
        >
          <div class="mb-3 flex flex-wrap items-center justify-between gap-3">
            <div>
              <div class="text-sm font-semibold">{{ credential.name }}</div>
              <div class="mt-1 text-xs text-[var(--muted)]">
                {{ credential.project || 'none' }} · {{ credential.scope }}
              </div>
            </div>
            <div class="mono-data text-xs text-[var(--muted)]">
              {{ formatAbsoluteDate(credential.created_at) }}
            </div>
          </div>

          <div v-if="revealedValues[credential.name]" class="mb-3 rounded-lg border border-[color:rgba(52,211,153,0.3)] bg-[color:rgba(52,211,153,0.1)] px-3 py-2">
            <div class="flex flex-wrap items-center gap-2">
              <code class="mono-data text-xs text-[var(--success)]">
                {{ revealedValues[credential.name].value }}
              </code>
              <button
                class="rounded-md border border-[var(--border)] bg-[var(--surface)] px-2 py-1 text-xs text-[var(--fg-2)]"
                @click="copyValue(credential.name)"
              >
                {{ copyFeedback === credential.name ? 'copied' : 'copy' }}
              </button>
            </div>
            <div class="mt-2 text-xs text-[var(--muted)]">
              value auto-hides after 30 seconds
            </div>
          </div>

          <div class="flex flex-wrap gap-2">
            <button
              class="rounded-md border border-[var(--border)] bg-[var(--surface)] px-3 py-2 text-xs font-semibold text-[var(--fg-2)]"
              @click="revealValue(credential.name, credential.project)"
            >
              Reveal
            </button>
            <button
              class="rounded-md border border-[color:rgba(248,113,113,0.4)] bg-transparent px-3 py-2 text-xs font-semibold text-[var(--danger)] disabled:opacity-60"
              :disabled="deleteCredentialMutation.isPending.value"
              @click="deleteCredentialMutation.mutate({ name: credential.name, project: credential.project })"
            >
              Delete
            </button>
          </div>
        </article>
      </div>
    </div>
  </section>
</template>
