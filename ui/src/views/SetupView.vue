<script setup lang="ts">
import { ref } from 'vue'
import { useRouter } from 'vue-router'

const router = useRouter()
const email = ref('')
const password = ref('')
const confirmPassword = ref('')
const error = ref('')
const submitting = ref(false)

async function handleSetup() {
  error.value = ''
  if (!email.value.trim() || !password.value) {
    error.value = 'Email and password are required.'
    return
  }
  if (password.value !== confirmPassword.value) {
    error.value = 'Passwords do not match.'
    return
  }
  if (password.value.length < 8) {
    error.value = 'Password must be at least 8 characters.'
    return
  }

  submitting.value = true
  try {
    const resp = await fetch('/api/auth/setup', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ email: email.value.trim(), password: password.value }),
    })
    if (!resp.ok) {
      const data = await resp.json().catch(() => ({}))
      error.value = data.error || 'Setup failed.'
      return
    }
    router.push({ name: 'login' })
  } catch {
    error.value = 'Connection error. Is the server running?'
  } finally {
    submitting.value = false
  }
}
</script>

<template>
  <div class="min-h-screen flex items-center justify-center bg-slate-950 px-4">
    <div class="w-full max-w-sm">
      <!-- Logo -->
      <div class="text-center mb-8">
        <div
          class="w-16 h-16 mx-auto mb-4 rounded-2xl bg-gradient-to-br from-claude-500 to-claude-700 flex items-center justify-center shadow-lg"
        >
          <i class="fas fa-brain text-3xl text-white" />
        </div>
        <h1 class="text-2xl font-bold text-white">
          <span class="text-claude-400">Engram</span> Setup
        </h1>
        <p class="text-sm text-slate-400 mt-1">Create your admin account</p>
      </div>

      <!-- Setup Card -->
      <form
        class="bg-slate-800/50 border border-slate-700/50 rounded-xl p-6 shadow-2xl"
        @submit.prevent="handleSetup"
      >
        <div class="space-y-4">
          <div>
            <label class="block text-sm font-medium text-slate-300 mb-1">Email</label>
            <input
              v-model="email"
              type="email"
              required
              autocomplete="email"
              class="w-full px-4 py-3 rounded-lg bg-slate-900/50 border border-slate-600/50 text-white placeholder-slate-500 focus:outline-none focus:ring-2 focus:ring-claude-500/50 focus:border-claude-500 transition-colors"
              :disabled="submitting"
              placeholder="admin@example.com"
            />
          </div>
          <div>
            <label class="block text-sm font-medium text-slate-300 mb-1">Password</label>
            <input
              v-model="password"
              type="password"
              required
              autocomplete="new-password"
              class="w-full px-4 py-3 rounded-lg bg-slate-900/50 border border-slate-600/50 text-white placeholder-slate-500 focus:outline-none focus:ring-2 focus:ring-claude-500/50 focus:border-claude-500 transition-colors"
              :disabled="submitting"
              placeholder="Min 8 characters"
            />
          </div>
          <div>
            <label class="block text-sm font-medium text-slate-300 mb-1">Confirm Password</label>
            <input
              v-model="confirmPassword"
              type="password"
              required
              autocomplete="new-password"
              class="w-full px-4 py-3 rounded-lg bg-slate-900/50 border border-slate-600/50 text-white placeholder-slate-500 focus:outline-none focus:ring-2 focus:ring-claude-500/50 focus:border-claude-500 transition-colors"
              :disabled="submitting"
            />
          </div>
        </div>

        <!-- Error message -->
        <p v-if="error" class="mt-3 text-sm text-red-400">
          <i class="fas fa-exclamation-circle mr-1" />{{ error }}
        </p>

        <button
          type="submit"
          class="mt-4 w-full py-3 rounded-lg bg-claude-500 text-white font-semibold hover:bg-claude-400 disabled:opacity-50 disabled:cursor-not-allowed transition-colors"
          :disabled="submitting"
        >
          <i v-if="submitting" class="fas fa-spinner fa-spin mr-2" />
          {{ submitting ? 'Creating account...' : 'Create Admin Account' }}
        </button>
      </form>
    </div>
  </div>
</template>
