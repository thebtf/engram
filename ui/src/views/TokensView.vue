<script setup lang="ts">
import { ref, computed } from 'vue'
import { useTokens } from '@/composables/useTokens'
import { formatRelativeTime } from '@/utils/formatters'
import { copyToClipboard } from '@/utils/clipboard'
import EmptyState from '@/components/layout/EmptyState.vue'
import { Card } from '@/components/ui/card'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { RadioGroup, RadioGroupItem } from '@/components/ui/radio-group'
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
  DialogFooter,
} from '@/components/ui/dialog'
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from '@/components/ui/alert-dialog'
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@/components/ui/table'
import { Skeleton } from '@/components/ui/skeleton'
import {
  Key,
  Plus,
  RefreshCw,
  Copy,
  Check,
  Ban,
  AlertTriangle,
  Clock,
  ArrowLeftRight,
  BarChart2,
  Loader2,
} from 'lucide-vue-next'

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
  <div class="space-y-6">
    <!-- Header -->
    <div class="flex items-center justify-between">
      <div class="flex items-center gap-3">
        <Key class="text-primary size-5" />
        <h1 class="text-2xl font-bold">API Tokens</h1>
        <span v-if="tokens.length > 0" class="text-sm text-muted-foreground">({{ tokens.length }})</span>
      </div>
      <div class="flex items-center gap-2">
        <Button variant="outline" size="sm" :disabled="loading" @click="loadTokens()">
          <RefreshCw :class="['size-4', loading && 'animate-spin']" />
          Refresh
        </Button>
        <Button size="sm" @click="openCreateModal()">
          <Plus class="size-4" />
          Create Token
        </Button>
      </div>
    </div>

    <!-- Loading skeleton -->
    <div v-if="loading && tokens.length === 0" class="space-y-2">
      <Skeleton class="h-16 w-full rounded-lg" />
      <Skeleton class="h-16 w-full rounded-lg" />
      <Skeleton class="h-16 w-full rounded-lg" />
    </div>

    <!-- Error -->
    <div v-else-if="error" class="flex flex-col items-center justify-center py-16 gap-3">
      <AlertTriangle class="size-8 text-destructive" />
      <p class="text-destructive text-sm">{{ error }}</p>
      <Button variant="ghost" size="sm" @click="loadTokens()">Try again</Button>
    </div>

    <!-- Empty State -->
    <EmptyState
      v-else-if="tokens.length === 0 && !loading"
      icon="fa-key"
      title="No API tokens"
      description="Create a token to authenticate API requests."
    />

    <!-- Tokens Table -->
    <Card v-else>
      <Table>
        <TableHeader>
          <TableRow>
            <TableHead>Name</TableHead>
            <TableHead>Prefix</TableHead>
            <TableHead>Scope</TableHead>
            <TableHead>Status</TableHead>
            <TableHead>Activity</TableHead>
            <TableHead>Stats</TableHead>
            <TableHead class="text-right">Actions</TableHead>
          </TableRow>
        </TableHeader>
        <TableBody>
          <TableRow
            v-for="token in sortedTokens"
            :key="token.id"
            :class="token.revoked ? 'opacity-50' : ''"
          >
            <TableCell class="font-medium">{{ token.name }}</TableCell>
            <TableCell>
              <code class="px-1.5 py-0.5 text-[10px] font-mono rounded bg-muted border text-muted-foreground">
                {{ token.token_prefix }}...
              </code>
            </TableCell>
            <TableCell>
              <Badge :variant="token.scope === 'read-write' ? 'default' : 'secondary'" class="text-[10px]">
                {{ token.scope }}
              </Badge>
            </TableCell>
            <TableCell>
              <Badge v-if="token.revoked" variant="destructive" class="text-[10px]">Revoked</Badge>
              <Badge v-else variant="outline" class="text-[10px] text-green-600 border-green-600/40">Active</Badge>
            </TableCell>
            <TableCell>
              <div class="space-y-0.5 text-xs text-muted-foreground">
                <div class="flex items-center gap-1">
                  <ArrowLeftRight class="size-3 text-muted-foreground/60" />
                  {{ token.request_count }} requests
                </div>
                <div v-if="token.error_count" class="flex items-center gap-1 text-destructive/70">
                  <AlertTriangle class="size-3" />
                  {{ token.error_count }} errors
                </div>
                <div v-if="token.revoked_at" class="flex items-center gap-1">
                  <Ban class="size-3 text-destructive/60" />
                  Revoked {{ formatRelativeTime(token.revoked_at) }}
                </div>
                <div v-else-if="token.last_used_at" class="flex items-center gap-1">
                  <Clock class="size-3 text-muted-foreground/60" />
                  Last used {{ formatRelativeTime(token.last_used_at) }}
                </div>
              </div>
            </TableCell>
            <TableCell>
              <!-- Per-token stats (lazy loaded) -->
              <button
                v-if="tokenStats[token.id] === undefined && !statsLoading[token.id]"
                class="flex items-center gap-1 text-[10px] text-muted-foreground/60 hover:text-muted-foreground transition-colors"
                @click="loadTokenStats(token.id)"
              >
                <BarChart2 class="size-3" />
                Load stats
              </button>
              <span v-else-if="statsLoading[token.id]" class="flex items-center gap-1 text-[10px] text-muted-foreground/60">
                <Loader2 class="size-3 animate-spin" />
                Loading...
              </span>
              <div v-else-if="tokenStats[token.id]" class="text-[10px] text-muted-foreground">
                <div class="flex items-center gap-1">
                  <BarChart2 class="size-3 text-muted-foreground/60" />
                  {{ tokenStats[token.id].request_count }} requests
                </div>
                <div v-if="tokenStats[token.id].last_used_at">
                  Last: {{ formatRelativeTime(tokenStats[token.id].last_used_at!) }}
                </div>
                <div v-else>Never used</div>
              </div>
            </TableCell>
            <TableCell class="text-right">
              <Button
                v-if="!token.revoked"
                variant="outline"
                size="xs"
                class="text-destructive border-destructive/30 hover:bg-destructive/10 hover:text-destructive"
                @click="confirmRevoke(token.id)"
              >
                <Ban class="size-3.5" />
                Revoke
              </Button>
            </TableCell>
          </TableRow>
        </TableBody>
      </Table>
    </Card>

    <!-- Create Token Dialog -->
    <Dialog :open="showCreateModal" @update:open="(v) => { if (!v) closeCreateModal() }">
      <DialogContent class="max-w-md">
        <DialogHeader>
          <DialogTitle>{{ createdToken ? 'Token Created' : 'Create API Token' }}</DialogTitle>
        </DialogHeader>

        <!-- One-time token reveal -->
        <template v-if="createdToken">
          <Card class="border-amber-500/30 bg-amber-500/5">
            <div class="p-4 space-y-3">
              <p class="text-xs text-amber-500 flex items-center gap-1.5">
                <AlertTriangle class="size-3.5 shrink-0" />
                Copy this token now. It will not be shown again.
              </p>
              <div class="flex items-center gap-2">
                <code class="flex-1 px-2 py-1.5 rounded bg-muted border text-xs text-green-500 font-mono break-all select-all">
                  {{ createdToken }}
                </code>
                <Button variant="outline" size="icon-sm" @click="copyToken">
                  <Check v-if="copyFeedback" class="size-4 text-green-500" />
                  <Copy v-else class="size-4" />
                </Button>
              </div>
            </div>
          </Card>
          <DialogFooter>
            <Button @click="closeCreateModal">Done</Button>
          </DialogFooter>
        </template>

        <!-- Create form -->
        <template v-else>
          <div class="space-y-4 py-2">
            <div class="space-y-1.5">
              <Label for="token-name">Token Name</Label>
              <Input
                id="token-name"
                v-model="newTokenName"
                placeholder="e.g., my-workstation"
                @keydown.enter="handleCreate"
              />
            </div>
            <div class="space-y-2">
              <Label>Scope</Label>
              <RadioGroup v-model="newTokenScope" class="flex gap-6">
                <div class="flex items-center gap-2">
                  <RadioGroupItem id="scope-rw" value="read-write" />
                  <Label for="scope-rw" class="font-normal cursor-pointer">Read-Write</Label>
                </div>
                <div class="flex items-center gap-2">
                  <RadioGroupItem id="scope-ro" value="read-only" />
                  <Label for="scope-ro" class="font-normal cursor-pointer">Read-Only</Label>
                </div>
              </RadioGroup>
            </div>
            <div v-if="createError" class="rounded-lg border border-destructive/30 bg-destructive/10 px-3 py-2 text-xs text-destructive">
              {{ createError }}
            </div>
          </div>
          <DialogFooter>
            <Button variant="outline" @click="closeCreateModal">Cancel</Button>
            <Button :disabled="creating || !newTokenName.trim()" @click="handleCreate">
              <Loader2 v-if="creating" class="size-4 animate-spin" />
              Create
            </Button>
          </DialogFooter>
        </template>
      </DialogContent>
    </Dialog>

    <!-- Revoke Confirmation AlertDialog -->
    <AlertDialog :open="showRevokeConfirm" @update:open="showRevokeConfirm = $event">
      <AlertDialogContent>
        <AlertDialogHeader>
          <AlertDialogTitle>Revoke Token</AlertDialogTitle>
          <AlertDialogDescription>
            Are you sure you want to revoke <strong>{{ revokeTarget }}</strong>? Any clients using this token will lose access.
          </AlertDialogDescription>
        </AlertDialogHeader>
        <AlertDialogFooter>
          <AlertDialogCancel @click="showRevokeConfirm = false">Cancel</AlertDialogCancel>
          <AlertDialogAction
            class="bg-destructive text-destructive-foreground hover:bg-destructive/90"
            @click="handleRevoke"
          >
            Revoke
          </AlertDialogAction>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  </div>
</template>
