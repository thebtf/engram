/**
 * Navigation model — single source of truth for sections, grouping, route, and honesty.
 * The shell sidebar and the Overview cards both read this, so a section can never drift
 * between nav and page. Mirrors the .od/index.html NAV array.
 *
 * i18n: labels are KEYS, not strings. `grpKey` → nav.groups.*, `labelKey` → nav.items.*,
 * `evidenceKey` → nav.evidence.* (optional; literal `evidence` still allowed for one-offs).
 * The consumer resolves them with t(). This keeps the nav structure (ids, routes, honesty)
 * language-independent — translating the menu means editing the dictionary, not this file.
 */
import type { HonestyClass } from './useHonesty'
import { computed } from 'vue'
import { operatorFetchJson } from './useOperatorApi'
import { useOperatorQueue } from './useOperatorQueue'

export interface NavItem {
  id: string
  /** i18n key under nav.items.* */
  labelKey: string
  to: string
  cls: Extract<HonestyClass, 'live' | 'dormant' | 'stale' | 'mustbuild'> | 'live'
  /** gate flag (dormant) or i18n key (mustbuild evidence) shown beside the dot */
  evidence?: string
  /** i18n key under nav.evidence.* — preferred over literal evidence for translatable text */
  evidenceKey?: string
  count?: number
  admin?: boolean
}
export interface NavGroup { grpKey: string; items: NavItem[] }

export const NAV: NavGroup[] = [
  { grpKey: 'workspace', items: [
    { id: 'overview', labelKey: 'overview', to: '/',        cls: 'live' },
    { id: 'search',   labelKey: 'search',   to: '/search',  cls: 'live' },
  ] },
  { grpKey: 'memoryProduct', items: [
    { id: 'memory', labelKey: 'memory', to: '/memory',  cls: 'live' },
    { id: 'queue',  labelKey: 'queue',  to: '/queue',   cls: 'live' },
    { id: 'noise',  labelKey: 'noise',  to: '/noise',   cls: 'live' },
    { id: 'graph',  labelKey: 'graph',  to: '/graph',   cls: 'live' },
    { id: 'books',  labelKey: 'books',  to: '/books',   cls: 'live' },
  ] },
  { grpKey: 'behaviorWork', items: [
    { id: 'rules',    labelKey: 'rules',    to: '/rules',    cls: 'live' },
    { id: 'issues',   labelKey: 'issues',   to: '/issues',   cls: 'live' },
    { id: 'projects', labelKey: 'projects', to: '/projects', cls: 'live' },
  ] },
  { grpKey: 'storage', items: [
    { id: 'secrets',     labelKey: 'secrets',     to: '/secrets',     cls: 'live' },
    { id: 'documents',   labelKey: 'documents',   to: '/documents',   cls: 'live' },
  ] },
  { grpKey: 'administration', items: [
    { id: 'access',   labelKey: 'access',   to: '/access',   cls: 'live', admin: true },
    { id: 'settings', labelKey: 'settings', to: '/settings', cls: 'live' },
    { id: 'health',   labelKey: 'health',   to: '/health',   cls: 'live' },
  ] },
]

/** Static structure with i18n KEYS. Use when you resolve labels yourself, or need the raw
 *  shape (routes, honesty class, ids) without a translation context. */
export function useNav() {
  const queue = useOperatorQueue()
  // Runtime flags are unknown until the request settles. A neutral stale dot is
  // deliberately less misleading than a green live dot for a gated feature.
  const graphClass = useState<Extract<HonestyClass, 'live' | 'dormant' | 'stale'>>('live:nav:graph-class', () => 'stale')
  const graphStarted = useState<boolean>('live:nav:graph-started', () => false)
  if (import.meta.client && !graphStarted.value) {
    graphStarted.value = true
    void operatorFetchJson<{ flags?: Record<string, boolean> }>('/api/flags', undefined, 'navigation-flags')
      .then((payload) => { graphClass.value = payload.flags?.ENGRAM_GRAPH_ENABLED === true ? 'live' : 'dormant' })
      .catch(() => { graphClass.value = 'stale' })
  }
  const classFor = (kind: string): NavItem['cls'] => {
    if (kind === 'live' || kind === 'empty') return 'live'
    if (kind === 'gated') return 'dormant'
    if (kind === 'mustbuild') return 'mustbuild'
    return 'stale'
  }
  const resolved = computed(() => NAV.map((group) => ({
    ...group,
    items: group.items.map((item) => item.id === 'queue'
      ? { ...item, cls: classFor(queue.loadState.value.kind), evidence: queue.loadState.value.kind === 'gated' ? 'ENGRAM_VNEXT_F_ENABLED' : undefined }
      : item.id === 'graph'
        ? { ...item, cls: graphClass.value, evidence: graphClass.value === 'dormant' ? 'ENGRAM_GRAPH_ENABLED' : undefined }
        : item),
  })))
  return { NAV: resolved, flat: computed(() => resolved.value.flatMap(g => g.items)) }
}

/**
 * RESOLVED nav — call inside <script setup>. Returns the same tree but with `.label`
 * (and group `.label`, item `.evidence`) already translated to display strings via t().
 *
 * WHY THIS EXISTS: `useNav()` carries i18n keys, not text. A consumer that builds cards or
 * a sidebar by reading `item.label` off the static list gets `undefined` (the field is
 * `labelKey` now) — which renders as an EMPTY card, a silent porting bug. `useNavTree()`
 * restores `.label` as a real, translated string, so `item.label` / `group.label` just work.
 * Rule of thumb: building UI from nav? use `useNavTree()`. Need only routes/ids/honesty? `useNav()`.
 */
export interface NavItemResolved extends NavItem { label: string }
export interface NavGroupResolved { grpKey: string; label: string; items: NavItemResolved[] }
export function useNavTree(): { groups: NavGroupResolved[]; flat: NavItemResolved[] } {
  const { t } = useI18n()
  const groups: NavGroupResolved[] = NAV.map(g => ({
    grpKey: g.grpKey,
    label: t(`nav.groups.${g.grpKey}`),
    items: g.items.map(it => ({
      ...it,
      label: t(`nav.items.${it.labelKey}`),
      evidence: it.evidenceKey ? t(`nav.evidence.${it.evidenceKey}`) : it.evidence,
    })),
  }))
  return { groups, flat: groups.flatMap(g => g.items) }
}
