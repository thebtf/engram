<script setup lang="ts">
import { ref } from 'vue'
import { useOperatorAuth } from '~/composables/useOperatorAuth'

definePageMeta({
  layout: 'auth',
})

const { loginWithToken, loginWithCredentials } = useOperatorAuth()

const activeTab = ref<'credentials' | 'token'>('credentials')
const token = ref('')
const email = ref('')
const password = ref('')
const error = ref('')
const submitting = ref(false)

async function handleTokenLogin() {
  error.value = ''
  if (!token.value.trim()) {
    error.value = 'Введите admin token.'
    return
  }

  submitting.value = true
  try {
    const ok = await loginWithToken(token.value.trim())
    if (ok) {
      await navigateTo('/')
    } else {
      error.value = 'Неверный token.'
      token.value = ''
    }
  } catch {
    error.value = 'Ошибка соединения с сервером.'
  } finally {
    submitting.value = false
  }
}

async function handleEmailLogin() {
  error.value = ''
  if (!email.value.trim() || !password.value) {
    error.value = 'Нужны email и password.'
    return
  }

  submitting.value = true
  try {
    const ok = await loginWithCredentials(email.value.trim(), password.value)
    if (ok) {
      await navigateTo('/')
    } else {
      error.value = 'Неверный email или password.'
      password.value = ''
    }
  } catch {
    error.value = 'Ошибка соединения с сервером.'
  } finally {
    submitting.value = false
  }
}
</script>

<template>
  <div class="flex min-h-[calc(100vh-120px)] items-center justify-center">
    <section class="surface-panel w-full max-w-md p-6">
      <div class="mb-6 text-center">
        <div class="mx-auto mb-4 flex h-14 w-14 items-center justify-center rounded-2xl border border-[var(--border)] bg-[var(--surface-warm)] text-xl font-bold">
          E
        </div>
        <h1 class="text-2xl font-semibold">Sign in to engram</h1>
        <p class="mt-2 text-sm text-[var(--fg-2)]">
          Current Go-backed auth flow, now inside the new app shell.
        </p>
      </div>

      <div class="mb-5 grid grid-cols-2 gap-2 rounded-xl border border-[var(--border)] bg-[var(--surface-warm)] p-1">
        <button
          class="rounded-lg px-3 py-2 text-sm font-semibold transition-colors"
          :class="activeTab === 'credentials'
            ? 'bg-[var(--surface)] text-[var(--fg)]'
            : 'text-[var(--fg-2)]'"
          @click="activeTab = 'credentials'; error = ''"
        >
          Email
        </button>
        <button
          class="rounded-lg px-3 py-2 text-sm font-semibold transition-colors"
          :class="activeTab === 'token'
            ? 'bg-[var(--surface)] text-[var(--fg)]'
            : 'text-[var(--fg-2)]'"
          @click="activeTab = 'token'; error = ''"
        >
          Token
        </button>
      </div>

      <form v-if="activeTab === 'credentials'" class="space-y-4" @submit.prevent="handleEmailLogin">
        <label class="block">
          <span class="mb-1 block text-xs text-[var(--muted)]">Email</span>
          <input
            v-model="email"
            type="email"
            autocomplete="email"
            class="w-full rounded-lg border border-[var(--border)] bg-[var(--surface-warm)] px-3 py-2 text-sm text-[var(--fg)]"
            placeholder="you@example.com"
          />
        </label>

        <label class="block">
          <span class="mb-1 block text-xs text-[var(--muted)]">Password</span>
          <input
            v-model="password"
            type="password"
            autocomplete="current-password"
            class="w-full rounded-lg border border-[var(--border)] bg-[var(--surface-warm)] px-3 py-2 text-sm text-[var(--fg)]"
            placeholder="Enter your password"
          />
        </label>

        <div v-if="error" class="rounded-lg border border-[color:rgba(248,113,113,0.4)] bg-[color:rgba(248,113,113,0.12)] px-3 py-2 text-sm text-[var(--danger)]">
          {{ error }}
        </div>

        <button
          type="submit"
          class="w-full rounded-lg border border-[var(--accent)] bg-[var(--accent)] px-4 py-2 text-sm font-semibold text-[var(--accent-on)] disabled:opacity-60"
          :disabled="submitting"
        >
          {{ submitting ? 'Logging in...' : 'Login' }}
        </button>
      </form>

      <form v-else class="space-y-4" @submit.prevent="handleTokenLogin">
        <label class="block">
          <span class="mb-1 block text-xs text-[var(--muted)]">Admin Token</span>
          <input
            v-model="token"
            type="password"
            autocomplete="current-password"
            class="w-full rounded-lg border border-[var(--border)] bg-[var(--surface-warm)] px-3 py-2 text-sm text-[var(--fg)]"
            placeholder="Enter your admin token"
          />
        </label>

        <div v-if="error" class="rounded-lg border border-[color:rgba(248,113,113,0.4)] bg-[color:rgba(248,113,113,0.12)] px-3 py-2 text-sm text-[var(--danger)]">
          {{ error }}
        </div>

        <button
          type="submit"
          class="w-full rounded-lg border border-[var(--accent)] bg-[var(--accent)] px-4 py-2 text-sm font-semibold text-[var(--accent-on)] disabled:opacity-60"
          :disabled="submitting"
        >
          {{ submitting ? 'Logging in...' : 'Login' }}
        </button>
      </form>
    </section>
  </div>
</template>
