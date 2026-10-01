import { expect, test } from '@playwright/test'

test.use({ locale: 'en-US' })

test('SSO session explains identity-provider signout without pretending to end the session', async ({ page }) => {
  let logoutPosts = 0
  await page.route('**/api/auth/me', (route) => route.fulfill({
    json: { authenticated: true, auth_disabled: false, role: 'operator', source: 'authentik', auth_source: 'authentik' },
  }))
  await page.route('**/api/auth/logout', (route) => {
    logoutPosts += 1
    return route.fulfill({ json: { authenticated: false } })
  })

  await page.goto('/')
  await page.getByRole('button', { name: 'Profile menu' }).click()
  await expect(page.getByRole('menuitem', { name: 'Sign out of console' })).toBeDisabled()
  await expect(page.getByText('Sign out through your identity provider to end this session.')).toBeVisible()
  expect(logoutPosts).toBe(0)
  await expect(page).toHaveURL('/')
})

test('mixed local cookie can be revoked while IdP remains active', async ({ page }) => {
  let authReadsWithCookie = 0
  let authReadsAfterLogout = 0
  let logoutPosts = 0
  let localActive = true
  await page.context().addCookies([{
    name: 'engram_auth', value: 'local-browser-session', domain: '127.0.0.1', path: '/',
  }])
  await page.route('**/api/auth/me', (route) => {
    if (route.request().headers().cookie?.includes('engram_auth=local-browser-session')) authReadsWithCookie += 1
    if (!localActive) authReadsAfterLogout += 1
    return route.fulfill({
      json: localActive
        ? { authenticated: true, auth_disabled: false, role: 'admin', auth_source: 'local', sso_active: true, source: 'local+authentik' }
        : { authenticated: true, auth_disabled: false, role: 'operator', auth_source: 'authentik', sso_active: true, source: 'authentik' },
    })
  })
  await page.route('**/api/auth/logout', (route) => {
    logoutPosts += 1
    localActive = false
    return route.fulfill({ json: { authenticated: false }, headers: { 'Set-Cookie': 'engram_auth=; Max-Age=0; Path=/; HttpOnly' } })
  })

  await page.goto('/')
  await page.getByRole('button', { name: 'Profile menu' }).click()
  const logout = page.getByRole('menuitem', { name: 'End local session' })
  await expect(logout).toBeEnabled()
  await expect(page.getByText('Ending this local session will not sign you out of your identity provider.')).toBeVisible()
  expect(authReadsWithCookie).toBeGreaterThan(0)
  await logout.click()
  await expect.poll(() => authReadsAfterLogout).toBeGreaterThan(0)
  await expect(page).toHaveURL('/')
  expect(logoutPosts).toBe(1)
  expect((await page.context().cookies()).filter((cookie) => cookie.name === 'engram_auth')).toEqual([])
  await page.getByRole('button', { name: 'Profile menu' }).click()
  await expect(page.getByRole('menuitem', { name: 'Sign out of console' })).toBeDisabled()
  await expect(page.getByText('Sign out through your identity provider to end this session.')).toBeVisible()
})

test('local password session requests revocation and navigates to login', async ({ page }) => {
  let authenticated = true
  let logoutPosts = 0
  await page.route('**/api/auth/me', (route) => route.fulfill({
    status: authenticated ? 200 : 401,
    json: authenticated
      ? { authenticated: true, auth_disabled: false, role: 'operator', source: 'local', auth_source: 'local' }
      : { authenticated: false, auth_disabled: false },
  }))
  await page.route('**/api/auth/logout', (route) => {
    logoutPosts += 1
    authenticated = false
    return route.fulfill({ json: { authenticated: false } })
  })

  await page.goto('/')
  await page.getByRole('button', { name: 'Profile menu' }).click()
  const logout = page.getByRole('menuitem', { name: 'Sign out of console' })
  await expect(logout).toBeEnabled()
  await logout.click()
  await expect(page).toHaveURL('/login')
  expect(logoutPosts).toBe(1)
})
