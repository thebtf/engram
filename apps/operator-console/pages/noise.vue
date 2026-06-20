<script setup lang="ts">
/** Noise & usefulness — snapshot gauge of noise ratio. Seam: → GET /api/stats/vnext. */
const ratio = 0.41
</script>
<template>
  <div>
    <header class="head"><h1>Шум и польза</h1><p>Доля шумовых инъекций: что показывали агентам против того, что реально использовалось.</p></header>
    <div class="card">
      <div class="gauge" :style="`--p:${ratio*100}`"><div class="gv">{{ ratio.toFixed(2) }}</div><div class="gl">доля шума</div></div>
      <div class="figs">
        <div class="fig"><div class="fn">3589</div><div class="fl">показывали</div></div>
        <div class="fig" style="color:var(--class-live)"><div class="fn">2117</div><div class="fl">использовали</div></div>
        <div class="fig" style="color:var(--state-warn)"><div class="fn">1472</div><div class="fl">без пользы</div></div>
      </div>
    </div>
  </div>
</template>
<style scoped>
.head h1 { margin:0 0 4px; font-size:var(--text-xl); font-weight:700; }
.head p { margin:0 0 16px; font-size:var(--text-sm); color:var(--muted); }
.card { border:1px solid var(--border); border-radius:var(--r-md); background:var(--surface); padding:24px; display:flex; align-items:center; gap:32px; flex-wrap:wrap; }
.gauge { --p:0; width:150px; height:150px; border-radius:50%; flex:none; position:relative; display:grid; place-items:center; background:conic-gradient(var(--state-warn) calc(var(--p)*1%), color-mix(in oklab,var(--class-live),transparent 60%) 0); }
.gauge::after { content:""; position:absolute; inset:14px; border-radius:50%; background:var(--surface); }
.gv { position:relative; font-family:var(--font-mono); font-weight:700; font-size:var(--text-2xl); }
.gl { position:relative; font-size:10px; text-transform:uppercase; letter-spacing:.08em; color:var(--muted); }
.figs { display:flex; gap:28px; }
.fig .fn { font-family:var(--font-mono); font-weight:700; font-size:var(--text-2xl); }
.fig .fl { font-size:11px; color:var(--muted); margin-top:4px; }
</style>
