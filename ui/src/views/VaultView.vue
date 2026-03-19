<script setup lang="ts">
import { ref, computed, watch, onUnmounted } from 'vue'
import { useVault } from '@/composables/useVault'
import { safeAbsoluteDate } from '@/utils/formatters'
import EmptyState from '@/components/layout/EmptyState.vue'
import ConfirmDialog from '@/components/layout/ConfirmDialog.vue'

const {
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
} = useVault()

const deleteTarget = ref<string | null>(null)
const showDeleteConfirm = ref(false)
const copyFeedback = ref<string | null>(null)

// Countdown tick for revealed values
const now = ref(Date.now())
let tickInterval: ReturnType<typeof setInterval> | null = null

function startTick() {
  if (tickInterval) return
  tickInterval = setInterval(() => {
    now.value = Date.now()
  }, 1000)
}

function stopTick() {
  if (tickInterval) {
    clearInterval(tickInterval)
    tickInterval = null
  }
}

const hasRevealed = computed(() => Object.keys(revealedValues.value).length > 0)

watch(hasRevealed, (val) => {
  if (val) startTick()
  else stopTick()
})

let isMounted = true
onUnmounted(() => {
  isMounted = false
  stopTick()
})

function remainingSeconds(name: string): number {
  const entry = revealedValues.value[name]
  if (!entry) return 0
  return Math.max(0, Math.ceil((entry.expiresAt - now.value) / 1000))
}

async function copyToClipboard(value: string, name: string) {
  try {
    await navigator.clipboard.writeText(value)
    copyFeedback.value = name
    setTimeout(() => { copyFeedback.value = null }, 2000)
  } catch {
    // Fallback ignored
  }
}

function confirmDelete(name: string) {
  deleteTarget.value = name
  showDeleteConfirm.value = true
}

async function handleDelete() {
  if (!deleteTarget.value) return
  showDeleteConfirm.value = false
  try {
    await removeCredential(deleteTarget.value)
  } catch {
    // Error handled by composable
  }
  if (isMounted) {
    deleteTarget.value = null
  }
}
</script>

