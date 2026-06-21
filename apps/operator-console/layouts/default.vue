<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useNav } from '../composables/useNav'
import { useOperatorMemoryLab } from '../composables/useOperatorMemoryLab'
import { useOperatorShellStatus } from '../composables/useOperatorShell'

const { NAV } = useNav()
const shell = useOperatorShellStatus()
const info = shell.info
const memoryLab = useOperatorMemoryLab()
const memories = memoryLab.rows
const route = useRoute()
const router = useRouter()
const colorMode = useColorMode()
const density = useState<'comfortable' | 'compact'>('density', () => 'compact')
const { t, locale, locales, setLocale } = useI18n()

const NAV_COLLAPSE_KEY = 'engram.console.navCollapsed'
const navCollapsed = useState<boolean>('nav-collapsed', () => false)
const navPeek = ref(false)
const peekSuppressed = ref(false)
const mobileNavOpen = ref(false)
const search = ref('')

const NAV_ICONS: Record<string, string> = {
  overview: '<rect x="2" y="2" width="5" height="5" rx="1"/><rect x="9" y="2" width="5" height="5" rx="1"/><rect x="2" y="9" width="5" height="5" rx="1"/><rect x="9" y="9" width="5" height="5" rx="1"/>',
  search: '<circle cx="6.5" cy="6.5" r="4.3"/><path d="M9.6 9.6 L14 14"/>',
  memory: '<path d="M8 2 L14 5 L8 8 L2 5 Z"/><path d="M2 8 L8 11 L14 8"/><path d="M2 11 L8 14 L14 11"/>',
  queue: '<path d="M2.4 8.6 L4.2 3.4 H11.8 L13.6 8.6"/><path d="M2.4 8.6 V13 H13.6 V8.6 H10.5 L9.3 10.4 H6.7 L5.5 8.6 Z"/>',
  noise: '<path d="M3 10.5 V12.5"/><path d="M6.3 7 V12.5"/><path d="M9.6 4 V12.5"/><path d="M12.9 8.5 V12.5"/>',
  graph: '<circle cx="4" cy="4.4" r="1.8"/><circle cx="12.2" cy="5.4" r="1.8"/><circle cx="8" cy="12.4" r="1.8"/><path d="M5.5 5.3 L10.7 6.4 M5 6 L7.2 10.8 M10.6 6.9 L8.8 10.8"/>',
  books: '<path d="M8 3.4 C6.6 2.5 4.4 2.4 2.6 2.9 V12.4 C4.4 11.9 6.6 12 8 12.9 C9.4 12 11.6 11.9 13.4 12.4 V2.9 C11.6 2.4 9.4 2.5 8 3.4 Z"/><path d="M8 3.4 V12.9"/>',
  rules: '<path d="M3 4 H13 M3 8 H10 M3 12 H8"/><path d="M10.7 11.6 L12 12.9 L14 10.4"/>',
  issues: '<circle cx="8" cy="8" r="5.7"/><circle cx="8" cy="8" r="1.55"/>',
  projects: '<path d="M2 4.6 H6.2 L7.7 6.1 H14 V12.6 H2 Z"/>',
  secrets: '<circle cx="5.2" cy="5.4" r="2.7"/><path d="M7.1 7.3 L13 13.2 M11 11.2 V13.4 M13 13.2 H10.8"/>',
  documents: '<path d="M4 2.2 H10 L13 5.2 V14 H4 Z"/><path d="M9.6 2.4 V5.4 H12.6"/>',
  collections: '<path d="M2.3 4 H13.7 V6.1 H2.3 Z"/><path d="M3.4 6.1 V13 H12.6 V6.1"/><path d="M6.4 9 H9.6"/>',
  access: '<path d="M8 2 L13 4.2 V8 C13 11 10.7 13.6 8 14.6 C5.3 13.6 3 11 3 8 V4.2 Z"/><path d="M6.2 8 L7.5 9.4 L10 6.5"/>',
  settings: '<circle cx="8" cy="8" r="2.2"/><path d="M8 1.6 V3.3 M8 12.7 V14.4 M14.4 8 H12.7 M3.3 8 H1.6 M12.53 3.47 L11.3 4.7 M4.7 11.3 L3.47 12.53 M12.53 12.53 L11.3 11.3 M4.7 4.7 L3.47 3.47"/>',
  health: '<path d="M2 8.2 H5 L6.8 4 L9.2 12.4 L11 8.2 H14"/>',
}

