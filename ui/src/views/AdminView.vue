<script setup lang="ts">
import { ref, onMounted } from 'vue'
import { useAuth } from '@/composables/useAuth'
import { useRouter } from 'vue-router'
import { copyToClipboard } from '@/utils/clipboard'
import { formatRelativeTime } from '@/utils/formatters'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card } from '@/components/ui/card'
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@/components/ui/table'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { Switch } from '@/components/ui/switch'
import { Skeleton } from '@/components/ui/skeleton'
import {
  Shield,
  Users,
  Ticket,
  RefreshCw,
  Plus,
  Copy,
  Check,
  AlertTriangle,
  Loader2,
} from 'lucide-vue-next'

const { isAdmin } = useAuth()
const router = useRouter()

// --- Users ---
interface User {
  id: number
  email: string
  role: string
  disabled: boolean
  created_at: string
  last_login_at?: string
}

const users = ref<User[]>([])
const usersLoading = ref(false)
const usersError = ref<string | null>(null)

async function loadUsers() {
  usersLoading.value = true
  usersError.value = null
  try {
    const res = await fetch('/api/admin/users', { credentials: 'include' })
    if (!res.ok) throw new Error(`HTTP ${res.status}`)
    const data = await res.json()
    users.value = data.users ?? []
  } catch (err) {
    usersError.value = err instanceof Error ? err.message : 'Failed to load users'
  } finally {
    usersLoading.value = false
  }
}

async function updateUser(id: number, patch: { disabled?: boolean; role?: string }) {
  usersError.value = null
  try {
    const res = await fetch(`/api/admin/users/${id}`, {
      method: 'PUT',
      headers: { 'Content-Type': 'application/json' },
      credentials: 'include',
      body: JSON.stringify(patch),
    })
    if (!res.ok) {
      const data = await res.json().catch(() => ({}))
      throw new Error(data.error ?? `HTTP ${res.status}`)
    }
    await loadUsers()
  } catch (err) {
    usersError.value = err instanceof Error ? err.message : 'Failed to update user'
  }
}

async function toggleDisabled(user: User) {
  await updateUser(user.id, { disabled: !user.disabled })
}

async function changeRole(user: User, role: string) {
  await updateUser(user.id, { role })
}

// --- Invitations ---
interface Invitation {
  id: number
  code: string
  created_by: number
  used_by?: number
  used_at?: string
  created_at: string
}

const invitations = ref<Invitation[]>([])
const invitationsLoading = ref(false)
const invitationsError = ref<string | null>(null)
const generatingCode = ref(false)
const copiedId = ref<number | null>(null)

async function loadInvitations() {
  invitationsLoading.value = true
  invitationsError.value = null
  try {
    const res = await fetch('/api/admin/invitations', { credentials: 'include' })
    if (!res.ok) throw new Error(`HTTP ${res.status}`)
    const data = await res.json()
    invitations.value = data.invitations ?? []
  } catch (err) {
    invitationsError.value = err instanceof Error ? err.message : 'Failed to load invitations'
  } finally {
    invitationsLoading.value = false
  }
}

async function generateCode() {
  generatingCode.value = true
  try {
    const res = await fetch('/api/admin/invitations', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      credentials: 'include',
      body: JSON.stringify({}),
    })
    if (!res.ok) {
      const data = await res.json().catch(() => ({}))
      throw new Error(data.error ?? `HTTP ${res.status}`)
    }
    await loadInvitations()
  } catch (err) {
    invitationsError.value = err instanceof Error ? err.message : 'Failed to generate invitation code'
  } finally {
    generatingCode.value = false
  }
}

async function copyCode(inv: Invitation) {
  const ok = await copyToClipboard(inv.code)
  if (ok) {
    copiedId.value = inv.id
    setTimeout(() => { copiedId.value = null }, 2000)
  }
}

onMounted(async () => {
  if (!isAdmin.value) {
    router.push({ name: 'home' })
    return
  }
  await Promise.all([loadUsers(), loadInvitations()])
})
</script>

