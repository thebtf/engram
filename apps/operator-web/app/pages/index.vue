<script setup lang="ts">
import { computed, ref } from 'vue'
import { useProjectsQuery, useRulesQuery, useSessionsQuery } from '~/composables/useOperatorQueries'

const selectedProject = ref('')
const projectsQuery = useProjectsQuery()
const sessionsQuery = useSessionsQuery(selectedProject)
const rulesQuery = useRulesQuery(selectedProject)

const projectCount = computed(() => projectsQuery.data.value?.length ?? 0)
const sessionCount = computed(() => sessionsQuery.data.value?.total ?? 0)
const ruleCount = computed(() => rulesQuery.data.value?.total ?? 0)
</script>

<template>
  <section class="space-y-5">
    <div class="surface-panel p-5">
      <div class="mb-2 text-xs uppercase tracking-[0.08em] text-[var(--muted)]">
        Foundation checkpoint
      </div>
      <h1 class="text-2xl font-semibold">Nuxt app-shell for the operator control plane</h1>
      <p class="mt-3 max-w-4xl text-sm leading-6 text-[var(--fg-2)]">
        Это уже не расширение старого SPA, а новый host substrate. Он держит route families для
        `Operator`, `Access Admin` и будущего `Platform Hub`, но при этом остается привязан к
        текущим Go API seams.
      </p>
    </div>

    <div class="grid gap-4 md:grid-cols-3">
      <div class="surface-panel p-4">
        <div class="text-xs uppercase tracking-[0.08em] text-[var(--muted)]">Projects</div>
        <div class="mono-data mt-3 text-3xl font-semibold">{{ projectCount }}</div>
        <div class="mt-2 text-sm text-[var(--fg-2)]">live `GET /api/projects` seam</div>
      </div>

      <div class="surface-panel p-4">
        <div class="text-xs uppercase tracking-[0.08em] text-[var(--muted)]">Sessions</div>
        <div class="mono-data mt-3 text-3xl font-semibold">{{ sessionCount }}</div>
        <div class="mt-2 text-sm text-[var(--fg-2)]">live `GET /api/sessions/list` seam</div>
      </div>

      <div class="surface-panel p-4">
        <div class="text-xs uppercase tracking-[0.08em] text-[var(--muted)]">Rules</div>
        <div class="mono-data mt-3 text-3xl font-semibold">{{ ruleCount }}</div>
        <div class="mt-2 text-sm text-[var(--fg-2)]">live `GET /api/rules` seam</div>
      </div>
    </div>

    <div class="surface-panel-warm p-5">
      <div class="mb-2 text-xs uppercase tracking-[0.08em] text-[var(--muted)]">
        Growth direction
      </div>
      <ul class="space-y-2 text-sm leading-6 text-[var(--fg-2)]">
        <li>Operator surfaces stay tied to the current Engram instance and Go API authority.</li>
        <li>Access-admin becomes a separate route family instead of more tabs in one dashboard tree.</li>
        <li>Future SaaS/platform surfaces stay extraction-ready for a separate multi-product hub.</li>
      </ul>
    </div>
  </section>
</template>
