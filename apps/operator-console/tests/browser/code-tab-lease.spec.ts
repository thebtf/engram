import { expect, test, type Route } from '@playwright/test'

const TAB_BINDING_ID = '60000000-0000-4000-8000-000000000041'
const DOCUMENT_PROOF = 'proof-current'

test('Code Explorer renews its live tab lease and leaves no renewal timer after teardown', async ({ page }) => {
  const handshakePayloads: unknown[] = []
  const leasePayloads: unknown[] = []

  await page.clock.install({ time: new Date('2026-09-15T00:00:00Z') })
  await page.route('**/api/code/**', async (route: Route) => {
    const pathname = new URL(route.request().url()).pathname
    if (pathname === '/api/code/tabs/handshake') {
      handshakePayloads.push(route.request().postDataJSON())
      await route.fulfill({
        json: {
          state: 'TAB_BINDING_READY',
          tab_binding_id: TAB_BINDING_ID,
          document_proof: DOCUMENT_PROOF,
          resume_nonce: 'resume-current',
          reload_token: 'reload-current',
        }
      })
      return
    }
    if (pathname === '/api/code/contexts') {
      await route.fulfill({ json: { contexts: [] } })
      return
    }
    if (pathname === `/api/code/tabs/${TAB_BINDING_ID}/lease`) {
      leasePayloads.push(route.request().postDataJSON())
      await route.fulfill({ status: 204 })
      return
    }
    if (pathname === `/api/code/tabs/${TAB_BINDING_ID}` && route.request().method() === 'DELETE') {
      await route.fulfill({ status: 204 })
      return
    }
    await route.fulfill({ status: 500 })
  })

  await page.goto('/code')
  await expect(page.getByTestId('code-context-empty')).toBeVisible()
  expect(handshakePayloads).toHaveLength(1)
  expect(handshakePayloads[0]).not.toHaveProperty('ambiguous')

  await page.clock.fastForward('01:00')
  await expect.poll(() => leasePayloads).toEqual([{ document_proof: DOCUMENT_PROOF }])
  await page.clock.fastForward('01:00')
  await expect.poll(() => leasePayloads).toEqual([
    { document_proof: DOCUMENT_PROOF },
    { document_proof: DOCUMENT_PROOF },
  ])

  await page.goto('/settings')
  await page.clock.fastForward('04:00')
  expect(leasePayloads).toEqual([
    { document_proof: DOCUMENT_PROOF },
    { document_proof: DOCUMENT_PROOF },
  ])
})

test('Code Explorer distinguishes failed embedding from never-indexed and pending work', async ({ page }) => {
  let embedding: Record<string, unknown> = { coverage: 'partial', job_state: 'failed_terminal', error_code: 'provider_contract', pending_jobs: 0 }
  let freshnessState = 'unknown'
  await page.route('**/api/code/**', async (route: Route) => {
    const pathname = new URL(route.request().url()).pathname
    if (pathname === '/api/code/tabs/handshake') {
      await route.fulfill({ json: { state: 'TAB_BINDING_READY', tab_binding_id: TAB_BINDING_ID, document_proof: DOCUMENT_PROOF, resume_nonce: 'resume-current', reload_token: 'reload-current' } })
    } else if (pathname === '/api/code/contexts') {
      await route.fulfill({ json: { contexts: [{ source_ref: 'source-engram', checkout_ref: 'checkout-current', repository: 'Engram', working_copy: 'Current checkout', indexed_snapshot: { label: 'Current snapshot', revision: '1a9dad0', published_at: '2026-09-17T00:00:00Z' }, view_ref: 'view-current', selection_ref: 'context-current', index_intent_available: false }] } })
    } else if (pathname === `/api/code/tabs/${TAB_BINDING_ID}/context`) {
      await route.fulfill({ status: 204 })
    } else if (pathname === '/api/code/status') {
      await route.fulfill({ json: { total_chunks: 0, embedded_chunks: 0, embedding, freshness: { state: freshnessState } } })
    } else {
      await route.fulfill({ status: 500 })
    }
  })

  await page.goto('/code')
  await page.getByTestId('code-context-repository').selectOption({ label: 'Engram' })
  await page.getByTestId('code-context-working-copy').selectOption({ label: 'Current checkout' })
  await page.getByTestId('code-context-snapshot').selectOption({ label: 'Current snapshot' })
  await page.getByTestId('code-pin-context').click()
  await expect(page.locator('.readiness')).toHaveAttribute('data-state', 'failed')
  await expect(page.locator('.readiness')).toContainText(/индекс/i)
  await expect(page.locator('.readiness')).not.toContainText('provider_contract')
  await expect(page.getByRole('heading', { name: 'Исследование выбранного снимка' })).toBeVisible()
  await expect(page.locator('.empty-index')).toHaveCount(0)

  embedding = { coverage: 'none', job_state: null, error_code: null, pending_jobs: 0 }
  await page.getByRole('button', { name: 'Обновить статус' }).click()
  await expect(page.locator('.readiness')).toHaveAttribute('data-state', 'needs-indexing')
  await expect(page.locator('.empty-index').getByRole('button', { name: 'Запросить переиндексацию' })).toBeVisible()

  embedding = { coverage: 'partial', job_state: 'retry_scheduled', error_code: 'provider_unavailable', pending_jobs: 1 }
  freshnessState = 'observed_current'
  await page.getByRole('button', { name: 'Обновить статус' }).click()
  await expect(page.locator('.readiness')).toHaveAttribute('data-state', 'updating')
  await expect(page.locator('.empty-index').getByRole('button', { name: 'Запросить переиндексацию' })).toBeVisible()

  embedding = { Coverage: 'partial', JobState: 'failed_terminal', ErrorCode: 'provider_contract' }
  await page.getByRole('button', { name: 'Обновить статус' }).click()
  await expect(page.locator('.readiness')).toHaveAttribute('data-state', 'failed')
  await expect(page.locator('.empty-index')).toHaveCount(0)

  embedding = { coverage: 'partial', job_state: 'future_state', error_code: null }
  await page.getByRole('button', { name: 'Обновить статус' }).click()
  await expect(page.locator('.readiness')).toHaveAttribute('data-state', 'unknown')
})

