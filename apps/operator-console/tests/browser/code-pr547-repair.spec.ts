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
    const item = { ref: { source_id: context.source_id, view_id: context.view_id, entity_key: 'old-result' }, path: 'src/old-view.ts', span: { byte_start: 0, byte_end: 12, line_start: 1, line_end: 1 }, content_digest: 'old-digest', kind: 'function', language: 'typescript', excerpt: 'old result', match_sources: ['lexical'], score: 1 }
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
    const stored = await page.evaluate(() => ({ pin: sessionStorage.getItem('engram.operator-code.view-candidate.v2'), intent: sessionStorage.getItem('engram.operator-code.index-intent.v1') }))
    published = true
    failStatus = true
    await page.getByRole('button', { name: 'Обновить разрешённые варианты' }).click()
    await expect(page.getByTestId('code-context-message')).toContainText(/не выдал читаемый каталог|не может связаться/)
    await expect(page.getByTestId('code-context-pinned')).toContainText('Pinned snapshot')
    await expect(page.getByTestId('code-search-results')).toContainText('src/old-view.ts')
    await expect(page.getByTestId('code-source-result')).toHaveText('old result')
    await expect(page.getByTestId('index-intent-state')).toHaveAttribute('data-state', 'queued')
    expect(await page.evaluate(() => ({ pin: sessionStorage.getItem('engram.operator-code.view-candidate.v2'), intent: sessionStorage.getItem('engram.operator-code.index-intent.v1') }))).toEqual(stored)
    failStatus = false
    await page.getByRole('button', { name: 'Обновить статус', exact: true }).click()
    await expect(page.getByTestId('code-status')).toContainText('Старый снимок')
    await expect(page.getByTestId('code-context-snapshot').getByRole('option', { name: 'Newer snapshot' })).toHaveCount(1)
    await expect(page.getByTestId('code-context-pinned')).toContainText('Pinned snapshot')
    await expect(page.getByTestId('code-search-results')).toContainText('src/old-view.ts')
    expect(pins).toEqual(['selection'])
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
    const item = { ref, path: 'src/old-view.ts', span, content_digest: 'old-digest', kind: 'function', language: 'typescript', excerpt: 'oldCall()', match_sources: ['lexical'], score: 1 }
    const envelope = { schema: 'engram.code-query/1', status: 'ok', contexts: [context], items: [item], warnings: [], retrieval: { mode: 'lexical' }, freshness: { state: 'historical' }, coverage: {}, truncated: false }
    const referenceSiteId = '50000000-0000-4000-8000-000000000005'
    const evidence = { ref, precision: 'reference_site', reference_site_id: referenceSiteId }
    const callerNode = { entity: ref, context_ref: { ...context, analysis_profile_id: context.profile_id }, source_state: 'available', source_read: { entity_key: ref.entity_key, span, content_digest: item.content_digest } }
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
      await expect(page.getByTestId('code-context-message')).toContainText(/не выдал читаемый каталог|отклонил доступ/)
      await expect(page.getByTestId('code-context-pinned')).toHaveCount(0)
      await expect(page.getByTestId('code-query-input')).toHaveCount(0)
      await expect(page.getByRole('button', { name: 'Обновить статус', exact: true })).toBeDisabled()
      await expect(page.getByTestId('index-intent-reindex')).toHaveCount(0)
      expect(requests.slice(remountStart)).toEqual(['/api/code/tabs/resume', '/api/code/contexts', '/api/code/status'])
      expect(await page.evaluate(() => sessionStorage.getItem('engram.operator-code.view-candidate.v2'))).toBe(reauthorization === 'malformed' || reauthorization === 'unavailable' ? stored : null)
      statusMode = 'ready'
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
    expect(sourceRequests).toEqual([{ tab_binding_id: binding.tab_binding_id, document_proof: 'resumed-proof', entity_key: ref.entity_key, span, content_digest: item.content_digest, reference_site_id: referenceSiteId }])
    expect(await page.evaluate(() => sessionStorage.getItem('engram.operator-code.view-candidate.v2'))).toBe(stored)
    expect(pins).toEqual(['selection'])
  })
}
