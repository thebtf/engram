<script setup lang="ts">
import { computed } from 'vue'
import { useRoute } from '#imports'
import StatusBadge from '~/components/surfaces/StatusBadge.vue'
import { useOperatorAuth } from '~/composables/useOperatorAuth'

const route = useRoute()
const { state, logout } = useOperatorAuth()

const authTone = computed(() => {
  if (state.value.phase === 'authenticated') return 'live'
  if (state.value.phase === 'loading') return 'dormant'
  return 'must-build'
})

const authLabel = computed(() => {
  if (state.value.phase === 'authenticated') {
    return state.value.user?.email ?? 'signed in'
  }
  if (state.value.phase === 'loading') {
    return 'restoring session'
  }
  return 'sign in required'
})

async function handleLogout() {
  await logout()
  await navigateTo('/login')
}
</script>

<template>
  <header class="border-b border-[var(--border)] bg-[var(--surface)] px-4 py-3 lg:px-6">
    <div class="flex flex-col gap-3 md:flex-row md:items-center md:justify-between">
      <div>
        <div class="text-xs uppercase tracking-[0.08em] text-[var(--muted)]">
          Growth-oriented control plane
        </div>
        <div class="text-lg font-semibold">
          {{ route.path === '/' ? 'Operator Console' : route.path }}
        </div>
      </div>

      <div class="flex flex-wrap items-center gap-2">
        <StatusBadge tone="live" label="Nuxt 4 foundation" />
        <StatusBadge :tone="authTone" :label="authLabel" />
        <button
          v-if="state.phase === 'authenticated'"
          class="rounded-lg border border-[var(--border)] bg-[var(--surface-warm)] px-3 py-2 text-xs font-semibold text-[var(--fg-2)] transition-colors hover:border-[var(--accent)] hover:text-[var(--fg)]"
          @click="handleLogout"
        >
          Sign out
        </button>
      </div>
    </div>
  </header>
</template>
