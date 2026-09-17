import { mkdirSync } from 'node:fs'
import { join } from 'node:path'
import { expect, test, type Page, type Route } from '@playwright/test'

test.use({ locale: 'ru' })

/**
 * Canonical acceptance evidence is generated ONCE with OPERATOR_EVIDENCE_DIR set
 * (see evidence/p0-a3/manifest.json for the frozen packet + hashes). Ordinary
 * regression replays leave OPERATOR_EVIDENCE_DIR unset, write nothing into
 * tracked paths, and therefore leave the worktree clean.
 */
const EVIDENCE_DIR = process.env.OPERATOR_EVIDENCE_DIR || ''

async function evidenceShot(page: Page, name: string) {
 if (!EVIDENCE_DIR) return
 mkdirSync(EVIDENCE_DIR, { recursive: true })
 await page.screenshot({ path: join(EVIDENCE_DIR, name), fullPage: true })
}

function jsonRoute(body: unknown, status = 200) {
 return (route: Route) => route.fulfill({ status, contentType: 'application/json', body: JSON.stringify(body) })
}

const RU = {
 shellReady: 'console reachable · server ready',
 shellUnavailable: 'console reachable · server unavailable',
 diagnosis: {
  unauthorized: 'Войдите снова, чтобы продолжить.',
  forbidden: 'Текущая сессия не имеет доступа к этой поверхности.',
  endpointMissing: 'Этот эндпоинт сервера недоступен.',
  serverUnavailable: 'Сервер Engram недоступен.',
  unreachable: 'Не удалось связаться с сервером Engram.',
 },
}

const ACCESS_EMPTY_BODIES: Record<string, unknown> = {
 '/providers': { providers: [], auth_disabled: false, local_login_enabled: true, authentik_trusted_proxy_count: 0 },
 '/invitations': { invitations: [] },
 '/users': { users: [] },
 '/roles': { roles: [] },
 '/sessions': { sessions: [] },
 '/log': { entries: [] },
}

const ACCESS_POPULATED_BODIES: Record<string, unknown> = {
 '/providers': {
  providers: [{ id: 'local', label: 'Local login', kind: 'password', enabled: true, configured: true, operable: true, honesty: 'live', evidence: '/api/access/providers', description: 'mock provider' }],
  auth_disabled: false,
  local_login_enabled: true,
  authentik_trusted_proxy_count: 1,
 },
 '/invitations': { invitations: [{ id: 1, code: '', email: 'op@example.test', role: 'operator', created_by: 1, created_by_email: 'admin@example.test', expires_at: '2030-01-01T00:00:00Z', revocation_reason: '', created_at: '2026-01-01T00:00:00Z', status: 'pending' }] },
 '/users': { users: [{ id: 1, email: 'admin@example.test', role: 'admin', disabled: false, created_at: '2026-01-01T00:00:00Z', last_login_at: '2026-01-02T00:00:00Z' }] },
 '/roles': { roles: [{ role: 'admin', user_count: 1 }, { role: 'operator', user_count: 0 }] },
 '/sessions': { sessions: [{ id: 's1', user_id: 1, user_email: 'admin@example.test', user_role: 'admin', user_disabled: false, created_at: '2026-01-01T00:00:00Z', expires_at: '2030-01-01T00:00:00Z', user_agent: 'mock', remote_addr: '127.0.0.1', revocation_reason: '', status: 'active' }] },
 '/log': { entries: [] },
}

function accessRoute(bodies: Record<string, unknown>) {
 return (route: Route) => {
  const path = new URL(route.request().url()).pathname
  const key = Object.keys(bodies).find((suffix) => path.startsWith(`/api/access${suffix}`))
  return route.fulfill({ contentType: 'application/json', body: JSON.stringify(key ? bodies[key] : { entries: [] }) })
 }
}

const GRAPH_NODES = {
 nodes: [
  { id: 1, node_type: 'skill', external_ref: 'alpha', project: 'demo', privacy_scope: 'project', created_at: '2026-01-01T00:00:00Z', updated_at: '2026-01-01T00:00:00Z' },
  { id: 2, node_type: 'repo', external_ref: 'beta', project: 'demo', privacy_scope: 'project', created_at: '2026-01-01T00:00:00Z', updated_at: '2026-01-01T00:00:00Z' },
 ],
 count: 2,
}

