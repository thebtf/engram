<script setup lang="ts">
import { ref, onMounted } from 'vue'
import { useAuth } from '@/composables/useAuth'
import { useRouter } from 'vue-router'
import { copyToClipboard } from '@/utils/clipboard'
import { formatRelativeTime } from '@/utils/formatters'

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
      <i class="fas fa-shield-halved text-claude-400 text-xl" />
      <h1 class="text-2xl font-bold text-white">Admin</h1>
    </div>

    <!-- Users Section -->
    <section class="space-y-4">
      <div class="flex items-center justify-between">
        <div class="flex items-center gap-2">
          <i class="fas fa-users text-slate-400" />
          <h2 class="text-lg font-semibold text-white">Users</h2>
          <span v-if="users.length > 0" class="text-sm text-slate-500">({{ users.length }})</span>
        </div>
        <button
          class="flex items-center gap-2 px-3 py-1.5 rounded-lg text-sm bg-slate-800 text-slate-300 hover:text-white hover:bg-slate-700 transition-colors"
          :disabled="usersLoading"
          @click="loadUsers"
        >
          <i :class="['fas fa-rotate-right text-xs', usersLoading ? 'animate-spin' : '']" />
          Refresh
        </button>
      </div>

      <div v-if="usersError" class="rounded-lg bg-red-500/10 border border-red-500/30 px-4 py-3 text-red-400 text-sm">
        {{ usersError }}
      </div>

      <div v-else-if="usersLoading && users.length === 0" class="text-slate-500 text-sm">
        Loading users...
      </div>

      <div v-else-if="users.length === 0" class="text-slate-500 text-sm">
        No users found.
      </div>

      <div v-else class="rounded-xl border border-slate-700/50 overflow-hidden">
        <table class="w-full text-sm">
          <thead>
            <tr class="bg-slate-800/60 text-slate-400 text-xs uppercase tracking-wider">
              <th class="text-left px-4 py-3 font-medium">Email</th>
              <th class="text-left px-4 py-3 font-medium">Role</th>
              <th class="text-left px-4 py-3 font-medium">Last Login</th>
              <th class="text-left px-4 py-3 font-medium">Created</th>
              <th class="text-right px-4 py-3 font-medium">Status</th>
            </tr>
          </thead>
          <tbody class="divide-y divide-slate-700/30">
            <tr
              v-for="user in users"
              :key="user.id"
              :class="['transition-colors', user.disabled ? 'bg-slate-900/30 opacity-60' : 'hover:bg-slate-800/30']"
            >
              <td class="px-4 py-3 text-white font-medium">{{ user.email }}</td>
              <td class="px-4 py-3">
                <select
                  :value="user.role"
                  class="bg-slate-800 border border-slate-700 rounded-lg px-2 py-1 text-xs text-slate-300 focus:outline-none focus:ring-1 focus:ring-claude-500"
                  @change="changeRole(user, ($event.target as HTMLSelectElement).value)"
                >
                  <option value="admin">admin</option>
                  <option value="operator">operator</option>
                </select>
              </td>
              <td class="px-4 py-3 text-slate-400">
                {{ user.last_login_at ? formatRelativeTime(user.last_login_at) : '—' }}
              </td>
              <td class="px-4 py-3 text-slate-400">
                {{ formatRelativeTime(user.created_at) }}
              </td>
              <td class="px-4 py-3 text-right">
                <button
                  :class="[
                    'px-3 py-1 rounded-lg text-xs font-medium transition-colors',
                    user.disabled
                      ? 'bg-green-500/20 text-green-400 hover:bg-green-500/30'
                      : 'bg-red-500/20 text-red-400 hover:bg-red-500/30',
                  ]"
                  @click="toggleDisabled(user)"
                >
                  {{ user.disabled ? 'Enable' : 'Disable' }}
                </button>
              </td>
            </tr>
          </tbody>
        </table>
      </div>
    </section>

    <!-- Invitations Section -->
    <section class="space-y-4">
      <div class="flex items-center justify-between">
        <div class="flex items-center gap-2">
          <i class="fas fa-envelope-open-text text-slate-400" />
          <h2 class="text-lg font-semibold text-white">Invitations</h2>
          <span v-if="invitations.length > 0" class="text-sm text-slate-500">({{ invitations.length }})</span>
        </div>
        <button
          class="flex items-center gap-2 px-3 py-1.5 rounded-lg text-sm bg-claude-600 text-white hover:bg-claude-500 transition-colors disabled:opacity-50"
          :disabled="generatingCode"
          @click="generateCode"
        >
          <i :class="['fas fa-plus text-xs', generatingCode ? 'animate-spin fa-spinner' : '']" />
          Generate Code
        </button>
      </div>

      <div v-if="invitationsError" class="rounded-lg bg-red-500/10 border border-red-500/30 px-4 py-3 text-red-400 text-sm">
        {{ invitationsError }}
      </div>

      <div v-else-if="invitationsLoading && invitations.length === 0" class="text-slate-500 text-sm">
        Loading invitations...
      </div>

      <div v-else-if="invitations.length === 0" class="text-slate-500 text-sm">
        No invitation codes yet. Generate one to invite users.
      </div>

      <div v-else class="rounded-xl border border-slate-700/50 overflow-hidden">
        <table class="w-full text-sm">
          <thead>
            <tr class="bg-slate-800/60 text-slate-400 text-xs uppercase tracking-wider">
              <th class="text-left px-4 py-3 font-medium">Code</th>
              <th class="text-left px-4 py-3 font-medium">Status</th>
              <th class="text-left px-4 py-3 font-medium">Created</th>
              <th class="text-left px-4 py-3 font-medium">Used</th>
              <th class="text-right px-4 py-3 font-medium">Action</th>
            </tr>
          </thead>
          <tbody class="divide-y divide-slate-700/30">
            <tr
              v-for="inv in invitations"
              :key="inv.id"
              :class="['transition-colors', inv.used_by ? 'opacity-50' : 'hover:bg-slate-800/30']"
            >
              <td class="px-4 py-3">
                <code class="font-mono text-xs text-claude-300 bg-slate-800/60 px-2 py-1 rounded">
                  {{ inv.code }}
                </code>
              </td>
              <td class="px-4 py-3">
                <span
                  :class="[
                    'px-2 py-0.5 rounded-full text-xs font-medium',
                    inv.used_by
                      ? 'bg-slate-700/50 text-slate-400'
                      : 'bg-green-500/20 text-green-400',
                  ]"
                >
                  {{ inv.used_by ? 'Used' : 'Unused' }}
                </span>
              </td>
              <td class="px-4 py-3 text-slate-400">
                {{ formatRelativeTime(inv.created_at) }}
              </td>
              <td class="px-4 py-3 text-slate-400">
                {{ inv.used_at ? formatRelativeTime(inv.used_at) : '—' }}
              </td>
              <td class="px-4 py-3 text-right">
                <button
                  v-if="!inv.used_by"
                  :class="[
                    'flex items-center gap-1.5 ml-auto px-3 py-1 rounded-lg text-xs font-medium transition-colors',
                    copiedId === inv.id
                      ? 'bg-green-500/20 text-green-400'
                      : 'bg-slate-700/50 text-slate-300 hover:bg-slate-700 hover:text-white',
                  ]"
                  :title="copiedId === inv.id ? 'Copied!' : 'Copy to clipboard'"
                  @click="copyCode(inv)"
                >
                  <i :class="['fas text-xs', copiedId === inv.id ? 'fa-check' : 'fa-copy']" />
                  {{ copiedId === inv.id ? 'Copied' : 'Copy' }}
                </button>
                <span v-else class="text-slate-600 text-xs">—</span>
              </td>
            </tr>
          </tbody>
        </table>
      </div>
    </section>
  </div>
</template>