<template>
  <div class="space-y-8">
    <!-- Header -->
    <div class="flex items-center gap-3">
      <Shield class="text-primary size-5" />
      <h1 class="text-2xl font-bold">Admin</h1>
    </div>

    <!-- Users Section -->
    <section class="space-y-4">
      <div class="flex items-center justify-between">
        <div class="flex items-center gap-2">
          <Users class="size-4 text-muted-foreground" />
          <h2 class="text-lg font-semibold">Users</h2>
          <span v-if="users.length > 0" class="text-sm text-muted-foreground">({{ users.length }})</span>
        </div>
        <Button variant="outline" size="sm" :disabled="usersLoading" @click="loadUsers">
          <RefreshCw :class="['size-4', usersLoading && 'animate-spin']" />
          Refresh
        </Button>
      </div>

      <div v-if="usersError" class="flex items-start gap-2 rounded-lg border border-destructive/30 bg-destructive/10 px-4 py-3">
        <AlertTriangle class="size-4 text-destructive mt-0.5 shrink-0" />
        <span class="text-sm text-destructive">{{ usersError }}</span>
      </div>

      <div v-else-if="usersLoading && users.length === 0" class="space-y-2">
        <Skeleton class="h-12 w-full rounded-lg" />
        <Skeleton class="h-12 w-full rounded-lg" />
      </div>

      <p v-else-if="users.length === 0" class="text-sm text-muted-foreground">No users found.</p>

      <Card v-else>
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>Email</TableHead>
              <TableHead>Role</TableHead>
              <TableHead>Last Login</TableHead>
              <TableHead>Created</TableHead>
              <TableHead class="text-right">Enabled</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            <TableRow
              v-for="user in users"
              :key="user.id"
              :class="user.disabled ? 'opacity-50' : ''"
            >
              <TableCell class="font-medium">{{ user.email }}</TableCell>
              <TableCell>
                <Select :model-value="user.role" @update:model-value="(role) => { if (role) changeRole(user, String(role)) }">
                  <SelectTrigger class="h-7 w-28 text-xs">
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    <SelectItem value="admin">admin</SelectItem>
                    <SelectItem value="operator">operator</SelectItem>
                  </SelectContent>
                </Select>
              </TableCell>
              <TableCell class="text-sm text-muted-foreground">
                {{ user.last_login_at ? formatRelativeTime(user.last_login_at) : '—' }}
              </TableCell>
              <TableCell class="text-sm text-muted-foreground">
                {{ formatRelativeTime(user.created_at) }}
              </TableCell>
              <TableCell class="text-right">
                <Switch
                  :checked="!user.disabled"
                  @update:checked="toggleDisabled(user)"
                />
              </TableCell>
            </TableRow>
          </TableBody>
        </Table>
      </Card>
    </section>

    <!-- Invitations Section -->
    <section class="space-y-4">
      <div class="flex items-center justify-between">
        <div class="flex items-center gap-2">
          <Ticket class="size-4 text-muted-foreground" />
          <h2 class="text-lg font-semibold">Invitations</h2>
          <span v-if="invitations.length > 0" class="text-sm text-muted-foreground">({{ invitations.length }})</span>
        </div>
        <Button size="sm" :disabled="generatingCode" @click="generateCode">
          <Loader2 v-if="generatingCode" class="size-4 animate-spin" />
          <Plus v-else class="size-4" />
          Generate Code
        </Button>
      </div>

      <div v-if="invitationsError" class="flex items-start gap-2 rounded-lg border border-destructive/30 bg-destructive/10 px-4 py-3">
        <AlertTriangle class="size-4 text-destructive mt-0.5 shrink-0" />
        <span class="text-sm text-destructive">{{ invitationsError }}</span>
      </div>

      <div v-else-if="invitationsLoading && invitations.length === 0" class="space-y-2">
        <Skeleton class="h-12 w-full rounded-lg" />
        <Skeleton class="h-12 w-full rounded-lg" />
      </div>

      <p v-else-if="invitations.length === 0" class="text-sm text-muted-foreground">
        No invitation codes yet. Generate one to invite users.
      </p>

      <Card v-else>
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>Code</TableHead>
              <TableHead>Status</TableHead>
              <TableHead>Created</TableHead>
              <TableHead>Used</TableHead>
              <TableHead class="text-right">Action</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            <TableRow
              v-for="inv in invitations"
              :key="inv.id"
              :class="inv.used_by ? 'opacity-50' : ''"
            >
              <TableCell>
                <code class="font-mono text-xs text-primary bg-muted px-2 py-1 rounded">
                  {{ inv.code }}
                </code>
              </TableCell>
              <TableCell>
                <Badge :variant="inv.used_by ? 'secondary' : 'outline'" class="text-[10px]">
                  {{ inv.used_by ? 'Used' : 'Unused' }}
                </Badge>
              </TableCell>
              <TableCell class="text-sm text-muted-foreground">
                {{ formatRelativeTime(inv.created_at) }}
              </TableCell>
              <TableCell class="text-sm text-muted-foreground">
                {{ inv.used_at ? formatRelativeTime(inv.used_at) : '—' }}
              </TableCell>
              <TableCell class="text-right">
                <Button
                  v-if="!inv.used_by"
                  variant="outline"
                  size="xs"
                  :title="copiedId === inv.id ? 'Copied!' : 'Copy to clipboard'"
                  @click="copyCode(inv)"
                >
                  <Check v-if="copiedId === inv.id" class="size-3.5 text-green-500" />
                  <Copy v-else class="size-3.5" />
                  {{ copiedId === inv.id ? 'Copied' : 'Copy' }}
                </Button>
                <span v-else class="text-muted-foreground text-sm">—</span>
              </TableCell>
            </TableRow>
          </TableBody>
        </Table>
      </Card>
    </section>
  </div>
</template>
