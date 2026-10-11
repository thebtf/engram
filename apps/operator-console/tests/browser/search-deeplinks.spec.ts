import { expect, test } from '@playwright/test'

test('search deep links preserve a q-only prefill and open the scoped memory row', async ({ page }) => {
  const searchRequests: URL[] = []
  await page.setViewportSize({ width: 390, height: 844 })
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
  await expect(page.getByTestId('project-context-select')).toHaveValue('operator-console')
  await expect(page.getByTestId('memory-row-101')).toHaveAttribute('aria-pressed', 'true')
  await expect(page.getByTestId('memory-content')).toHaveText('operator console = data plane: manage memory PRODUCT, config one Settings tab')
  await expect(page.locator('#memory-reader-heading')).toBeFocused()
  await page.getByRole('button', { name: 'Вернуться к результатам', exact: true }).click()
  await expect(page.getByTestId('search-result-open-101')).toBeFocused()
  await expect(page.getByTestId('project-context-select')).toHaveValue('operator-console')
})

test('search deep link fetches and opens a memory omitted by the capped list', async ({ page }) => {
  const exactRequests: URL[] = []
  page.on('request', (request) => {
    const url = new URL(request.url())
    if (url.pathname === '/api/memories/501') exactRequests.push(url)
  })

  await page.goto('/search?q=older%20memory&project=operator-console')
  await page.getByTestId('search-result-open-501').click()

  await expect(page).toHaveURL(/\/memory\?project=operator-console&memory=501/)
  await expect(page.getByTestId('memory-content')).toHaveText('older memory omitted by the 500-row project list')
  expect(exactRequests[0].searchParams.get('project')).toBe('operator-console')
  expect(exactRequests).toHaveLength(1)
})

test('memory deep link does not open an exact-ID row from another project', async ({ page }) => {
  await page.route('**/api/memories/501?**', (route) => route.fulfill({
    json: {
      id: 501,
      project: 'other-project',
      content: 'wrong project',
      tags: [],
      tier: 'semantic',
      status: 'active',
    },
  }))

  await page.goto('/memory?project=operator-console&memory=501')

  await expect(page.getByTestId('memory-content')).toHaveCount(0)
  await expect(page.getByTestId('project-context-select')).toHaveValue('operator-console')
  await expect(page.locator('.reader .error')).toBeVisible()
})

test('late exact-ID deep link response cannot override a newer route', async ({ page }) => {
  let markStarted!: () => void
  let release!: () => void
  const started = new Promise<void>((resolve) => { markStarted = resolve })
  const delayed = new Promise<void>((resolve) => { release = resolve })

  await page.route('**/api/memories/501?**', async (route) => {
    markStarted()
    await delayed
    await route.fulfill({
      json: {
        id: 501,
        project: 'operator-console',
        content: 'older memory omitted by the 500-row project list',
        tags: ['deep-link'],
        tier: 'semantic',
        status: 'active',
      }
    })
  })

  await page.goto('/memory?project=operator-console&memory=501')
  await started
  await page.evaluate(() => {
    window.history.pushState({}, '', '/memory?project=operator-console&memory=101')
    window.dispatchEvent(new PopStateEvent('popstate'))
  })

  await expect(page.getByTestId('memory-row-101')).toHaveAttribute('aria-pressed', 'true')
  release()
  await expect(page.getByTestId('memory-row-101')).toHaveAttribute('aria-pressed', 'true')
  await expect(page.getByTestId('memory-content')).not.toContainText('older memory omitted')
  await expect(page.getByTestId('project-context-select')).toHaveValue('operator-console')
})

test('search result actions require explicit memory discriminants', async ({ page }) => {
  await page.goto('/search?q=behavioral%20rule%20guidance&project=operator-console')

  await expect(page.getByText('behavioral rule guidance: preserve explicit memory evidence')).toBeVisible()
  await expect(page.getByTestId('search-result-open-303')).toHaveCount(0)
})

test('newer scoped route owns results over a delayed older search response', async ({ page }) => {
  let markOlderStarted!: () => void
  let releaseOlder!: () => void
  const olderStarted = new Promise<void>((resolve) => { markOlderStarted = resolve })
  const olderReleased = new Promise<void>((resolve) => { releaseOlder = resolve })

  await page.route('**/api/context/search**', async (route) => {
    const query = new URL(route.request().url()).searchParams.get('query')
    if (query === 'older') {
      markOlderStarted()
      await olderReleased
      await route.fulfill({ json: { observations: [{ id: 701, project: 'operator-console', title: 'older scoped result', type: 'discovery', memory_type: 'context' }] } })
      return
    }
    await route.fulfill({ json: { observations: [{ id: 702, project: 'operator-console', title: 'newer scoped result', type: 'discovery', memory_type: 'context' }] } })
  })

  await page.goto('/search?q=older&project=operator-console')
  await olderStarted
  await page.evaluate(() => {
    window.history.pushState({}, '', '/search?q=newer&project=operator-console')
    window.dispatchEvent(new PopStateEvent('popstate'))
  })

  await expect(page.getByText('newer scoped result')).toBeVisible()
  releaseOlder()
  await expect(page.getByText('newer scoped result')).toBeVisible()
  await expect(page.getByText('older scoped result')).toHaveCount(0)
})
