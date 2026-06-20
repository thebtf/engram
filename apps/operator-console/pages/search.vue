<script setup lang="ts">
/** Search — console-wide search across memory, issues, rules, secrets, projects, settings. */
const scopes = ['Всё','Память','Задачи','Правила','Секреты','Проекты','Настройки']
</script>
<template>
  <div>
    <header class="head"><h1>Поиск</h1><p>Поиск по всей консоли: память, задачи, правила, секреты, проекты, настройки.</p></header>
    <div class="sbar"><span>⌕</span><input placeholder="Что ищем…" autofocus /></div>
    <div class="scopes"><button v-for="(s,i) in scopes" :key="s" class="fchip" :aria-pressed="i===0">{{ s }}</button></div>
    <div class="empty">Введите запрос, чтобы искать по разделам.</div>
  </div>
</template>
<style scoped>
.head h1 { margin:0 0 4px; font-size:var(--text-xl); font-weight:700; }
.head p { margin:0 0 16px; font-size:var(--text-sm); color:var(--muted); }
.sbar { display:flex; align-items:center; gap:9px; padding:10px 14px; border:1px solid var(--border); border-radius:var(--r-md); background:var(--surface); color:var(--muted); margin-bottom:12px; }
.sbar input { border:0; background:transparent; color:var(--fg); flex:1; outline:none; font-size:var(--text-base); }
.scopes { display:flex; gap:8px; flex-wrap:wrap; margin-bottom:16px; }
.fchip { font-size:var(--text-sm); padding:5px 14px; border:0; border-radius:var(--radius-pill); background:var(--surface-warm); color:var(--fg-2); cursor:pointer; }
.fchip[aria-pressed="true"] { background:var(--accent); color:var(--accent-on); }
.empty { border:1px dashed var(--border); border-radius:var(--r-md); padding:40px; text-align:center; color:var(--muted); background:var(--surface); }
</style>
