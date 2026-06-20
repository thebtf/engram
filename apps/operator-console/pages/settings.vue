<script setup lang="ts">
/** Settings → Runtime flags — demonstrates SwitchRow with the restart-required contract.
 *  Seam: writes go to the flag endpoint; reload=true flags need a server restart to apply. */
import { ref } from 'vue'

const quiet = ref(true)
const graph = ref(true)
const vnextF = ref(false)
const legacyLogin = ref(false)
</script>

<template>
  <div class="wrap">
    <header class="head"><h1>Настройки</h1><p>Среда выполнения и поведение сервера. «restart» — изменение применится после перезапуска.</p></header>

    <section class="card">
      <h3 class="card-t">Среда выполнения</h3>
      <SwitchRow v-model="quiet" cls="live" title="Тихий режим"
        desc="Глушит только injection-хуки; capture и обучение продолжают работать." evidence="ENGRAM_QUIET" />
      <SwitchRow v-model="graph" cls="live" title="Связи знаний (граф)"
        desc="Строит граф концептов и переходов между записями памяти." evidence="ENGRAM_GRAPH" />
      <SwitchRow v-model="vnextF" cls="dormant" title="vNext F (крис­таллизация)"
        desc="Очередь кандидатов в память за флагом. Пока выключено — раздел «На проверку» dormant." evidence="VNEXT_F" :reload="true" />
      <SwitchRow v-model="legacyLogin" cls="live" danger title="Legacy admin-key вход"
        desc="Опасно: разрешает вход по admin-токену в обход OAuth. Держите выключенным." evidence="ENGRAM_AUTH_LEGACY" :reload="true" />
    </section>
  </div>
</template>

<style scoped>
.wrap { max-width:760px; }
.head h1 { margin:0 0 4px; font-size:var(--text-xl); font-weight:700; }
.head p { margin:0 0 16px; font-size:var(--text-sm); color:var(--muted); }
.card { border:1px solid var(--border); border-radius:var(--r-md); background:var(--surface); padding:8px 20px 16px; }
.card-t { margin:14px 0 4px; font-size:var(--text-sm); font-weight:700; color:var(--fg); }
</style>
