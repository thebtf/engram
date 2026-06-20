<script setup lang="ts">
/** Access (admin) — users, providers, roles. Self-hosted: no billing/tenancy here.
 *  Full surface mocked in .od/access-admin.html; this is the entry slice. */
const users = [
  { name: 'Олег Доброяков', email: 'oleg@novamedia.ru', role: 'Owner', status: 'live' },
  { name: 'Мария Карпова', email: 'maria@novamedia.ru', role: 'Admin', status: 'live' },
  { name: 'Дмитрий Новиков', email: 'dmitry@gmail.com', role: 'Operator', status: 'warn' },
]
</script>
<template>
  <div>
    <header class="head"><div class="row"><h1>Доступ</h1><span class="lock">admin</span></div><p>Пользователи, провайдеры входа и роли. Self-hosted — без тарифов и мультитенантности.</p></header>
    <div class="grid">
      <EntityRow v-for="u in users" :key="u.email" :preview="u.name" :meta="[u.email, u.role]" :status="u.status as any">
        <template #side><HonestyBadge :cls="u.status==='warn' ? 'dormant' : 'live'" :evidence="u.status==='warn' ? 'приостановлен' : undefined" /></template>
      </EntityRow>
    </div>
  </div>
</template>
<style scoped>
.head .row { display:flex; align-items:center; gap:10px; }
.head h1 { margin:0 0 4px; font-size:var(--text-xl); font-weight:700; }
.head p { margin:0 0 16px; font-size:var(--text-sm); color:var(--muted); }
.lock { font-size:9px; color:var(--muted); border:1px solid var(--border); border-radius:4px; padding:1px 5px; }
.grid { border:1px solid var(--border); border-radius:var(--r-md); background:var(--surface); overflow:hidden; }
</style>