test('Code Explorer resynchronizes a completed selection after catalog refresh while retaining a later partial choice', async ({ page }) => {
  let contextRequests = 0
  const initialCatalog = {
    contexts: [{
      source_ref: 'source-engram', checkout_ref: 'checkout-current',
      repository: 'Engram',
      working_copy: 'stale candidate checkout',
      indexed_snapshot: { label: 'Stale candidate snapshot', revision: '1a9dad0', published_at: '2026-09-17T00:00:00Z' },
      view_ref: 'view-stale',
      selection_ref: 'context-current',
      index_intent_available: false,
    }, {
      repository: 'Engram',
      source_ref: 'source-engram', checkout_ref: 'checkout-malformed',
      working_copy: null,
      indexed_snapshot: { label: 'Malformed snapshot', revision: '1a9dad3', published_at: '2026-09-17T00:03:00Z' },
      view_ref: 'view-malformed',
      selection_ref: 'context-malformed',
      index_intent_available: false,
    }, {
      repository: 'Other repository',
      source_ref: 'source-other', checkout_ref: 'checkout-manual',
      working_copy: 'manual checkout',
      indexed_snapshot: { label: 'Manual snapshot', revision: '1a9dad1', published_at: '2026-09-17T00:01:00Z' },
      view_ref: 'view-manual',
      selection_ref: 'context-manual',
      index_intent_available: false,
    }],
  }
  const refreshedCatalog = {
    contexts: [{
      source_ref: 'source-engram', checkout_ref: 'checkout-current',
      repository: 'Engram',
      working_copy: 'refreshed candidate checkout',
      indexed_snapshot: { label: 'Refreshed candidate snapshot', revision: '1a9dad2', published_at: '2026-09-17T00:02:00Z' },
      view_ref: 'view-new-generation',
      selection_ref: 'context-current',
      index_intent_available: false,
    }, initialCatalog.contexts[2]],
  }

  await page.route('**/api/code/**', async (route: Route) => {
    const pathname = new URL(route.request().url()).pathname
    if (pathname === '/api/code/tabs/handshake') {
      await route.fulfill({ json: { state: 'TAB_BINDING_READY', tab_binding_id: TAB_BINDING_ID, document_proof: DOCUMENT_PROOF, resume_nonce: 'resume-current', reload_token: 'reload-current' } })
      return
    }
    if (pathname === '/api/code/contexts') {
      await route.fulfill({ json: contextRequests++ === 0 ? initialCatalog : refreshedCatalog })
      return
    }
    await route.fulfill({ status: 500 })
  })

  await page.goto('/code', { waitUntil: 'domcontentloaded' })
  await page.getByTestId('code-context-repository').selectOption({ label: 'Engram' })
  await page.getByTestId('code-context-working-copy').selectOption({ label: 'stale candidate checkout' })
  await page.getByTestId('code-context-snapshot').selectOption({ label: 'Stale candidate snapshot' })
  await page.getByRole('button', { name: 'Обновить разрешённые варианты' }).click()
  await expect(page.getByTestId('code-context-candidate')).toHaveCount(0)
  await expect(page.getByTestId('code-context-snapshot')).toHaveValue('')

  await page.getByTestId('code-context-repository').selectOption({ label: 'Other repository' })
  await expect(page.getByTestId('code-context-repository')).toHaveValue('source-other')
  await expect(page.getByTestId('code-context-working-copy')).toHaveValue('')
})
test('Code Explorer retains only the same uniquely identified pinned snapshot across rotated catalog references', async ({ page }) => {
  let catalogReads = 0
  let changedSnapshot = false
  let pins = 0
  const context = (ref: string) => ({
    source_ref: `source-${ref}`, checkout_ref: `checkout-${ref}`,
    repository: 'Engram', working_copy: 'operator desk',
    view_ref: changedSnapshot ? 'view-new-generation' : 'view-stable',
    selection_ref: `selection-${ref}`, index_intent_available: false,
    indexed_snapshot: { label: 'Current snapshot', revision: changedSnapshot ? 'new-revision' : '1a9dad0', published_at: '2026-09-17T00:00:00Z' },
  })
  await page.route('**/api/code/**', async (route: Route) => {
    const pathname = new URL(route.request().url()).pathname
    if (pathname === '/api/code/tabs/handshake') {
      await route.fulfill({ json: { state: 'TAB_BINDING_READY', tab_binding_id: TAB_BINDING_ID, document_proof: DOCUMENT_PROOF, resume_nonce: 'resume-current', reload_token: 'reload-current' } })
    } else if (pathname === '/api/code/contexts') {
      await route.fulfill({ json: { contexts: [context(catalogReads++ === 0 ? 'first' : 'fresh')] } })
    } else if (pathname === `/api/code/tabs/${TAB_BINDING_ID}/context`) {
      pins++
      await route.fulfill({ status: 204 })
    } else if (pathname === '/api/code/status') {
      await route.fulfill({ json: { total_chunks: 0, embedded_chunks: 0, embedding: { coverage: 'none', job_state: null, error_code: null } } })
    } else if (pathname === '/api/code/structure') {
      await route.fulfill({ status: 403 })
    } else {
      await route.fulfill({ status: 500 })
    }
  })
  await page.goto('/code')
  await page.getByTestId('code-context-snapshot').selectOption('selection-first')
  await page.getByTestId('code-pin-context').click()
  await expect(page.getByTestId('code-context-pinned')).toBeVisible()
  await page.getByRole('button', { name: 'Обновить разрешённые варианты' }).click()
  await expect(page.getByTestId('code-context-pinned')).toBeVisible()
  await expect(page.getByTestId('code-context-snapshot')).toHaveValue('selection-fresh')
  expect(pins).toBe(1)
  changedSnapshot = true
  await page.getByRole('button', { name: 'Обновить разрешённые варианты' }).click()
  await expect(page.getByTestId('code-context-pinned')).toHaveCount(0)
  expect(pins).toBe(1)
})

