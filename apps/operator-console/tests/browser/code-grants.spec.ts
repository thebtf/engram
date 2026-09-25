import { expect, test, type Route } from '@playwright/test'

const grant = (ref: string, reader: string, expires_at: string | null = null) => ({ grant_ref: ref, state: 'active', expires_at, repository: 'Engram', working_copy: 'checkout / feature', reader })

test('Owner reload sees every paged active grant and revokes a reader without an identifier form', async ({ page }) => {
  let revoked = false
  const requests: string[] = []
  await page.route('**/api/code/**', async (route: Route) => {
    const url = new URL(route.request().url())
    requests.push(`${route.request().method()} ${url.pathname}${url.search}`)
    if (url.pathname === '/api/code/grants/choices') {
      await route.fulfill({ json: { choices: [{ choice_ref: 'opaque-checkout', repository: 'Engram', working_copy: 'checkout / feature' }], targets: [{ target_ref: 'opaque-reader', label: 'Other reader' }] } })
    } else if (url.pathname === '/api/code/grants' && route.request().method() === 'GET') {
      await route.fulfill({
        json: url.searchParams.get('next_ref') === 'cursor-2'
          ? { grants: revoked ? [] : [grant('opaque-second', 'Other reader', '2027-02-01T00:00:00Z')] }
          : { grants: [grant('opaque-first', 'First reader')], next_ref: 'cursor-2' }
      })
    } else if (url.pathname === '/api/code/grants/opaque-second/revoke') {
      revoked = true
      await route.fulfill({ status: 204 })
    } else if (url.pathname === '/api/code/contexts') {
      await route.fulfill({ status: revoked ? 403 : 200, json: revoked ? { error: 'denied' } : { contexts: [] } })
    } else if (url.pathname === '/api/code/tabs/handshake') {
      await route.fulfill({ json: { state: 'TAB_BINDING_READY', tab_binding_id: '60000000-0000-4000-8000-000000000041', document_proof: 'proof', resume_nonce: 'resume', reload_token: 'reload' } })
    } else {
      await route.fulfill({ status: 500 })
    }
  })
  await page.goto('/code')
  await page.getByTestId('code-grant-chooser').locator('summary').click()
  await expect(page.getByTestId('code-grant-chooser').getByRole('listitem')).toHaveCount(2)
  await page.reload()
  const chooser = page.getByTestId('code-grant-chooser')
  await chooser.locator('summary').click()
  await expect(chooser.getByRole('listitem')).toHaveCount(2)
  await expect(chooser).toContainText('checkout / feature')
  await expect(chooser).toContainText('Other reader')
  await expect(chooser).toContainText('2027')
  await chooser.getByRole('button', { name: /Отозвать доступ Other reader/ }).click()
  await expect(chooser.getByRole('listitem')).toHaveCount(1)
  expect(requests).toContain('GET /api/code/grants?next_ref=cursor-2')
  expect(requests).toContain('POST /api/code/grants/opaque-second/revoke')
  expect(await page.evaluate(async () => (await fetch('/api/code/contexts', { method: 'POST', credentials: 'include', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ tab_binding_id: 'reader-tab', document_proof: 'reader-proof' }) })).status)).toBe(403)
  await page.setViewportSize({ width: 375, height: 812 })
  expect(await chooser.evaluate(element => element.scrollWidth <= element.clientWidth)).toBe(true)
  await chooser.locator('summary').focus()
  await expect(chooser.locator('summary')).toBeFocused()
})

test('Grant inventory reports denied, empty and failed page without exposing incomplete inventory', async ({ page }) => {
  let state: 'denied' | 'empty' | 'bad-page' = 'denied'
  await page.route('**/api/code/**', async (route: Route) => {
    const pathname = new URL(route.request().url()).pathname
    if (pathname === '/api/code/grants/choices') {
      await route.fulfill({ json: { choices: [], targets: [] } })
    } else if (pathname === '/api/code/grants') {
      await route.fulfill(state === 'denied' ? { status: 403 } : state === 'empty' ? { json: { grants: [] } } : { json: { grants: [grant('first', 'Reader')], next_ref: 'again' } })
    } else if (pathname === '/api/code/tabs/handshake') {
      await route.fulfill({ json: { state: 'TAB_BINDING_READY', tab_binding_id: '60000000-0000-4000-8000-000000000041', document_proof: 'proof', resume_nonce: 'resume', reload_token: 'reload' } })
    } else if (pathname === '/api/code/contexts') {
      await route.fulfill({ json: { contexts: [] } })
    } else {
      await route.fulfill({ status: 500 })
    }
  })
  await page.goto('/code')
  const chooser = page.getByTestId('code-grant-chooser')
  await chooser.locator('summary').click()
  await expect(chooser.getByRole('alert')).toContainText('недоступны')
  state = 'empty'
  await page.reload()
  await chooser.locator('summary').click()
  await expect(chooser.getByRole('status')).toContainText('Нет действующих разрешений')
  state = 'bad-page'
  await page.reload()
  await chooser.locator('summary').click()
  await expect(chooser.getByRole('alert')).toContainText('Не удалось изменить доступ')
  await expect(chooser.getByRole('listitem')).toHaveCount(0)
})

test('Expired grants are not offered for revocation and labels are rendered as text', async ({ page }) => {
  await page.route('**/api/code/**', async (route: Route) => {
    const pathname = new URL(route.request().url()).pathname
    if (pathname === '/api/code/grants/choices') {
      await route.fulfill({ json: { choices: [], targets: [] } })
    } else if (pathname === '/api/code/grants') {
      await route.fulfill({ json: { grants: [grant('expired', 'Past reader', '2020-01-01T00:00:00Z'), grant('current', '<img src=x onerror=alert(1)>')] } })
    } else if (pathname === '/api/code/tabs/handshake') {
      await route.fulfill({ json: { state: 'TAB_BINDING_READY', tab_binding_id: '60000000-0000-4000-8000-000000000041', document_proof: 'proof', resume_nonce: 'resume', reload_token: 'reload' } })
    } else if (pathname === '/api/code/contexts') {
      await route.fulfill({ json: { contexts: [] } })
    } else {
      await route.fulfill({ status: 500 })
    }
  })
  await page.goto('/code')
  const chooser = page.getByTestId('code-grant-chooser')
  await chooser.locator('summary').click()
  await expect(chooser.getByRole('listitem')).toHaveCount(1)
  await expect(chooser).not.toContainText('Past reader')
  await expect(chooser).toContainText('<img src=x onerror=alert(1)>')
  await expect(chooser.locator('img')).toHaveCount(0)
})
