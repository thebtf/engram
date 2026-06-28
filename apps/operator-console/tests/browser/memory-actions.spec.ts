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

  const noisyRow = page.getByTestId('memory-row-102')
  await expect(noisyRow).toBeVisible()
  await noisyRow.click()

  const suppressAction = page.getByTestId('memory-suppress-action')
  await suppressAction.click()
  await suppressAction.click()

  await expect(page.getByTestId('memory-notice')).toBeVisible()
  await expect(noisyRow).toHaveCount(0)

  expect(consoleProblems).toEqual([])
  expect(failedRequests).toEqual([])
  expect(badResponses).toEqual([])
  expect(pageErrors).toEqual([])
})

test('principal memory surface uses live query and honest MCP-only brief state', async ({ page }) => {
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

  await expect(page.getByTestId('principal-memory-surface')).toBeVisible()
  await expect(page.getByTestId('principal-state-banner')).toHaveAttribute('data-state', 'gated')

  await page.getByTestId('principal-select').fill('agent/alice')
  await page.getByTestId('domain-select').fill('operator-console')
  await page.getByTestId('refresh').click()

  await expect(page.getByTestId('principal-state-banner')).toHaveAttribute('data-state', 'live')
  await expect(page.getByTestId('principal-knowledge-summary')).toContainText('operator console = data plane')
  await expect(page.getByTestId('principal-knowledge-summary')).toContainText('agent/alice')
  await expect(page.getByTestId('principal-brief-panel')).toHaveAttribute('data-state', 'mustbuild')
  await expect(page.getByTestId('principal-brief-panel')).toContainText('MCP get_memory_brief')

  expect(consoleProblems).toEqual([])
  expect(failedRequests).toEqual([])
  expect(badResponses).toEqual([])
  expect(pageErrors).toEqual([])
})
