<script setup lang="ts">
import { ref } from 'vue'
import { useRouter } from 'vue-router'

const router = useRouter()
const email = ref('')
const password = ref('')
const confirmPassword = ref('')
const invitationCode = ref('')
const error = ref('')
const submitting = ref(false)

async function handleRegister() {
  error.value = ''
  if (!email.value.trim() || !password.value || !invitationCode.value.trim()) {
    error.value = 'All fields are required.'
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
    const resp = await fetch('/api/auth/register', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({
        email: email.value.trim(),
        password: password.value,
        invitation: invitationCode.value.trim(),
      }),
    })
    if (!resp.ok) {
      const data = await resp.json().catch(() => ({}))
      error.value = data.error || 'Registration failed.'
      return
    }
    router.push({ name: 'login' })
  } catch {
    error.value = 'Connection error.'
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
          <span class="text-claude-400">Engram</span> Register
        </h1>
        <p class="text-sm text-slate-400 mt-1">Create your account with an invitation code</p>
      </div>

      <!-- Register Card -->
      <form
        class="bg-slate-800/50 border border-slate-700/50 rounded-xl p-6 shadow-2xl"
        @submit.prevent="handleRegister"
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
              placeholder="you@example.com"
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
          <div>
            <label class="block text-sm font-medium text-slate-300 mb-1">Invitation Code</label>
            <input
              v-model="invitationCode"
              type="text"
              required
              autocomplete="off"
              class="w-full px-4 py-3 rounded-lg bg-slate-900/50 border border-slate-600/50 text-white placeholder-slate-500 focus:outline-none focus:ring-2 focus:ring-claude-500/50 focus:border-claude-500 transition-colors font-mono text-sm"
              :disabled="submitting"
              placeholder="Paste your invitation code"
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
          {{ submitting ? 'Creating account...' : 'Create Account' }}
        </button>

        <p class="mt-4 text-center text-sm text-slate-400">
          Already have an account?
          <router-link :to="{ name: 'login' }" class="text-claude-400 hover:text-claude-300 transition-colors">
            Sign in
          </router-link>
        </p>
      </form>
    </div>
  </div>
</template>