const flatNav = computed(() => NAV.flatMap((group) => group.items))
const currentArea = computed(() => {
  const currentPath = normalizePath(route.path)
  const current = flatNav.value.find((item) => normalizePath(item.to) === currentPath)
  return current ? t(`nav.items.${current.labelKey}`) : t('nav.items.overview')
})
const memoryRecordsLabel = computed(() => (
  memoryLab.pending.value && memories.length === 0
    ? t('shell.recordsLoading')
    : t('shell.records', memories.length)
))
const authPostureLabel = computed(() => {
  switch (info.value.authPosture) {
    case 'auth-disabled':
      return t('shell.authDisabled')
    case 'authenticated':
      return t('shell.authAuthenticated')
    case 'locked':
      return t('shell.authLocked')
    default:
      return t('shell.authUnknown')
  }
})

onMounted(() => {
  navCollapsed.value = window.localStorage.getItem(NAV_COLLAPSE_KEY) === '1'
})

function navIcon(id: string) {
  const path = NAV_ICONS[id] || ''
  return `<svg class="nav-ico" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.75" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">${path}</svg>`
}

function normalizePath(path: string) {
  return path === '/' ? path : path.replace(/\/+$/, '')
}

function dotState(cls: string) {
  if (cls === 'dormant') return 'gated'
  if (cls === 'stale') return 'off'
  if (cls === 'mustbuild') return 'mustbuild'
  return 'active'
}

function setNavCollapsed(value: boolean) {
  navCollapsed.value = value
  if (import.meta.client) window.localStorage.setItem(NAV_COLLAPSE_KEY, value ? '1' : '0')
  if (!value) {
    navPeek.value = false
    peekSuppressed.value = false
  }
}

function toggleNavCollapsed() {
  const next = !navCollapsed.value
  if (next) peekSuppressed.value = true
  setNavCollapsed(next)
}

function onNavEnter() {
  if (navCollapsed.value && !peekSuppressed.value) navPeek.value = true
}

function onNavLeave() {
  navPeek.value = false
  peekSuppressed.value = false
}

function toggleTheme() {
  colorMode.preference = colorMode.value === 'dark' ? 'light' : 'dark'
}

function cycleLocale() {
  const codes = locales.value.map((item: any) => item.code)
  const next = codes[(codes.indexOf(locale.value) + 1) % codes.length]
  setLocale(next)
}

function goSearch() {
  const query = search.value.trim()
  void router.push(query ? { path: '/search', query: { q: query } } : '/search')
}
</script>

