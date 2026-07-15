<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useNav, type NavItem } from '../composables/useNav'
import { operatorFetchJson } from '../composables/useOperatorApi'
import { useOperatorMemoryLab } from '../composables/useOperatorMemoryLab'
import { useOperatorQueue } from '../composables/useOperatorQueue'
import { useOperatorShellStatus } from '../composables/useOperatorShell'

const { NAV } = useNav()
const shell = useOperatorShellStatus()
const info = shell.info
const memoryLab = useOperatorMemoryLab()
const queue = useOperatorQueue()
const memories = memoryLab.rows
const route = useRoute()
const router = useRouter()
const colorMode = useColorMode()
const density = useState<'comfortable' | 'compact'>('density', () => 'compact')
const { t, locale, locales, setLocale } = useI18n()
const { settingsModalOpen, settingsModalTab, openSettingsModal } = useSettingsModal()

const NAV_COLLAPSE_KEY = 'engram.console.navCollapsed'
const navCollapsed = useState<boolean>('nav-collapsed', () => false)
const navPeek = ref(false)
const peekSuppressed = ref(false)
const mobileNavOpen = ref(false)
const mobileMenuButton = ref<HTMLButtonElement | null>(null)
const mobileNav = ref<HTMLElement | null>(null)
const compactViewport = ref(false)
const previousBodyOverflow = ref<string | null>(null)
const search = ref('')
const identityMenuOpen = ref(false)
const identityMenuRef = ref<HTMLElement | null>(null)
const profileModalOpen = ref(false)
const logoutInFlight = ref(false)

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
  access: '<path d="M8 2 L13 4.2 V8 C13 11 10.7 13.6 8 14.6 C5.3 13.6 3 11 3 8 V4.2 Z"/><path d="M6.2 8 L7.5 9.4 L10 6.5"/>',
  settings: '<circle cx="8" cy="8" r="2.2"/><path d="M8 1.6 V3.3 M8 12.7 V14.4 M14.4 8 H12.7 M3.3 8 H1.6 M12.53 3.47 L11.3 4.7 M4.7 11.3 L3.47 12.53 M12.53 12.53 L11.3 11.3 M4.7 4.7 L3.47 3.47"/>',
  health: '<path d="M2 8.2 H5 L6.8 4 L9.2 12.4 L11 8.2 H14"/>',
}

