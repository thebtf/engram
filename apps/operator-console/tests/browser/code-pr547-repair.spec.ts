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

for (const failure of ['unavailable', 'timeout', 'offline', 'malformed'] as const) {
  test(`historical pin and released results survive ${failure} status until explicit retry`, async ({ page }) => {
    let published = false
    let failStatus = false
    const pins: string[] = []
    const newer = { ...entry, indexed_snapshot: { label: 'Newer snapshot' }, view_ref: 'newer-view', selection_ref: 'newer-selection' }
    const context = { source_id: 'source-1', checkout_id: 'checkout-1', view_id: 'view-1', profile_id: 'profile-1', generation: 1 }
    const item = { ref: { source_id: context.source_id, view_id: context.view_id, entity_key: 'old-result' }, membership_id: '60000000-0000-4000-8000-000000000001', path: 'src/old-view.ts', span: { byte_start: 0, byte_end: 12, line_start: 1, line_end: 1 }, content_digest: 'old-digest', kind: 'function', language: 'typescript', excerpt: 'old result', match_sources: ['lexical'], score: 1 }
    const envelope = { schema: 'engram.code-query/1', status: 'ok', contexts: [context], items: [item], warnings: [], retrieval: { mode: 'lexical' }, freshness: { state: 'historical' }, coverage: {}, truncated: false }
    const intent = { intent_ref: 'historical-intent', state: 'queued', attempt: 1, retryable: false, created_at: '2026-09-15T00:00:00Z', updated_at: '2026-09-15T00:00:00Z' }
    await page.clock.install()
    await page.route('**/api/auth/me', route => route.fulfill({ json: { auth_disabled: true } }))
    await page.route('**/api/code/**', async route => {
      const path = new URL(route.request().url()).pathname
      if (path === '/api/code/tabs/handshake') await route.fulfill({ json: binding })
      else if (path === '/api/code/contexts') await route.fulfill({ json: { contexts: published ? [newer] : [entry] } })
      else if (path.endsWith('/context')) { pins.push(route.request().postDataJSON().selection_ref); await route.fulfill({ status: 204 }) }
      else if (path === '/api/code/status') {
        if (!failStatus) await route.fulfill({ json: { total_chunks: 1, embedded_chunks: 1, embedding: { coverage: 'complete' }, freshness: { state: published ? 'historical' : 'observed_current' } } })
        else if (failure === 'offline') await route.abort('internetdisconnected')
        else if (failure === 'malformed') await route.fulfill({ json: { total_chunks: 'invalid', embedded_chunks: 1, embedding: { coverage: 'complete' } } })
        else await route.fulfill({ status: failure === 'timeout' ? 408 : 503 })
      } else if (path === '/api/code/structure' || path === '/api/code/search' || path === '/api/code/source') await route.fulfill({ json: envelope })
      else if (path === '/api/code/index-intents') await route.fulfill({ status: 202, json: intent })
      else if (path === '/api/code/index-intents/historical-intent') await route.fulfill({ json: intent })
      else await route.fulfill({ status: 500 })
    })
    await page.goto('/code')
    await page.getByTestId('code-context-snapshot').selectOption('selection')
    await page.getByTestId('code-pin-context').click()
    await page.getByTestId('code-query-input').fill('old result')
    await page.getByTestId('code-search-submit').click()
    await page.getByTestId('code-search-source').click()
    await expect(page.getByTestId('code-source-result')).toHaveText('old result')
    await page.getByTestId('index-intent-reindex').click()
    await expect(page.getByTestId('index-intent-state')).toHaveAttribute('data-state', 'queued')
    await expect(page.getByTestId('index-intent-reindex')).toBeDisabled()
    await expect(page.getByRole('button', { name: 'Обновить разрешённые варианты', exact: true })).toBeEnabled()
    await expect(page.getByTestId('code-context-snapshot')).toBeEnabled()
    const stored = await page.evaluate(() => ({ pin: sessionStorage.getItem('engram.operator-code.view-candidate.v2'), intent: sessionStorage.getItem('engram.operator-code.index-intent.v1') }))
    published = true
    failStatus = true
    await page.getByRole('button', { name: 'Обновить разрешённые варианты' }).click()
    await expect(page.locator('main.code-page')).toHaveAttribute('data-catalog-state', failure === 'offline' ? 'offline' : 'unavailable')
    await expect(page.getByTestId('code-context-pinned')).toContainText('Pinned snapshot')
    await expect(page.getByTestId('code-search-results')).toContainText('src/old-view.ts')
    await expect(page.getByTestId('code-source-result')).toHaveText('old result')
    await expect(page.getByTestId('index-intent-state')).toHaveAttribute('data-state', 'queued')
    expect(await page.evaluate(() => ({ pin: sessionStorage.getItem('engram.operator-code.view-candidate.v2'), intent: sessionStorage.getItem('engram.operator-code.index-intent.v1') }))).toEqual(stored)
    failStatus = false
    await expect(page.getByRole('button', { name: 'Обновить статус', exact: true })).toBeEnabled()
    await page.getByRole('button', { name: 'Обновить статус', exact: true }).click()
    await expect(page.getByTestId('code-status')).toContainText('Старый снимок')
    await expect(page.getByTestId('code-context-snapshot').getByRole('option', { name: 'Newer snapshot' })).toHaveCount(1)
    await expect(page.getByTestId('code-context-pinned')).toContainText('Pinned snapshot')
    await expect(page.getByTestId('code-search-results')).toContainText('src/old-view.ts')
    expect(pins).toEqual(['selection'])
    await expect(page.getByTestId('index-intent-state')).toHaveAttribute('data-state', 'queued')
    await page.getByTestId('code-context-snapshot').selectOption('newer-selection')
    await expect(page.getByTestId('code-pin-context')).toBeEnabled()
    await page.getByTestId('code-pin-context').click()
    await expect(page.getByTestId('code-context-pinned')).toContainText('Newer snapshot')
    expect(pins).toEqual(['selection', 'newer-selection'])
  })
}

