<script setup lang="ts">
import { useOperatorPreferences } from '~/composables/useOperatorPreferences'

const { density } = useOperatorPreferences()
</script>

<template>
  <div class="min-h-screen bg-[var(--bg)] text-[var(--fg)] lg:flex">
    <AppSidebar />
    <div class="min-w-0 flex-1">
      <AppTopbar />
      <main
        :class="density === 'compact'
          ? 'px-3 py-3 lg:px-5 lg:py-4'
          : 'px-4 py-4 lg:px-6 lg:py-6'"
      >
        <slot />
      </main>
    </div>
  </div>
</template>
