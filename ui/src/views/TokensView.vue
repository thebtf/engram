<script setup lang="ts">
import { ref, computed } from 'vue'
import { useTokens } from '@/composables/useTokens'
import { formatRelativeTime } from '@/utils/formatters'
import { copyToClipboard } from '@/utils/clipboard'
import EmptyState from '@/components/layout/EmptyState.vue'
import ConfirmDialog from '@/components/layout/ConfirmDialog.vue'

interface TokenStats {
  request_count: number
  last_used_at?: string
}

const { tokens, loading, error, loadTokens, create, revoke } = useTokens()

// Per-token stats: keyed by token id
const tokenStats = ref<Record<string, TokenStats>>({})
const statsLoading = ref<Record<string, boolean>>({})

async function loadTokenStats(tokenId: string) {
  if (tokenStats.value[tokenId] !== undefined || statsLoading.value[tokenId]) return
  statsLoading.value = { ...statsLoading.value, [tokenId]: true }
  try {
    const res = await fetch(`/api/auth/tokens/${encodeURIComponent(tokenId)}/stats`)
    if (res.ok) {
      const data: TokenStats = await res.json()
      tokenStats.value = { ...tokenStats.value, [tokenId]: data }
    }
  } catch {
    // Non-critical — stats are supplemental
  } finally {
    const updated = { ...statsLoading.value }
    delete updated[tokenId]
    statsLoading.value = updated
  }
}

// Create token modal
const showCreateModal = ref(false)
const newTokenName = ref('')
const newTokenScope = ref('read-write')
const creating = ref(false)
const createError = ref<string | null>(null)

// Newly created token (show once)
const createdToken = ref<string | null>(null)
const copyFeedback = ref(false)

// Sorted tokens: active first, revoked at bottom
const sortedTokens = computed(() => {
  const active = tokens.value.filter(t => !t.revoked)
  const revoked = tokens.value.filter(t => t.revoked)
  return [...active, ...revoked]
})

// Revoke confirmation
const revokeTarget = ref<string | null>(null)
const showRevokeConfirm = ref(false)

function openCreateModal() {
  newTokenName.value = ''
  newTokenScope.value = 'read-write'
  createError.value = null
  createdToken.value = null
  showCreateModal.value = true
}

async function handleCreate() {
  if (!newTokenName.value.trim()) {
    createError.value = 'Token name is required'
    return
  }
  creating.value = true
  createError.value = null
  try {
    const result = await create(newTokenName.value.trim(), newTokenScope.value)
    createdToken.value = result.token
    newTokenName.value = ''
  } catch (err) {
    createError.value = err instanceof Error ? err.message : 'Failed to create token'
  } finally {
    creating.value = false
  }
}

function closeCreateModal() {
  showCreateModal.value = false
  createdToken.value = null
}

async function copyToken() {
  if (!createdToken.value) return
  const ok = await copyToClipboard(createdToken.value)
  if (ok) {
    copyFeedback.value = true
    setTimeout(() => { copyFeedback.value = false }, 2000)
  }
}

function confirmRevoke(id: string) {
  revokeTarget.value = id
  showRevokeConfirm.value = true
}

async function handleRevoke() {
  if (!revokeTarget.value) return
  showRevokeConfirm.value = false
  try {
    await revoke(revokeTarget.value)
  } catch {
    // Error handled by composable
  }
  revokeTarget.value = null
}
</script>