test('Failed and malformed catalog refresh revoke candidate pin authority until successful rebind', async ({ page }) => {
  let catalogMode: 'ready' | 'failed' | 'malformed' | 'rotated' = 'ready'
  const pins: unknown[] = []
  const context = (selection_ref: string) => ({
    source_ref: 'source', checkout_ref: 'checkout', repository: 'Engram', working_copy: 'desk',
    indexed_snapshot: { label: 'Snapshot', revision: 'revision', published_at: '2026-09-17T00:00:00Z' },
    view_ref: 'view', selection_ref, index_intent_available: false,
  })
  await page.route('**/api/code/**', async (route: Route) => {
    const pathname = new URL(route.request().url()).pathname
    if (pathname === '/api/code/tabs/handshake') {
      await route.fulfill({ json: { state: 'TAB_BINDING_READY', tab_binding_id: TAB_BINDING_ID, document_proof: DOCUMENT_PROOF, resume_nonce: 'resume', reload_token: 'reload' } })
    } else if (pathname === '/api/code/contexts') {
      await route.fulfill(catalogMode === 'failed' ? { status: 503 } : { json: catalogMode === 'malformed' ? { contexts: 'bad' } : { contexts: [context(catalogMode === 'rotated' ? 'fresh' : 'old')] } })
    } else if (pathname === `/api/code/tabs/${TAB_BINDING_ID}/context`) {
      pins.push(route.request().postDataJSON())
      await route.fulfill({ status: 204 })
    } else if (pathname === '/api/code/status') {
      await route.fulfill({ json: { total_chunks: 0, embedded_chunks: 0, embedding: { coverage: 'none', job_state: null, error_code: null } } })
    } else if (pathname === '/api/code/structure') {
      await route.fulfill({ status: 403 })
    } else {
      await route.fulfill({ status: 500 })
    }
  })
  await page.goto('/code')
  await page.getByTestId('code-context-snapshot').selectOption('old')
  catalogMode = 'failed'
  await page.getByRole('button', { name: 'Обновить разрешённые варианты' }).click()
  await expect(page.getByTestId('code-pin-context')).toBeDisabled()
  await expect(page.getByTestId('code-context-candidate')).toHaveCount(0)
  await expect(page.getByTestId('code-context-snapshot')).toHaveCount(0)
  await expect.poll(() => page.evaluate(() => sessionStorage.getItem('engram.operator-code.view-candidate.v2'))).toBeNull()
  catalogMode = 'ready'
  await page.getByRole('button', { name: 'Обновить разрешённые варианты' }).click()
  await page.getByTestId('code-context-snapshot').selectOption('old')
  catalogMode = 'malformed'
  await page.getByRole('button', { name: 'Обновить разрешённые варианты' }).click()
  await expect(page.getByTestId('code-pin-context')).toBeDisabled()
  catalogMode = 'rotated'
  await page.getByRole('button', { name: 'Обновить разрешённые варианты' }).click()
  await page.getByTestId('code-context-snapshot').selectOption('fresh')
  await page.getByTestId('code-pin-context').click()
  expect(pins).toEqual([{ document_proof: DOCUMENT_PROOF, selection_ref: 'fresh' }])
  await expect(page.getByTestId('code-context-pinned')).toBeVisible()
  catalogMode = 'failed'
  await page.getByRole('button', { name: 'Обновить разрешённые варианты' }).click()
  await expect(page.getByTestId('code-context-pinned')).toBeVisible()
  await expect(page.getByTestId('code-pin-context')).toBeDisabled()
  expect(pins).toHaveLength(1)
})

test('Reload restores only a unique View candidate, not a server pin, and uses the fresh pin authority', async ({ page }) => {
  let rotated = false
  let duplicate = false
  const pins: unknown[] = []
  const entry = (viewRef: string, selectionRef: string) => ({
    source_ref: 'source', checkout_ref: 'checkout', repository: 'Same label', working_copy: 'Same label',
    indexed_snapshot: { label: 'Same snapshot', revision: 'revision', published_at: '2026-09-17T00:00:00Z' },
    view_ref: viewRef, selection_ref: selectionRef, index_intent_available: false,
  })
  await page.route('**/api/code/**', async (route: Route) => {
    const pathname = new URL(route.request().url()).pathname
    if (pathname === '/api/code/tabs/handshake' || pathname === '/api/code/tabs/resume') {
      await route.fulfill({ json: { state: 'TAB_BINDING_READY', tab_binding_id: TAB_BINDING_ID, document_proof: 'current-proof', resume_nonce: 'resume', reload_token: 'reload' } })
    } else if (pathname === '/api/code/contexts') {
      await route.fulfill({ json: { contexts: [entry('view-a', rotated ? 'fresh-a' : 'old-a'), entry(duplicate ? 'view-a' : 'view-b', 'fresh-b')] } })
    } else if (pathname === `/api/code/tabs/${TAB_BINDING_ID}/context`) {
      pins.push(route.request().postDataJSON())
      await route.fulfill({ status: 204 })
    } else if (pathname === '/api/code/status') {
      await route.fulfill({ json: { total_chunks: 0, embedded_chunks: 0, embedding: { coverage: 'none', job_state: null, error_code: null } } })
    } else if (pathname === '/api/code/structure') {
      await route.fulfill({ status: 403 })
    } else {
      await route.fulfill({ status: 500 })
    }
  })
  await page.goto('/code')
  await page.getByTestId('code-context-snapshot').selectOption('old-a')
  await page.getByTestId('code-pin-context').click()
  await expect(page.getByTestId('code-context-pinned')).toBeVisible()
  expect(await page.evaluate(() => sessionStorage.getItem('engram.operator-code.view-candidate.v2'))).toBe('view-a')
  rotated = true
  await page.reload()
  await expect(page.getByTestId('code-context-candidate')).toBeVisible()
  await expect(page.getByTestId('code-context-snapshot')).toHaveValue('fresh-a')
  await expect(page.getByTestId('code-context-pinned')).toHaveCount(0)
  await page.getByTestId('code-pin-context').click()
  expect(pins).toEqual([{ document_proof: 'current-proof', selection_ref: 'old-a' }, { document_proof: 'current-proof', selection_ref: 'fresh-a' }])
  duplicate = true
  await page.reload()
  await expect(page.getByTestId('code-context-snapshot').locator('option')).toHaveCount(3)
  await expect(page.getByTestId('code-context-candidate')).toHaveCount(0)
  await expect(page.getByTestId('code-context-pinned')).toHaveCount(0)
  expect(await page.evaluate(() => sessionStorage.getItem('engram.operator-code.view-candidate.v2'))).toBeNull()
})


