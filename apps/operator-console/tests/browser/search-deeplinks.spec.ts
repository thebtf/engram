import { expect, test } from '@playwright/test'

test('search deep links preserve a q-only prefill and open the scoped memory row', async ({ page }) => {
  const searchRequests: URL[] = []
  page.on('request', (request) => {
    const url = new URL(request.url())
    if (url.pathname === '/api/context/search') searchRequests.push(url)
  })

  await page.goto('/search?q=operator%20console')
  await expect(page.getByLabel('Запрос')).toHaveValue('operator console')
  await expect.poll(() => searchRequests.length).toBe(0)

  await page.goto('/search?q=operator%20console&project=operator-console')
  await expect(page.getByTestId('search-result-open-101')).toBeVisible()
  await expect.poll(() => searchRequests.length).toBe(1)
  expect(searchRequests[0].searchParams.get('project')).toBe('operator-console')
  expect(searchRequests[0].searchParams.get('query')).toBe('operator console')

  await page.getByTestId('search-result-open-101').click()
  await expect(page).toHaveURL(/\/memory\?project=operator-console&memory=101/)
  await expect(page.locator('#memory-project-filter')).toHaveValue('operator-console')
  await expect(page.getByTestId('memory-row-101')).toHaveClass(/open/)
  await expect(page.locator('.detail .dcontent')).toHaveText('operator console = data plane: manage memory PRODUCT, config one Settings tab')
})
