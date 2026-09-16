import { expect, test } from '@playwright/test'
import type { Locator, Page } from '@playwright/test'

const deferredSettingsPaths = [
  '/api/config',
  '/api/memory-domains',
  '/api/models',
] as const

function settingsRequestCounts(requests: string[]) {
  return Object.fromEntries(deferredSettingsPaths.map((path) => [
    path,
    requests.filter((request) => request === `GET ${path}`).length,
  ]))
}


async function expectModalEnvironmentRestored(page: Page, trigger?: Locator) {
  await expect.poll(() => page.locator('body').evaluate((body) => body.style.overflow)).toBe('')
  await expect(page.locator('.app')).not.toHaveAttribute('aria-hidden')
  await expect.poll(() => page.locator('.app').evaluate((app) => (app as HTMLElement).inert)).toBe(false)
  if (trigger) await expect(trigger).toBeFocused()
}

test('settings defers its traffic until an actual open and restores the shell after close', async ({ page }) => {
  const settingsRequests: string[] = []
  const consoleProblems: string[] = []
  const failedRequests: string[] = []
  const unexpectedBadResponses: string[] = []
  const pageErrors: string[] = []
  let expectedDomainRefreshFailure = false
  const firstOpenRequestCounts = Object.fromEntries(deferredSettingsPaths.map((path) => [path, 1]))

  page.on('request', (request) => {
    const url = new URL(request.url())
    if (deferredSettingsPaths.includes(url.pathname as typeof deferredSettingsPaths[number])) {
      settingsRequests.push(`${request.method()} ${url.pathname}`)
    }
  })
  page.on('console', (message) => {
    const isExpectedDomainRefreshFailure = expectedDomainRefreshFailure
      && message.type() === 'error'
      && message.text().includes('503')
    if ((message.type() === 'error' || message.type() === 'warning') && !isExpectedDomainRefreshFailure) {
      consoleProblems.push(`${message.type()}: ${message.text()}`)
    }
  })
  page.on('requestfailed', (request) => {
    failedRequests.push(`${request.method()} ${request.url()} ${request.failure()?.errorText || 'failed'}`)
  })
  page.on('response', (response) => {
    const url = new URL(response.url())
    if (response.status() >= 400 && url.pathname !== '/api/memory-domains') {
      unexpectedBadResponses.push(`${response.status()} ${response.request().method()} ${url.pathname}`)
    }
  })
  page.on('pageerror', (error) => {
    pageErrors.push(error.message)
  })

  await page.goto('/')
  const trigger = page.locator('.status-action')
  const dialog = page.getByRole('dialog', { name: 'Настройки сервера' })
  await expect(trigger).toHaveAccessibleName(/.+/)
  await expect(dialog).toHaveCount(0)
  await expect.poll(() => settingsRequestCounts(settingsRequests)).toEqual(
    Object.fromEntries(deferredSettingsPaths.map((path) => [path, 0])),
  )

  await trigger.focus()
  await page.keyboard.press('Enter')
  await expect(dialog).toBeVisible()
  await expect(dialog).toHaveAttribute('aria-modal', 'true')
  await expect(dialog.getByRole('button', { name: 'Закрыть настройки' })).toBeFocused()
  await expect(page.locator('.app')).toHaveAttribute('aria-hidden', 'true')
  await expect.poll(() => page.locator('.app').evaluate((app) => (app as HTMLElement).inert)).toBe(true)
  await expect.poll(() => page.locator('body').evaluate((body) => body.style.overflow)).toBe('hidden')
  await expect.poll(() => settingsRequestCounts(settingsRequests)).toEqual(firstOpenRequestCounts)

  await dialog.getByRole('button', { name: 'Модели' }).click()
  await expect(dialog.getByRole('heading', { name: 'Модели' })).toBeVisible()
  await page.waitForTimeout(150)
  expect(settingsRequestCounts(settingsRequests)).toEqual(firstOpenRequestCounts)

  await page.keyboard.press('Escape')
  await expect(dialog).toBeHidden()
  await expectModalEnvironmentRestored(page, trigger)

  await page.goto('/settings')
  await expect(dialog).toBeVisible()
  await expect.poll(() => new URL(page.url()).pathname).toBe('/')
  const directRouteRequestCounts = Object.fromEntries(deferredSettingsPaths.map((path) => [path, 2]))
  await expect.poll(() => settingsRequestCounts(settingsRequests)).toEqual(directRouteRequestCounts)
  await page.keyboard.press('Escape')
  await expect(dialog).toBeHidden()
  await expectModalEnvironmentRestored(page)

  await trigger.focus()
  await page.keyboard.press('Enter')
  await expect(dialog).toBeVisible()
  expect(settingsRequestCounts(settingsRequests)).toEqual(directRouteRequestCounts)
  await dialog.getByRole('button', { name: 'Модели' }).click()
  const reopenedModelRequestCounts = { ...directRouteRequestCounts, '/api/models': 3 }
  await expect.poll(() => settingsRequestCounts(settingsRequests)).toEqual(reopenedModelRequestCounts)

  expectedDomainRefreshFailure = true
  await page.route('**/api/memory-domains', (route) => route.fulfill({
    status: 503,
    contentType: 'application/json',
    body: JSON.stringify({ message: 'forced domain refresh failure' }),
  }))
  await dialog.getByRole('button', { name: 'Домены' }).click()
  await dialog.getByRole('button', { name: 'Обновить' }).click()
  await expect(dialog.locator('.state.error')).toBeVisible()

  await page.keyboard.press('Escape')
  await expect(dialog).toBeHidden()
  await expectModalEnvironmentRestored(page, trigger)
  expect(consoleProblems).toEqual([])
  expect(failedRequests).toEqual([])
  expect(unexpectedBadResponses).toEqual([])
  expect(pageErrors).toEqual([])
})