const flatNav = computed(() => NAV.value.flatMap((group) => group.items))
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
const reviewQueueLabel = computed(() => (
  ['live', 'empty'].includes(queue.loadState.value.kind)
    ? t('shell.reviewQueue', queue.rows.length)
    : t('nav.items.queue')
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
const backendStatusLabel = computed(() => t(`shell.backend.${info.value.backendStatus}`))

onMounted(() => {
  navCollapsed.value = window.localStorage.getItem(NAV_COLLAPSE_KEY) === '1'
  syncViewport()
  window.addEventListener('resize', syncViewport)
  window.addEventListener('pointerdown', onDocumentPointerDown)
  window.addEventListener('keydown', onDocumentKeydown)
})
const canLogout = computed(() => info.value.authenticated && !info.value.authDisabled)
const logoutTitle = computed(() => {
  if (logoutInFlight.value) return t('shell.profileMenuLogoutPending')
  if (canLogout.value) return t('shell.profileMenuLogout')
  if (info.value.authDisabled) return t('shell.profileMenuLogoutAuthDisabled')
  if (info.value.authPosture === 'locked') return t('shell.profileMenuLogoutLocked')
  return t('shell.profileMenuLogoutUnavailable')
})

onBeforeUnmount(() => {
  if (!import.meta.client) return
  window.removeEventListener('resize', syncViewport)
  window.removeEventListener('pointerdown', onDocumentPointerDown)
  window.removeEventListener('keydown', onDocumentKeydown)
  if (previousBodyOverflow.value !== null) {
    document.body.style.overflow = previousBodyOverflow.value
    previousBodyOverflow.value = null
  }
})

watch(() => route.fullPath, () => closeMobileNav())
watch([mobileNavOpen, compactViewport], ([open, compact]) => {
  if (!import.meta.client) return
  if (open && compact) {
    if (previousBodyOverflow.value === null) previousBodyOverflow.value = document.body.style.overflow
    document.body.style.overflow = 'hidden'
    return
  }
  if (previousBodyOverflow.value !== null) {
    document.body.style.overflow = previousBodyOverflow.value
    previousBodyOverflow.value = null
  }
})

function syncViewport() {
  compactViewport.value = window.innerWidth <= 980
  if (!compactViewport.value) mobileNavOpen.value = false
}

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

function handleNavItemClick(event: MouseEvent, item: NavItem) {
  closeMobileNav()
  if (item.id !== 'settings') return
  event.preventDefault()
  openSettingsModal('general')
}

function openMobileNav() {
  mobileNavOpen.value = true
  void nextTick(() => mobileNav.value?.querySelector<HTMLElement>('a, button')?.focus())
}

function closeMobileNav() {
  if (!mobileNavOpen.value) return
  mobileNavOpen.value = false
  void nextTick(() => mobileMenuButton.value?.focus())
}

function toggleMobileNav() {
  if (mobileNavOpen.value) closeMobileNav()
  else openMobileNav()
}

function toggleIdentityMenu() {
  identityMenuOpen.value = !identityMenuOpen.value
}

function closeIdentityMenu() {
  identityMenuOpen.value = false
}

function openIdentitySettings() {
  closeIdentityMenu()
  openSettingsModal('general')
}

function openIdentityProfile() {
  closeIdentityMenu()
  profileModalOpen.value = true
}

async function logoutIdentity() {
  if (!canLogout.value || logoutInFlight.value) return
  logoutInFlight.value = true
  closeIdentityMenu()
  try {
    await operatorFetchJson('/api/auth/logout', { method: 'POST' }, 'shell-auth-logout')
    await shell.refresh()
  } finally {
    logoutInFlight.value = false
  }
}

function onDocumentPointerDown(event: PointerEvent) {
  const target = event.target
  if (!identityMenuOpen.value || !(target instanceof Node)) return
  if (!identityMenuRef.value?.contains(target)) closeIdentityMenu()
}

function onDocumentKeydown(event: KeyboardEvent) {
  if (mobileNavOpen.value && compactViewport.value && event.key === 'Tab') {
    const focusable = [...(mobileNav.value?.querySelectorAll<HTMLElement>('a[href], button:not(:disabled)') || [])]
    if (focusable.length) {
      const first = focusable[0]
      const last = focusable[focusable.length - 1]
      if (event.shiftKey && document.activeElement === first) {
        event.preventDefault()
        last.focus()
      } else if (!event.shiftKey && document.activeElement === last) {
        event.preventDefault()
        first.focus()
      }
    }
  }
  if (event.key === 'Escape') {
    closeIdentityMenu()
    closeMobileNav()
  }
}
</script>

<template>
  <div
    class="app"
    :class="{ 'nav-collapsed': navCollapsed, 'nav-peek': navPeek, 'mobile-nav-open': mobileNavOpen }"
    :data-density="density"
  >
    <nav
      ref="mobileNav"
      id="primary-navigation"
      class="nav"
      :class="{ open: mobileNavOpen }"
      :aria-hidden="compactViewport && !mobileNavOpen"
      :inert="compactViewport && !mobileNavOpen"
      @pointerenter="onNavEnter"
      @pointerleave="onNavLeave"
    >
      <div class="navhead">
        <NuxtLink to="/" class="navbrand" :title="t('shell.brandHome')" @click="closeMobileNav">
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
            @click="handleNavItemClick($event, item)"
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

    <button v-if="mobileNavOpen" class="nav-scrim" :aria-label="t('shell.mobileMenu')" @click="closeMobileNav" />

    <header class="topbar" :inert="compactViewport && mobileNavOpen">
      <button ref="mobileMenuButton" class="tbtn mobile-menu-button" type="button" :aria-label="t('shell.mobileMenu')" :aria-expanded="mobileNavOpen" aria-controls="primary-navigation" @click="toggleMobileNav">☰</button>
      <form class="gsearch-wrap" @submit.prevent="goSearch">
        <div class="gsearch">
          <span>⌕</span>
          <input v-model="search" name="operator-console-search" :placeholder="t('shell.searchPlaceholder')" />
          <kbd>/</kbd>
        </div>
      </form>
      <div class="tspacer" />
      <div class="seg topbar-secondary" role="group" :aria-label="t('shell.density')">
        <button :aria-pressed="density === 'comfortable'" @click="density = 'comfortable'">{{ t('shell.densityComfortable') }}</button>
        <button :aria-pressed="density === 'compact'" @click="density = 'compact'">{{ t('shell.densityCompact') }}</button>
      </div>
      <button class="tbtn lang topbar-secondary" :title="t('shell.language')" @click="cycleLocale">{{ String(locale).toUpperCase() }}</button>
      <button class="tbtn topbar-secondary" @click="toggleTheme" :title="colorMode.value === 'dark' ? t('shell.themeToLight') : t('shell.themeToDark')">◐</button>
      <div ref="identityMenuRef" class="identity-wrap">
        <button
          class="identity"
          type="button"
          :title="t('shell.identityTitle')"
          :aria-label="t('shell.profileMenu')"
          :aria-expanded="identityMenuOpen"
          aria-haspopup="menu"
          :data-auth="info.authPosture"
          @click="toggleIdentityMenu"
        >
          <span class="iav">{{ info.identityInitials }}</span>
          <span class="iwho">
            <span class="iname">{{ info.identityName }}</span>
            <span class="imeta">{{ authPostureLabel }}</span>
          </span>
          <span class="icaret">⌄</span>
        </button>
        <div v-if="identityMenuOpen" class="idmenu" role="menu">
          <div class="idm-head">
            <span class="iav">{{ info.identityInitials }}</span>
            <span class="idm-id">
              <b>{{ info.identityName }}</b>
              <span>{{ info.identityProvider }}</span>
              <span class="idm-via">{{ authPostureLabel }}</span>
            </span>
          </div>
          <div class="idm-list">
            <button class="idm-item" type="button" role="menuitem" @click="openIdentityProfile">
              <svg viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><circle cx="8" cy="5.5" r="2.6"/><path d="M3 13.4c0-2.5 2.2-4 5-4s5 1.5 5 4"/></svg>
              <span>{{ t('shell.profileMenuProfile') }}</span>
            </button>
            <button class="idm-item" type="button" role="menuitem" @click="openIdentitySettings">
              <svg viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><circle cx="8" cy="8" r="2.2"/><path d="M8 1.8V3.4M8 12.6v1.6M14.2 8h-1.6M3.4 8H1.8M12.4 3.6 11.3 4.7M4.7 11.3 3.6 12.4M12.4 12.4 11.3 11.3M4.7 4.7 3.6 3.6"/></svg>
              <span>{{ t('shell.profileMenuSettings') }}</span>
            </button>
            <div class="idm-sep" />
            <button
              class="idm-item danger"
              type="button"
              role="menuitem"
              :disabled="!canLogout || logoutInFlight"
              :title="logoutTitle"
              @click="logoutIdentity"
            >
              <svg viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M10 11.5v1.4a1 1 0 0 1-1 1H3.4a1 1 0 0 1-1-1V3.1a1 1 0 0 1 1-1H9a1 1 0 0 1 1 1v1.4"/><path d="M7 8h7M11.6 5.4 14.2 8l-2.6 2.6"/></svg>
              <span>{{ t('shell.profileMenuLogout') }}</span>
            </button>
          </div>
        </div>
      </div>
    </header>

    <main class="content" :inert="compactViewport && mobileNavOpen">
      <slot />
    </main>

    <footer class="statusbar" :inert="compactViewport && mobileNavOpen">
      <span class="si" :data-state="info.backendStatus"><span class="dot" />{{ backendStatusLabel }}</span>
      <button type="button" class="si status-action" @click="openSettingsModal('general')"><span>{{ info.host }}</span><strong>{{ info.version }}</strong></button>
      <span class="si">{{ t('shell.postgres') }}</span>
      <span class="si">{{ currentArea }}</span>
      <span class="ssp" />
      <NuxtLink to="/health" class="si warn"><span class="dot" />{{ t('shell.statusDegradation') }} <strong>{{ info.health }}</strong></NuxtLink>
      <span class="si">{{ memoryRecordsLabel }}</span>
      <NuxtLink to="/noise" class="si warn"><span class="dot" />{{ t('shell.statusNoise') }} <strong>{{ info.noise }}</strong></NuxtLink>
      <NuxtLink to="/queue" class="si">{{ reviewQueueLabel }}</NuxtLink>
      <span class="si">{{ t('shell.uptime', { value: info.uptime }) }}</span>
    </footer>

    <SettingsModal v-model:open="settingsModalOpen" v-model:active-tab="settingsModalTab" />
    <ProfileModal v-model:open="profileModalOpen" :info="info" :auth-posture-label="authPostureLabel" />
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
.nav-scrim { display:none; }
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
.identity-wrap { position:relative; display:inline-flex; }
.identity { display:inline-flex; align-items:center; gap:8px; height:32px; padding:3px 10px 3px 3px; border-radius:var(--radius-pill); background:var(--surface-warm); border:1px solid var(--border); color:var(--fg); font:inherit; font-weight:600; font-size:var(--text-xs); text-align:left; max-width:230px; cursor:pointer; }
.identity:hover,
.identity[aria-expanded="true"] { border-color:var(--accent); }
.identity .iav { width:26px; height:26px; border-radius:50%; flex:none; display:grid; place-items:center; font-weight:800; font-size:11px; color:#fff; letter-spacing:-.02em; background:var(--accent); overflow:hidden; }
.identity .iwho { display:flex; flex-direction:column; line-height:1.05; min-width:0; }
.identity .iname { font-weight:700; color:var(--fg); white-space:nowrap; overflow:hidden; text-overflow:ellipsis; }
.identity .imeta { font-size:9px; font-weight:600; color:var(--muted); font-family:var(--font-mono); white-space:nowrap; overflow:hidden; text-overflow:ellipsis; }
.identity .icaret { color:var(--muted); font-size:9px; flex:none; }
.idmenu { position:absolute; right:0; top:38px; z-index:90; min-width:248px; padding:0; overflow:hidden; border:1px solid var(--border); border-radius:var(--r-md); background:var(--surface); box-shadow:var(--elev-raised); }
.idm-head { display:flex; gap:10px; align-items:center; padding:13px 14px; border-bottom:1px solid var(--border); }
.idm-head .iav { width:38px; height:38px; border-radius:50%; display:grid; place-items:center; font-weight:800; font-size:14px; color:#fff; background:var(--accent); flex:none; overflow:hidden; }
.idm-id { min-width:0; }
.idm-id b { display:block; font-size:var(--text-sm); color:var(--fg); white-space:nowrap; overflow:hidden; text-overflow:ellipsis; }
.idm-id > span:not(.idm-via) { font-size:var(--text-xs); color:var(--muted); font-family:var(--font-mono); white-space:nowrap; overflow:hidden; text-overflow:ellipsis; display:block; }
.idm-via { display:inline-flex; align-items:center; gap:5px; margin-top:4px; font-size:10px; font-weight:700; color:var(--fg-2); padding:2px 7px; border-radius:var(--radius-pill); background:var(--surface-warm); border:1px solid var(--border); }
.idm-list { padding:6px; }
.idm-item { display:flex; align-items:center; gap:9px; width:100%; text-align:left; border:0; background:transparent; color:var(--fg-2); padding:8px 9px; border-radius:var(--r-sm); font:inherit; font-size:var(--text-sm); font-weight:600; cursor:pointer; }
.idm-item:hover:not(:disabled) { background:var(--surface-warm); color:var(--fg); }
.idm-item:disabled { cursor:not-allowed; opacity:.58; }
.idm-item.danger { color:var(--danger); }
.idm-item.danger:hover:not(:disabled) { background:color-mix(in oklab,var(--danger),transparent 90%); }
.idm-item svg { width:15px; height:15px; flex:none; }
.idm-sep { height:1px; background:var(--border); margin:5px 0; }
.content { min-width:0; overflow-y:auto; padding:22px 24px 90px; }
.statusbar { display:flex; align-items:center; gap:var(--space-4); padding:0 var(--space-4); background:var(--surface); border-top:1px solid var(--border); font-size:11px; color:var(--muted); font-family:var(--font-mono); min-width:0; overflow:hidden; }
.statusbar .si { display:inline-flex; align-items:center; gap:6px; white-space:nowrap; color:var(--muted); text-decoration:none; }
.statusbar button.si { border:0; background:transparent; padding:0; font:inherit; cursor:pointer; }
.statusbar button.si:hover { color:var(--fg-2); }
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
@media (max-width:980px) {
  .app,
  .app.nav-collapsed { grid-template-columns:1fr; grid-template-areas:"topbar" "content" "statusbar"; }
  .topbar { gap:8px; padding:0 10px; }
  .topbar .mobile-menu-button { display:inline-flex; }
  .topbar-secondary { display:none; }
  .gsearch { min-width:0; width:min(360px,48vw); }
  .app > .nav { position:fixed; top:0; bottom:0; left:0; width:min(320px, calc(100vw - 48px)); transform:translateX(-100%); transition:transform var(--motion-base) var(--ease-standard); z-index:60; }
  .app > .nav.open { transform:none; }
  .nav-scrim { display:block; position:fixed; inset:0; z-index:50; border:0; background:color-mix(in srgb, #000, transparent 52%); cursor:pointer; }
  .navcollapse,
  .identity,
  .statusbar .si:nth-last-child(-n+3) { display:none; }
  .content { padding:18px 14px 70px; }
}
</style>
