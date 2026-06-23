import { expect, test } from '@playwright/test'

test('memory hide-as-noise uses the live suppress action and removes the row', async ({ page }) => {
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

  await page.goto('/memory')

  const noisyRow = page.getByRole('button', { name: /stale recall hits should be suppressed/ })
  await expect(noisyRow).toBeVisible()
  await noisyRow.click()

  await page.getByRole('button', { name: 'Скрыть как шум' }).click()
  await page.getByRole('button', { name: 'Подтвердить скрытие' }).click()

  await expect(page.getByText('Запись памяти 102 скрыта как шум')).toBeVisible()
  await expect(noisyRow).toHaveCount(0)

  expect(consoleProblems).toEqual([])
  expect(failedRequests).toEqual([])
  expect(badResponses).toEqual([])
  expect(pageErrors).toEqual([])
})
