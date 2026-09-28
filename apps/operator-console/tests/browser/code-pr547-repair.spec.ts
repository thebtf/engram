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

test('a documented auth-enabled 401 enters denied mode without mounting grants or binding', async ({ page }) => {
  const codeRequests: string[] = []
  await page.route('**/api/auth/me', route => route.fulfill({ status: 401, json: { authenticated: false, auth_disabled: false } }))
  await page.route('**/api/code/**', route => { codeRequests.push(new URL(route.request().url()).pathname); return route.fulfill({ status: 403 }) })
  await page.goto('/code')
  await expect(page.locator('.phase')).toHaveAttribute('data-state', 'denied')
  await expect(page.getByTestId('code-grant-chooser')).toHaveCount(0)
  expect(codeRequests).toEqual([])
})

test('unknown and no-auth mode never mount grant administration', async ({ page }) => {
  const codeRequests: string[] = []
  let releaseProbe: () => void = () => { }
  await page.route('**/api/auth/me', async route => {
    await new Promise<void>(resolve => { releaseProbe = resolve })
    await route.fulfill({ json: { authenticated: true, auth_disabled: true } })
  })
  await page.route('**/api/code/**', route => {
    codeRequests.push(new URL(route.request().url()).pathname)
    if (route.request().url().endsWith('/tabs/handshake')) return route.fulfill({ json: binding })
    if (route.request().url().endsWith('/contexts')) return route.fulfill({ json: { contexts: [] } })
    return route.fulfill({ status: 403 })
  })
  await page.goto('/code', { waitUntil: 'domcontentloaded' })
  await expect(page.locator('.phase')).toHaveAttribute('data-state', 'binding')
  await expect(page.getByTestId('code-grant-chooser')).toHaveCount(0)
  expect(codeRequests).toEqual([])
  releaseProbe()
  await expect(page.locator('.phase')).toHaveAttribute('data-state', 'ready')
  await expect(page.getByTestId('code-grant-chooser')).toHaveCount(0)
  expect(codeRequests).toEqual(['/api/code/tabs/handshake', '/api/code/contexts'])
})