<template>
  <div
    class="app"
    :class="{ 'nav-collapsed': navCollapsed, 'nav-peek': navPeek }"
    :data-density="density"
  >
    <nav
      class="nav"
      :class="{ open: mobileNavOpen }"
      @pointerenter="onNavEnter"
      @pointerleave="onNavLeave"
    >
      <div class="navhead">
        <NuxtLink to="/" class="navbrand" :title="t('shell.brandHome')" @click="mobileNavOpen = false">
          <span class="glyph">e</span>
          <span class="navbrand-wm">engram</span>
        </NuxtLink>
        <button
          class="navcollapse"
          :aria-label="navCollapsed ? t('shell.expandNav') : t('shell.collapseNav')"
          :aria-pressed="navCollapsed"
          :title="navCollapsed ? t('shell.expandNav') : t('shell.collapseNav')"
          @click="toggleNavCollapsed"
        >
          <svg viewBox="0 0 16 16" width="17" height="17" fill="none" stroke="currentColor" stroke-width="1.4" aria-hidden="true">
            <rect x="1.5" y="2.5" width="13" height="11" rx="2" />
            <line x1="6" y1="2.5" x2="6" y2="13.5" />
          </svg>
        </button>
      </div>

      <div class="nav-scroll">
        <div v-for="group in NAV" :key="group.grpKey" class="nav-grp">
          <div class="gl">{{ t(`nav.groups.${group.grpKey}`) }}</div>
          <NuxtLink
            v-for="item in group.items"
            :key="item.id"
            :to="item.to"
            class="nav-item"
            :class="{ tomb: item.cls === 'stale' }"
            exact-active-class="on"
            :title="t(`nav.items.${item.labelKey}`)"
            @click="mobileNavOpen = false"
          >
            <span class="ndot" :data-s="dotState(item.cls)" />
            <span v-html="navIcon(item.id)" />
            <span class="lbl">{{ t(`nav.items.${item.labelKey}`) }}</span>
            <span v-if="item.cls === 'dormant'" class="flag">{{ item.evidence || t('shell.flag') }}</span>
            <span v-if="item.admin" class="lock">{{ t('common.admin') }}</span>
            <span v-if="item.count" class="cnt">{{ item.count }}</span>
          </NuxtLink>
        </div>
      </div>
    </nav>

    <header class="topbar">
      <button class="tbtn mobile-menu-button" :aria-label="t('shell.mobileMenu')" @click="mobileNavOpen = !mobileNavOpen">☰</button>
      <form class="gsearch-wrap" @submit.prevent="goSearch">
        <div class="gsearch">
          <span>⌕</span>
          <input v-model="search" :placeholder="t('shell.searchPlaceholder')" />
          <kbd>/</kbd>
        </div>
      </form>
      <div class="tspacer" />
      <div class="seg" role="group" :aria-label="t('shell.density')">
        <button :aria-pressed="density === 'comfortable'" @click="density = 'comfortable'">{{ t('shell.densityComfortable') }}</button>
        <button :aria-pressed="density === 'compact'" @click="density = 'compact'">{{ t('shell.densityCompact') }}</button>
      </div>
      <button class="tbtn lang" :title="t('shell.language')" @click="cycleLocale">{{ String(locale).toUpperCase() }}</button>
      <button class="tbtn" @click="toggleTheme" :title="colorMode.value === 'dark' ? t('shell.themeToLight') : t('shell.themeToDark')">◐</button>
      <div class="identity" role="status" :title="t('shell.identityTitle')" :data-auth="info.authPosture">
        <span class="iav">{{ info.identityInitials }}</span>
        <span class="iwho">
          <span class="iname">{{ info.identityName }}</span>
          <span class="imeta">{{ authPostureLabel }}</span>
        </span>
        <span class="icaret">⌄</span>
      </div>
    </header>

    <main class="content">
      <slot />
    </main>

    <footer class="statusbar">
      <span class="si"><span class="dot" />{{ t('shell.online') }}</span>
      <NuxtLink to="/settings" class="si"><span>{{ info.host }}</span><strong>{{ info.version }}</strong></NuxtLink>
      <span class="si">{{ t('shell.postgres') }}</span>
      <span class="si">{{ currentArea }}</span>
      <span class="ssp" />
      <NuxtLink to="/health" class="si warn"><span class="dot" />{{ t('shell.statusDegradation') }} <strong>{{ info.health }}</strong></NuxtLink>
      <span class="si">{{ memoryRecordsLabel }}</span>
      <NuxtLink to="/noise" class="si warn"><span class="dot" />{{ t('shell.statusNoise') }} <strong>{{ info.noise }}</strong></NuxtLink>
      <NuxtLink to="/queue" class="si">{{ t('shell.reviewQueue', 7) }}</NuxtLink>
      <span class="si">{{ t('shell.uptime', { value: info.uptime }) }}</span>
    </footer>
  </div>
