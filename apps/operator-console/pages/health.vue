<script setup lang="ts">
/** Health — server health snapshot. Seam: useModels()/selfcheck → GET /api/selfcheck, /api/stats/vnext. */
import { useModels, useServerInfo } from '../composables/useMockData'
const models = useModels(), info = useServerInfo()
const subsys = [['PostgreSQL','ok'],['Worker','ok'],['Vault','ok'],['Поиск похожего','warn'],['Пересортировка','off'],['LLM','off'],['Связи знаний','ok']] as const
const dot: Record<string,string> = { ok:'var(--class-live)', warn:'var(--state-warn)', off:'var(--muted)' }
const hcls: Record<string,'live'|'dormant'|'stale'> = { ok:'live', standby:'live', degraded:'dormant' }
</script>
<template>
  <div>
    <header class="head"><h1>Состояние</h1><p>Здоровье сервера: сервисы, деградации, модели, недостающие endpoint’ы.</p></header>
    <div class="cards">
      <div class="card">
        <h3>Покрытие эмбеддингами <span class="src">/api/stats/vnext</span></h3>
        <div class="figs">
          <div class="fig"><div class="fn">2837</div><div class="fl">фрагментов</div></div>
          <div class="fig"><div class="fn" style="color:var(--class-live)">2825</div><div class="fl">с вектором</div></div>
          <div class="fig"><div class="fn">1536</div><div class="fl">размерность</div></div>
        </div>
        <div class="subsys"><span v-for="s in subsys" :key="s[0]" class="s"><i :style="{ background: dot[s[1]] }" />{{ s[0] }}</span></div>
      </div>
      <div class="card">
        <h3>Модели <span class="src">/api/stats</span></h3>
        <div v-for="m in models" :key="m.id" class="mrow">
          <code>{{ m.id }}</code><HonestyBadge :cls="hcls[m.health]" :evidence="m.health==='degraded' ? 'degraded' : undefined" /><span class="cost">{{ m.costs }}</span>
        </div>
      </div>
      <div class="card mb">
        <h3 style="color:var(--class-mustbuild)">Миграции</h3>
        <p class="mid">Нужен endpoint <code>GET /api/migrations</code> для честного отображения схемы.</p>
      </div>
    </div>
  </div>
</template>
<style scoped>
.head h1 { margin:0 0 4px; font-size:var(--text-xl); font-weight:700; }
.head p { margin:0 0 16px; font-size:var(--text-sm); color:var(--muted); }
.cards { display:grid; grid-template-columns:repeat(auto-fit,minmax(280px,1fr)); gap:14px; }
.card { border:1px solid var(--border); border-radius:var(--r-md); background:var(--surface); padding:16px 20px; }
.card h3 { margin:0 0 14px; font-size:var(--text-sm); font-weight:600; display:flex; }
.card h3 .src { margin-left:auto; font-family:var(--font-mono); font-size:10px; color:var(--muted); font-weight:500; }
.figs { display:flex; gap:28px; margin-bottom:14px; }
.fig .fn { font-family:var(--font-mono); font-weight:700; font-size:var(--text-2xl); }
.fig .fl { font-size:11px; color:var(--muted); margin-top:4px; }
.subsys { display:flex; flex-wrap:wrap; gap:10px; }
.subsys .s { display:inline-flex; align-items:center; gap:6px; font-size:var(--text-xs); color:var(--fg-2); }
.subsys .s i { width:7px; height:7px; border-radius:50%; }
.mrow { display:flex; align-items:center; gap:10px; padding:7px 0; border-bottom:1px solid var(--border-soft); font-size:var(--text-sm); }
.mrow code { font-family:var(--font-mono); font-size:var(--text-xs); }
.mrow .cost { margin-left:auto; font-family:var(--font-mono); font-size:11px; color:var(--muted); }
.card.mb { border-color:color-mix(in oklab,var(--class-mustbuild),transparent 55%); }
.mid { font-size:var(--text-sm); color:var(--fg-2); margin:0; }
</style>