for (const reauthorization of ['ready', 'unavailable', 'malformed', 'denied', 'mismatch'] as const) {
  test(`SPA historical remount reauthorizes the bound View with ${reauthorization} status and a newer current catalog`, async ({ page }) => {
    let published = false
    let statusMode: typeof reauthorization | 'ready' = 'ready'
    const requests: string[] = []
    const pins: string[] = []
    const sourceRequests: Record<string, unknown>[] = []
    const newer = { ...entry, indexed_snapshot: { label: 'Newer snapshot' }, view_ref: 'newer-view', selection_ref: 'newer-selection' }
    const context = { source_id: 'source-1', checkout_id: 'checkout-1', view_id: 'view-1', profile_id: 'profile-1', generation: 1 }
    const ref = { source_id: context.source_id, view_id: context.view_id, entity_key: 'historical-caller' }
    const related = { ...ref, entity_key: 'historical-callee' }
    const span = { byte_start: 0, byte_end: 12, line_start: 1, line_end: 1 }
    const item = { ref, membership_id: '60000000-0000-4000-8000-000000000001', path: 'src/old-view.ts', span, content_digest: 'old-digest', kind: 'function', language: 'typescript', excerpt: 'oldCall()', match_sources: ['lexical'], score: 1 }
    const envelope = { schema: 'engram.code-query/1', status: 'ok', contexts: [context], items: [item], warnings: [], retrieval: { mode: 'lexical' }, freshness: { state: 'historical' }, coverage: {}, truncated: false }
    const referenceSiteId = '50000000-0000-4000-8000-000000000005'
    const evidence = { ref, precision: 'reference_site', reference_site_id: referenceSiteId }
    const callerNode = { entity: ref, context_ref: { ...context, analysis_profile_id: context.profile_id }, source_state: 'available', source_read: { entity_key: ref.entity_key, membership_id: item.membership_id, span, content_digest: item.content_digest } }
    const calleeNode = { entity: related, context_ref: callerNode.context_ref, source_state: 'unavailable' }
    await page.route('**/api/auth/me', route => route.fulfill({ json: { auth_disabled: true } }))
    await page.route('**/api/code/**', async route => {
      const path = new URL(route.request().url()).pathname
      requests.push(path)
      if (path === '/api/code/tabs/handshake') await route.fulfill({ json: binding })
      else if (path === '/api/code/tabs/resume') await route.fulfill({ json: { ...binding, document_proof: 'resumed-proof' } })
      else if (path === '/api/code/contexts') await route.fulfill({ json: { contexts: published ? [newer] : [entry] } })
      else if (path.endsWith('/context')) { pins.push(route.request().postDataJSON().selection_ref); await route.fulfill({ status: 204 }) }
      else if (path === '/api/code/status') {
        if (published) expect(route.request().postDataJSON()).toEqual({ tab_binding_id: binding.tab_binding_id, document_proof: 'resumed-proof' })
        if (statusMode === 'denied' || statusMode === 'mismatch') await route.fulfill({ status: statusMode === 'denied' ? 403 : 409 })
        else if (statusMode === 'unavailable') await route.fulfill({ status: 503 })
        else if (statusMode === 'malformed') await route.fulfill({ json: { total_chunks: 'invalid', embedded_chunks: 1 } })
        else await route.fulfill({ json: { total_chunks: 1, embedded_chunks: 1, embedding: { coverage: 'complete' }, freshness: { state: published ? 'historical' : 'observed_current' } } })
      } else if (path === '/api/code/structure' || path === '/api/code/search') await route.fulfill({ json: envelope })
      else if (path === '/api/code/graph') await route.fulfill({
        json: {
          ...envelope,
          graph: { nodes: [ref, related], edges: [{ from: ref, to: related, relation: 'calls', evidence_kind: 'RESOLVED', evidence: [evidence] }], stop_reason: 'complete' },
          navigation: { nodes: [callerNode, calleeNode], edges: [{ from: callerNode, to: calleeNode, relation: 'calls', evidence_kind: 'RESOLVED', evidence: [{ ref, precision: 'reference_site', source_state: 'available', source_read: { ...callerNode.source_read, reference_site_id: referenceSiteId } }] }] },
        }
      })
      else if (path === '/api/code/source') { sourceRequests.push(route.request().postDataJSON()); await route.fulfill({ json: envelope }) }
      else await route.fulfill({ status: 500 })
    })
    await page.goto('/code')
    await page.getByTestId('code-context-snapshot').selectOption('selection')
    await page.getByTestId('code-pin-context').click()
    await expect(page.getByTestId('code-structure-results')).toContainText('src/old-view.ts')
    const stored = await page.evaluate(() => sessionStorage.getItem('engram.operator-code.view-candidate.v2'))
    published = true
    await page.locator('a[href="/memory"]').first().click()
    await expect(page).toHaveURL(/\/memory$/)
    statusMode = reauthorization
    const remountStart = requests.length
    await page.locator('a[href="/code"]').first().click()
    await expect(page).toHaveURL(/\/code$/)
    if (reauthorization !== 'ready') {
      await expect(page.locator('main.code-page')).toHaveAttribute('data-catalog-state', reauthorization === 'denied' || reauthorization === 'mismatch' ? 'snapshot-rejected' : 'unavailable')
      await expect(page.getByTestId('code-context-pinned')).toHaveCount(0)
      await expect(page.getByTestId('code-query-input')).toHaveCount(0)
      await expect(page.getByRole('button', { name: 'Обновить статус', exact: true })).toBeDisabled()
      await expect(page.getByTestId('index-intent-reindex')).toHaveCount(0)
      expect(requests.slice(remountStart)).toEqual(['/api/code/tabs/resume', '/api/code/contexts', '/api/code/status'])
      expect(await page.evaluate(() => sessionStorage.getItem('engram.operator-code.view-candidate.v2'))).toBe(reauthorization === 'malformed' || reauthorization === 'unavailable' ? stored : null)
      statusMode = 'ready'
      await expect(page.getByRole('button', { name: 'Обновить разрешённые варианты', exact: true })).toBeEnabled()
      await page.getByRole('button', { name: 'Обновить разрешённые варианты' }).click()
      if (reauthorization === 'denied' || reauthorization === 'mismatch') {
        await expect(page.getByTestId('code-context-snapshot').getByRole('option', { name: 'Newer snapshot' })).toHaveCount(1)
        await page.locator('a[href="/memory"]').first().click()
        await expect(page).toHaveURL(/\/memory$/)
        await page.locator('a[href="/code"]').first().click()
        await expect(page.getByTestId('code-context-snapshot').getByRole('option', { name: 'Newer snapshot' })).toHaveCount(1)
        await expect(page.getByTestId('code-context-pinned')).toHaveCount(0)
        expect(requests.filter(path => path === '/api/code/status')).toHaveLength(2)
        expect(pins).toEqual(['selection'])
        return
      }
    }
    await expect(page.getByTestId('code-context-pinned')).toContainText('Pinned snapshot')
    await expect(page.getByTestId('code-context-snapshot').getByRole('option', { name: 'Newer snapshot' })).toHaveCount(1)
    await expect(page.getByTestId('code-context-snapshot').getByRole('option', { name: 'Pinned snapshot' })).toHaveCount(0)
    await expect(page.getByTestId('code-status')).toContainText('Старый снимок')
    await expect(page.getByTestId('code-structure-results')).toContainText('src/old-view.ts')
    await page.getByTestId('code-query-input').fill('oldCall')
    await page.getByTestId('code-search-submit').click()
    await page.getByTestId('code-search-explore').click()
    await expect(page.getByTestId('code-graph-results')).toContainText('historical-callee')
    await page.getByRole('button', { name: /Relation list|Список связей/ }).click()
    await page.getByTestId('code-graph-results').getByRole('button').first().click()
    await expect(page.getByTestId('code-graph-evidence')).toContainText('reference_site')
    await page.getByTestId('code-graph-reference-source').click()
    await expect(page.getByTestId('code-source-result')).toHaveText('oldCall()')
    expect(sourceRequests).toEqual([{ tab_binding_id: binding.tab_binding_id, document_proof: 'resumed-proof', entity_key: ref.entity_key, membership_id: item.membership_id, span, content_digest: item.content_digest, reference_site_id: referenceSiteId }])
    expect(await page.evaluate(() => sessionStorage.getItem('engram.operator-code.view-candidate.v2'))).toBe(stored)
    expect(pins).toEqual(['selection'])
  })
}

