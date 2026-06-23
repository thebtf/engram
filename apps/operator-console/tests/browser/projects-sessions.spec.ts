import { expect, test } from '@playwright/test'

test('projects and sessions expose live rows and safe project archive', async ({ page }) => {
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

  await page.goto('/projects')
  await expect(page.getByRole('heading', { name: 'Проекты и сессии' })).toBeVisible()
  await expect(page.getByTestId('project-row-operator-console')).toBeVisible()
  await expect(page.getByTestId('project-row-project-alpha')).toBeVisible()

  await page.getByTestId('project-session-501').click()
  await expect(page.getByRole('heading', { name: 'Деталь сессии' })).toBeVisible()
  await expect(page.getByText('operator-console').last()).toBeVisible()
  await expect(page.getByText('balanced')).toBeVisible()
  await expect(page.getByText('GET /api/sessions/{id}/transcript')).toBeVisible()

  await page.getByTestId('project-archive-open-project-alpha').click()
  await expect(page.getByTestId('project-archive-confirm-project-alpha')).toBeVisible()
  await expect(page.getByText('DELETE /api/projects/project-alpha')).toBeVisible()
  await expect(page.getByText(/soft-delete проекта project-alpha/)).toBeVisible()

  const confirmButton = page.getByTestId('project-archive-confirm-button-project-alpha')
  await expect(confirmButton).toBeDisabled()
  await page.getByTestId('project-archive-input-project-alpha').fill('wrong-project')
  await expect(confirmButton).toBeDisabled()
  await page.getByTestId('project-archive-input-project-alpha').fill('project-alpha')
  await expect(confirmButton).toBeEnabled()
  await confirmButton.click()

  await expect(page.getByTestId('project-row-project-alpha')).toHaveCount(0)
  await expect(page.getByTestId('project-archive-confirm-project-alpha')).toHaveCount(0)

  expect(consoleProblems).toEqual([])
  expect(failedRequests).toEqual([])
  expect(badResponses).toEqual([])
  expect(pageErrors).toEqual([])
})
