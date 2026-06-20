<script setup lang="ts">
/** Rules — always-inject behavioural directives. Seam: → GET/POST/PATCH/DELETE /api/rules. */
const rules = [
  { id: 'r1', text: 'Прод unleashed.lan read-only: только GET без явного грант-токена.', pri: 30 },
  { id: 'r2', text: 'Секреты write-only; reveal одноразовый, не персистить значение.', pri: 20 },
  { id: 'r3', text: 'Каждая поверхность классифицирована: live/dormant/stale/mustbuild.', pri: 10 },
]
</script>
<template>
  <div>
    <header class="head"><h1>Правила поведения</h1><p>Директивы, которые инжектируются в каждый контекст агента. Порядок задаёт приоритет.</p></header>
    <div class="grid">
      <EntityRow v-for="r in rules" :key="r.id" :preview="r.text" :meta="[`приоритет ${r.pri}`]" status="live">
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
