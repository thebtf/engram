import { expect, test, type Route } from '@playwright/test'

const TAB_BINDING_ID = '60000000-0000-4000-8000-000000000041'
const DOCUMENT_PROOF = 'proof-current'

test('Code Explorer renews its live tab lease and leaves no renewal timer after teardown', async ({ page }) => {
  const leasePayloads: unknown[] = []

  await page.clock.install({ time: new Date('2026-09-15T00:00:00Z') })
  await page.route('**/api/code/**', async (route: Route) => {
    const pathname = new URL(route.request().url()).pathname
    if (pathname === '/api/code/tabs/handshake') {
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
test('Code Explorer resumes a same-document SPA remount but isolates copied storage', async ({ page }) => {
  const handshakePayloads: unknown[] = []
  const resumePayloads: unknown[] = []
  const leasePayloads: unknown[] = []
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
      source: { id: 'source-current', label: 'Current source' },
      checkout: { id: 'checkout-current', label: 'Current checkout' },
      view: {
        context_ref: {
          source_id: 'source-current',
          checkout_id: 'checkout-current',
          view_id: 'view-current',
          analysis_profile_id: 'profile-current',
          generation: 1,
        },
        label: 'Current view',
      },
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
      await route.fulfill({ status: 204 })
      return
    }
    if (pathname === '/api/code/status') {
      await route.fulfill({ json: { total_chunks: 0, embedded_chunks: 0, embedding: { Coverage: 'none' } } })
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
  const contextOption = page.getByTestId('code-context-select').locator('option').filter({ hasText: 'Current view' })
  const contextValue = await contextOption.getAttribute('value')
  if (contextValue === null) throw new Error('Code catalog did not expose Current view')
  await page.getByTestId('code-context-select').selectOption(contextValue)
  await page.getByTestId('code-pin-context').click()
  await expect(page.getByTestId('code-context-pinned')).toContainText('Current view')
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
  await expect(page.getByTestId('code-context-pinned')).toContainText('Current view')
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
  await expect(page.getByTestId('code-context-pinned')).toContainText('Current view')
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
