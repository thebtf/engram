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

test('Code Explorer resynchronizes a completed selection after catalog refresh while retaining a later partial choice', async ({ page }) => {
  let contextRequests = 0
  const initialCatalog = {
    contexts: [{
      repository: 'Engram',
      working_copy: 'stale candidate checkout',
      indexed_snapshot: { label: 'Stale candidate snapshot', revision: '1a9dad0', published_at: '2026-09-17T00:00:00Z' },
      selection_ref: 'context-current',
      index_intent_available: false,
    }, {
      repository: 'Engram',
      working_copy: ' ',
      indexed_snapshot: { label: 'Malformed snapshot', revision: '1a9dad3', published_at: '2026-09-17T00:03:00Z' },
      selection_ref: 'context-malformed',
      index_intent_available: false,
    }, {
      repository: 'Other repository',
      working_copy: 'manual checkout',
      indexed_snapshot: { label: 'Manual snapshot', revision: '1a9dad1', published_at: '2026-09-17T00:01:00Z' },
      selection_ref: 'context-manual',
      index_intent_available: false,
    }],
  }
  const refreshedCatalog = {
    contexts: [{
      repository: 'Engram',
      working_copy: 'refreshed candidate checkout',
      indexed_snapshot: { label: 'Refreshed candidate snapshot', revision: '1a9dad2', published_at: '2026-09-17T00:02:00Z' },
      selection_ref: 'context-current',
      index_intent_available: false,
    }, initialCatalog.contexts[1]],
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
  await expect(page.getByTestId('code-context-working-copy')).toHaveValue('refreshed candidate checkout')
  await expect(page.getByTestId('code-context-snapshot')).toHaveValue('context-current')

  await page.getByTestId('code-context-repository').selectOption({ label: 'Other repository' })
  await expect(page.getByTestId('code-context-repository')).toHaveValue('Other repository')
  await expect(page.getByTestId('code-context-working-copy')).toHaveValue('')
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
      repository: 'Engram',
      working_copy: 'feature/operator-workspace · operator desk',
      indexed_snapshot: {
        label: 'Current snapshot',
        revision: '1a9dad0',
        published_at: '2026-09-17T00:00:00Z',
      },
      selection_ref: 'context-current',
      index_intent_available: false,
    }, {
      repository: 'Engram',
      working_copy: '',
      index_intent_available: false,
    }, {
      repository: 'Other repository',
      working_copy: 'D working copy',
      indexed_snapshot: {
        label: 'D snapshot',
        revision: '1a9dad1',
        published_at: '2026-09-17T00:01:00Z',
      },
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
    pin: sessionStorage.getItem('engram.operator-code.pinned-context.v1'),
    intent: sessionStorage.getItem('engram.operator-code.index-intent.v1'),
  }))
  const copied = await page.context().newPage()
  await copied.addInitScript((storage) => {
    if (storage.resume !== null) sessionStorage.setItem('engram.operator-code.resume.v1', storage.resume)
    if (storage.pin !== null) sessionStorage.setItem('engram.operator-code.pinned-context.v1', storage.pin)
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
