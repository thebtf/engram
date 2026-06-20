<script setup lang="ts">
import { ref, computed, watch, onUnmounted } from 'vue'
import { useUiI18n } from '@/composables/useUiI18n'
import { useVault } from '@/composables/useVault'
import { safeAbsoluteDate } from '@/utils/formatters'
import { copyToClipboard } from '@/utils/clipboard'
import EmptyState from '@/components/layout/EmptyState.vue'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@/components/ui/table'
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
import { Skeleton } from '@/components/ui/skeleton'
import {
  Lock,
  LockOpen,
  Key,
  Eye,
  EyeOff,
  Copy,
  Check,
  Trash2,
  RefreshCw,
  AlertTriangle,
} from 'lucide-vue-next'

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
const { t } = useUiI18n()

const deleteTarget = ref<{ name: string; project?: string } | null>(null)
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

async function copyValue(value: string, name: string) {
  const ok = await copyToClipboard(value)
  if (ok) {
    copyFeedback.value = name
    setTimeout(() => { copyFeedback.value = null }, 2000)
  }
}

function confirmDelete(name: string, project?: string) {
  deleteTarget.value = { name, project }
  showDeleteConfirm.value = true
}

async function handleDelete() {
  if (!deleteTarget.value) return
  showDeleteConfirm.value = false
  try {
    await removeCredential(deleteTarget.value.name, deleteTarget.value.project)
  } catch {
    // Error handled by composable
  }
  if (isMounted) {
    deleteTarget.value = null
  }
}
</script>

