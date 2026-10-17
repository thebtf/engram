<script setup lang="ts">
import { useOperatorProjectContext, projectName } from '../composables/useOperatorProjectContext'
import { operatorDiagnosisKey } from '../composables/useOperatorApi'
const { t } = useI18n()
const context = useOperatorProjectContext()
const { project, projects, descriptors, unresolved, pending, error, selectionPending, selectionError } = context
function select(event: Event) { if (event.target instanceof HTMLSelectElement) void context.select(event.target.value) }
</script>

<template>
  <div class="project-context">
    <label>
      <span>{{ t('knowledge.contextProject') }}</span>
      <select :value="project" :aria-label="t('knowledge.contextProject')" :disabled="pending && !projects.length" @change="select" data-testid="project-context-select">
        <option value="" disabled>{{ t('knowledge.chooseProject') }}</option>
        <option v-for="id in projects" :key="id" :value="id">{{ projectName(id) }}{{ descriptors.filter(row => projectName(row.project_id) === projectName(id)).length > 1 ? ` · ${id}` : '' }}</option>
        <option value="all">{{ t('knowledge.allProjects') }}</option>
      </select>
    </label>
    <span v-if="pending || selectionPending" role="status">{{ t('projectCatalog.loading') }}</span>
    <span v-if="project && project !== 'all' && descriptors.filter(row => projectName(row.project_id) === projectName(project)).length > 1">{{ t('projectCatalog.ambiguousName') }}</span>
    <div v-if="error || selectionError" class="context-error" role="alert">
      {{ t('projectCatalog.unknownProject') }} {{ t(operatorDiagnosisKey(error || selectionError)) }} <button type="button" @click="context.refresh">{{ t('knowledge.retry') }}</button>
    </div>
    <RouterLink v-if="unresolved.length" to="/projects/unresolved">{{ t('projectCatalog.review') }} ({{ unresolved.length }})</RouterLink>
  </div>
</template>

<style scoped>
.project-context { min-width:0; max-width:100%; flex:0 1 340px; }
label { display:flex; align-items:center; gap:8px; color:var(--muted); font-size:var(--text-sm); }
select { min-width:0; max-width:100%; width:clamp(140px,22vw,300px); min-height:36px; padding:6px 10px; color:var(--fg); background:var(--surface); border:1px solid var(--border); border-radius:var(--r-sm); }
.context-error { font-size:var(--text-xs); color:var(--state-warn); }
button { background:transparent; color:var(--accent); border:0; cursor:pointer; }
@media(max-width:760px) { .project-context { flex:1; max-width:calc(100vw - 80px); } label > span { display:none; } select { width:100%; min-height:44px; } }
</style>
