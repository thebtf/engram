<script setup lang="ts">
import { ref } from 'vue'
import { useRouter } from 'vue-router'
import { useAuth } from '@/composables/useAuth'

const router = useRouter()
const { login, loginWithCredentials } = useAuth()

// Tab state: 'credentials' | 'token'
const activeTab = ref<'credentials' | 'token'>('credentials')

// Token login state
const token = ref('')

// Credential login state
const email = ref('')
const password = ref('')

const error = ref('')
const submitting = ref(false)

async function handleTokenLogin() {
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

async function handleEmailLogin() {
  error.value = ''
  if (!email.value.trim() || !password.value) {
    error.value = 'Email and password are required.'
    return
  }

  submitting.value = true
  try {
    const ok = await loginWithCredentials(email.value.trim(), password.value)
    if (ok) {
      router.push({ name: 'home' })
    } else {
      error.value = 'Invalid email or password.'
      password.value = ''
    }
  } catch {
    error.value = 'Connection error. Is the server running?'
  } finally {
    submitting.value = false
  }
}

function switchTab(tab: 'credentials' | 'token') {
  activeTab.value = tab
  error.value = ''
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
      <div class="bg-slate-800/50 border border-slate-700/50 rounded-xl p-6 shadow-2xl">
        <!-- Tabs -->
        <div class="flex rounded-lg bg-slate-900/50 p-1 mb-5">
          <button
            type="button"
            class="flex-1 py-2 text-sm font-medium rounded-md transition-colors"
            :class="
              activeTab === 'credentials'
                ? 'bg-claude-500 text-white'
                : 'text-slate-400 hover:text-white'
            "
            @click="switchTab('credentials')"
          >
            Email
          </button>
          <button
            type="button"
            class="flex-1 py-2 text-sm font-medium rounded-md transition-colors"
            :class="
              activeTab === 'token'
                ? 'bg-claude-500 text-white'
                : 'text-slate-400 hover:text-white'
            "
            @click="switchTab('token')"
          >
            Token
          </button>
        </div>

        <!-- Email/Password Form -->
        <form v-if="activeTab === 'credentials'" @submit.prevent="handleEmailLogin">
          <div class="space-y-4">
            <div>
              <label class="block text-sm font-medium text-slate-300 mb-1">Email</label>
              <input
                v-model="email"
                type="email"
                required
                autocomplete="email"
                placeholder="you@example.com"
                class="w-full px-4 py-3 rounded-lg bg-slate-900/50 border border-slate-600/50 text-white placeholder-slate-500 focus:outline-none focus:ring-2 focus:ring-claude-500/50 focus:border-claude-500 transition-colors"
                :disabled="submitting"
              />
            </div>
            <div>
              <label class="block text-sm font-medium text-slate-300 mb-1">Password</label>
              <input
                v-model="password"
                type="password"
                required
                autocomplete="current-password"
                placeholder="Enter your password"
                class="w-full px-4 py-3 rounded-lg bg-slate-900/50 border border-slate-600/50 text-white placeholder-slate-500 focus:outline-none focus:ring-2 focus:ring-claude-500/50 focus:border-claude-500 transition-colors"
                :disabled="submitting"
              />
            </div>
          </div>

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

          <p class="mt-4 text-center text-sm text-slate-400">
            Have an invitation?
            <router-link to="/register" class="text-claude-400 hover:text-claude-300 transition-colors">
              Register
            </router-link>
          </p>
        </form>

        <!-- Token Form -->
        <form v-else @submit.prevent="handleTokenLogin">
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
  </div>
</template>