<template>
  <div>
    <!-- Header -->
    <div class="flex items-center justify-between mb-6">
      <div class="flex items-center gap-3">
        <i class="fas fa-vault text-claude-400 text-xl" />
        <h1 class="text-2xl font-bold text-white">Vault</h1>
      </div>
      <button
        @click="loadCredentials()"
        :disabled="loading"
        class="px-3 py-1.5 rounded-lg text-sm bg-slate-800/50 border border-slate-700/50 text-slate-300 hover:text-white hover:border-claude-500/50 transition-colors disabled:opacity-50"
      >
        <i :class="['fas fa-sync-alt mr-1.5', loading && 'fa-spin']" />
        Refresh
      </button>
    </div>

    <!-- Vault Status Card -->
    <div v-if="vaultStatus" class="p-4 rounded-xl border-2 border-slate-700/50 bg-gradient-to-br from-slate-800/50 to-slate-900/50 mb-6">
      <div class="grid grid-cols-3 gap-4">
        <div>
          <span class="text-xs text-slate-500 block">Encryption</span>
          <span :class="vaultStatus.encrypted ? 'text-green-400' : 'text-red-400'" class="text-sm font-medium">
            <i :class="['fas mr-1', vaultStatus.encrypted ? 'fa-lock' : 'fa-lock-open']" />
            {{ vaultStatus.encrypted ? 'Enabled' : 'Disabled' }}
          </span>
          <div v-if="!vaultStatus.encrypted" class="mt-2 text-sm text-amber-400">
            <i class="fas fa-info-circle mr-1" />
            To enable encryption: <code class="bg-slate-700 px-1 rounded">openssl rand -hex 32</code>
            → set as <code class="bg-slate-700 px-1 rounded">ENGRAM_VAULT_KEY</code> env var
          </div>
        </div>
        <div>
          <span class="text-xs text-slate-500 block">Key Fingerprint</span>
          <span class="text-sm font-mono text-slate-300">{{ vaultStatus.key_fingerprint || 'N/A' }}</span>
        </div>
        <div>
          <span class="text-xs text-slate-500 block">Credentials</span>
          <span class="text-sm font-mono text-slate-300">{{ vaultStatus.credential_count }}</span>
        </div>
      </div>
    </div>

    <!-- Loading -->
    <div v-if="loading && credentials.length === 0" class="flex items-center justify-center py-20">
      <i class="fas fa-circle-notch fa-spin text-claude-400 text-2xl" />
    </div>

    <!-- Error -->
    <div v-else-if="error" class="text-center py-16">
      <i class="fas fa-exclamation-triangle text-red-400 text-3xl mb-3 block" />
      <p class="text-red-400 mb-2">{{ error }}</p>
      <button @click="loadCredentials()" class="text-sm text-slate-400 hover:text-white transition-colors">
        Try again
      </button>
    </div>

    <!-- Empty State -->
    <EmptyState
      v-else-if="credentials.length === 0 && !loading"
      icon="fa-vault"
      title="No credentials stored"
      description="Credentials will appear here when stored via MCP tools."
    />

    <!-- Credentials List -->
    <div v-else class="space-y-2">
      <!-- Inline action error (reveal/delete failures) -->
      <div v-if="actionError" class="bg-red-500/20 border border-red-500/30 rounded-lg p-3">
        <i class="fas fa-exclamation-triangle text-red-400 mr-2" />
        <span class="text-red-300">{{ actionError }}</span>
      </div>
      <div
        v-for="cred in credentials"
        :key="cred.name"
        class="p-4 rounded-xl border-2 border-slate-700/50 bg-gradient-to-br from-slate-800/50 to-slate-900/50"
      >
        <div class="flex items-center justify-between">
          <div class="flex-1 min-w-0">
            <div class="flex items-center gap-2 mb-1">
              <i class="fas fa-key text-amber-500/70 text-sm" />
              <h3 class="text-sm font-medium text-white truncate">{{ cred.name }}</h3>
              <span class="px-2 py-0.5 text-[10px] font-medium rounded-full bg-slate-700/50 text-slate-400 border border-slate-600/50">
                {{ cred.scope }}
              </span>
            </div>
            <span class="text-xs text-slate-500">Created {{ safeAbsoluteDate(cred.created_at) }}</span>
          </div>

          <div class="flex items-center gap-2 flex-shrink-0">
            <!-- Revealed value -->
            <div v-if="revealedValues[cred.name]" class="flex items-center gap-2">
              <code class="px-2 py-1 rounded bg-slate-900 border border-slate-700 text-xs text-green-400 font-mono max-w-xs truncate">
                {{ revealedValues[cred.name].value }}
              </code>
              <span class="text-[10px] text-amber-400 whitespace-nowrap">
                Hides in {{ remainingSeconds(cred.name) }}s
              </span>
              <button
                @click="copyToClipboard(revealedValues[cred.name].value, cred.name)"
                class="px-2 py-1 rounded text-xs text-slate-400 hover:text-white hover:bg-slate-700/50 transition-colors"
                :title="copyFeedback === cred.name ? 'Copied!' : 'Copy'"
              >
                <i :class="['fas', copyFeedback === cred.name ? 'fa-check text-green-400' : 'fa-copy']" />
              </button>
              <button
                @click="hideCredential(cred.name)"
                class="px-2 py-1 rounded text-xs text-slate-400 hover:text-white hover:bg-slate-700/50 transition-colors"
                title="Hide"
              >
                <i class="fas fa-eye-slash" />
              </button>
            </div>

            <!-- Action buttons (when not revealed) -->
            <template v-else>
              <button
                @click="revealCredential(cred.name)"
                class="px-3 py-1.5 rounded-lg text-xs bg-slate-800/50 border border-slate-700/50 text-slate-300 hover:text-white hover:border-claude-500/50 transition-colors"
              >
                <i class="fas fa-eye mr-1" />
                Reveal
              </button>
            </template>

            <button
              @click="confirmDelete(cred.name)"
              class="px-2 py-1.5 rounded-lg text-xs text-slate-500 hover:text-red-400 hover:bg-red-500/10 transition-colors"
              title="Delete"
            >
              <i class="fas fa-trash" />
            </button>
          </div>
        </div>
      </div>
    </div>

    <!-- Delete Confirmation -->
    <ConfirmDialog
      :show="showDeleteConfirm"
      title="Delete Credential"
      :message="`Are you sure you want to delete '${deleteTarget}'? This action cannot be undone.`"
      confirm-label="Delete"
      :danger="true"
      @confirm="handleDelete"
      @cancel="showDeleteConfirm = false"
    />
  </div>
</template>