test('Code Explorer resumes a same-document SPA remount but isolates copied storage', async ({ page }) => {
  const handshakePayloads: unknown[] = []
  const resumePayloads: unknown[] = []
  const leasePayloads: unknown[] = []
  const pinPayloads: unknown[] = []
  const closePayloads: unknown[] = []
  let bindingClosed = false
  const intent = {
    intent_ref: 'intent-current',
    state: 'queued',
    attempt: 1,
    retryable: false,
    created_at: '2026-09-15T00:00:00Z',
    updated_at: '2026-09-15T00:00:00Z',
  }
  const catalog = {
    contexts: [{
      source_ref: 'source-engram', checkout_ref: 'checkout-current',
      repository: 'Engram',
      working_copy: 'feature/operator-workspace · operator desk',
      indexed_snapshot: {
        label: 'Current snapshot',
        revision: '1a9dad0',
        published_at: '2026-09-17T00:00:00Z',
      },
      view_ref: 'view-current',
      selection_ref: 'context-current',
      index_intent_available: false,
    }, {
      source_ref: 'source-engram', checkout_ref: 'checkout-unnamed',
      repository: 'Engram',
      working_copy: '',
      index_intent_available: false,
    }, {
      repository: 'Other repository',
      working_copy: 'D working copy',
      source_ref: 'source-other', checkout_ref: 'checkout-d',
      indexed_snapshot: {
        label: 'D snapshot',
        revision: '1a9dad1',
        published_at: '2026-09-17T00:01:00Z',
      },
      view_ref: 'view-d',
      selection_ref: 'context-d',
      index_intent_available: false,
    }],
  }

  await page.clock.install({ time: new Date('2026-09-15T00:00:00Z') })

  await page.context().route('**/api/code/**', async (route: Route) => {
    const pathname = new URL(route.request().url()).pathname
    if (pathname === '/api/code/tabs/handshake') {
      const payload = route.request().postDataJSON()
      handshakePayloads.push(payload)
      await route.fulfill({
        json: 'copied_tab_binding_id' in payload
          ? {
            state: 'TAB_BINDING_COLLISION',
            tab_binding_id: '60000000-0000-4000-8000-000000000042',
            document_proof: 'proof-copy',
            resume_nonce: 'resume-copy',
            reload_token: 'reload-copy',
          }
          : {
            state: 'TAB_BINDING_READY',
            tab_binding_id: TAB_BINDING_ID,
            document_proof: DOCUMENT_PROOF,
            resume_nonce: 'resume-current',
            reload_token: 'reload-current',
          },
      })
      return
    }
    if (pathname === '/api/code/tabs/resume') {
      resumePayloads.push(route.request().postDataJSON())
      if (bindingClosed) {
        await route.fulfill({ status: 409 })
        return
      }
      await route.fulfill({
        json: {
          state: 'TAB_BINDING_READY',
          tab_binding_id: TAB_BINDING_ID,
          document_proof: 'proof-resumed',
          resume_nonce: 'resume-current',
          reload_token: 'reload-resumed',
        },
      })
      return
    }
    if (pathname === '/api/code/contexts') {
      await route.fulfill({ json: catalog })
      return
    }
    if (pathname === `/api/code/tabs/${TAB_BINDING_ID}/context`) {
      pinPayloads.push(route.request().postDataJSON())
      await route.fulfill({ status: 204 })
      return
    }
    if (pathname === '/api/code/status') {
      await route.fulfill({ json: { total_chunks: 1, embedded_chunks: 1, embedding: { Coverage: 'complete' }, freshness: { state: 'unknown' } } })
      return
    }
    if (pathname === '/api/code/index-intents' && route.request().method() === 'POST') {
      await route.fulfill({ status: 202, json: intent })
      return
    }
    if (pathname === `/api/code/index-intents/${intent.intent_ref}` && route.request().method() === 'GET') {
      await route.fulfill({ json: intent })
      return
    }
    if (pathname === `/api/code/tabs/${TAB_BINDING_ID}/lease` && route.request().method() === 'PUT') {
      leasePayloads.push(route.request().postDataJSON())
      await route.fulfill({ status: 204 })
      return
    }
    if (pathname === `/api/code/tabs/${TAB_BINDING_ID}` && route.request().method() === 'DELETE') {
      bindingClosed = true
      closePayloads.push(route.request().postDataJSON())
      await route.fulfill({ status: 204 })
      return
    }
    await route.fulfill({ status: 500 })
  })

  await page.goto('/code', { waitUntil: 'domcontentloaded' })
  await page.getByTestId('code-context-repository').selectOption({ label: 'Engram' })
  await page.getByTestId('code-context-working-copy').selectOption({ label: 'feature/operator-workspace · operator desk' })
  await page.getByTestId('code-context-snapshot').selectOption({ label: 'Current snapshot' })
  await page.getByTestId('code-pin-context').click()
  expect(pinPayloads).toEqual([{ document_proof: DOCUMENT_PROOF, selection_ref: 'context-current' }])
  await expect(page.getByTestId('code-context-pinned')).toContainText('Current snapshot')
  await expect(page.locator('.readiness')).toHaveAttribute('data-state', 'unknown')
  await page.getByTestId('code-context-repository').selectOption({ label: 'Other repository' })
  await page.getByTestId('code-context-working-copy').selectOption({ label: 'D working copy' })
  await page.getByTestId('code-context-snapshot').selectOption({ label: 'D snapshot' })
  await expect(page.getByTestId('code-context-candidate')).toContainText('D snapshot')
  await expect(page.getByTestId('code-context-pinned')).toContainText('Current snapshot')
  await page.getByTestId('index-intent-reindex').click()
  await expect(page.getByTestId('index-intent-state')).toHaveAttribute('data-state', 'queued')

  await page.locator('a[href="/memory"]').first().click()
  await expect(page).toHaveURL(/\/memory$/)
  await page.goBack()
  await expect(page).toHaveURL(/\/code$/)

  await expect.poll(() => resumePayloads.length).toBe(1)
  expect(resumePayloads[0]).toMatchObject({
    tab_binding_id: TAB_BINDING_ID,
    resume_nonce: 'resume-current',
    reload_token: 'reload-current',
  })
  expect(closePayloads).toEqual([])
  await expect(page.getByTestId('code-context-pinned')).toContainText('Current snapshot')
  await expect(page.getByTestId('index-intent-state')).toHaveAttribute('data-state', 'queued')
  expect(handshakePayloads).toHaveLength(1)

  const copiedStorage = await page.evaluate(() => ({
    resume: sessionStorage.getItem('engram.operator-code.resume.v1'),
    pin: sessionStorage.getItem('engram.operator-code.view-candidate.v2'),
    intent: sessionStorage.getItem('engram.operator-code.index-intent.v1'),
  }))
  const copied = await page.context().newPage()
  await copied.addInitScript((storage) => {
    if (storage.resume !== null) sessionStorage.setItem('engram.operator-code.resume.v1', storage.resume)
    if (storage.pin !== null) sessionStorage.setItem('engram.operator-code.view-candidate.v2', storage.pin)
    if (storage.intent !== null) sessionStorage.setItem('engram.operator-code.index-intent.v1', storage.intent)
  }, copiedStorage)
  await copied.goto('/code', { waitUntil: 'domcontentloaded' })

  await expect(copied.getByTestId('code-bootstrap-evidence')).toContainText('TAB_BINDING_COLLISION')
  await expect(copied.getByTestId('code-context-pinned')).toHaveCount(0)
  await expect(copied.getByTestId('index-intent-state')).toHaveAttribute('data-state', 'idle')
  await expect(page.getByTestId('code-context-pinned')).toContainText('Current snapshot')
  expect(handshakePayloads).toHaveLength(2)
  expect(handshakePayloads[1]).toMatchObject({
    copied_tab_binding_id: TAB_BINDING_ID,
    copied_resume_nonce: 'resume-current',
  })
  expect(resumePayloads).toHaveLength(1)
  await page.clock.fastForward('01:00')
  await expect.poll(() => leasePayloads).toEqual([{ document_proof: 'proof-resumed' }])
  await copied.close()
})