</template>

<style scoped>
.app {
  --nav-rail-w:60px;
  --nav-w:236px;
  position:relative;
  display:grid;
  grid-template-columns:var(--nav-w) 1fr;
  grid-template-rows:48px 1fr 26px;
  grid-template-areas:"nav topbar" "nav content" "nav statusbar";
  height:100vh;
  background:var(--bg);
  color:var(--fg);
  font-family:var(--font-body);
  transition:grid-template-columns var(--motion-base) var(--ease-standard);
}
.app[data-density="comfortable"] { --nav-w:260px; }
.app[data-density="compact"] { --nav-w:216px; }
.app.nav-collapsed { grid-template-columns:var(--nav-rail-w) 1fr; }
.topbar { grid-area:topbar; }
.content { grid-area:content; }
.statusbar { grid-area:statusbar; }
.nav {
  grid-area:nav;
  position:absolute;
  inset:0 auto 0 0;
  z-index:30;
  display:flex;
  flex-direction:column;
  width:var(--nav-w);
  min-width:0;
  overflow:hidden;
  background:var(--surface);
  border-right:1px solid var(--border);
  transition:width var(--motion-base) var(--ease-standard);
}
.app.nav-collapsed > .nav { width:var(--nav-rail-w); }
.app.nav-collapsed.nav-peek > .nav { width:var(--nav-w); z-index:55; box-shadow:var(--elev-raised); }
.navhead { display:flex; align-items:center; gap:6px; flex:none; height:48px; padding:0 8px 0 14px; border-bottom:1px solid var(--border); }
.navbrand { display:flex; align-items:center; gap:9px; flex:1; min-width:0; color:var(--fg); text-decoration:none; padding:5px 6px; border-radius:var(--r-sm); font-weight:800; letter-spacing:-.02em; }
.navbrand:hover { background:var(--surface-warm); text-decoration:none; }
.navbrand .glyph { width:26px; height:26px; border-radius:7px; display:grid; place-items:center; flex:none; color:var(--accent-on); background:var(--accent); font-weight:800; font-size:13px; }
.navbrand-wm { transition:opacity var(--motion-fast) var(--ease-standard); overflow:hidden; }
.navcollapse { display:inline-flex; align-items:center; justify-content:center; width:32px; height:32px; flex:none; border:0; border-radius:var(--r-sm); background:transparent; color:var(--muted); cursor:pointer; }
.navcollapse:hover { background:var(--surface-warm); color:var(--fg); }
.navcollapse svg line { transition:opacity var(--motion-fast) var(--ease-standard); }
.app.nav-collapsed .navcollapse { color:var(--accent); }
.app.nav-collapsed .navcollapse svg line { opacity:.35; }
.nav-scroll { flex:1; min-height:0; overflow-y:auto; padding:var(--space-3) var(--space-2) var(--space-6); }
.nav-grp { margin-bottom:var(--space-4); }
.nav-grp .gl { font-size:10px; text-transform:uppercase; letter-spacing:.09em; color:var(--muted); font-weight:700; padding:4px 10px 6px; white-space:nowrap; }
.nav-item .lbl,
.nav-item .cnt,
.nav-item .flag,
.nav-item .lock,
.navbrand-wm,
.nav-grp .gl { transition:opacity var(--motion-fast) var(--ease-standard); }
.app.nav-collapsed:not(.nav-peek) > .nav .navhead { padding:0 10px 0 0; justify-content:flex-end; }
.app.nav-collapsed:not(.nav-peek) > .nav .navbrand { flex:none; justify-content:center; padding:5px 0; }
.app.nav-collapsed:not(.nav-peek) > .nav .navbrand-wm { opacity:0; width:0; }
.app.nav-collapsed:not(.nav-peek) > .nav .navcollapse { display:none; }
.app.nav-collapsed:not(.nav-peek) > .nav .nav-grp .gl { height:0; padding:0; margin:0; opacity:0; overflow:hidden; }
.app.nav-collapsed:not(.nav-peek) > .nav .nav-item { justify-content:center; padding:9px 0; gap:6px; }
.app.nav-collapsed:not(.nav-peek) > .nav .nav-item .lbl,
.app.nav-collapsed:not(.nav-peek) > .nav .nav-item .cnt,
.app.nav-collapsed:not(.nav-peek) > .nav .nav-item .flag,
.app.nav-collapsed:not(.nav-peek) > .nav .nav-item .lock { opacity:0; width:0; padding:0; margin:0; overflow:hidden; pointer-events:none; }
.app.nav-collapsed:not(.nav-peek) > .nav .nav-item :deep(.nav-ico) { width:19px; height:19px; opacity:.78; }
.app.nav-collapsed:not(.nav-peek) > .nav .nav-item .ndot { width:6px; height:6px; }
.nav-item { display:flex; align-items:center; gap:9px; width:100%; min-width:0; color:var(--fg-2); text-decoration:none; border-radius:var(--r-sm); padding:7px 10px; font-size:var(--text-sm); line-height:1.2; }
.app[data-density="comfortable"] .nav-item { padding:8px 12px; }
.app[data-density="compact"] .nav-scroll { padding:8px 6px 18px; }
.app[data-density="compact"] .nav-item { padding:5px 8px; gap:7px; }
.nav-item:hover { background:var(--surface-warm); color:var(--fg); text-decoration:none; }
.nav-item.on { background:color-mix(in oklab,var(--accent),transparent 86%); color:var(--fg); font-weight:600; }
.nav-item.tomb { color:var(--muted); }
.nav-item .ndot { width:7px; height:7px; border-radius:50%; flex:none; }
.nav-item .ndot[data-s="active"] { background:var(--class-live); }
.nav-item .ndot[data-s="gated"] { background:var(--class-dormant); }
.nav-item .ndot[data-s="mustbuild"] { background:var(--class-mustbuild); }
.nav-item .ndot[data-s="off"] { background:transparent; box-shadow:inset 0 0 0 1.5px var(--class-stale); }
.nav-item :deep(.nav-ico) { width:15px; height:15px; flex:none; opacity:.62; transition:opacity var(--motion-fast) var(--ease-standard); }
.nav-item:hover :deep(.nav-ico) { opacity:.85; }
.nav-item.on :deep(.nav-ico) { opacity:1; color:var(--accent); }
.nav-item.tomb :deep(.nav-ico) { opacity:.4; }
.nav-item .lbl { flex:1; min-width:0; overflow:hidden; text-overflow:ellipsis; white-space:nowrap; }
.nav-item .cnt { font-family:var(--font-mono); font-size:10px; font-weight:700; padding:1px 6px; border-radius:var(--radius-pill); background:color-mix(in oklab,var(--accent),transparent 84%); color:var(--fg); }
.nav-item .flag,
.nav-item .lock { font-size:11px; color:var(--muted); }
.topbar { display:flex; align-items:center; gap:var(--space-3); padding:0 var(--space-4); background:var(--surface); border-bottom:1px solid var(--border); min-width:0; }
.gsearch-wrap { position:relative; }
.gsearch { display:flex; align-items:center; gap:7px; width:min(360px,30vw); min-width:280px; background:var(--surface-warm); border:1px solid var(--border); border-radius:var(--r-sm); padding:5px 10px; color:var(--muted); font-size:var(--text-sm); transition:border-color var(--motion-fast) var(--ease-standard), box-shadow var(--motion-fast) var(--ease-standard), background var(--motion-fast) var(--ease-standard); }
.gsearch:focus-within { background:var(--surface); border-color:var(--accent); box-shadow:var(--focus-ring); }
.gsearch input { border:0; background:transparent; color:var(--fg); font-size:var(--text-sm); width:100%; outline:none; }
.gsearch kbd { font-family:var(--font-mono); font-size:10px; border:1px solid var(--border); border-radius:4px; padding:1px 5px; color:var(--muted); }
.tspacer { flex:1; }
.seg { display:inline-flex; background:var(--surface-warm); border:1px solid var(--border); border-radius:var(--r-sm); overflow:hidden; }
.seg button { border:0; background:transparent; padding:5px 9px; font-size:10px; font-weight:700; color:var(--muted); cursor:pointer; }
.seg button[aria-pressed="true"] { background:var(--surface); color:var(--fg); }
.tbtn { display:inline-flex; align-items:center; gap:6px; height:30px; padding:0 10px; border-radius:var(--r-sm); background:var(--surface); border:1px solid var(--border); color:var(--fg-2); font-size:var(--text-xs); font-weight:600; cursor:pointer; }
.tbtn:hover { border-color:var(--accent); color:var(--fg); }
.tbtn.lang { font-family:var(--font-mono); letter-spacing:.04em; }
.topbar .mobile-menu-button { display:none; }
.identity { display:inline-flex; align-items:center; gap:8px; height:32px; padding:3px 10px 3px 3px; border-radius:var(--radius-pill); background:var(--surface-warm); border:1px solid var(--border); color:var(--fg); font-weight:600; font-size:var(--text-xs); max-width:230px; }
.identity .iav { width:26px; height:26px; border-radius:50%; flex:none; display:grid; place-items:center; font-weight:800; font-size:11px; color:#fff; letter-spacing:-.02em; background:var(--accent); overflow:hidden; }
.identity .iwho { display:flex; flex-direction:column; line-height:1.05; min-width:0; }
.identity .iname { font-weight:700; color:var(--fg); white-space:nowrap; overflow:hidden; text-overflow:ellipsis; }
.identity .imeta { font-size:9px; font-weight:600; color:var(--muted); font-family:var(--font-mono); white-space:nowrap; overflow:hidden; text-overflow:ellipsis; }
.identity .icaret { color:var(--muted); font-size:9px; flex:none; }
.content { min-width:0; overflow-y:auto; padding:22px 24px 90px; }
.statusbar { display:flex; align-items:center; gap:var(--space-4); padding:0 var(--space-4); background:var(--surface); border-top:1px solid var(--border); font-size:11px; color:var(--muted); font-family:var(--font-mono); min-width:0; overflow:hidden; }
.statusbar .si { display:inline-flex; align-items:center; gap:6px; white-space:nowrap; color:var(--muted); text-decoration:none; }
.statusbar .si strong { color:var(--fg-2); }
.statusbar .si .dot { width:6px; height:6px; border-radius:50%; background:var(--class-live); flex:none; }
.statusbar .si.warn { color:var(--state-warn); }
.statusbar .si.warn .dot { background:var(--state-warn); }
.statusbar .ssp { flex:1; }
@media (pointer:coarse) {
  .nav-item,
  .tbtn,
  .seg button { min-height:40px; }
}
@media (max-width:900px) {
  .app,
  .app.nav-collapsed { grid-template-columns:1fr; grid-template-areas:"topbar" "content" "statusbar"; }
  .topbar { gap:8px; padding:0 10px; }
  .mobile-menu-button { display:inline-flex; }
  .gsearch { min-width:0; width:min(360px,48vw); }
  .app > .nav { position:fixed; top:0; bottom:0; left:0; width:236px; transform:translateX(-100%); transition:transform var(--motion-base) var(--ease-standard); z-index:60; }
  .app > .nav.open { transform:none; }
  .navcollapse,
  .identity,
  .statusbar .si:nth-last-child(-n+3) { display:none; }
  .content { padding:18px 14px 70px; }
}
</style>
