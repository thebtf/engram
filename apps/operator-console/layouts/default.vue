<script setup lang="ts">
/**
 * Shell — topbar (brand → home, search, density, theme) + status-bearing nav rail +
 * status bar. Mirrors the .od/index.html shell. Nav items carry their honesty dot so
 * the operator sees at a glance which sections are gated/must-build/stale.
 */
import { useNav } from '../composables/useNav'
import { useHonesty } from '../composables/useHonesty'
import { useServerInfo } from '../composables/useMockData'

const { NAV } = useNav()
const info = useServerInfo()
const colorMode = useColorMode()
const density = useState<'comfortable' | 'compact'>('density', () => 'comfortable')
// i18n: t() resolves dictionary keys; setLocale flips the cookie-backed locale in place
// (no_prefix → no navigation). locales drives the language switcher.
const { t, locale, locales, setLocale } = useI18n()

function dotColor(cls: string) { return useHonesty(cls as any).color }
function toggleTheme() { colorMode.preference = colorMode.value === 'dark' ? 'light' : 'dark' }
function cycleLocale() {
  const codes = locales.value.map((l: any) => l.code)
  const next = codes[(codes.indexOf(locale.value) + 1) % codes.length]
  setLocale(next)
}
</script>

<template>
  <div class="shell" :data-density="density">
    <!-- TOPBAR -->
    <header class="topbar">
      <NuxtLink to="/" class="brand" :title="t('shell.brandHome')">
        <span class="glyph">e</span> engram
      </NuxtLink>
      <div class="tspacer" />
      <div class="gsearch"><span>⌕</span><input :placeholder="t('shell.searchPlaceholder')" /></div>
      <div class="seg">
        <button :aria-pressed="density==='comfortable'" @click="density='comfortable'">{{ t('shell.densityComfortable') }}</button>
        <button :aria-pressed="density==='compact'" @click="density='compact'">{{ t('shell.densityCompact') }}</button>
      </div>
      <button class="tbtn lang" :title="t('shell.language')" @click="cycleLocale">{{ String(locale).toUpperCase() }}</button>
      <button class="tbtn" @click="toggleTheme" :title="colorMode.value==='dark'?t('shell.themeToLight'):t('shell.themeToDark')">◐</button>
    </header>

    <div class="body">
      <!-- NAV RAIL -->
      <nav class="nav">
        <div v-for="g in NAV" :key="g.grpKey" class="nav-grp">
          <div v-if="g.grpKey" class="nav-grp-t">{{ t(`nav.groups.${g.grpKey}`) }}</div>
          <NuxtLink v-for="it in g.items" :key="it.id" :to="it.to" class="nav-item" active-class="on">
            <span class="ndot" :style="{ background: it.cls==='stale' ? 'transparent' : dotColor(it.cls), border: it.cls==='stale' ? `1.5px solid ${dotColor(it.cls)}` : '0' }" />
            <span class="lbl">{{ t(`nav.items.${it.labelKey}`) }}</span>
            <span v-if="it.admin" class="lock">{{ t('common.admin') }}</span>
            <span v-if="it.count" class="cnt">{{ it.count }}</span>
          </NuxtLink>
        </div>
      </nav>

      <!-- CONTENT -->
      <main class="content"><slot /></main>
    </div>

    <!-- STATUS BAR -->
    <footer class="statusbar">
      <NuxtLink to="/settings" class="si"><span>{{ info.host }}</span><strong>{{ info.version }}</strong></NuxtLink>
      <span class="sp" />
      <NuxtLink to="/health" class="si warn"><span class="dot" />{{ t('shell.statusDegradation') }} <strong>{{ info.health }}</strong></NuxtLink>
      <NuxtLink to="/noise" class="si warn"><span class="dot" />{{ t('shell.statusNoise') }} <strong>{{ info.noise }}</strong></NuxtLink>
    </footer>
  </div>
</template>

<style scoped>
.shell { display:flex; flex-direction:column; height:100vh; background:var(--bg); color:var(--fg); font-family:var(--font-body); }
.topbar { display:flex; align-items:center; gap:12px; height:48px; padding:0 14px; border-bottom:1px solid var(--border); background:var(--surface); flex:none; }
.brand { display:flex; align-items:center; gap:8px; font-weight:700; letter-spacing:-.02em; color:var(--fg); text-decoration:none; padding:4px 6px; border-radius:var(--r-sm); }
.brand:hover { background:var(--surface-warm); }
.brand .glyph { width:24px; height:24px; border-radius:6px; display:grid; place-items:center; background:var(--accent); color:var(--accent-on); font-weight:800; font-size:12px; }
.tspacer { flex:1; }
.gsearch { display:flex; align-items:center; gap:7px; padding:5px 11px; border:1px solid var(--border); border-radius:var(--r-md); background:var(--bg); width:300px; color:var(--muted); }
.gsearch input { border:0; background:transparent; color:var(--fg); flex:1; outline:none; font-size:var(--text-sm); }
.seg { display:flex; border:1px solid var(--border); border-radius:var(--r-md); overflow:hidden; }
.seg button { border:0; background:transparent; color:var(--muted); font-size:var(--text-xs); padding:5px 10px; cursor:pointer; }
.seg button[aria-pressed="true"] { background:var(--surface-warm); color:var(--fg); }
.tbtn { border:0; background:transparent; color:var(--fg-2); font-size:16px; cursor:pointer; padding:5px 8px; border-radius:var(--r-sm); }
.tbtn:hover { background:var(--surface-warm); }
.tbtn.lang { font-family:var(--font-mono); font-size:var(--text-xs); font-weight:600; letter-spacing:.04em; }
.body { flex:1; display:flex; min-height:0; }
.nav { width:236px; flex:none; border-right:1px solid var(--border); background:var(--surface); padding:10px 8px; overflow-y:auto; }
.shell[data-density="compact"] .nav { width:216px; }
.nav-grp { margin-bottom:14px; }
.nav-grp-t { font-size:10px; font-weight:700; text-transform:uppercase; letter-spacing:.08em; color:var(--muted); padding:0 8px 6px; }
.nav-item { display:flex; align-items:center; gap:9px; padding:7px 8px; border-radius:var(--r-sm); color:var(--fg-2); text-decoration:none; font-size:var(--text-sm); }
.nav-item:hover { background:var(--surface-warm); }
.nav-item.on { background:color-mix(in oklab,var(--accent),transparent 86%); color:var(--fg); font-weight:600; }
.ndot { width:7px; height:7px; border-radius:50%; flex:none; }
.lbl { flex:1; }
.lock { font-size:9px; color:var(--muted); border:1px solid var(--border); border-radius:4px; padding:0 4px; }
.cnt { font-family:var(--font-mono); font-size:11px; color:var(--muted); }
.content { flex:1; overflow-y:auto; padding:20px 24px 90px; min-width:0; }
.statusbar { display:flex; align-items:center; gap:14px; height:26px; padding:0 14px; border-top:1px solid var(--border); background:var(--surface); flex:none; font-size:11px; }
.si { display:flex; align-items:center; gap:6px; color:var(--muted); text-decoration:none; font-family:var(--font-mono); }
.si strong { color:var(--fg-2); }
.si.warn { color:var(--state-warn); }
.si.warn .dot { width:6px; height:6px; border-radius:50%; background:var(--state-warn); }
.sp { flex:1; }
</style>