test('Workspace explains an insecure origin without creating a browser binding', async ({ page }) => {
  const requests: string[] = []
  await page.addInitScript(() => {
    Object.defineProperty(window, 'isSecureContext', { value: false })
  })
  await page.route('**/api/code/**', async (route) => {
    requests.push(route.request().url())
    await route.fulfill({ status: 500 })
  })
  await page.goto('/code')
  await expect(page.getByTestId('code-context-message')).toContainText('HTTPS')
  await expect(page.getByTestId('code-context-empty')).toBeVisible()
  expect(requests).toEqual([])
})

test('Home opens a no-View working copy, then follows its released index to search, relation and source', async ({ page }) => {
  const submitted: unknown[] = []
  const sourceRequests: unknown[] = []
  let published = false
  const context = { source_id: 'source-1', checkout_id: 'checkout-1', view_id: 'view-1', profile_id: 'profile-1', generation: 1 }
  const ref = { source_id: 'source-1', view_id: 'view-1', entity_key: 'implementation' }
  const related = { source_id: 'source-1', view_id: 'view-1', entity_key: 'dependency' }
  const span = { byte_start: 0, byte_end: 12, line_start: 1, line_end: 1 }
  const item = { ref, path: 'src/implementation.ts', span, content_digest: 'digest-1', kind: 'function', language: 'typescript', excerpt: 'function go()', match_sources: ['lexical'], score: 1 }
  const referenceSpan = { byte_start: 9, byte_end: 13, line_start: 1, line_end: 1 }
  const reference = { ref, precision: 'reference_site', reference_site_id: '50000000-0000-4000-8000-000000000005' }
  const callerNode = { entity: ref, context_ref: { ...context, analysis_profile_id: context.profile_id }, source_state: 'available', source_read: { entity_key: ref.entity_key, span, content_digest: item.content_digest } }
  const relatedNode = { entity: related, context_ref: { ...context, analysis_profile_id: context.profile_id }, source_state: 'unavailable' }
  const envelope = { schema: 'engram.code-query/1', status: 'ok', contexts: [context], items: [item], warnings: [], retrieval: { mode: 'lexical' }, freshness: { state: 'observed_current' }, coverage: {}, truncated: false }
  const intent = { intent_ref: 'intent-first', state: 'queued', attempt: 1, retryable: false, created_at: '2026-09-15T00:00:00Z', updated_at: '2026-09-15T00:00:00Z' }
  await page.route('**/api/code/**', async (route) => {
    const pathname = new URL(route.request().url()).pathname
    if (pathname === '/api/code/tabs/handshake') {
      await route.fulfill({ json: { state: 'TAB_BINDING_READY', tab_binding_id: TAB_BINDING_ID, document_proof: DOCUMENT_PROOF, resume_nonce: 'resume-current', reload_token: 'reload-current' } })
    } else if (pathname === '/api/code/contexts') {
      await route.fulfill({
        json: {
          contexts: [published
            ? { source_ref: 'source-engram', checkout_ref: 'checkout-feature', repository: 'Engram', working_copy: 'feature/workspace', indexed_snapshot: { label: 'Published implementation' }, view_ref: 'view-1', selection_ref: 'server-issued-view', index_intent_available: false }
            : { source_ref: 'source-engram', checkout_ref: 'checkout-feature', repository: 'Engram', working_copy: 'feature/workspace', index_intent_available: true, index_intent_selection_ref: 'server-issued-target' }]
        }
      })
    } else if (pathname === '/api/code/index-intents') {
      submitted.push(route.request().postDataJSON())
      await route.fulfill({ status: 202, json: intent })
    } else if (pathname === '/api/code/index-intents/intent-first') {
      published = true
      await route.fulfill({ json: { ...intent, state: 'completed', result: { view_ref: 'view-1', generation: 1 } } })
    } else if (pathname === `/api/code/tabs/${TAB_BINDING_ID}/context`) {
      await route.fulfill({ status: 204 })
    } else if (pathname === '/api/code/status') {
      await route.fulfill({ json: { total_chunks: 1, embedded_chunks: 1, embedding: { Coverage: 'complete' }, freshness: { state: 'observed_current' } } })
    } else if (pathname === '/api/code/structure' || pathname === '/api/code/search') {
      await route.fulfill({ json: envelope })
    } else if (pathname === '/api/code/source') {
      const body = route.request().postDataJSON()
      sourceRequests.push(body)
      if (body.entity_key === ref.entity_key && body.content_digest === item.content_digest && body.reference_site_id === reference.reference_site_id && JSON.stringify(body.span) === JSON.stringify(referenceSpan)) {
        await route.fulfill({ json: { ...envelope, items: [{ ...item, span: referenceSpan, excerpt: 'go()' }] } })
      } else if (body.entity_key === ref.entity_key && body.content_digest === item.content_digest && JSON.stringify(body.span) === JSON.stringify(span)) {
        await route.fulfill({ json: envelope })
      } else {
        await route.fulfill({ status: 403 })
      }
    } else if (pathname === '/api/code/graph') {
      await route.fulfill({ json: { ...envelope, graph: { nodes: [ref, related], edges: [{ from: ref, to: related, relation: 'calls', evidence_kind: 'RESOLVED', evidence: [reference] }, { from: related, to: ref, relation: 'may_call', evidence_kind: 'HEURISTIC', evidence: [{ ref: related, precision: 'unsupported' }] }], stop_reason: 'complete' }, navigation: { nodes: [callerNode, relatedNode], edges: [{ from: callerNode, to: relatedNode, relation: 'calls', evidence_kind: 'RESOLVED', evidence: [{ ref, precision: 'reference_site', source_state: 'available', source_read: { entity_key: ref.entity_key, span: referenceSpan, content_digest: item.content_digest, reference_site_id: reference.reference_site_id } }] }, { from: relatedNode, to: callerNode, relation: 'may_call', evidence_kind: 'HEURISTIC', evidence: [{ ref: related, precision: 'unsupported', source_state: 'unavailable' }] }] } } })
    } else {
      await route.fulfill({ status: 500 })
    }
  })
  await page.goto('/')
  await page.getByTestId('overview-workspace-entry').click()
  await expect(page.getByTestId('code-context-working-copy').locator('option:checked')).toHaveText('feature/workspace')
  await expect(page.getByTestId('code-context-index-affordance')).toBeVisible()
  await page.getByTestId('code-request-first-index').click()
  await expect(page.getByTestId('index-intent-state')).toHaveAttribute('data-state', 'queued')
  expect(submitted).toHaveLength(1)
  expect(submitted[0]).toMatchObject({ kind: 'reindex', target: { selection_ref: 'server-issued-target' } })
  await page.getByTestId('index-intent-check-status').click()
  await expect(page.getByTestId('index-intent-state')).toHaveAttribute('data-state', 'completed')
  await page.getByTestId('code-context-snapshot').selectOption({ label: 'Published implementation' })
  await page.getByTestId('code-pin-context').click()
  await expect(page.getByTestId('code-status')).toContainText('1 / 1')
  await page.getByTestId('code-query-input').fill('implementation')
  await page.getByTestId('code-search-submit').click()
  await expect(page.getByTestId('code-search-results')).toContainText('src/implementation.ts')
  await page.getByTestId('code-search-explore').click()
  await expect(page.getByTestId('code-graph-results')).toBeVisible()
  await page.getByRole('button', { name: /Relation list|Список связей/ }).click()
  await page.getByTestId('code-graph-results').getByRole('button').first().click()
  await expect(page.getByTestId('code-graph-evidence')).toContainText('reference_site')
  await page.getByTestId('code-graph-reference-source').click()
  expect(sourceRequests[0]).toMatchObject({ entity_key: ref.entity_key, span: referenceSpan, content_digest: item.content_digest, reference_site_id: reference.reference_site_id })
  await expect(page.getByTestId('code-source-result')).toHaveText('go()')
  await page.getByTestId('code-graph-results').getByRole('button').nth(1).click()
  await expect(page.getByTestId('code-graph-evidence')).toContainText('unsupported')
  await expect(page.getByTestId('code-graph-reference-source')).toHaveCount(0)
  expect(sourceRequests).toHaveLength(1)
  await page.getByTestId('code-search-source').click()
  await expect(page.getByTestId('code-source-result')).toContainText('function go()')
})