async function routeGraphFoundation(page: Page, graphEnabled = true) {
 await page.route('**/api/flags', jsonRoute({ flags: { ENGRAM_GRAPH_ENABLED: graphEnabled } }))
 await page.route('**/api/projects', jsonRoute(['demo']))
}

async function expectShellReady(page: Page) {
 await expect(page.locator('.statusbar .si').first()).toHaveText(RU.shellReady)
}

async function expectNoHorizontalOverflow(page: Page) {
 const overflow = await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth)
 expect(overflow).toBeLessThanOrEqual(0)
}

test('Access delayed first response stays pending, then becomes populated live truth', async ({ page }) => {
 let releaseAccess: (() => void) | undefined
 const accessReleased = new Promise<void>((resolve) => { releaseAccess = resolve })
 await page.route('**/api/access/**', async (route) => {
  await accessReleased
  return accessRoute(ACCESS_POPULATED_BODIES)(route)
 })

 await page.goto('/access')
 await expect(page.locator('.access-page')).toHaveAttribute('data-state', 'pending')
 await expect(page.locator('.statebar.pending')).toBeVisible()
 await expect(page.locator('.access-grid')).toHaveCount(0)

 releaseAccess?.()
 await expect(page.locator('.access-page')).toHaveAttribute('data-state', 'live')
 await expect(page.locator('.access-brief')).toBeVisible()
 const metrics = page.locator('.access-brief .metric b')
 await expect(metrics.nth(0)).toHaveText('1')
 await expect(metrics.nth(1)).toHaveText('1')
 await expect(metrics.nth(2)).toHaveText('1')
 await expect(metrics.nth(3)).toHaveText('1')
 await expectShellReady(page)
 await evidenceShot(page, 'access-populated-1280.png')
})

test('Access empty responses render live state with real zero counts, not synthesized values', async ({ page }) => {
 await page.route('**/api/access/**', accessRoute(ACCESS_EMPTY_BODIES))
 await page.goto('/access')
 await expect(page.locator('.access-page')).toHaveAttribute('data-state', 'live')
 await expect(page.locator('.access-brief .metric b')).toHaveText(['0', '0', '0', '0'])
 await expectShellReady(page)
})

test('Access 401 is unauthorized with localized primary copy and disabled surface', async ({ page }) => {
 await page.route('**/api/access/**', jsonRoute({ error: 'unauthorized' }, 401))
 await page.goto('/access')
 await expect(page.locator('.access-page')).toHaveAttribute('data-state', 'unauthorized')
 const guard = page.getByRole('alert')
 await expect(guard).toBeVisible()
 await expect(guard).toContainText('сессия')
 await expect(guard).not.toContainText('Sign in again')
 await expect(page.locator('.access-grid')).toHaveCount(0)
 await expectShellReady(page)
 await evidenceShot(page, 'access-unauthorized-1280.png')
})

test('Access 403 is forbidden with a distinct localized next action', async ({ page }) => {
 await page.route('**/api/access/**', jsonRoute({ error: 'forbidden' }, 403))
 await page.goto('/access')
 await expect(page.locator('.access-page')).toHaveAttribute('data-state', 'forbidden')
 const guard = page.getByRole('alert')
 await expect(guard).not.toContainText('does not have access')
 await expect(guard.locator('p')).not.toHaveText('')
 await expectShellReady(page)
 await evidenceShot(page, 'access-forbidden-1280.png')
})

test('Access 404 and 500 render localized diagnosis, technical evidence stays secondary, retry recovers after readback', async ({ page }) => {
 await page.setViewportSize({ width: 390, height: 844 })
 await page.route('**/api/access/**', jsonRoute({ error: 'missing' }, 404))
 await page.goto('/access')
 const guard = page.getByRole('alert')
 await expect(guard).toBeVisible()
 await expect(page.locator('.access-page')).toHaveAttribute('data-state', 'error')
 await expect(guard).toContainText(RU.diagnosis.endpointMissing)
 await expect(guard).not.toContainText('missing')
 await expect(guard).not.toContainText('This server endpoint is unavailable.')
 await expect(guard.locator('details summary')).toHaveText('Техническая сводка')
 await expect(page.locator('.access-brief')).toHaveCount(0)
 await expect(page.locator('.nav')).not.toHaveClass(/open/)
 await expectNoHorizontalOverflow(page)
 await evidenceShot(page, 'access-error-390.png')

 await page.unroute('**/api/access/**')
 await page.route('**/api/access/**', jsonRoute({ error: 'boom' }, 500))
 await page.getByRole('button', { name: 'Повторить' }).click()
 await expect(guard).toContainText(RU.diagnosis.serverUnavailable)
 await expect(guard).not.toContainText('The Engram server is unavailable.')

 await page.unroute('**/api/access/**')
 await page.route('**/api/access/**', accessRoute(ACCESS_EMPTY_BODIES))
 await page.setViewportSize({ width: 980, height: 844 })
 await expect(page.locator('.nav')).not.toHaveClass(/open/)
 await page.getByRole('button', { name: 'Повторить' }).click()
 await expect(page.locator('.access-page')).toHaveAttribute('data-state', 'live')
 await expect(page.locator('.access-brief')).toBeVisible()
 await expectShellReady(page)
 await evidenceShot(page, 'access-recovery-980.png')
})

