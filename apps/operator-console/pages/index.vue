<script setup lang="ts">
/** Overview (home) — the surface the logo links to. Honest snapshots of the live arrays;
 *  trend-over-time is must-build until a daily-history endpoint exists. */
import { useNav } from '../composables/useNav'
import { useMemories, useIssues, useModels, useServerInfo } from '../composables/useMockData'

const { flat } = useNav()
const mem = useMemories(), iss = useIssues(), models = useModels(), info = useServerInfo()

const memTiers = (['semantic','procedural','episodic'] as const).map(t => mem.filter(m => m.tier === t).length)
const memTotal = mem.length || 1
let acc = 0
const donutStops = memTiers.map(n => (acc += n / memTotal * 100))
const tierColors = ['var(--accent)', 'var(--class-live)', 'var(--state-warn)']

const tasks = [
  ['Открытые', iss.filter(i => i.status === 'open').length, 'var(--state-warn)'],
  ['В работе', iss.filter(i => ['acknowledged','reopened'].includes(i.status)).length, 'var(--accent)'],
  ['Решённые', iss.filter(i => i.status === 'resolved').length, 'var(--class-live)'],
  ['Закрытые', iss.filter(i => i.status === 'closed').length, 'var(--muted)'],
] as const
const tMax = Math.max(1, ...tasks.map(t => t[1] as number))

const mh = [
  ['ok', models.filter(m => m.health === 'ok').length, 'var(--class-live)'],
  ['standby', models.filter(m => m.health === 'standby').length, 'var(--muted)'],
  ['degraded', models.filter(m => m.health === 'degraded').length, 'var(--state-warn)'],
] as const
const mhTotal = models.length || 1

const attention = [
  { dot: 'var(--state-warn)', txt: 'Модель «fallback/gpt-4-1-mini» деградирует', to: '/health' },
  { dot: 'var(--class-dormant)', txt: '7 кандидатов ждут проверки (за флагом VNEXT_F)', to: '/queue' },
  { dot: 'var(--class-mustbuild)', txt: 'Книги-контекст — конвейера обработки ещё нет', to: '/books' },
  { dot: 'var(--state-warn)', txt: 'Шум памяти 0.41 — выше нормы', to: '/noise' },
]
const cards = flat.filter(i => !['overview','search'].includes(i.id))
</script>

<template>
  <div>
    <header class="ov-hero">
      <div><h1>engram · обзор</h1><p>{{ info.host }} · {{ info.version }} · работает {{ info.uptime }}</p></div>
      <div class="ov-id"><span class="pill warn">деградация · {{ info.health }}</span><span class="pill">шум {{ info.noise }}</span></div>
    </header>

    <section class="ov-attention">
      <h3>⚠ Требует внимания</h3>
      <NuxtLink v-for="a in attention" :key="a.txt" :to="a.to" class="att-row">
        <span class="dot" :style="{ background: a.dot }" /><span>{{ a.txt }}</span><span class="go">→</span>
      </NuxtLink>
    </section>

    <div class="ov-viz">
      <div class="vz">
        <h4>Состав памяти <span class="src">{{ mem.length }} записей</span></h4>
        <div class="donut-row">
          <div class="donut" :style="`--c1:${tierColors[0]};--s1:${donutStops[0]};--c2:${tierColors[1]};--s2:${donutStops[1]};--c3:${tierColors[2]};--s3:${donutStops[2]}`">
            <span class="dn">{{ mem.length }}</span><span class="dl">всего</span>
          </div>
          <div class="donut-legend">
            <div v-for="(t,i) in ['semantic','procedural','episodic']" :key="t" class="dlg">
              <span class="sw" :style="{ background: tierColors[i] }" />{{ t }}<span class="dv">{{ memTiers[i] }}</span>
            </div>
          </div>
        </div>
      </div>
      <div class="vz">
        <h4>Задачи по статусу <span class="src">{{ iss.length }} всего</span></h4>
        <div class="hbars">
          <div v-for="t in tasks" :key="t[0]" class="hbar">
            <span class="hl">{{ t[0] }}</span>
            <span class="ht"><i :style="{ width: Math.round((t[1] as number)/tMax*100)+'%', background: t[2] }" /></span>
            <span class="hv">{{ t[1] }}</span>
          </div>
        </div>
      </div>
      <div class="vz">
        <h4>Здоровье моделей <span class="src">/api/stats</span></h4>
        <div class="segbar"><i v-for="m in mh" :key="m[0]" :style="{ width: Math.round((m[1] as number)/mhTotal*100)+'%', background: m[2] }" /></div>
        <div class="seg-legend"><span v-for="m in mh" :key="m[0]"><span class="sw" :style="{ background: m[2] }" />{{ m[0] }} · {{ m[1] }}</span></div>
        <div class="vz-trend"><svg viewBox="0 0 120 30" preserveAspectRatio="none"><line x1="0" y1="15" x2="120" y2="15" stroke="currentColor" stroke-width="1.5" stroke-dasharray="3 5" opacity=".6"/></svg><span class="tt">Доступность по дням — <b style="color:var(--class-mustbuild)">must-build</b>; сейчас снимок.</span></div>
      </div>
    </div>

    <div class="ov-section-t">Разделы</div>
    <div class="ov-grid">
      <NuxtLink v-for="c in cards" :key="c.id" :to="c.to" class="ov-card">
        <div class="ov-top"><span class="ov-name">{{ c.label }}</span><span class="ov-arr">→</span></div>
        <HonestyBadge :cls="c.cls" :evidence="c.evidence" />
      </NuxtLink>
    </div>
  </div>