test('Offline no-View first indexing reaches the mock intent while unknown targets stay denied', async ({ page, request }) => {
  await page.goto('/code')
  await page.getByTestId('code-context-repository').selectOption({ label: 'Engram' })
  await page.getByTestId('code-context-working-copy').selectOption({ label: 'recovery · offline owner' })
  await expect(page.getByTestId('code-context-pinned')).toHaveCount(0)

  const submit = page.waitForResponse((response) => new URL(response.url()).pathname === '/api/code/index-intents' && response.request().method() === 'POST')
  await page.getByTestId('code-request-first-index').click()
  const submission = await submit
  expect(submission.status()).toBe(202)
  const { tab_binding_id, document_proof } = submission.request().postDataJSON()
  await expect(page.getByTestId('index-intent-state')).toHaveAttribute('data-state', 'queued')
  const status = page.waitForResponse((response) => new URL(response.url()).pathname === '/api/code/index-intents/mock-index-intent' && response.request().method() === 'GET')
  await page.getByTestId('index-intent-check-status').click()
  expect((await status).status()).toBe(200)
  await expect(page.getByTestId('index-intent-state')).toHaveAttribute('data-state', 'unavailable')

  const target = { tab_binding_id, document_proof, request_ref: 'unknown-ref', kind: 'reindex', target: { selection_ref: 'not-advertised' } }
  expect((await request.post('/api/code/index-intents', { data: target })).status()).toBe(403)
  expect((await request.post('/api/code/index-intents', { data: { ...target, target: { selection_ref: '' } } })).status()).toBe(400)
  expect((await request.post('/api/code/index-intents', { data: { ...target, target: { selection_ref: 'mock-first-index' }, document_proof: 'wrong-proof' } })).status()).toBe(403)
  expect((await request.get('/api/code/index-intents/unknown-intent', { headers: { 'X-Engram-Tab-Binding-ID': tab_binding_id, 'X-Engram-Document-Proof': document_proof } })).status()).toBe(403)
  const secondTab = await (await request.post('/api/code/tabs/handshake', { data: {} })).json()
  expect((await request.get('/api/code/index-intents/mock-index-intent', { headers: { 'X-Engram-Tab-Binding-ID': secondTab.tab_binding_id, 'X-Engram-Document-Proof': secondTab.document_proof } })).status()).toBe(403)
  const unrelated = await request.post('/api/code/search', { data: { tab_binding_id, document_proof } })
  expect(unrelated.status(), await unrelated.text()).toBe(403)
})