<template>
  <div class="space-y-6 pt-4">
    <!-- Header -->
    <div class="flex items-center justify-between">
      <div class="flex items-center gap-3">
        <Key class="text-primary size-5" />
        <h1 class="text-2xl font-bold">{{ t.vault.title }}</h1>
      </div>
      <Button variant="outline" size="sm" :disabled="loading" @click="loadCredentials()">
        <RefreshCw :class="['size-4', loading && 'animate-spin']" />
        {{ t.vault.refresh }}
      </Button>
    </div>

    <!-- Vault Status Card -->
    <Card v-if="vaultStatus">
      <CardHeader class="pb-2">
        <CardTitle class="text-sm font-medium text-muted-foreground">{{ t.vault.statusTitle }}</CardTitle>
      </CardHeader>
      <CardContent>
        <div class="grid grid-cols-3 gap-6">
          <div class="space-y-1">
            <p class="text-xs text-muted-foreground">{{ t.vault.fields.encryption }}</p>
            <div class="flex items-center gap-2">
              <Badge :variant="vaultStatus.encrypted ? 'default' : 'destructive'">
                <Lock v-if="vaultStatus.encrypted" class="size-3" />
                <LockOpen v-else class="size-3" />
                {{ vaultStatus.encrypted ? t.vault.encryptionEnabled : t.vault.encryptionDisabled }}
              </Badge>
            </div>
            <p v-if="!vaultStatus.encrypted" class="text-xs text-amber-500 mt-2">
              {{ t.vault.enableHint }}
            </p>
          </div>
          <div class="space-y-1">
            <p class="text-xs text-muted-foreground">{{ t.vault.fields.keyFingerprint }}</p>
            <p class="text-sm font-mono">{{ vaultStatus.key_fingerprint || t.vault.notAvailable }}</p>
          </div>
          <div class="space-y-1">
            <p class="text-xs text-muted-foreground">{{ t.vault.fields.credentials }}</p>
            <p class="text-sm font-mono">{{ vaultStatus.credential_count }}</p>
          </div>
        </div>
      </CardContent>
    </Card>

    <!-- Loading skeleton -->
    <div v-if="loading && credentials.length === 0" class="space-y-2">
      <Skeleton class="h-12 w-full rounded-lg" />
      <Skeleton class="h-12 w-full rounded-lg" />
      <Skeleton class="h-12 w-full rounded-lg" />
    </div>

    <!-- Error -->
    <div v-else-if="error" class="flex flex-col items-center justify-center py-16 gap-3">
      <AlertTriangle class="size-8 text-destructive" />
      <p class="text-destructive text-sm">{{ error }}</p>
      <Button variant="ghost" size="sm" @click="loadCredentials()">{{ t.vault.tryAgain }}</Button>
    </div>

    <!-- Empty State -->
    <EmptyState
      v-else-if="credentials.length === 0 && !loading"
      icon="fa-vault"
      :title="t.vault.emptyTitle"
      :description="t.vault.emptyDescription"
    />

    <!-- Credentials Table -->
    <div v-else class="space-y-3">
      <!-- Inline action error -->
      <div v-if="actionError" class="flex items-start gap-2 rounded-lg border border-destructive/30 bg-destructive/10 p-3">
        <AlertTriangle class="size-4 text-destructive mt-0.5 shrink-0" />
        <span class="text-sm text-destructive">{{ actionError }}</span>
      </div>

      <Card>
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>{{ t.vault.fields.name }}</TableHead>
              <TableHead>{{ t.vault.fields.project }}</TableHead>
              <TableHead>{{ t.vault.fields.scope }}</TableHead>
              <TableHead>{{ t.vault.fields.created }}</TableHead>
              <TableHead class="text-right">{{ t.vault.fields.actions }}</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            <TableRow v-for="cred in credentials" :key="cred.name">
              <TableCell>
                <div class="flex items-center gap-2">
                  <Key class="size-3.5 text-amber-500/70 shrink-0" />
                  <span class="font-medium text-sm">{{ cred.name }}</span>
                </div>
                <!-- Revealed value inline -->
                <div v-if="revealedValues[cred.name]" class="mt-2 flex items-center gap-2">
                  <code class="px-2 py-1 rounded bg-muted border text-xs text-green-500 font-mono max-w-xs truncate">
                    {{ revealedValues[cred.name].value }}
                  </code>
                  <span class="text-[10px] text-amber-500 whitespace-nowrap">
                    {{ t.vault.hidesIn.replace('{seconds}', String(remainingSeconds(cred.name))) }}
                  </span>
                  <Button
                    variant="ghost"
                    size="icon-sm"
                    :title="copyFeedback === cred.name ? t.vault.copied : t.vault.copy"
                    @click="copyValue(revealedValues[cred.name].value, cred.name)"
                  >
                    <Check v-if="copyFeedback === cred.name" class="size-3.5 text-green-500" />
                    <Copy v-else class="size-3.5" />
                  </Button>
                  <Button
                    variant="ghost"
                    size="icon-sm"
                    :title="t.vault.hide"
                    @click="hideCredential(cred.name)"
                  >
                    <EyeOff class="size-3.5" />
                  </Button>
                </div>
              </TableCell>
              <TableCell class="text-xs text-muted-foreground font-mono">
                {{ cred.project || t.common.none }}
              </TableCell>
              <TableCell>
                <Badge variant="secondary" class="text-[10px]">{{ cred.scope }}</Badge>
              </TableCell>
              <TableCell class="text-xs text-muted-foreground">
                {{ safeAbsoluteDate(cred.created_at) }}
              </TableCell>
              <TableCell class="text-right">
                <div class="flex items-center justify-end gap-1">
                  <Button
                    v-if="!revealedValues[cred.name]"
                    variant="outline"
                    size="xs"
                    @click="revealCredential(cred.name, cred.project)"
                  >
                    <Eye class="size-3.5" />
                    {{ t.vault.reveal }}
                  </Button>
                  <Button
                    variant="ghost"
                    size="icon-sm"
                    class="text-muted-foreground hover:text-destructive"
                    :title="t.vault.delete"
                    @click="confirmDelete(cred.name, cred.project)"
                  >
                    <Trash2 class="size-3.5" />
                  </Button>
                </div>
              </TableCell>
            </TableRow>
          </TableBody>
        </Table>
      </Card>
    </div>

    <!-- Delete Confirmation AlertDialog -->
    <AlertDialog :open="showDeleteConfirm" @update:open="showDeleteConfirm = $event">
      <AlertDialogContent>
        <AlertDialogHeader>
          <AlertDialogTitle>{{ t.vault.deleteTitle }}</AlertDialogTitle>
          <AlertDialogDescription>
            {{ t.vault.deleteDescription }}
          </AlertDialogDescription>
        </AlertDialogHeader>
        <AlertDialogFooter>
          <AlertDialogCancel @click="showDeleteConfirm = false">{{ t.issues.dialog.cancel }}</AlertDialogCancel>
          <AlertDialogAction
            class="bg-destructive text-destructive-foreground hover:bg-destructive/90"
            @click="handleDelete"
          >
            {{ t.vault.delete }}
          </AlertDialogAction>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  </div>
</template>
