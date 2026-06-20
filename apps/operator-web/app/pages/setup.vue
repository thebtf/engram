<script setup lang="ts">
import { ref } from 'vue'
import { useOperatorAuth } from '~/composables/useOperatorAuth'

definePageMeta({
  layout: 'auth',
})

const { setupFirstUser } = useOperatorAuth()

const email = ref('')
const password = ref('')
const confirmPassword = ref('')
const error = ref('')
const submitting = ref(false)

async function handleSetup() {
  error.value = ''

  if (!email.value.trim() || !password.value) {
    error.value = 'Нужны email и password.'
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
    const result = await setupFirstUser(email.value.trim(), password.value)
    if (!result.ok) {
      error.value = result.error || 'Setup failed.'
      return
    }
    await navigateTo('/login')
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
        <h1 class="text-2xl font-semibold">Set up engram</h1>
        <p class="mt-2 text-sm text-[var(--fg-2)]">
          Create the first admin account using the current Go-backed setup flow.
        </p>
      </div>

      <form class="space-y-4" @submit.prevent="handleSetup">
        <label class="block">
          <span class="mb-1 block text-xs text-[var(--muted)]">Email</span>
          <input
            v-model="email"
            type="email"
            autocomplete="email"
            class="w-full rounded-lg border border-[var(--border)] bg-[var(--surface-warm)] px-3 py-2 text-sm text-[var(--fg)]"
            placeholder="admin@example.com"
          />
        </label>

        <label class="block">
          <span class="mb-1 block text-xs text-[var(--muted)]">Password</span>
          <input
            v-model="password"
            type="password"
            autocomplete="new-password"
            class="w-full rounded-lg border border-[var(--border)] bg-[var(--surface-warm)] px-3 py-2 text-sm text-[var(--fg)]"
            placeholder="Min 8 characters"
          />
        </label>

        <label class="block">
          <span class="mb-1 block text-xs text-[var(--muted)]">Confirm password</span>
          <input
            v-model="confirmPassword"
            type="password"
            autocomplete="new-password"
            class="w-full rounded-lg border border-[var(--border)] bg-[var(--surface-warm)] px-3 py-2 text-sm text-[var(--fg)]"
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
          {{ submitting ? 'Creating account...' : 'Create admin account' }}
        </button>
      </form>
    </section>
  </div>
</template>