test('An unnamed working copy remains selectable without confusing it with the prompt', async ({ page }) => {
  await page.route('**/api/code/**', async (route) => {
    const pathname = new URL(route.request().url()).pathname
    if (pathname === '/api/code/tabs/handshake') {
      await route.fulfill({ json: { state: 'TAB_BINDING_READY', tab_binding_id: TAB_BINDING_ID, document_proof: DOCUMENT_PROOF, resume_nonce: 'resume-current', reload_token: 'reload-current' } })
    } else if (pathname === '/api/code/contexts') {
      await route.fulfill({
        json: {
          contexts: [
            { source_ref: 'source-engram', checkout_ref: 'checkout-unnamed', repository: 'Engram', working_copy: '', index_intent_available: true, index_intent_selection_ref: 'opaque-unnamed' },
            { source_ref: 'source-engram', checkout_ref: 'checkout-other', repository: 'Engram', working_copy: 'other checkout', index_intent_available: false },
          ]
        }
      })
    } else {
      await route.fulfill({ status: 500 })
    }
  })
  await page.goto('/code')
  await expect(page.getByTestId('code-context-index-affordance')).toHaveCount(0)
  await page.getByTestId('code-context-working-copy').selectOption({ label: 'Рабочая копия без имени' })
  await expect(page.getByTestId('code-context-index-affordance')).toBeVisible()
  await expect(page.getByTestId('code-request-first-index')).toBeEnabled()
})

test('Home selects a published unnamed checkout and reads its authorized source', async ({ page }) => {
  const pinned: unknown[] = []
  const sourceRequests: unknown[] = []
  const context = { source_id: 'source-1', checkout_id: 'checkout-1', view_id: 'view-1', profile_id: 'profile-1', generation: 1 }
  const item = { ref: { source_id: 'source-1', view_id: 'view-1', entity_key: 'implementation' }, path: 'src/implementation.ts', span: { byte_start: 0, byte_end: 12, line_start: 1, line_end: 1 }, content_digest: 'digest-1', kind: 'function', language: 'typescript', excerpt: 'function go()', match_sources: ['lexical'], score: 1 }
  const envelope = { schema: 'engram.code-query/1', status: 'ok', contexts: [context], items: [item], warnings: [], retrieval: { mode: 'lexical' }, freshness: { state: 'observed_current' }, coverage: {}, truncated: false }
  await page.route('**/api/code/**', async (route) => {
    const pathname = new URL(route.request().url()).pathname
    if (pathname === '/api/code/tabs/handshake') {
      await route.fulfill({ json: { state: 'TAB_BINDING_READY', tab_binding_id: TAB_BINDING_ID, document_proof: DOCUMENT_PROOF, resume_nonce: 'resume-current', reload_token: 'reload-current' } })
    } else if (pathname === '/api/code/contexts') {
      await route.fulfill({
        json: {
          contexts: [
            { source_ref: 'source-engram', checkout_ref: 'checkout-unnamed', repository: 'Engram', working_copy: '', indexed_snapshot: { label: 'Published implementation' }, view_ref: 'view-1', selection_ref: 'server-issued-view', index_intent_available: false },
            { source_ref: 'source-engram', checkout_ref: 'checkout-other', repository: 'Engram', working_copy: 'other checkout', index_intent_available: false },
          ]
        }
      })
    } else if (pathname === `/api/code/tabs/${TAB_BINDING_ID}/context`) {
      pinned.push(route.request().postDataJSON())
      await route.fulfill({ status: 204 })
    } else if (pathname === '/api/code/status') {
      await route.fulfill({ json: { total_chunks: 1, embedded_chunks: 1, embedding: { Coverage: 'complete' }, freshness: { state: 'unknown' } } })
    } else if (pathname === '/api/code/structure' || pathname === '/api/code/search') {
      await route.fulfill({ json: envelope })
    } else if (pathname === '/api/code/source') {
      sourceRequests.push(route.request().postDataJSON())
      await route.fulfill({ json: envelope })
    } else {
      await route.fulfill({ status: 500 })
    }
  })

  await page.goto('/')
  await page.getByTestId('overview-workspace-entry').click()
  await expect(page.getByTestId('code-context-working-copy').getByRole('option', { name: 'Рабочая копия без имени' })).toHaveCount(1)
  await expect(page.getByTestId('code-context-index-affordance')).toHaveCount(0)
  await page.getByTestId('code-context-working-copy').selectOption({ label: 'Рабочая копия без имени' })
  await page.getByTestId('code-context-snapshot').selectOption({ label: 'Published implementation' })
  await expect(page.getByTestId('code-context-candidate')).toContainText('Рабочая копия без имени')
  await expect(page.getByTestId('code-context-candidate')).not.toContainText('src/implementation.ts')
  await page.getByTestId('code-pin-context').click()
  expect(pinned).toEqual([{ document_proof: DOCUMENT_PROOF, selection_ref: 'server-issued-view' }])
  await expect(page.getByTestId('code-context-pinned')).toContainText('Рабочая копия без имени')
  await expect(page.locator('.readiness')).toHaveAttribute('data-state', 'unknown')
  await page.getByTestId('code-query-input').fill('implementation')
  await page.getByTestId('code-search-submit').click()
  await expect(page.getByTestId('code-search-results')).toContainText('src/implementation.ts')
  await page.getByTestId('code-search-source').click()
  await expect(page.getByTestId('code-source-result')).toContainText('function go()')
  expect(sourceRequests).toEqual([expect.objectContaining({ entity_key: 'implementation', content_digest: 'digest-1' })])
})