for (const { boundary, leaseStatus } of [
  { boundary: 'contexts', leaseStatus: 503 },
  { boundary: 'status', leaseStatus: 503 },
  { boundary: 'status', leaseStatus: 403 },
] as const) {
  test(`delayed ${boundary} after lease ${leaseStatus} preserves pending ownership and safe recovery`, async ({ page }) => {
    let handshakes = 0
    let holdOld = false
    let oldHeld = false
    let recoveryHeld = false
    let releaseOld: () => void = () => { }
    let releaseRecovery: () => void = () => { }
    const pins: string[] = []
    const contexts: string[] = []
    const recovered = { ...entry, view_ref: 'recovered-view', selection_ref: 'recovered-selection', indexed_snapshot: { label: 'Recovered snapshot' } }
    const stale = { ...entry, indexed_snapshot: { label: 'Stale response snapshot' } }
    await page.clock.install({ time: new Date('2026-09-15T00:00:00Z') })
    await page.route('**/api/auth/me', route => route.fulfill({ json: { auth_disabled: true } }))
    await page.route('**/api/code/**', async route => {
      const path = new URL(route.request().url()).pathname
      if (path === '/api/code/tabs/handshake') {
        handshakes++
        await route.fulfill({ json: { ...binding, document_proof: handshakes === 1 ? 'proof' : 'recovered-proof' } })
      } else if (path.endsWith('/lease')) await route.fulfill({ status: leaseStatus })
      else if (path === '/api/code/contexts' || path === '/api/code/status') {
        const proof = route.request().postDataJSON().document_proof
        if (path === '/api/code/contexts') contexts.push(proof)
        const obsolete = holdOld && proof === 'proof' && path === `/api/code/${boundary}`
        if (obsolete) await new Promise<void>(resolve => { oldHeld = true; releaseOld = resolve })
        else if (path === '/api/code/contexts' && proof === 'recovered-proof') {
          await new Promise<void>(resolve => { recoveryHeld = true; releaseRecovery = resolve })
        }
        if (path === '/api/code/contexts') await route.fulfill({ json: { contexts: [obsolete ? stale : proof === 'proof' ? entry : recovered] } })
        else await route.fulfill({ json: { total_chunks: obsolete ? 999 : 1, embedded_chunks: 1, embedding: { coverage: 'complete' } } })
      } else if (path.endsWith('/context')) {
        pins.push(route.request().postDataJSON().selection_ref)
        await route.fulfill({ status: 204 })
      } else await route.fulfill({ status: 403 })
    })
    await page.goto('/code')
    await page.getByTestId('code-context-snapshot').selectOption('selection')
    await page.getByTestId('code-pin-context').click()
    const refresh = page.getByRole('button', { name: 'Обновить разрешённые варианты' })
    const refreshStatus = page.getByRole('button', { name: 'Обновить статус', exact: true })
    await expect(refreshStatus).toBeEnabled()
    await page.clock.fastForward('00:50')
    holdOld = true
    await (boundary === 'contexts' ? refresh : refreshStatus).click()
    await expect.poll(() => oldHeld).toBe(true)
    await page.clock.fastForward('00:11')
    await expect(page.locator('.phase')).toHaveAttribute('data-state', leaseStatus === 403 ? 'denied' : 'error')
    await expect(page.getByTestId('code-context-pinned')).toHaveCount(0)
    await expect(refreshStatus).toBeDisabled()
    await expect(refresh).toBeDisabled()
    await refresh.dispatchEvent('click')
    await page.getByTestId('code-pin-context').dispatchEvent('click')
    expect(pins).toEqual(['selection'])
    expect(contexts).toEqual(boundary === 'contexts' ? ['proof', 'proof'] : ['proof'])
    const obsoleteResponse = page.waitForResponse(response => new URL(response.url()).pathname === `/api/code/${boundary}` && response.request().postDataJSON().document_proof === 'proof')
    if (leaseStatus === 403) {
      await expect(page.getByTestId('code-retry-lease')).toHaveCount(0)
      await expect(page.getByTestId('code-context-snapshot')).toBeEnabled()
      releaseOld()
      await (await obsoleteResponse).finished()
      await page.clock.runFor(50)
      await expect(page.locator('.phase')).toHaveAttribute('data-state', 'denied')
      await expect(page.getByTestId('code-context-pinned')).toHaveCount(0)
      await expect(page.getByTestId('code-status')).toHaveCount(0)
      expect(handshakes).toBe(1)
      return
    }
    await page.getByTestId('code-retry-lease').click()
    await expect.poll(() => recoveryHeld).toBe(true)
    await expect(page.locator('.phase')).toHaveAttribute('data-state', 'ready')
    await expect(refresh).toBeDisabled()
    releaseOld()
    await (await obsoleteResponse).finished()
    await page.clock.runFor(50)
    await expect(refresh).toBeDisabled()
    await expect(page.getByTestId('code-context-snapshot')).toHaveCount(0)
    await expect(page.getByTestId('code-status')).toHaveCount(0)
    releaseRecovery()
    await expect(refresh).toBeEnabled()
    await expect(page.getByTestId('code-context-snapshot').getByRole('option', { name: 'Recovered snapshot' })).toHaveCount(1)
    await expect(page.getByTestId('code-context-snapshot').getByRole('option', { name: 'Stale response snapshot' })).toHaveCount(0)
    await expect(page.getByTestId('code-context-pinned')).toHaveCount(0)
    await page.getByTestId('code-context-snapshot').selectOption('recovered-selection')
    await page.getByTestId('code-pin-context').click()
    await expect(page.getByTestId('code-context-pinned')).toContainText('Recovered snapshot')
    await expect(refreshStatus).toBeEnabled()
    await expect(page.getByTestId('code-status')).toContainText('1 / 1')
    expect(pins).toEqual(['selection', 'recovered-selection'])
    expect(handshakes).toBe(2)
  })
}

