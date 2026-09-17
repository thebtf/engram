<script setup lang="ts">
import { useOperatorGraph } from '../composables/useOperatorGraph'

const retirement = useOperatorGraph()
</script>

<template>
  <main class="retirement-page" :data-state="retirement.state">
    <header>
      <h1>Knowledge graph editing retired</h1>
      <p>{{ retirement.message }}</p>
    </header>
    <section role="status" aria-live="polite">
      <strong>Historical data is unchanged.</strong>
      <p>Manual graph create and delete actions are no longer available.</p>
      <NuxtLink :to="retirement.documentsHref">Open Documents</NuxtLink>
    </section>
  </main>
</template>

<style scoped>
.retirement-page { display: grid; gap: 14px; max-width: 720px; }
header, section { padding: 18px; border: 1px solid var(--border); border-radius: var(--r-md); background: var(--surface); }
h1 { margin: 0 0 6px; font-size: var(--text-xl); }
p { margin: 0; color: var(--muted); }
section { display: grid; gap: 8px; }
a { color: var(--accent); font-weight: 700; }
</style>
