import { expect, test } from '@playwright/test'

const entry = { source_ref: 'source', checkout_ref: 'checkout', repository: 'Engram', working_copy: 'Operator desk', indexed_snapshot: { label: 'Pinned snapshot' }, view_ref: 'legacy-view', selection_ref: 'selection', index_intent_available: false }
const binding = { state: 'TAB_BINDING_READY', tab_binding_id: '60000000-0000-4000-8000-000000000041', document_proof: 'proof', resume_nonce: 'resume', reload_token: 'reload' }

test('an unresponsive authentication-mode probe expires and allows a safe retry', async ({ page }) => {
  let attempts = 0
  const releases: Array<() => void> = []
  let recovered = false
  await page.clock.install()
  await page.route('**/api/auth/me', async route => {
    attempts++
    if (!recovered) await new Promise<void>(resolve => { releases.push(resolve) })
    await route.fulfill({ json: { auth_disabled: true } }).catch(() => { })
  })
  await page.route('**/api/code/**', async route => {
    if (route.request().url().endsWith('/tabs/handshake')) await route.fulfill({ json: binding })
    else if (route.request().url().endsWith('/contexts')) await route.fulfill({ json: { contexts: [entry] } })
    else await route.fulfill({ status: 500 })
  })
  await page.goto('/code', { waitUntil: 'domcontentloaded' })
  await expect(page.locator('.phase')).toHaveAttribute('data-state', 'binding')
  await page.clock.fastForward('00:31')
  await expect(page.locator('.phase')).toHaveAttribute('data-state', 'identity-unavailable')
  recovered = true
  for (const release of releases) release()
  await page.getByTestId('code-retry-identity').click()
  await expect(page.getByTestId('code-context-snapshot').getByRole('option', { name: 'Pinned snapshot' })).toHaveCount(1)
  expect(attempts).toBeGreaterThanOrEqual(2)
})

test('an obsolete initialization cannot consume the pinned SPA remount', async ({ page }) => {
  let holdProbes = false
  const releases: Array<() => void> = []
  const transitions: string[] = []
  await page.route('**/api/auth/me', async route => {
    if (holdProbes) await new Promise<void>(resolve => { releases.push(resolve) })
    await route.fulfill({ json: { auth_disabled: true } }).catch(() => { })
  })
  await page.route('**/api/code/**', async route => {
    const path = new URL(route.request().url()).pathname
    transitions.push(path)
    if (path === '/api/code/tabs/handshake' || path === '/api/code/tabs/resume') await route.fulfill({ json: binding })
    else if (path === '/api/code/contexts') await route.fulfill({ json: { contexts: [entry] } })
    else if (path.endsWith('/context')) await route.fulfill({ status: 204 })
    else if (path === '/api/code/status') await route.fulfill({ json: { total_chunks: 0, embedded_chunks: 0, embedding: { coverage: 'none', job_state: null, error_code: null } } })
    else await route.fulfill({ status: 403 })
  })
  await page.goto('/code')
  await page.getByTestId('code-context-snapshot').selectOption('selection')
  await page.getByTestId('code-pin-context').click()
  await expect(page.getByTestId('code-context-pinned')).toBeVisible()
  await page.locator('a[href="/memory"]').first().click()
  await expect(page).toHaveURL(/\/memory$/)
  holdProbes = true
  await page.locator('a[href="/code"]').first().click()
  await expect(page).toHaveURL(/\/code$/)
  await expect.poll(() => releases.length).toBeGreaterThan(0)
  await expect(page.locator('.phase')).toHaveAttribute('data-state', 'binding')
  await page.locator('a[href="/memory"]').first().click()
  await expect(page).toHaveURL(/\/memory$/)
  holdProbes = false
  for (const release of releases) release()
  await page.locator('a[href="/code"]').first().click()
  await expect(page.getByTestId('code-context-pinned')).toContainText('Pinned snapshot')
  expect(transitions.filter(path => path === '/api/code/tabs/resume')).toHaveLength(1)
})

test('a prior raw viewRef restores only a unique authorized candidate, never a server pin', async ({ page }) => {
  let duplicates = false
  const pins: unknown[] = []
  await page.route('**/api/auth/me', async route => route.fulfill({ json: { auth_disabled: true } }))
  await page.route('**/api/code/**', async route => {
    const path = new URL(route.request().url()).pathname
    if (path === '/api/code/tabs/handshake' || path === '/api/code/tabs/resume') await route.fulfill({ json: binding })
    else if (path === '/api/code/contexts') await route.fulfill({ json: { contexts: duplicates ? [entry, { ...entry, source_ref: 'another', checkout_ref: 'other', selection_ref: 'other' }] : [entry] } })
    else if (path.endsWith('/context')) { pins.push(route.request().postDataJSON()); await route.fulfill({ status: 204 }) }
    else if (path === '/api/code/status') await route.fulfill({ json: { total_chunks: 0, embedded_chunks: 0, embedding: { coverage: 'none', job_state: null, error_code: null } } })
    else await route.fulfill({ status: 403 })
  })
  await page.goto('/code')
  await page.getByTestId('code-context-snapshot').selectOption('selection')
  await page.getByTestId('code-pin-context').click()
  await expect(page.getByTestId('code-context-pinned')).toBeVisible()
  await page.evaluate(() => sessionStorage.setItem('engram.operator-code.view-candidate.v2', 'legacy-view'))
  await page.reload()
  await expect(page.getByTestId('code-context-snapshot')).toHaveValue('selection')
  await expect(page.getByTestId('code-context-pinned')).toHaveCount(0)
  expect(pins).toHaveLength(1)
  duplicates = true
  await page.reload()
  await expect(page.getByTestId('code-context-repository').locator('option')).toHaveCount(3)
  await expect(page.getByTestId('code-context-candidate')).toHaveCount(0)
  await expect.poll(() => page.evaluate(() => sessionStorage.getItem('engram.operator-code.view-candidate.v2'))).toBeNull()
})

test('offline freshness renders its translated status instead of an i18n key', async ({ page }) => {
  await page.route('**/api/auth/me', async route => route.fulfill({ json: { auth_disabled: true } }))
  await page.route('**/api/code/**', async route => {
    const path = new URL(route.request().url()).pathname
    if (path === '/api/code/tabs/handshake') await route.fulfill({ json: binding })
    else if (path === '/api/code/contexts') await route.fulfill({ json: { contexts: [entry] } })
    else if (path.endsWith('/context')) await route.fulfill({ status: 204 })
    else if (path === '/api/code/status') await route.fulfill({ json: { total_chunks: 1, embedded_chunks: 1, embedding: { coverage: 'complete', job_state: null, error_code: null }, freshness: { state: 'offline' } } })
    else await route.fulfill({ status: 403 })
  })
  await page.goto('/code')
  await page.getByTestId('code-context-snapshot').selectOption('selection')
  await page.getByTestId('code-pin-context').click()
  await expect(page.getByTestId('code-status')).toContainText('Офлайн')
  await expect(page.getByTestId('code-status')).not.toContainText('workspace.freshness.offline')
})
