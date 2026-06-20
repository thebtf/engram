<script setup lang="ts">
/** Projects & sessions. Seam: → GET /api/projects, /api/sessions/list. */
import { useProjects } from '../composables/useMockData'

const { t } = useI18n()
const projects = useProjects()
</script>

<template>
  <div>
    <header class="head">
      <h1>{{ t('projects.title') }}</h1>
      <p>{{ t('projects.subtitle') }}</p>
    </header>

    <p v-if="projects.error" class="msg err">{{ projects.error }}</p>
    <p v-else-if="projects.pending && !projects.rows.length" class="msg note">{{ t('projects.loading') }}</p>
    <p v-else-if="!projects.rows.length" class="msg note">{{ t('projects.empty') }}</p>

    <div v-else class="grid">
      <EntityRow
        v-for="project in projects.rows"
        :key="project.id"
        :preview="project.id"
        :meta="[
          t('projects.sessionsCount', project.sessions),
          t('projects.lastActivity', { value: project.last }),
        ]"
        status="live"
      >
        <template #side><HonestyBadge cls="live" evidence="GET /api/projects · /api/sessions/list" /></template>
      </EntityRow>
    </div>
  </div>
</template>

<style scoped>
.head h1 { margin:0 0 4px; font-size:var(--text-xl); font-weight:700; }
.head p { margin:0 0 16px; font-size:var(--text-sm); color:var(--muted); }
.msg { margin:0 0 14px; font-size:var(--text-sm); }
.msg.note { color:var(--muted); }
.msg.err { color:var(--state-danger); }
.grid { border:1px solid var(--border); border-radius:var(--r-md); background:var(--surface); overflow:hidden; }
</style>
