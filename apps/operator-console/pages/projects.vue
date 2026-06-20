<script setup lang="ts">
/** Projects & sessions. Seam: → GET /api/projects, /api/sessions/list. */
const projects = [
  { id: 'engram', sessions: 12, last: 'сейчас' },
  { id: 'nvmd-platform', sessions: 3, last: '2 ч назад' },
]
</script>
<template>
  <div>
    <header class="head"><h1>Проекты и сессии</h1><p>Проекты на этом сервере и их рабочие сессии.</p></header>
    <div class="grid">
      <EntityRow v-for="p in projects" :key="p.id" :preview="p.id" :meta="[`${p.sessions} сессий`, `активность ${p.last}`]" status="live">
        <template #side><HonestyBadge cls="live" /></template>
      </EntityRow>
    </div>
  </div>
</template>
<style scoped>
.head h1 { margin:0 0 4px; font-size:var(--text-xl); font-weight:700; }
.head p { margin:0 0 16px; font-size:var(--text-sm); color:var(--muted); }
.grid { border:1px solid var(--border); border-radius:var(--r-md); background:var(--surface); overflow:hidden; }
</style>
