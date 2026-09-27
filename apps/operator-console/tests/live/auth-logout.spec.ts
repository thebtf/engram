import { chromium, expect, test } from '@playwright/test'
import { readLiveFixture } from './fixture-bootstrap'

test('local operator signs out of Workspace and returns to the localized login', async () => {
  const browser = process.env.ENGRAM_LOGOUT_BROWSER_CDP
    ? await chromium.connectOverCDP(process.env.ENGRAM_LOGOUT_BROWSER_CDP)
    : await chromium.launch()
  const context = await browser.newContext({ locale: 'ru-RU' })
  const page = await context.newPage()
  try {
    const fixture = process.env.ENGRAM_LOGOUT_BROWSER_BASE_URL
      ? {
        frontend: { baseUrl: process.env.ENGRAM_LOGOUT_BROWSER_BASE_URL },
        backend: { baseUrl: process.env.ENGRAM_LOGOUT_BROWSER_BACKEND_URL || '' },
        browserCredential: { email: process.env.ENGRAM_LOGOUT_BROWSER_EMAIL || '', password: process.env.ENGRAM_LOGOUT_BROWSER_PASSWORD || '' },
      }
      : await readLiveFixture()
    const base = fixture.frontend.baseUrl
    expect(base).toMatch(/^http:\/\/127\.0\.0\.1:\d+$/)
    expect(fixture.backend.baseUrl).toMatch(/^http:\/\/127\.0\.0\.1:\d+$/)
    expect(fixture.browserCredential.email).toBeTruthy()
    expect(fixture.browserCredential.password).toBeTruthy()

    await page.goto(`${base}/login`)
    await expect(page.getByRole('heading', { name: 'Вход в консоль' })).toBeVisible()
    await page.getByLabel('Электронная почта').fill(fixture.browserCredential.email)
    await page.getByLabel('Пароль').fill(fixture.browserCredential.password)
    await page.getByRole('button', { name: 'Войти' }).click()
    await expect(page).toHaveURL(`${base}/`)
    await expect(page.getByRole('button', { name: 'Меню профиля' })).toBeVisible()
    await page.goto(`${base}/code`)
    await expect(page).toHaveURL(`${base}/code`)

    await page.getByRole('button', { name: 'Меню профиля' }).click()
    const logout = page.waitForResponse((response) => response.url().endsWith('/api/auth/logout') && response.request().method() === 'POST')
    await page.getByRole('menuitem', { name: 'Выйти из консоли' }).click()
    expect((await logout).status()).toBe(200)
    await expect(page).toHaveURL(`${base}/login`)
    await expect(page.getByRole('heading', { name: 'Вход в консоль' })).toBeVisible()
    expect((await page.request.get(`${base}/api/auth/me`)).status()).toBe(401)
    expect((await page.context().cookies(base)).filter((cookie) => cookie.name === 'engram_auth' || cookie.name === 'engram_session')).toEqual([])
    expect((await page.request.get(`${fixture.backend.baseUrl}/code`)).status()).toBe(401)
    await page.reload()
    await expect(page.getByRole('textbox', { name: 'Электронная почта' })).toBeVisible()
  } finally {
    await context.close()
    await browser.close()
  }
})
