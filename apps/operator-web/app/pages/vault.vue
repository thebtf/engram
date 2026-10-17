<script setup lang="ts">
import { computed, ref } from 'vue'
import { useMutation, useQueryClient } from '@tanstack/vue-query'
import { deleteCredential, fetchCredential } from '~/composables/useOperatorApi'
import { useVaultOverviewQuery } from '~/composables/useOperatorQueries'
import { formatAbsoluteDate } from '~/utils/formatters'
import type { VaultCredential } from '~/composables/useOperatorApi'
import { useProjectCatalog } from '~/composables/useProjectCatalog'
import ProjectCatalogStatus from '~/components/ProjectCatalogStatus.vue'

const queryClient = useQueryClient()
const vaultQuery = useVaultOverviewQuery()
const { unresolved, selectionPending, selectionError, unresolvedReference, projectName } = useProjectCatalog('all')

const revealedValues = ref<Record<number, { value: string; expiresAt: number }>>({})
const actionError = ref('')
const copyFeedback = ref<number | null>(null)

const credentials = computed(() => vaultQuery.data.value?.credentials ?? [])
const vaultStatus = computed(() => vaultQuery.data.value?.status ?? null)

async function revealValue(credential: VaultCredential) {
  actionError.value = ''

  try {
    const result = await fetchCredential(credential)
    revealedValues.value = {
      ...revealedValues.value,
      [credential.id]: {
        value: result.value,
        expiresAt: Date.now() + 30_000,
      },
    }

    setTimeout(() => {
      const current = revealedValues.value[credential.id]
      if (current && current.value === result.value) {
        const next = { ...revealedValues.value }
        delete next[credential.id]
        revealedValues.value = next
      }
    }, 30_000)
  } catch (error) {
    actionError.value = error instanceof Error ? error.message : 'Не удалось раскрыть credential'
  }
}

async function copyValue(id: number) {
  const value = revealedValues.value[id]?.value
  if (!value) return

  await navigator.clipboard.writeText(value)
  copyFeedback.value = id
  setTimeout(() => {
    if (copyFeedback.value === id) {
      copyFeedback.value = null
    }
  }, 2_000)
}

const deleteCredentialMutation = useMutation({
  mutationFn: (credential: VaultCredential) => deleteCredential(credential),
  onSuccess: async (_, credential) => {
    const next = { ...revealedValues.value }
    delete next[credential.id]
    revealedValues.value = next
    await queryClient.invalidateQueries({ queryKey: ['vault-overview'] })
  },
  onError: (error) => {
    actionError.value = error instanceof Error ? error.message : 'Не удалось удалить credential'
  },
})
</script>

<template>
  <section class="space-y-5">
    <ProjectCatalogStatus :records="unresolved" :pending="selectionPending" :error="selectionError" :reference="unresolvedReference" />
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
          :key="credential.id"
          class="surface-panel-warm p-4"
        >
          <div class="mb-3 flex flex-wrap items-center justify-between gap-3">
            <div>
              <div class="text-sm font-semibold">{{ credential.name }}</div>
              <div class="mt-1 text-xs text-[var(--muted)]">
                {{ credential.binding_error ? `Unavailable #${credential.id}` : credential.unresolved_reference ? `Retained #${credential.id} · read-only` : (credential.project_id ?? credential.project) ? projectName(credential.project_id ?? credential.project ?? '') : 'global' }} · {{ credential.binding_error ? 'unavailable' : credential.scope }}
                <div v-if="credential.binding_error">{{ credential.binding_error === 'CREDENTIAL_BINDING_CONFLICT' ? 'Conflicting membership' : 'Membership unavailable' }}</div>
                <div v-if="credential.origin_project">Origin: {{ credential.origin_project }}</div>
              </div>
            </div>
            <div class="mono-data text-xs text-[var(--muted)]">
              {{ formatAbsoluteDate(credential.created_at) }}
            </div>
          </div>

          <div v-if="revealedValues[credential.id]" class="mb-3 rounded-lg border border-[color:rgba(52,211,153,0.3)] bg-[color:rgba(52,211,153,0.1)] px-3 py-2">
            <div class="flex flex-wrap items-center gap-2">
              <code class="mono-data text-xs text-[var(--success)]">
                {{ revealedValues[credential.id].value }}
              </code>
              <button
                class="rounded-md border border-[var(--border)] bg-[var(--surface)] px-2 py-1 text-xs text-[var(--fg-2)]"
                @click="copyValue(credential.id)"
              >
                {{ copyFeedback === credential.id ? 'copied' : 'copy' }}
              </button>
            </div>
            <div class="mt-2 text-xs text-[var(--muted)]">
              value auto-hides after 30 seconds
            </div>
          </div>

          <div class="flex flex-wrap gap-2">
            <button
              class="rounded-md border border-[var(--border)] bg-[var(--surface)] px-3 py-2 text-xs font-semibold text-[var(--fg-2)]"
              :disabled="Boolean(credential.binding_error)"
              :title="credential.binding_error ? 'Credential binding unavailable' : undefined"
              @click="revealValue(credential)"
            >
              Reveal
            </button>
            <button
              class="rounded-md border border-[color:rgba(248,113,113,0.4)] bg-transparent px-3 py-2 text-xs font-semibold text-[var(--danger)] disabled:opacity-60"
              :disabled="deleteCredentialMutation.isPending.value || Boolean(credential.binding_error || credential.unresolved_reference)"
              :title="credential.binding_error ? 'Credential binding unavailable' : credential.unresolved_reference ? 'Retained credential: read-only' : undefined"
              @click="deleteCredentialMutation.mutate(credential)"
            >
              Delete
            </button>
          </div>
        </article>
      </div>
    </div>
  </section>
</template>
