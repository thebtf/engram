import { expect, test } from '@playwright/test'

test('settings opens as a modal overlay and keeps unwired controls honest', async ({ page }) => {
  const consoleProblems: string[] = []
  const failedRequests: string[] = []
  const badResponses: string[] = []
  const pageErrors: string[] = []

  page.on('console', (message) => {
    if (message.type() === 'error' || message.type() === 'warning') {
      consoleProblems.push(`${message.type()}: ${message.text()}`)
    }
  })
  page.on('requestfailed', (request) => {
    failedRequests.push(`${request.method()} ${request.url()} ${request.failure()?.errorText || 'failed'}`)
  })
  page.on('response', (response) => {
    if (response.status() >= 400) {
      badResponses.push(`${response.status()} ${response.request().method()} ${response.url()}`)
    }
  })
  page.on('pageerror', (error) => {
    pageErrors.push(error.message)
  })

  await page.goto('/settings')
  await expect(page.getByRole('heading', { name: 'Настройки сервера' })).toBeVisible()
  await expect(page.getByText('не уводят оператора из задачи')).toBeVisible()

  await page.getByRole('button', { name: 'Открыть настройки' }).click()
  const dialog = page.getByRole('dialog', { name: 'Настройки сервера' })
  await expect(dialog).toBeVisible()
  await expect(dialog.getByRole('heading', { name: 'Общие' })).toBeVisible()
  await expect(dialog.getByText('модальное окно')).toBeVisible()

  await dialog.getByRole('button', { name: 'Флаги сервера' }).click()
  await expect(dialog.getByRole('heading', { name: 'Флаги сервера' })).toBeVisible()
  await expect(dialog.getByText('memory.inject_unified').first()).toBeVisible()
  await expect(dialog.getByText('PATCH /api/config').first()).toBeVisible()
  await expect(dialog.getByText('GET /api/flags')).toBeVisible()
  await expect(dialog.getByText('ENGRAM_VNEXT_F_ENABLED')).toBeVisible()
  await expect(dialog.getByText('включено').first()).toBeVisible()
  await expect(dialog.getByText('Полная карта feature flags')).toHaveCount(0)
  const injectUnifiedRow = dialog.locator('.sw-row', { hasText: 'Единая инъекция памяти' })
  await injectUnifiedRow.getByRole('switch').click()
  await dialog.getByRole('button', { name: 'Сохранить config' }).click()
  await expect(dialog.getByText(/Сохранено: inject_unified/)).toBeVisible()
  await expect(dialog.getByText(/Restart required: да/)).toBeVisible()
  await expect(dialog.getByText(/Restart нужен для: memory\.inject_unified/)).toBeVisible()

  await dialog.getByRole('button', { name: /Модели/ }).click()
  await expect(dialog.getByRole('heading', { name: 'Модели' })).toBeVisible()
  await expect(dialog.getByText('Backend seam ещё не готов')).toBeVisible()
  await expect(dialog.getByText('GET /api/models').first()).toBeVisible()

  await page.keyboard.press('Escape')
  await expect(dialog).toBeHidden()
  await expect(page).toHaveURL(/\/settings$/)
  await expect(page.getByRole('heading', { name: 'Настройки сервера' })).toBeVisible()

  expect(consoleProblems).toEqual([])
  expect(failedRequests).toEqual([])
  expect(badResponses).toEqual([])
  expect(pageErrors).toEqual([])
})