for (const boundary of ['structure', 'search', 'graph', 'source'] as const) {
  for (const scenario of [
    { leaseStatus: 503, outcome: 'released' },
    { leaseStatus: 403, outcome: 'released' },
    { leaseStatus: 503, outcome: 'denied' },
    ...(boundary === 'source' ? [{ leaseStatus: 503, outcome: 'offline' }] : []),
    ...(boundary === 'search' ? [{ leaseStatus: 503, outcome: 'malformed' }] : []),
  ]) {
    test(`delayed ${boundary} ${scenario.outcome} after lease ${scenario.leaseStatus} cannot commit into a replacement request`, async ({ page }) => {
      let handshakes = 0
      let holdOld = false
      let oldHeld = false
      let holdCurrent = false
      let currentHeld = false
      let releaseOld: () => void = () => { }
      let releaseCurrent: () => void = () => { }
      const pins: string[] = []
      const searches: string[] = []
      const requests: string[] = []
      const recovered = { ...entry, selection_ref: 'recovered-selection', indexed_snapshot: { label: 'Recovered snapshot' }, view_ref: boundary === 'structure' ? 'new-view' : entry.view_ref }
      const released = (marker: string, current: boolean) => {
        const context = { source_id: 'source-1', checkout_id: 'checkout-1', view_id: current && boundary === 'structure' ? 'view-2' : 'view-1', profile_id: 'profile-1', generation: current && boundary === 'structure' ? 2 : 1 }
        const ref = { source_id: context.source_id, view_id: context.view_id, entity_key: marker }
        const related = { ...ref, entity_key: `${marker}-callee` }
        const span = { byte_start: 0, byte_end: 12, line_start: 1, line_end: 1 }
        const source = { entity_key: ref.entity_key, membership_id: '60000000-0000-4000-8000-000000000001', span, content_digest: `${marker}-digest` }
        const node = { entity: ref, context_ref: { ...context, analysis_profile_id: context.profile_id }, source_state: 'available', source_read: source }
        const callee = { entity: related, context_ref: node.context_ref, source_state: 'unavailable' }
        return {
          schema: 'engram.code-query/1', status: 'partial', contexts: [context],
          items: [{ ref, membership_id: '60000000-0000-4000-8000-000000000001', path: `src/${marker}.ts`, span, content_digest: source.content_digest, kind: 'function', language: 'typescript', excerpt: `${marker}()`, match_sources: ['lexical'], score: 1 }],
          warnings: [`${marker}-warning`], retrieval: { mode: `${marker}-mode` }, freshness: { state: 'observed_current' }, coverage: {}, truncated: true, continuation: `${marker}-next`,
          graph: { nodes: [ref, related], edges: [{ from: ref, to: related, relation: 'calls', evidence_kind: 'RESOLVED', evidence: [] }], stop_reason: 'budget' },
          navigation: { nodes: [node, callee], edges: [{ from: node, to: callee, relation: 'calls', evidence_kind: 'RESOLVED', evidence: [] }] },
        }
      }
      await page.clock.install({ time: new Date('2026-09-15T00:00:00Z') })
      await page.route('**/api/auth/me', route => route.fulfill({ json: { auth_disabled: true } }))
      await page.route('**/api/code/**', async route => {
        const path = new URL(route.request().url()).pathname
        requests.push(path)
        const body = route.request().postDataJSON()
        if (path === '/api/code/tabs/handshake') {
          handshakes++
          await route.fulfill({ json: { ...binding, document_proof: handshakes === 1 ? 'proof' : 'recovered-proof' } })
        } else if (path.endsWith('/lease')) await route.fulfill({ status: scenario.leaseStatus })
        else if (path === '/api/code/contexts') await route.fulfill({ json: { contexts: [body.document_proof === 'proof' ? entry : recovered] } })
        else if (path.endsWith('/context')) {
          pins.push(body.selection_ref)
          await route.fulfill({ status: 204 })
        } else if (path === '/api/code/status') await route.fulfill({ json: { total_chunks: 1, embedded_chunks: 1, embedding: { coverage: 'complete' }, freshness: { state: 'observed_current' } } })
        else if (['/api/code/structure', '/api/code/search', '/api/code/graph', '/api/code/source'].includes(path)) {
          if (path === '/api/code/search') searches.push(body.query)
          const obsolete = holdOld && body.document_proof === 'proof' && path === `/api/code/${boundary}`
          if (obsolete) await new Promise<void>(resolve => { oldHeld = true; releaseOld = resolve })
          else if (holdCurrent && body.document_proof === 'recovered-proof' && path === `/api/code/${boundary}`) {
            await new Promise<void>(resolve => { currentHeld = true; releaseCurrent = resolve })
          }
          if (obsolete && scenario.outcome === 'denied') await route.fulfill({ status: 403 })
          else if (obsolete && scenario.outcome === 'offline') await route.abort('internetdisconnected')
          else if (obsolete && scenario.outcome === 'malformed') await route.fulfill({ json: { schema: 'engram.code-query/1', status: 'ok' } })
          else await route.fulfill({ json: released(obsolete ? 'obsolete' : body.document_proof === 'proof' ? 'initial' : 'current', body.document_proof !== 'proof') })
        } else await route.fulfill({ status: 403 })
      })
      await page.goto('/code')
      await page.getByTestId('code-context-snapshot').selectOption('selection')
      await page.getByTestId('code-pin-context').click()
      await expect(page.getByTestId('code-structure-results')).toContainText('src/initial.ts')
      if (boundary !== 'structure') {
        await page.getByTestId('code-query-input').fill('initial query')
        await page.getByTestId('code-search-submit').click()
        await expect(page.getByTestId('code-search-results')).toContainText('src/initial.ts')
      }
      if (boundary === 'graph') {
        await page.getByTestId('code-search-explore').click()
        await expect(page.getByTestId('code-graph-results')).toContainText('initial-callee')
      }
      await page.clock.fastForward('00:50')
      holdOld = true
      await page.getByTestId(boundary === 'structure' ? 'code-structure-next' : boundary === 'search' ? 'code-search-next' : boundary === 'graph' ? 'code-graph-continue' : 'code-search-source').click()
      await expect.poll(() => oldHeld).toBe(true)
      await page.clock.fastForward('00:11')
      await expect(page.locator('.phase')).toHaveAttribute('data-state', scenario.leaseStatus === 403 ? 'denied' : 'error')
      await expect(page.getByTestId('code-context-pinned')).toHaveCount(0)
      await expect(page.getByTestId('code-query-input')).toHaveCount(0)
      await expect(page.getByTestId('index-intent-reindex')).toHaveCount(0)
      await expect(page.getByTestId('code-grant-chooser')).toHaveCount(0)
      const refresh = page.getByRole('button', { name: 'Обновить разрешённые варианты' })
      await expect(refresh).toBeDisabled()
      await expect(page.getByRole('button', { name: 'Обновить статус', exact: true })).toBeDisabled()
      const oldFinished = scenario.outcome === 'offline'
        ? page.waitForEvent('requestfailed', request => new URL(request.url()).pathname === `/api/code/${boundary}` && request.postDataJSON().document_proof === 'proof')
        : page.waitForResponse(response => new URL(response.url()).pathname === `/api/code/${boundary}` && response.request().postDataJSON().document_proof === 'proof')
      if (scenario.leaseStatus === 403) {
        const deniedRequests = [...requests]
        await expect(page.getByTestId('code-results-unselected')).toBeVisible()
        await expect(page.getByTestId('code-release-state')).toHaveAttribute('data-state', 'unselected')
        await expect(page.getByTestId('code-retry-lease')).toHaveCount(0)
        releaseOld()
        await (await oldFinished).finished()
        await page.clock.runFor(50)
        await expect(page.getByTestId('code-results-unselected')).toBeVisible()
        await expect(page.getByTestId('code-release-state')).toHaveAttribute('data-state', 'unselected')
        await expect(page.locator('.result-grid')).toHaveCount(0)
        await expect(page.getByText('obsolete-warning', { exact: true })).toHaveCount(0)
        await expect(refresh).toBeDisabled()
        await expect(page.getByRole('button', { name: 'Обновить статус', exact: true })).toBeDisabled()
        await expect(page.locator('.phase')).toHaveAttribute('data-state', 'denied')
        await expect(page.getByTestId('code-context-pinned')).toHaveCount(0)
        await expect(page.locator('.readiness')).toHaveCount(0)
        await expect(page.getByTestId('code-source-result')).toHaveCount(0)
        await expect(page.getByTestId('code-grant-chooser')).toHaveCount(0)
        expect(await page.evaluate(() => sessionStorage.getItem('engram.operator-code.view-candidate.v2'))).toBeNull()
        expect(requests).toEqual(deniedRequests)
        expect(pins).toEqual(['selection'])
        expect(handshakes).toBe(1)
        return
      }
      await page.getByTestId('code-retry-lease').click()
      await expect(page.getByTestId('code-context-snapshot').getByRole('option', { name: 'Recovered snapshot' })).toHaveCount(1)
      await page.getByTestId('code-context-snapshot').selectOption('recovered-selection')
      holdCurrent = boundary === 'structure'
      await page.getByTestId('code-pin-context').click()
      await expect(page.getByTestId('code-context-pinned')).toContainText('Recovered snapshot')
      if (boundary !== 'structure') {
        await expect(page.getByTestId('code-structure-results')).toContainText('src/current.ts')
        await page.getByTestId('code-query-input').fill('current query')
        holdCurrent = boundary === 'search'
        await page.getByTestId('code-search-submit').click()
        if (boundary !== 'search') {
          await expect(page.getByTestId('code-search-results')).toContainText('src/current.ts')
          holdCurrent = true
          await page.getByTestId(boundary === 'graph' ? 'code-search-explore' : 'code-search-source').click()
        }
      }
      await expect.poll(() => currentHeld).toBe(true)
      const storedPin = await page.evaluate(() => sessionStorage.getItem('engram.operator-code.view-candidate.v2'))
      releaseOld()
      await oldFinished
      await page.clock.runFor(50)
      await expect(refresh).toBeDisabled()
      await expect(page.getByTestId('code-query-input')).toBeDisabled()
      const state = boundary === 'graph'
        ? page.getByTestId('code-graph-heading').locator('..').locator('..').locator('span[data-state]')
        : page.locator(`.${boundary === 'source' ? 'source' : `${boundary}-panel`} .panel-head span`)
      await expect(state).toHaveAttribute('data-state', 'loading')
      await expect(page.getByText('obsolete-warning', { exact: true })).toHaveCount(0)
      await expect(page.getByTestId('code-source-result')).toHaveCount(0)
      await expect(page.getByTestId('code-status')).toContainText('1 / 1')
      await expect(page.getByTestId('code-context-pinned')).toContainText('Recovered snapshot')
      expect(await page.evaluate(() => sessionStorage.getItem('engram.operator-code.view-candidate.v2'))).toBe(storedPin)
      holdCurrent = false
      releaseCurrent()
      await expect(state).toHaveAttribute('data-state', 'partial')
      await expect(refresh).toBeEnabled()
      await expect(page.getByTestId(boundary === 'source' ? 'code-source-result' : `code-${boundary}-results`)).toContainText(boundary === 'source' ? 'current()' : boundary === 'graph' ? 'current-callee' : 'src/current.ts')
      await expect(page.getByTestId('code-grant-chooser')).toHaveCount(0)
      if (boundary === 'search') {
        await expect(page.locator('.result-evidence')).toContainText('current-mode')
        await page.getByTestId('code-search-next').click()
        await expect(page.getByTestId('code-search-results')).toContainText('src/current.ts')
        expect(searches.slice(-2)).toEqual(['current query', 'current query'])
      }
      expect(pins).toEqual(['selection', 'recovered-selection'])
    })
  }
}

