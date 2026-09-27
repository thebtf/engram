import { expect, test, type Route } from '@playwright/test'

const bindingId = '60000000-0000-4000-8000-000000000041'
const source = { source_ref: 'source-opaque', checkout_ref: 'checkout-opaque', repository: 'Engram', working_copy: 'Desk A', indexed_snapshot: { label: 'Snapshot A', revision: 'abc123', published_at: '2026-09-27T00:00:00Z' }, view_ref: 'view-opaque', selection_ref: 'selection-opaque', index_intent_available: false }
const otherSource = { ...source, source_ref: 'source-b', checkout_ref: 'checkout-b', repository: 'Other', working_copy: 'Desk B', indexed_snapshot: { ...source.indexed_snapshot, label: 'Snapshot B' }, view_ref: 'view-b', selection_ref: 'selection-b' }

for (const identity of [{ auth_disabled: true }, { auth_disabled: false }, {}, { authenticated: true }] as const) {
  test(`HTTP LAN workspace honors server identity ${JSON.stringify(identity)}`, async ({ page, baseURL }) => {
    if (!baseURL || /^http:\/\/(?:localhost|127\.|\[::1\])/.test(baseURL)) test.skip(true, 'Requires OPERATOR_CONSOLE_SMOKE_HOST with a real nonloopback interface')
    const requests: string[] = []
    const ids: string[] = []
    await page.route('**/api/auth/me', async route => route.fulfill({ json: identity }))
    await page.route('**/api/code/**', async (route: Route) => {
      const { pathname } = new URL(route.request().url())
      requests.push(pathname)
      const requestId = route.request().headers()['x-engram-request-id']
      if (requestId) ids.push(requestId)
      if (pathname === '/api/code/tabs/handshake') {
        expect(route.request().postDataJSON().document_nonce).toMatch(/^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/i)
      }
      if (pathname === '/api/code/tabs/handshake') await route.fulfill({ json: { state: 'TAB_BINDING_READY', tab_binding_id: bindingId, document_proof: 'proof', resume_nonce: 'resume', reload_token: 'reload' } })
      else if (pathname === '/api/code/contexts') await route.fulfill({ json: { contexts: [source, otherSource] } })
      else if (pathname === `/api/code/tabs/${bindingId}/context`) await route.fulfill({ status: 204 })
      else if (pathname === '/api/code/status') await route.fulfill({ json: { total_chunks: 1, embedded_chunks: 1, freshness: { state: 'fresh' } } })
      else await route.fulfill({ status: 500 })
    })
    if (identity.auth_disabled === true) {
      await page.goto('/')
      await page.getByTestId('overview-workspace-entry').click()
    } else await page.goto('/code?auth_disabled=true')
    expect(await page.evaluate(() => window.isSecureContext)).toBe(false)
    if (identity.auth_disabled === true) {
      await expect(page.getByTestId('code-context-working-copy').getByRole('option', { name: 'Desk A' })).toHaveCount(1)
      await expect(page.getByTestId('code-grant-chooser')).toHaveCount(0)
      await page.getByTestId('code-context-repository').selectOption('source-opaque')
      await page.getByTestId('code-context-working-copy').selectOption('checkout-opaque')
      await page.getByTestId('code-context-snapshot').selectOption('selection-opaque')
      await page.getByTestId('code-pin-context').click()
      await expect(page.getByTestId('code-context-pinned')).toContainText('Desk A')
      await page.getByTestId('code-context-repository').selectOption('source-b')
      await page.getByTestId('code-context-working-copy').selectOption('checkout-b')
      await page.getByTestId('code-context-snapshot').selectOption('selection-b')
      await page.getByTestId('code-pin-context').click()
      await expect(page.getByTestId('code-context-pinned')).toContainText('Desk B')
      expect(requests).toContain('/api/code/status')
      expect(ids.length).toBeGreaterThan(1)
      expect(new Set(ids).size).toBe(ids.length)
      expect(ids.every(id => /^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/i.test(id))).toBe(true)
    } else {
      await expect(page.locator('.phase')).toHaveAttribute('data-state', 'secure-origin-required')
      await expect(page.getByTestId('code-context-working-copy')).toHaveCount(0)
      expect(requests).not.toContain('/api/code/tabs/handshake')
      expect(requests).not.toContain('/api/code/contexts')
    }
  })
}
