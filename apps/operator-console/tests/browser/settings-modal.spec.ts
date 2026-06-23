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
  const dialog = page.getByRole('dialog', { name: 'Настройки сервера' })
  await expect(dialog).toBeVisible()
  await expect(page).toHaveURL(/\/$/)
  await expect(page.getByRole('button', { name: 'Открыть настройки' })).toHaveCount(0)
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
  await expect(dialog.getByText(/Изменение ожидает restart/)).toBeVisible()
  await expect(dialog.getByText(/Сейчас: да/)).toBeVisible()
  await expect(dialog.getByText(/После restart: нет/)).toBeVisible()

  await dialog.getByRole('button', { name: 'Домены памяти' }).click()
  await expect(dialog.getByRole('heading', { name: 'Домены памяти' })).toBeVisible()
  await expect(dialog.getByText('GET /api/memory-domains')).toBeVisible()
  await expect(dialog.getByText('memory-lab')).toBeVisible()
  await dialog.getByRole('button', { name: 'Изменить домен memory-lab' }).click()
  await expect(dialog.getByTestId('domain-registry-domain')).toHaveValue('memory-lab')
  await expect(dialog.getByTestId('domain-registry-domain')).toBeDisabled()
  await dialog.getByTestId('domain-registry-owner').fill('agent/bob')
  await dialog.getByTestId('domain-registry-mode').selectOption('reject')
  await dialog.getByTestId('domain-registry-save').click()
  await expect(dialog.getByText('Домен memory-lab сохранён')).toBeVisible()
  await expect(dialog.getByText('agent/bob · agent')).toBeVisible()
  await dialog.getByTestId('domain-registry-delete-memory-lab').click()
  await expect(dialog.getByTestId('domain-registry-delete-memory-lab')).toHaveText('Подтвердить удаление')
  await dialog.getByTestId('domain-registry-delete-memory-lab').click()
  await expect(dialog.getByText('Домен memory-lab возвращён к implicit policy')).toBeVisible()
  await expect(dialog.locator('.domain-row').filter({ hasText: 'memory-lab' })).toHaveCount(0)

  await dialog.getByRole('button', { name: /Модели/ }).click()
  await expect(dialog.getByRole('heading', { name: 'Модели' })).toBeVisible()
  await expect(dialog.getByText('Backend seam ещё не готов')).toBeVisible()
  await expect(dialog.getByText('GET /api/models').first()).toBeVisible()

  await page.keyboard.press('Escape')
  await expect(dialog).toBeHidden()
  await expect(page).toHaveURL(/\/$/)

  await page.getByRole('button', { name: 'Меню профиля' }).click()
  await expect(page.getByRole('menu')).toBeVisible()
  await expect(page.getByRole('menuitem', { name: 'Профиль и настройки' })).toBeVisible()
  await expect(page.getByRole('menuitem', { name: 'Настройки консоли' })).toBeVisible()
  await expect(page.getByRole('menuitem', { name: 'Выйти из консоли' })).toBeDisabled()
  await expect(page.getByRole('menuitem', { name: 'Выйти из консоли' })).toHaveAttribute('title', /Вход отключён/)

  await page.getByRole('menuitem', { name: 'Профиль и настройки' }).click()
  const profileDialog = page.getByRole('dialog', { name: 'Профиль оператора' })
  await expect(profileDialog).toBeVisible()
  await expect(profileDialog.getByRole('heading', { name: 'admin' })).toBeVisible()
  await expect(profileDialog.locator('.hb-ev', { hasText: 'PATCH /api/profile' })).toBeVisible()
  await expect(profileDialog.locator('.hb-ev', { hasText: 'GET /api/auth/sessions' })).toBeVisible()
  await page.keyboard.press('Escape')
  await expect(profileDialog).toBeHidden()

  await page.getByRole('button', { name: 'Меню профиля' }).click()
  await page.getByRole('menuitem', { name: 'Настройки консоли' }).click()
  await expect(dialog).toBeVisible()
  await page.keyboard.press('Escape')
  await expect(dialog).toBeHidden()

  expect(consoleProblems).toEqual([])
  expect(failedRequests).toEqual([])
  expect(badResponses).toEqual([])
  expect(pageErrors).toEqual([])
})
