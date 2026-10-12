import { expect, test } from '@playwright/test'

test('candidate queue lists pending candidates and rejects through the live action route', async ({ page }) => {
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

  await page.goto('/queue?project=all')

  const candidate = page.getByTestId('queue-row-302')
  await expect(candidate).toBeVisible()

  const reject = page.getByTestId('queue-action-reject-302')
  await reject.click()
  await reject.click()

  await expect(page.getByTestId('queue-notice')).toBeVisible()
  await expect(candidate).toHaveCount(0)

  expect(consoleProblems).toEqual([])
  expect(failedRequests).toEqual([])
  expect(badResponses).toEqual([])
  expect(pageErrors).toEqual([])
})

test('candidate queue never widens a missing or failed project selection', async ({ page }) => {
  const projects: Array<string | null> = []
  page.on('request', (request) => { const url = new URL(request.url()); if (url.pathname === '/api/memory/candidates') projects.push(url.searchParams.get('project')) })
  await page.goto('/queue')
  await expect(page.getByTestId('project-context-select')).toHaveValue('')
  expect(projects).toEqual([])
  await page.route('**/api/projects/resolve?**', (route) => route.fulfill({ status: 503, json: { error: 'Mapping unavailable' } }))
  await page.goto('/queue?project=historical-unmapped')
  await expect(page.getByTestId('project-context-select')).toHaveValue('')
  await expect(page.locator('.statebar[data-state="error"]')).toBeVisible()
  expect(projects).toEqual([])
})