test('identical source and checkout labels retain separate first-index and published selections', async ({ page }) => {
  const intents: string[] = []
  const pins: string[] = []
  const noViews = [
    { source_ref: 'source-A', checkout_ref: 'checkout-A', repository: 'Engram', working_copy: '', index_intent_available: true, index_intent_selection_ref: 'index-A' },
    { source_ref: 'source-A', checkout_ref: 'checkout-C', repository: 'Engram', working_copy: '', index_intent_available: true, index_intent_selection_ref: 'index-C' },
    { source_ref: 'source-B', checkout_ref: 'checkout-B', repository: 'Engram', working_copy: '', index_intent_available: true, index_intent_selection_ref: 'index-B' },
  ]
  const published = [
    { source_ref: 'source-A', checkout_ref: 'checkout-A', repository: 'Engram', working_copy: '', indexed_snapshot: { label: 'Release' }, view_ref: 'stable-view-A', selection_ref: 'view-A', index_intent_available: false },
    { source_ref: 'source-A', checkout_ref: 'checkout-C', repository: 'Engram', working_copy: '', indexed_snapshot: { label: 'Release' }, view_ref: 'stable-view-C', selection_ref: 'view-C', index_intent_available: false },
    { source_ref: 'source-B', checkout_ref: 'checkout-B', repository: 'Engram', working_copy: '', indexed_snapshot: { label: 'Release' }, view_ref: 'stable-view-B', selection_ref: 'view-B', index_intent_available: false },
  ]
  await page.route('**/api/code/**', async (route) => {
    const pathname = new URL(route.request().url()).pathname
    if (pathname === '/api/code/tabs/handshake') {
      await route.fulfill({ json: { state: 'TAB_BINDING_READY', tab_binding_id: TAB_BINDING_ID, document_proof: DOCUMENT_PROOF, resume_nonce: 'resume-current', reload_token: 'reload-current' } })
    } else if (pathname === '/api/code/contexts') {
      await route.fulfill({ json: { contexts: intents.length === 3 ? published : noViews } })
    } else if (pathname === '/api/code/index-intents') {
      intents.push(route.request().postDataJSON().target.selection_ref)
      await route.fulfill({ status: 202, json: { intent_ref: `intent-${intents.length}`, state: 'queued', attempt: 1, retryable: false, created_at: '2026-09-15T00:00:00Z', updated_at: '2026-09-15T00:00:00Z' } })
    } else if (pathname.startsWith('/api/code/index-intents/')) {
      await route.fulfill({ json: { intent_ref: pathname.split('/').at(-1), state: 'completed', attempt: 1, retryable: false, created_at: '2026-09-15T00:00:00Z', updated_at: '2026-09-15T00:00:00Z', result: { view_ref: 'released', generation: 1 } } })
    } else if (pathname === `/api/code/tabs/${TAB_BINDING_ID}/context`) {
      pins.push(route.request().postDataJSON().selection_ref)
      await route.fulfill({ status: 204 })
    } else if (pathname === '/api/code/status') {
      await route.fulfill({ json: { total_chunks: 0, embedded_chunks: 0, embedding: { Coverage: 'unknown' } } })
    } else if (pathname === '/api/code/structure') {
      await route.fulfill({ json: { schema: 'engram.code-query/1', status: 'empty', contexts: [], items: [], warnings: [], retrieval: { mode: 'lexical' }, freshness: { state: 'unknown' }, coverage: {}, truncated: false } })
    } else {
      await route.fulfill({ status: 500 })
    }
  })

  await page.goto('/code')
  const repository = page.getByTestId('code-context-repository')
  const checkout = page.getByTestId('code-context-working-copy')
  await expect(repository.locator('option:not([disabled])')).toHaveCount(2)
  for (const [source, copy, indexRef] of [
    ['source-A', 'checkout-A', 'index-A'],
    ['source-A', 'checkout-C', 'index-C'],
    ['source-B', 'checkout-B', 'index-B'],
  ]) {
    await repository.selectOption(source)
    await expect(checkout.locator('option:not([disabled])')).toHaveCount(source === 'source-A' ? 2 : 1)
    await checkout.selectOption(copy)
    await expect(page.getByTestId('code-context-index-affordance')).toBeVisible()
    await page.getByTestId('code-request-first-index').click()
    await expect.poll(() => intents.at(-1)).toBe(indexRef)
    await page.getByTestId('index-intent-check-status').click()
    await expect(page.getByTestId('index-intent-state')).toHaveAttribute('data-state', 'completed')
  }
  expect(intents).toEqual(['index-A', 'index-C', 'index-B'])
  for (const [source, copy, viewRef] of [
    ['source-A', 'checkout-A', 'view-A'],
    ['source-A', 'checkout-C', 'view-C'],
    ['source-B', 'checkout-B', 'view-B'],
  ]) {
    await repository.selectOption(source)
    await checkout.selectOption(copy)
    await expect(page.getByTestId('code-context-index-affordance')).toHaveCount(0)
    await page.getByTestId('code-context-snapshot').selectOption(viewRef)
    await expect(page.getByTestId('code-pin-context')).toBeEnabled()
    expect(pins).toHaveLength(viewRef === 'view-A' ? 0 : viewRef === 'view-C' ? 1 : 2)
    await page.getByTestId('code-pin-context').click()
    await expect.poll(() => pins.at(-1)).toBe(viewRef)
    await expect(page.getByTestId('code-pin-context')).toBeDisabled()
  }
  expect(pins).toEqual(['view-A', 'view-C', 'view-B'])
})