test('Graph bookmark reports retirement without a manual editor', async ({ page }) => {
 await page.goto('/graph')
 await expect(page.getByRole('heading', { name: 'Knowledge graph editing retired' })).toBeVisible()
 await expect(page.locator('input, select, textarea, button')).toHaveCount(0)
 await expect(page.getByRole('link', { name: 'Open Documents' })).toHaveAttribute('href', '/documents')
})


test('Settings moves and contains focus with forward and reverse wrap while the background is inert', async ({ page }) => {
 await page.setViewportSize({ width: 390, height: 844 })
 await page.goto('/settings')
 const dialog = page.getByRole('dialog', { name: 'Настройки сервера' })
 await expect(dialog).toBeVisible()
 await expect(page.locator('.app')).toHaveAttribute('aria-hidden', 'true')
 await expect(page.locator('.app')).toHaveJSProperty('inert', true)
 await expect(dialog.locator('.settings-tab .tab-status').first()).toBeVisible()
 await expect(page.locator('.settings-head .close')).toBeFocused()
 await expect(page.locator('.nav')).not.toHaveClass(/open/)
 await expectNoHorizontalOverflow(page)
 await evidenceShot(page, 'settings-general-390.png')

 // Reverse wrap: Shift+Tab from the first focusable lands on the last one.
 await dialog.locator('.settings-tab').first().focus()
 await page.keyboard.press('Shift+Tab')
 await expect.poll(() => page.evaluate(() => {
  const dialogElement = document.querySelector('[role="dialog"]')
  if (!dialogElement || !document.activeElement) return 'outside'
  if (!dialogElement.contains(document.activeElement)) return 'outside'
  const focusables = [...dialogElement.querySelectorAll('button:not([disabled]), [href], input:not([disabled]), select:not([disabled]), textarea:not([disabled]), [tabindex]:not([tabindex="-1"])')]
   .filter((element) => !element.hasAttribute('hidden') && element.getClientRects().length > 0)
  return document.activeElement === focusables[focusables.length - 1] ? 'last' : 'inside'
 })).toBe('last')

 // Forward wrap: Tab from the last focusable lands on the first one.
 await page.keyboard.press('Tab')
 await expect.poll(() => page.evaluate(() => {
  const dialogElement = document.querySelector('[role="dialog"]')
  if (!dialogElement || !document.activeElement) return 'outside'
  const focusables = [...dialogElement.querySelectorAll('button:not([disabled]), [href], input:not([disabled]), select:not([disabled]), textarea:not([disabled]), [tabindex]:not([tabindex="-1"])')]
   .filter((element) => !element.hasAttribute('hidden') && element.getClientRects().length > 0)
  return document.activeElement === focusables[0] ? 'first' : 'inside'
 })).toBe('first')

 const accessTab = dialog.getByRole('button', { name: 'Доступ', exact: true })
 await accessTab.scrollIntoViewIfNeeded()
 await accessTab.click()
 await evidenceShot(page, 'settings-access-390.png')

 await page.keyboard.press('Escape')
 await expect(dialog).toBeHidden()
 await expect(page.locator('.app')).not.toHaveAttribute('aria-hidden')
 await expect(page.locator('.app')).toHaveJSProperty('inert', false)
})

