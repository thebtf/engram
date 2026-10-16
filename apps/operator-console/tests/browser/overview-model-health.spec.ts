import { expect, test } from '@playwright/test'

test('home loads only bounded selected-project knowledge, never global bodies for counts', async ({ page }) => {
  const requests: URL[] = []
  page.on('request', (request) => {
    const url = new URL(request.url())
    if (url.pathname === '/api/memories' || url.pathname === '/api/documents') requests.push(url)
  })
  await page.goto('/')
  await expect(page.getByRole('heading', { name: 'С чего начнём?' })).toBeVisible()
  expect(requests).toEqual([])
  await page.getByTestId('project-context-select').selectOption('11111111-1111-4111-8111-111111111111')
  await expect(page.getByRole('heading', { name: 'Последние записи' })).toBeVisible()
  await expect.poll(() => requests.length).toBe(2)
  for (const url of requests) {
    expect(url.searchParams.get('project')).toBe('11111111-1111-4111-8111-111111111111')
    expect(url.searchParams.get('limit')).toBe('3')
    expect(url.searchParams.get('offset')).toBe('0')
  }
  await expect(page.locator('.recent-knowledge')).toContainText('operator console = data plane')
})
