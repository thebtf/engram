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

test('trusted SSO still blocks local signout when a password cookie is present', async ({ page }) => {
  let authReadsWithCookie = 0
  let logoutPosts = 0
  await page.context().addCookies([{
    name: 'engram_auth', value: 'local-browser-session', domain: '127.0.0.1', path: '/',
  }])
  await page.route('**/api/auth/me', (route) => {
    if (route.request().headers().cookie?.includes('engram_auth=local-browser-session')) authReadsWithCookie += 1
    return route.fulfill({
      json: { authenticated: true, auth_disabled: false, role: 'operator', source: 'authentik', auth_source: 'authentik' },
    })
  })
  await page.route('**/api/auth/logout', (route) => {
    logoutPosts += 1
    return route.fulfill({ json: { authenticated: false } })
  })

  await page.goto('/')
  await page.getByRole('button', { name: 'Profile menu' }).click()
  await expect(page.getByRole('menuitem', { name: 'Sign out of console' })).toBeDisabled()
  await expect(page.getByText('Sign out through your identity provider to end this session.')).toBeVisible()
  expect(authReadsWithCookie).toBeGreaterThan(0)
  expect(logoutPosts).toBe(0)
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