for (const leaseStatus of [503, 403]) {
  test(`delayed pin after lease ${leaseStatus} cannot restore authority or start follow-up reads`, async ({ page }) => {
    let handshakes = 0
    let oldHeld = false
    let currentHeld = false
    let releaseOld: () => void = () => { }
    let releaseCurrent: () => void = () => { }
    const statusProofs: string[] = []
    const next = { ...entry, view_ref: 'next-view', selection_ref: 'next-selection', indexed_snapshot: { label: 'Next snapshot' } }
    await page.clock.install({ time: new Date('2026-09-15T00:00:00Z') })
    await page.route('**/api/auth/me', route => route.fulfill({ json: { auth_disabled: true } }))
    await page.route('**/api/code/**', async route => {
      const path = new URL(route.request().url()).pathname
      const body = route.request().postDataJSON()
      if (path === '/api/code/tabs/handshake') {
        handshakes++
        await route.fulfill({ json: { ...binding, document_proof: handshakes === 1 ? 'proof' : 'next-proof' } })
      } else if (path.endsWith('/lease')) await route.fulfill({ status: leaseStatus })
      else if (path === '/api/code/contexts') await route.fulfill({ json: { contexts: [body.document_proof === 'proof' ? entry : next] } })
      else if (path.endsWith('/context')) {
        if (body.document_proof === 'proof') await new Promise<void>(resolve => { oldHeld = true; releaseOld = resolve })
        await route.fulfill({ status: 204 })
      } else if (path === '/api/code/status') {
        statusProofs.push(body.document_proof)
        await new Promise<void>(resolve => { currentHeld = true; releaseCurrent = resolve })
        await route.fulfill({ json: { total_chunks: 1, embedded_chunks: 1, embedding: { coverage: 'complete' } } })
      } else await route.fulfill({ status: 403 })
    })
    await page.goto('/code')
    await page.getByTestId('code-context-snapshot').selectOption('selection')
    await page.clock.fastForward('00:50')
    await page.getByTestId('code-pin-context').click()
    await expect.poll(() => oldHeld).toBe(true)
    await page.clock.fastForward('00:11')
    await expect(page.locator('.phase')).toHaveAttribute('data-state', leaseStatus === 403 ? 'denied' : 'error')
    const oldFinished = page.waitForResponse(response => response.url().endsWith('/context') && response.request().postDataJSON().document_proof === 'proof')
    if (leaseStatus === 503) {
      await page.getByTestId('code-retry-lease').click()
      await page.getByTestId('code-context-snapshot').selectOption('next-selection')
      await page.getByTestId('code-pin-context').click()
      await expect.poll(() => currentHeld).toBe(true)
      await expect(page.getByTestId('code-context-pinned')).toContainText('Next snapshot')
    }
    releaseOld()
    await oldFinished
    await page.clock.runFor(50)
    await expect(page.getByTestId('code-grant-chooser')).toHaveCount(0)
    if (leaseStatus === 403) {
      await expect(page.getByTestId('code-context-pinned')).toHaveCount(0)
      await expect(page.getByTestId('code-query-input')).toHaveCount(0)
      expect(await page.evaluate(() => sessionStorage.getItem('engram.operator-code.view-candidate.v2'))).toBeNull()
      expect(statusProofs).toEqual([])
    } else {
      await expect(page.getByTestId('code-context-pinned')).toContainText('Next snapshot')
      await expect(page.getByTestId('code-query-input')).toBeDisabled()
      expect(await page.evaluate(() => sessionStorage.getItem('engram.operator-code.view-candidate.v2'))).toContain('next-view')
      expect(statusProofs).toEqual(['next-proof'])
      releaseCurrent()
      await expect(page.getByTestId('code-status')).toContainText('1 / 1')
      await expect(page.getByTestId('code-query-input')).toBeEnabled()
    }
  })
}