</template>

<style scoped>
.ov-hero { display:flex; align-items:center; justify-content:space-between; gap:16px; flex-wrap:wrap; margin-bottom:18px; }
.ov-hero h1 { margin:0 0 4px; font-size:var(--text-2xl); font-weight:700; }
.ov-hero p { margin:0; font-size:var(--text-sm); color:var(--muted); }
.pill { font-size:var(--text-xs); padding:3px 9px; border-radius:var(--radius-pill); border:1px solid var(--border); color:var(--fg-2); }
.pill.warn { color:var(--state-warn); border-color:color-mix(in oklab,var(--state-warn),transparent 60%); background:color-mix(in oklab,var(--state-warn),transparent 88%); }
.ov-attention { border:1px solid color-mix(in oklab,var(--state-warn),transparent 60%); border-radius:var(--r-md); background:color-mix(in oklab,var(--state-warn),transparent 93%); padding:16px 20px; margin-bottom:16px; }
.ov-attention h3 { margin:0 0 9px; font-size:var(--text-sm); font-weight:700; color:var(--state-warn); }
.att-row { display:flex; align-items:center; gap:10px; font-size:var(--text-sm); color:var(--fg-2); padding:5px 0; text-decoration:none; }
.att-row:hover { color:var(--fg); }
.att-row .dot { width:7px; height:7px; border-radius:50%; flex:none; }
.att-row .go { margin-left:auto; color:var(--muted); }
.ov-viz { display:grid; grid-template-columns:repeat(auto-fit,minmax(290px,1fr)); gap:14px; }
.vz { border:1px solid var(--border); border-radius:var(--r-md); background:var(--surface); padding:16px 20px; }
.vz h4 { margin:0 0 14px; font-size:var(--text-sm); font-weight:600; display:flex; align-items:center; gap:7px; }
.vz h4 .src { margin-left:auto; font-family:var(--font-mono); font-size:10px; color:var(--muted); font-weight:500; }
.donut-row { display:flex; align-items:center; gap:18px; }
.donut { --p:0; width:118px; height:118px; border-radius:50%; flex:none; position:relative; display:grid; place-items:center; background:conic-gradient(var(--c1) calc(var(--s1)*1%), var(--c2) 0 calc(var(--s2)*1%), var(--c3) 0 calc(var(--s3)*1%), var(--muted) 0); }
.donut::after { content:""; position:absolute; inset:15px; border-radius:50%; background:var(--surface); box-shadow:inset 0 0 0 1px var(--border-soft); }
.donut .dn { position:relative; font-family:var(--font-mono); font-weight:700; font-size:var(--text-xl); }
.donut .dl { position:relative; font-size:9px; text-transform:uppercase; letter-spacing:.08em; color:var(--muted); }
.donut-legend { display:flex; flex-direction:column; gap:7px; flex:1; }
.dlg { display:flex; align-items:center; gap:8px; font-size:var(--text-sm); color:var(--fg-2); }
.dlg .sw { width:10px; height:10px; border-radius:3px; flex:none; }
.dlg .dv { margin-left:auto; font-family:var(--font-mono); font-weight:600; }
.hbars { display:flex; flex-direction:column; gap:11px; }
.hbar { display:grid; grid-template-columns:96px 1fr 34px; align-items:center; gap:10px; font-size:var(--text-sm); }
.hbar .hl { color:var(--fg-2); }
.hbar .ht { height:9px; border-radius:var(--radius-pill); background:var(--border-soft); overflow:hidden; }
.hbar .ht i { display:block; height:100%; border-radius:var(--radius-pill); }
.hbar .hv { font-family:var(--font-mono); font-weight:600; text-align:right; }
.segbar { display:flex; height:14px; border-radius:var(--radius-pill); overflow:hidden; background:var(--border-soft); margin-bottom:12px; }
.seg-legend { display:flex; gap:16px; flex-wrap:wrap; font-size:var(--text-xs); color:var(--muted); }
.seg-legend span { display:inline-flex; align-items:center; gap:6px; }
.seg-legend .sw { width:9px; height:9px; border-radius:2px; }
.vz-trend { display:flex; align-items:center; gap:12px; margin-top:13px; padding-top:12px; border-top:1px solid var(--border-soft); }
.vz-trend svg { width:120px; height:30px; flex:none; color:var(--class-mustbuild); }
.vz-trend .tt { font-size:var(--text-xs); color:var(--muted); }
.ov-section-t { font-size:var(--text-xs); font-weight:700; text-transform:uppercase; letter-spacing:.07em; color:var(--muted); margin:22px 0 11px; }
.ov-grid { display:grid; grid-template-columns:repeat(auto-fill,minmax(220px,1fr)); gap:14px; }
.ov-card { border:1px solid var(--border); border-radius:var(--r-md); background:var(--surface); padding:14px 18px; text-decoration:none; display:flex; flex-direction:column; gap:8px; }
.ov-card:hover { border-color:var(--accent); background:var(--surface-warm); }
.ov-top { display:flex; align-items:center; }
.ov-name { font-size:var(--text-sm); font-weight:600; color:var(--fg); }
.ov-arr { margin-left:auto; color:var(--muted); }
</style>