test('malformed auth-enabled 401 remains a retryable probe failure without grants', async ({ page }) => {
  const codeRequests: string[] = []
  await page.route('**/api/auth/me', route => route.fulfill({ status: 401, json: { authenticated: true, auth_disabled: false } }))
  await page.route('**/api/code/**', route => { codeRequests.push(route.request().url()); return route.fulfill({ status: 403 }) })
  await page.goto('/code')
  await expect(page.locator('.phase')).toHaveAttribute('data-state', 'identity-unavailable')
  await expect(page.getByTestId('code-grant-chooser')).toHaveCount(0)
  expect(codeRequests).toEqual([])
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

test('unbound refresh cannot erase a historical View candidate before identity retry', async ({ page }) => {
  let identityUnavailable = false
  const requests: string[] = []
  const pins: unknown[] = []
  await page.route('**/api/auth/me', route => route.fulfill(identityUnavailable ? { status: 503 } : { json: { auth_disabled: true } }))
  await page.route('**/api/code/**', async route => {
    const path = new URL(route.request().url()).pathname
    requests.push(path)
    if (path === '/api/code/tabs/handshake' || path === '/api/code/tabs/resume') await route.fulfill({ json: binding })
    else if (path === '/api/code/contexts') await route.fulfill({ json: { contexts: [entry] } })
    else if (path.endsWith('/context')) { pins.push(route.request().postDataJSON()); await route.fulfill({ status: 204 }) }
    else if (path === '/api/code/status') await route.fulfill({ json: { total_chunks: 0, embedded_chunks: 0, embedding: { coverage: 'none', job_state: null, error_code: null } } })
    else await route.fulfill({ status: 403 })
  })
  await page.goto('/code')
  await page.getByTestId('code-context-snapshot').selectOption('selection')
  await page.getByTestId('code-pin-context').click()
  await expect(page.getByTestId('code-context-pinned')).toContainText('Pinned snapshot')
  const stored = await page.evaluate(() => sessionStorage.getItem('engram.operator-code.view-candidate.v2'))
  expect(stored).not.toBeNull()
  identityUnavailable = true
  await page.reload()
  await expect(page.locator('.phase')).toHaveAttribute('data-state', 'identity-unavailable')
  await expect(page.getByTestId('code-context-pinned')).toHaveCount(0)
  const refresh = page.getByRole('button', { name: 'Обновить разрешённые варианты' })
  await expect(refresh).toBeDisabled()
  await refresh.dispatchEvent('click')
  expect(requests.filter(path => path === '/api/code/contexts')).toHaveLength(1)
  await expect(page.getByTestId('code-retry-identity')).toBeEnabled()
  expect(await page.evaluate(() => sessionStorage.getItem('engram.operator-code.view-candidate.v2'))).toBe(stored)
  identityUnavailable = false
  await page.getByTestId('code-retry-identity').click()
  await expect(page.getByTestId('code-context-snapshot')).toHaveValue('selection')
  await expect(page.getByTestId('code-context-pinned')).toHaveCount(0)
  expect(pins).toHaveLength(1)
  await page.getByTestId('code-pin-context').click()
  await expect(page.getByTestId('code-context-pinned')).toContainText('Pinned snapshot')
  expect(pins).toHaveLength(2)
  expect(requests.filter(path => path === '/api/code/tabs/resume')).toHaveLength(1)
  expect(requests.filter(path => path === '/api/code/status')).toHaveLength(2)
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

test('offline freshness has a distinct readiness label, not unknown freshness', async ({ page }) => {
  await page.route('**/api/auth/me', route => route.fulfill({ json: { authenticated: true, auth_disabled: true } }))
  await page.route('**/api/code/**', route => {
    const path = new URL(route.request().url()).pathname
    if (path === '/api/code/tabs/handshake') return route.fulfill({ json: binding })
    if (path === '/api/code/contexts') return route.fulfill({ json: { contexts: [entry] } })
    if (path.endsWith('/context')) return route.fulfill({ status: 204 })
    if (path === '/api/code/status') return route.fulfill({ json: { total_chunks: 1, embedded_chunks: 1, embedding: { coverage: 'complete', job_state: 'succeeded', error_code: null }, freshness: { state: 'offline' } } })
    return route.fulfill({ status: 403 })
  })
  await page.goto('/code')
  await page.getByTestId('code-context-snapshot').selectOption('selection')
  await page.getByTestId('code-pin-context').click()
  await expect(page.locator('.readiness')).toHaveAttribute('data-state', 'offline')
  await expect(page.locator('.readiness')).toContainText(/офлайн/i)
  await expect(page.locator('.readiness')).not.toContainText('Свежесть неизвестна')
  await expect(page.locator('.readiness')).not.toContainText('workspace.readiness.offline')
})

for (const boundary of ['resume', 'handshake', 'catalog'] as const) {
  test(`obsolete late ${boundary} response cannot mutate the tab or lose its historical candidate`, async ({ page }) => {
    let held: typeof boundary | null = null
    const releases: Array<() => void> = []
    const requests: string[] = []
    const otherSnapshot = { ...entry, view_ref: 'other-view', selection_ref: 'other-selection', indexed_snapshot: { label: 'Other snapshot' } }
    await page.clock.install({ time: new Date('2026-09-15T00:00:00Z') })
    await page.addInitScript(() => {
      const original = performance.getEntriesByType.bind(performance)
      performance.getEntriesByType = (entryType) => entryType === 'navigation'
        ? [Object.create(PerformanceNavigationTiming.prototype, { type: { value: 'reload' } })]
        : original(entryType)
    })
    await page.route('**/api/auth/me', route => route.fulfill({ json: { auth_disabled: true } }))
    await page.route('**/api/code/**', async route => {
      const path = new URL(route.request().url()).pathname
      requests.push(path)
      const stage = path === '/api/code/tabs/resume' ? 'resume' : path === '/api/code/tabs/handshake' ? 'handshake' : path === '/api/code/contexts' ? 'catalog' : null
      if (stage !== null && stage === held) await new Promise<void>(resolve => { releases.push(resolve) })
      if (stage === 'resume' || stage === 'handshake') await route.fulfill({ json: binding }).catch(() => { })
      else if (stage === 'catalog') await route.fulfill({ json: { contexts: [otherSnapshot, entry] } }).catch(() => { })
      else if (path.endsWith('/context')) await route.fulfill({ status: 204 })
      else if (path === '/api/code/status') await route.fulfill({ json: { total_chunks: 0, embedded_chunks: 0, embedding: { coverage: 'none', job_state: null, error_code: null } } })
      else await route.fulfill({ status: 403 })
    })

    await page.goto('/code')
    await page.getByTestId('code-context-snapshot').selectOption('selection')
    await page.getByTestId('code-pin-context').click()
    await expect(page.getByTestId('code-context-pinned')).toBeVisible()
    const candidate = await page.evaluate(() => sessionStorage.getItem('engram.operator-code.view-candidate.v2'))
    await page.locator('a[href="/memory"]').first().click()
    await expect(page).toHaveURL(/\/memory$/)
    if (boundary === 'handshake') await page.evaluate(() => sessionStorage.removeItem('engram.operator-code.resume.v1'))
    held = boundary
    await page.locator('a[href="/code"]').first().click()
    await expect(page).toHaveURL(/\/code$/)
    await expect.poll(() => releases.length).toBe(1)
    await page.locator('a[href="/memory"]').first().click()
    await expect(page).toHaveURL(/\/memory$/)

    await page.evaluate(() => {
      const writes: string[] = []
      Object.assign(window, { codeBootstrapWrites: writes })
      const set = Storage.prototype.setItem
      const remove = Storage.prototype.removeItem
      Storage.prototype.setItem = function(key, value) {
        if (this === sessionStorage && key.startsWith('engram.operator-code.')) writes.push(`set:${key}`)
        set.call(this, key, value)
      }
      Storage.prototype.removeItem = function(key) {
        if (this === sessionStorage && key.startsWith('engram.operator-code.')) writes.push(`remove:${key}`)
        remove.call(this, key)
      }
    })
    const response = page.waitForResponse(response => new URL(response.url()).pathname === `/api/code/${boundary === 'catalog' ? 'contexts' : `tabs/${boundary}`}`)
    releases[0]!()
    await response
    await page.clock.fastForward('02:30')
    const state = await page.evaluate(() => ({
      writes: Reflect.get(window, 'codeBootstrapWrites'),
      candidate: sessionStorage.getItem('engram.operator-code.view-candidate.v2'),
      pair: sessionStorage.getItem('engram.operator-code.resume.v1'),
    }))
    expect(state.writes).toEqual([])
    expect(state.candidate).toBe(candidate)
    expect(state.pair === null).toBe(boundary === 'handshake')
    expect(requests.filter(path => path.endsWith('/lease'))).toEqual([])

    held = null
    await page.locator('a[href="/code"]').first().click()
    await expect(page.getByTestId('code-context-snapshot').getByRole('option', { name: 'Pinned snapshot' })).toHaveCount(1)
    if (boundary === 'handshake') {
      await page.getByTestId('code-context-snapshot').selectOption('selection')
      await page.getByTestId('code-pin-context').click()
      await expect(page.getByTestId('code-context-pinned')).toContainText('Pinned snapshot')
      expect(requests.filter(path => path === '/api/code/tabs/handshake')).toHaveLength(3)
    } else {
      await expect(page.getByTestId('code-context-pinned')).toContainText('Pinned snapshot')
      expect(requests.filter(path => path === '/api/code/tabs/resume')).toHaveLength(2)
    }
  })
}