<template>
  <div>
    <!-- Header -->
    <div class="flex items-center justify-between mb-6">
      <div class="flex items-center gap-3">
        <i class="fas fa-key text-claude-400 text-xl" />
        <h1 class="text-2xl font-bold text-white">API Tokens</h1>
        <span v-if="tokens.length > 0" class="text-sm text-slate-500">({{ tokens.length }})</span>
      </div>
      <div class="flex items-center gap-2">
        <button
          @click="loadTokens()"
          :disabled="loading"
          class="px-3 py-1.5 rounded-lg text-sm bg-slate-800/50 border border-slate-700/50 text-slate-300 hover:text-white hover:border-claude-500/50 transition-colors disabled:opacity-50"
        >
          <i :class="['fas fa-sync-alt mr-1.5', loading && 'fa-spin']" />
          Refresh
        </button>
        <button
          @click="openCreateModal()"
          class="px-3 py-1.5 rounded-lg text-sm bg-claude-500 text-white hover:bg-claude-400 transition-colors"
        >
          <i class="fas fa-plus mr-1.5" />
          Create Token
        </button>
      </div>
    </div>

    <!-- Loading -->
    <div v-if="loading && tokens.length === 0" class="flex items-center justify-center py-20">
      <i class="fas fa-circle-notch fa-spin text-claude-400 text-2xl" />
    </div>

    <!-- Error -->
    <div v-else-if="error" class="text-center py-16">
      <i class="fas fa-exclamation-triangle text-red-400 text-3xl mb-3 block" />
      <p class="text-red-400 mb-2">{{ error }}</p>
      <button @click="loadTokens()" class="text-sm text-slate-400 hover:text-white transition-colors">
        Try again
      </button>
    </div>

    <!-- Empty State -->
    <EmptyState
      v-else-if="tokens.length === 0 && !loading"
      icon="fa-key"
      title="No API tokens"
      description="Create a token to authenticate API requests."
    />

    <!-- Tokens List -->
    <div v-else class="space-y-2">
      <div
        v-for="token in sortedTokens"
        :key="token.id"
        :class="[
          'p-4 rounded-xl border-2 bg-gradient-to-br from-slate-800/50 to-slate-900/50',
          token.revoked ? 'border-slate-700/30 opacity-50' : 'border-slate-700/50'
        ]"
      >
        <div class="flex items-center justify-between">
          <div class="flex-1 min-w-0">
            <div class="flex items-center gap-2 mb-1">
              <h3 class="text-sm font-medium text-white">{{ token.name }}</h3>
              <code class="px-1.5 py-0.5 text-[10px] font-mono rounded bg-slate-900 border border-slate-700 text-slate-400">
                {{ token.token_prefix }}...
              </code>
              <span :class="[
                'px-2 py-0.5 text-[10px] font-medium rounded-full border',
                token.scope === 'read-write'
                  ? 'bg-amber-500/20 text-amber-300 border-amber-500/30'
                  : 'bg-blue-500/20 text-blue-300 border-blue-500/30'
              ]">
                {{ token.scope }}
              </span>
              <span v-if="token.revoked" class="px-2 py-0.5 text-[10px] font-medium rounded-full border bg-red-500/20 text-red-300 border-red-500/30">
                Revoked
              </span>
            </div>
            <div class="flex items-center gap-3 text-xs text-slate-500">
              <span>Created {{ formatRelativeTime(token.created_at) }}</span>
              <span v-if="token.revoked_at">
                <i class="fas fa-ban text-red-600 mr-0.5" />
                Revoked {{ formatRelativeTime(token.revoked_at) }}
              </span>
              <span v-else-if="token.last_used_at">
                <i class="fas fa-clock text-slate-600 mr-0.5" />
                Last used {{ formatRelativeTime(token.last_used_at) }}
              </span>
              <span>
                <i class="fas fa-arrow-right-arrow-left text-slate-600 mr-0.5" />
                {{ token.request_count }} requests
              </span>
              <span v-if="token.error_count" class="text-red-400/70">
                <i class="fas fa-exclamation-circle mr-0.5" />
                {{ token.error_count }} errors
              </span>
            </div>
            <!-- Per-token stats (lazy loaded) -->
            <div class="mt-1">
              <button
                v-if="tokenStats[token.id] === undefined && !statsLoading[token.id]"
                @click="loadTokenStats(token.id)"
                class="text-[10px] text-slate-600 hover:text-slate-400 transition-colors"
              >
                <i class="fas fa-chart-bar mr-0.5" />
                Load stats
              </button>
              <span v-else-if="statsLoading[token.id]" class="text-[10px] text-slate-600">
                <i class="fas fa-circle-notch fa-spin mr-0.5" />
                Loading stats...
              </span>
              <span v-else-if="tokenStats[token.id]" class="text-[10px] text-slate-500">
                <i class="fas fa-chart-bar mr-0.5 text-slate-600" />
                {{ tokenStats[token.id].request_count }} requests
                <span v-if="tokenStats[token.id].last_used_at">
                  · Last used: {{ formatRelativeTime(tokenStats[token.id].last_used_at!) }}
                </span>
                <span v-else> · Never used</span>
              </span>
            </div>
          </div>

          <button
            v-if="!token.revoked"
            @click="confirmRevoke(token.id)"
            class="px-3 py-1.5 rounded-lg text-xs text-slate-400 hover:text-red-400 hover:bg-red-500/10 border border-slate-700/50 hover:border-red-500/30 transition-colors flex-shrink-0"
          >
            <i class="fas fa-ban mr-1" />
            Revoke
          </button>
        </div>
      </div>
    </div>

    <!-- Create Token Modal -->
    <Teleport to="body">
      <Transition name="fade">
        <div v-if="showCreateModal" class="fixed inset-0 z-50 flex items-center justify-center p-4">
          <div class="absolute inset-0 bg-black/60 backdrop-blur-sm" @click="closeCreateModal" />
          <div class="relative glass border border-white/10 rounded-xl p-6 max-w-md w-full shadow-2xl">
            <!-- Created token display -->
            <template v-if="createdToken">
              <h3 class="text-lg font-semibold text-white mb-2">Token Created</h3>
              <div class="p-3 rounded-lg bg-amber-500/10 border border-amber-500/30 mb-4">
                <p class="text-xs text-amber-400 mb-2">
                  <i class="fas fa-triangle-exclamation mr-1" />
                  Copy this token now. It will not be shown again.
                </p>
                <div class="flex items-center gap-2">
                  <code class="flex-1 px-2 py-1.5 rounded bg-slate-900 border border-slate-700 text-xs text-green-400 font-mono break-all select-all">
                    {{ createdToken }}
                  </code>
                  <button
                    @click="copyToken"
                    class="px-3 py-1.5 rounded-lg text-sm bg-slate-800/50 border border-slate-700/50 text-slate-300 hover:text-white transition-colors flex-shrink-0"
                  >
                    <i :class="['fas', copyFeedback ? 'fa-check text-green-400' : 'fa-copy']" />
                  </button>
                </div>
              </div>
              <div class="flex justify-end">
                <button @click="closeCreateModal" class="px-4 py-2 rounded-lg text-sm bg-claude-500 text-white hover:bg-claude-400 transition-colors">
                  Done
                </button>
              </div>
            </template>

            <!-- Create form -->
            <template v-else>
              <h3 class="text-lg font-semibold text-white mb-4">Create API Token</h3>
              <div class="space-y-4">
                <div>
                  <label class="block text-xs text-slate-400 mb-1">Token Name</label>
                  <input
                    v-model="newTokenName"
                    type="text"
                    placeholder="e.g., my-workstation"
                    class="w-full px-3 py-2 rounded-lg bg-slate-900/50 border border-slate-700/50 text-sm text-white placeholder-slate-600 focus:outline-none focus:ring-2 focus:ring-claude-500/50 focus:border-claude-500"
                    @keydown.enter="handleCreate"
                  />
                </div>
                <div>
                  <label class="block text-xs text-slate-400 mb-2">Scope</label>
                  <div class="flex gap-3">
                    <label class="flex items-center gap-2 cursor-pointer">
                      <input type="radio" v-model="newTokenScope" value="read-write" class="text-claude-500 focus:ring-claude-500" />
                      <span class="text-sm text-slate-300">Read-Write</span>
                    </label>
                    <label class="flex items-center gap-2 cursor-pointer">
                      <input type="radio" v-model="newTokenScope" value="read-only" class="text-claude-500 focus:ring-claude-500" />
                      <span class="text-sm text-slate-300">Read-Only</span>
                    </label>
                  </div>
                </div>
                <div v-if="createError" class="p-2 rounded-lg bg-red-500/10 border border-red-500/30 text-xs text-red-400">
                  {{ createError }}
                </div>
              </div>
              <div class="flex items-center justify-end gap-3 mt-6">
                <button @click="closeCreateModal" class="px-4 py-2 rounded-lg text-sm text-slate-400 hover:text-white hover:bg-slate-800/50 transition-colors">
                  Cancel
                </button>
                <button
                  @click="handleCreate"
                  :disabled="creating || !newTokenName.trim()"
                  class="px-4 py-2 rounded-lg text-sm font-medium bg-claude-500 text-white hover:bg-claude-400 transition-colors disabled:opacity-50"
                >
                  <i v-if="creating" class="fas fa-circle-notch fa-spin mr-1.5" />
                  Create
                </button>
              </div>
            </template>
          </div>
        </div>
      </Transition>
    </Teleport>

    <!-- Revoke Confirmation -->
    <ConfirmDialog
      :show="showRevokeConfirm"
      title="Revoke Token"
      :message="`Are you sure you want to revoke '${revokeTarget}'? Any clients using this token will lose access.`"
      confirm-label="Revoke"
      :danger="true"
      @confirm="handleRevoke"
      @cancel="showRevokeConfirm = false"
    />
  </div>
</template>

<style scoped>
.fade-enter-active,
.fade-leave-active {
  transition: opacity 0.2s ease;
}
.fade-enter-from,
.fade-leave-to {
  opacity: 0;
}
</style>
