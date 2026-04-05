<script setup lang="ts">
import { ref } from 'vue'
import { useRouter } from 'vue-router'
import { useAuth } from '@/composables/useAuth'

const router = useRouter()
const { login } = useAuth()

const token = ref('')
const error = ref('')
const submitting = ref(false)

async function handleLogin() {
  error.value = ''
  if (!token.value.trim()) {
    error.value = 'Please enter your admin token.'
    return
  }

  submitting.value = true
  try {
    const ok = await login(token.value.trim())
    if (ok) {
      router.push({ name: 'home' })
    } else {
      error.value = 'Invalid token. Please try again.'
      token.value = ''
    }
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
          <span class="text-claude-400">Engram</span>
        </h1>
        <p class="text-sm text-slate-400 mt-1">Persistent Memory System</p>
      </div>

      <!-- Login Card -->
      <form
        class="bg-slate-800/50 border border-slate-700/50 rounded-xl p-6 shadow-2xl"
        @submit.prevent="handleLogin"
      >
        <label for="token-input" class="block text-sm font-medium text-slate-300 mb-2">
          Admin Token
        </label>

        <input
          id="token-input"
          v-model="token"
          type="password"
          placeholder="Enter your admin token"
          autocomplete="current-password"
          class="w-full px-4 py-3 rounded-lg bg-slate-900/50 border border-slate-600/50 text-white placeholder-slate-500 focus:outline-none focus:ring-2 focus:ring-claude-500/50 focus:border-claude-500 transition-colors"
          :disabled="submitting"
        />

        <!-- Error message -->
        <p v-if="error" class="mt-3 text-sm text-red-400">
          <i class="fas fa-exclamation-circle mr-1" />
          {{ error }}
        </p>

        <button
          type="submit"
          class="mt-4 w-full py-3 rounded-lg bg-claude-500 text-white font-semibold hover:bg-claude-400 disabled:opacity-50 disabled:cursor-not-allowed transition-colors"
          :disabled="submitting"
        >
          <i v-if="submitting" class="fas fa-spinner fa-spin mr-2" />
          {{ submitting ? 'Logging in...' : 'Login' }}
        </button>
      </form>
    </div>
  </div>
</template>
