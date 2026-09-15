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
