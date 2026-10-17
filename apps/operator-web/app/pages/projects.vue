<script setup lang="ts">
import { computed } from 'vue'
import { useSessionsQuery } from '~/composables/useOperatorQueries'
import { useProjectCatalog } from '~/composables/useProjectCatalog'
import ProjectCatalogStatus from '~/components/ProjectCatalogStatus.vue'
import { useOperatorI18n } from '~/composables/useOperatorI18n'

const { t } = useOperatorI18n()
const { projectsQuery, projects, unresolved, selectedProject, selectionPending, selectionError, ready, unresolvedReference, projectName } = useProjectCatalog('all')
const sessionsQuery = useSessionsQuery(selectedProject, ready)
const sessions = computed(() => sessionsQuery.data.value?.sessions ?? [])
</script>

<template>
  <section class="space-y-5">
    <ProjectCatalogStatus :records="unresolved" :pending="selectionPending" :error="selectionError" :reference="unresolvedReference" />
    <div class="surface-panel p-5">
      <div class="mb-3 flex flex-wrap items-center justify-between gap-3">
        <div>
          <h1 class="text-xl font-semibold">Projects & Sessions</h1>
          <p class="mt-2 text-sm text-[var(--fg-2)]">
            Первый live-backed slice в новом app-shell: текущие projects и session window из Go API.
          </p>
        </div>

        <label class="text-sm text-[var(--fg-2)]">
          <span class="mr-2">{{ t('common.allProjects') }}</span>
          <select v-model="selectedProject" class="rounded-lg border border-[var(--border)] bg-[var(--surface-warm)] px-3 py-2 text-sm text-[var(--fg)]">
            <option value="all">{{ t('common.allProjects') }}</option>
            <option v-for="project in projects" :key="project.project_id" :value="project.project_id">{{ projectName(project.project_id) }}</option>
          </select>
        </label>
      </div>

      <div class="grid gap-4 lg:grid-cols-[320px,minmax(0,1fr)]">
        <div class="surface-panel-warm p-4">
          <div class="mb-3 text-xs uppercase tracking-[0.08em] text-[var(--muted)]">Projects</div>
          <div v-if="projectsQuery.isPending.value" class="text-sm text-[var(--muted)]">{{ t('common.loading') }}</div>
          <ul v-else class="space-y-2 text-sm">
            <li v-for="project in projects" :key="project.project_id" class="rounded-lg border border-[var(--border)] bg-[var(--surface)] px-3 py-2">
              {{ projectName(project.project_id) }}
            </li>
          </ul>
        </div>

        <div class="surface-panel-warm p-4">
          <div class="mb-3 text-xs uppercase tracking-[0.08em] text-[var(--muted)]">Session Window</div>
          <div v-if="sessionsQuery.isPending.value" class="text-sm text-[var(--muted)]">{{ t('common.loading') }}</div>
          <div v-else-if="sessions.length === 0" class="text-sm text-[var(--muted)]">{{ t('common.empty') }}</div>
          <div v-else class="overflow-x-auto">
            <table class="min-w-full text-left text-sm">
              <thead class="text-xs uppercase tracking-[0.08em] text-[var(--muted)]">
                <tr>
                  <th class="px-3 py-2">Project</th>
                  <th class="px-3 py-2">Status</th>
                  <th class="px-3 py-2">Prompts</th>
                  <th class="px-3 py-2">Started</th>
                </tr>
              </thead>
              <tbody>
                <tr v-for="session in sessions" :key="session.id" class="border-t border-[var(--border)]">
                  <td class="px-3 py-2">{{ projectName(session.project) }}</td>
                  <td class="px-3 py-2">{{ session.status }}</td>
                  <td class="mono-data px-3 py-2">{{ session.prompt_counter }}</td>
                  <td class="mono-data px-3 py-2">{{ session.started_at }}</td>
                </tr>
              </tbody>
            </table>
          </div>
        </div>
      </div>
    </div>
  </section>
</template>