for (const { boundary, invalidated } of [
  { boundary: 'status', invalidated: false },
  { boundary: 'search', invalidated: false },
  { boundary: 'contexts', invalidated: false },
  { boundary: 'status', invalidated: true },
] as const) {
  test(`completed intent defers discovery through pending ${boundary}${invalidated ? ' then discards it after lease denial' : ' without replacing its historical pin'}`, async ({ page }) => {
    let published = false
    let held = false
    let release: () => void = () => { }
    let holdRead = false
    const pins: string[] = []
    const catalogProofs: string[] = []
    const newer = { ...entry, indexed_snapshot: { label: 'Newly published snapshot' }, view_ref: 'new-view', selection_ref: 'new-selection' }
    const intent = { intent_ref: 'publication-intent', state: 'queued', attempt: 1, retryable: false, created_at: '2026-09-15T00:00:00Z', updated_at: '2026-09-15T00:00:00Z' }
    const context = { source_id: 'source-1', checkout_id: 'checkout-1', view_id: 'view-1', profile_id: 'profile-1', generation: 1 }
    const item = { ref: { source_id: context.source_id, view_id: context.view_id, entity_key: 'old-result' }, membership_id: '60000000-0000-4000-8000-000000000001', path: 'src/old-view.ts', span: { byte_start: 0, byte_end: 12, line_start: 1, line_end: 1 }, content_digest: 'old-digest', kind: 'function', language: 'typescript', excerpt: 'old result', match_sources: ['lexical'], score: 1 }
    const envelope = { schema: 'engram.code-query/1', status: 'ok', contexts: [context], items: [item], warnings: [], retrieval: { mode: 'lexical' }, freshness: { state: 'historical' }, coverage: {}, truncated: false }
    await page.clock.install({ time: new Date('2026-09-15T00:00:00Z') })
    await page.route('**/api/auth/me', route => route.fulfill({ json: { auth_disabled: true } }))
    await page.route('**/api/code/**', async route => {
      const path = new URL(route.request().url()).pathname
      if (path === '/api/code/tabs/handshake') await route.fulfill({ json: binding })
      else if (path.endsWith('/lease')) await route.fulfill({ status: invalidated ? 403 : 204 })
      else if (path === '/api/code/index-intents') await route.fulfill({ status: 202, json: intent })
      else if (path === '/api/code/index-intents/publication-intent') {
        published = true
        await route.fulfill({ json: { ...intent, state: 'completed', result: { view_ref: 'new-view', generation: 2 } } })
      } else if (path.endsWith('/context')) {
        pins.push(route.request().postDataJSON().selection_ref)
        await route.fulfill({ status: 204 })
      } else if (path === '/api/code/contexts' || path === '/api/code/status' || path === '/api/code/search') {
        const catalog = { contexts: [published ? newer : entry] }
        if (path.endsWith('/contexts')) catalogProofs.push(route.request().postDataJSON().document_proof)
        if (holdRead && path === `/api/code/${boundary}`) await new Promise<void>(resolve => { held = true; release = resolve })
        await route.fulfill({ json: path.endsWith('/contexts') ? catalog : path.endsWith('/status') ? { total_chunks: 1, embedded_chunks: 1, embedding: { coverage: 'complete' }, freshness: { state: published ? 'historical' : 'observed_current' } } : envelope })
      } else if (path === '/api/code/structure') await route.fulfill({ json: envelope })
      else await route.fulfill({ status: 403 })
    })
    await page.goto('/code')
    await page.getByTestId('code-context-snapshot').selectOption('selection')
    await page.getByTestId('code-pin-context').click()
    await expect(page.getByTestId('code-query-input')).toBeEnabled()
    await page.clock.fastForward('00:50')
    const storedPin = await page.evaluate(() => sessionStorage.getItem('engram.operator-code.view-candidate.v2'))
    await page.getByTestId('index-intent-reindex').click()
    await expect(page.getByTestId('index-intent-state')).toHaveAttribute('data-state', 'queued')
    await expect(page.getByTestId('index-intent-reindex')).toBeDisabled()
    await expect(page.getByRole('button', { name: 'Обновить разрешённые варианты', exact: true })).toBeEnabled()
    holdRead = true
    if (boundary === 'search') {
      await page.getByTestId('code-query-input').fill('old result')
      await page.getByTestId('code-search-submit').click()
    } else await page.getByRole('button', { name: boundary === 'status' ? 'Обновить статус' : 'Обновить разрешённые варианты', exact: true }).click()
    await expect.poll(() => held).toBe(true)
    await page.clock.fastForward('00:03')
    await expect(page.getByTestId('index-intent-state')).toHaveAttribute('data-state', 'completed')
    expect(catalogProofs).toHaveLength(boundary === 'contexts' ? 2 : 1)
    await expect(page.getByTestId('code-context-pinned')).toContainText('Pinned snapshot')
    expect(pins).toEqual(['selection'])
    const heldResponse = page.waitForResponse(response => new URL(response.url()).pathname === `/api/code/${boundary}`)
    if (invalidated) {
      await page.clock.fastForward('00:08')
      await expect(page.locator('.phase')).toHaveAttribute('data-state', 'denied')
      await expect(page.getByTestId('code-context-pinned')).toHaveCount(0)
      holdRead = false
      release()
      await (await heldResponse).finished()
      await page.clock.runFor(50)
      await expect(page.locator('.phase')).toHaveAttribute('data-state', 'denied')
      await expect(page.getByTestId('code-context-pinned')).toHaveCount(0)
      await expect(page.getByTestId('code-context-snapshot').getByRole('option', { name: 'Newly published snapshot' })).toHaveCount(0)
      await expect(page.getByRole('button', { name: 'Обновить разрешённые варианты' })).toBeDisabled()
      await expect(page.getByTestId('code-grant-chooser')).toHaveCount(0)
      expect(catalogProofs).toEqual(['proof'])
      expect(pins).toEqual(['selection'])
      return
    }
    holdRead = false
    release()
    await (await heldResponse).finished()
    await expect(page.getByTestId('code-context-snapshot').getByRole('option', { name: 'Newly published snapshot' })).toHaveCount(1)
    await expect(page.getByTestId('code-context-pinned')).toContainText('Pinned snapshot')
    if (boundary === 'search') await expect(page.getByTestId('code-search-results')).toContainText('src/old-view.ts')
    expect(await page.evaluate(() => sessionStorage.getItem('engram.operator-code.view-candidate.v2'))).toBe(storedPin)
    expect(pins).toEqual(['selection'])
    expect(catalogProofs).toEqual(boundary === 'contexts' ? ['proof', 'proof', 'proof'] : ['proof', 'proof'])
    await page.getByTestId('code-context-snapshot').selectOption('new-selection')
    await page.getByTestId('code-pin-context').click()
    await expect(page.getByTestId('code-context-pinned')).toContainText('Newly published snapshot')
    expect(pins).toEqual(['selection', 'new-selection'])
  })
}