test('Settings restores environment and trigger focus across explicit-close, backdrop, and route-transition closes', async ({ page }) => {
 await page.goto('/')
 const trigger = page.locator('.statusbar .status-action')
 const dialog = page.getByRole('dialog', { name: 'Настройки сервера' })

 // Explicit close button restores the connected trigger.
 await trigger.click()
 await expect(dialog).toBeVisible()
 await page.locator('.settings-head .close').click()
 await expect(dialog).toBeHidden()
 await expect(trigger).toBeFocused()
 await expect(page.locator('.app')).toHaveJSProperty('inert', false)
 const bodyOverflowAfterClose = await page.evaluate(() => document.body.style.overflow)
 expect(bodyOverflowAfterClose).toBe('')

 // Backdrop click closes and restores.
 await trigger.click()
 await expect(dialog).toBeVisible()
 await page.locator('.settings-overlay').click({ position: { x: 8, y: 8 } })
 await expect(dialog).toBeHidden()
 await expect(trigger).toBeFocused()
 await expect(page.locator('.app')).not.toHaveAttribute('aria-hidden')

 // SPA route transition while open closes the dialog and re-enables the app.
 await page.locator('.nav').getByRole('link', { name: 'Состояние' }).click()
 await expect.poll(() => new URL(page.url()).pathname).toBe('/health')
 await trigger.click()
 await expect(dialog).toBeVisible()
 await page.goBack()
 await expect.poll(() => new URL(page.url()).pathname).toBe('/')
 await expect(dialog).toBeHidden()
 await expect(page.locator('.app')).toHaveJSProperty('inert', false)
 const bodyOverflowAfterRoute = await page.evaluate(() => document.body.style.overflow)
 expect(bodyOverflowAfterRoute).toBe('')

 // Unmount (full document teardown) leaves the fresh document interactive with no dialog.
 await trigger.click()
 await expect(dialog).toBeVisible()
 await page.goto('/rules')
 await expect(page.getByRole('dialog')).toHaveCount(0)
 await expect(page.locator('.app')).toHaveJSProperty('inert', false)
 const bodyOverflowAfterUnmount = await page.evaluate(() => document.body.style.overflow)
 expect(bodyOverflowAfterUnmount).toBe('')
})

test('locale switch drives html lang and localized Settings copy', async ({ page }) => {
 await page.goto('/settings')
 await expect(page.locator('html')).toHaveAttribute('lang', 'ru')
 const dialog = page.getByRole('dialog', { name: 'Настройки сервера' })
 await expect(dialog).toBeVisible()

 await dialog.getByRole('button', { name: 'English' }).click()
 await expect(page.locator('html')).toHaveAttribute('lang', 'en')

 await page.getByRole('dialog').getByRole('button', { name: '中文' }).click()
 await expect(page.locator('html')).toHaveAttribute('lang', 'zh-Hans')

 await page.getByRole('dialog').getByRole('button', { name: 'Русский' }).click()
 await expect(page.locator('html')).toHaveAttribute('lang', 'ru')
 await expect(dialog).toBeVisible()
})

test('Settings stays reachable at 200% zoom without hidden focus targets or horizontal overflow', async ({ page }) => {
 // 640x512 logical viewport approximates a 1280x1024 desktop at 200% browser zoom.
 await page.setViewportSize({ width: 640, height: 512 })
 await page.goto('/settings')
 const dialog = page.getByRole('dialog', { name: 'Настройки сервера' })
 await expect(dialog).toBeVisible()
 await expect(page.locator('.settings-head .close')).toBeFocused()
 await expectNoHorizontalOverflow(page)

 const hiddenFocusables = await page.evaluate(() => {
  const dialogElement = document.querySelector('[role="dialog"]')
  if (!dialogElement) return -1
  const focusables = [...dialogElement.querySelectorAll('button:not([disabled]), [href], input:not([disabled]), select:not([disabled]), textarea:not([disabled])')]
  return focusables.filter((element) => element.getClientRects().length === 0 && !element.closest('[hidden]')).length
 })
 expect(hiddenFocusables).toBe(0)

 const accessTab = dialog.getByRole('button', { name: 'Доступ', exact: true })
 await accessTab.scrollIntoViewIfNeeded()
 await accessTab.click()
 await expect(dialog.getByRole('heading', { name: 'Управление доступом' })).toBeVisible()
 await page.keyboard.press('Escape')
 await expect(dialog).toBeHidden()
})

test('Books bookmark reports retirement without a plaintext uploader', async ({ page }) => {
 await page.goto('/books')
 await expect(page.getByRole('heading', { name: 'Plaintext book intake retired' })).toBeVisible()
 await expect(page.locator('input, textarea, input[type="file"], button')).toHaveCount(0)
 await expect(page.getByRole('link', { name: 'Open Documents' })).toHaveAttribute('href', '/documents')
})
