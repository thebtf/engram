import { expect, test } from '@playwright/test'
const projectID = '11111111-1111-4111-8111-111111111111'
const otherProjectID = '22222222-2222-4222-8222-222222222222'

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

  await page.goto(`/search?q=operator%20console&project=${projectID}`)
  await expect(page.getByTestId('search-result-open-101')).toBeVisible()
  await expect.poll(() => searchRequests.length).toBe(1)
  expect(searchRequests[0].searchParams.get('project')).toBe(projectID)
  expect(searchRequests[0].searchParams.get('query')).toBe('operator console')

  await page.getByTestId('search-result-open-101').click()
  await expect(page).toHaveURL(new RegExp(`/memory\\?project=${projectID}&memory=101`))
  await expect(page.getByTestId('project-context-select')).toHaveValue(projectID)
  await expect(page.getByTestId('memory-row-101')).toHaveAttribute('aria-pressed', 'true')
  await expect(page.getByTestId('memory-content')).toHaveText('operator console = data plane: manage memory PRODUCT, config one Settings tab')
  await expect(page.locator('#memory-reader-heading')).toBeFocused()
  await page.getByRole('button', { name: 'Вернуться к результатам', exact: true }).click()
  await expect(page.getByTestId('search-result-open-101')).toBeFocused()
  await expect(page.getByTestId('project-context-select')).toHaveValue(projectID)
})

test('search deep link fetches and opens a memory omitted by the capped list', async ({ page }) => {
  const exactRequests: URL[] = []
  page.on('request', (request) => {
    const url = new URL(request.url())
    if (url.pathname === '/api/memories/501') exactRequests.push(url)
  })

  await page.goto(`/search?q=older%20memory&project=${projectID}`)
  await page.getByTestId('search-result-open-501').click()

  await expect(page).toHaveURL(new RegExp(`/memory\\?project=${projectID}&memory=501`))
  await expect(page.getByTestId('memory-content')).toHaveText('older memory omitted by the 500-row project list')
  expect(exactRequests[0].searchParams.get('project')).toBe(projectID)
  expect(exactRequests).toHaveLength(1)
})

test('memory deep link does not open an exact-ID row from another project', async ({ page }) => {
  await page.route('**/api/memories/501?**', (route) => route.fulfill({
    json: {
      id: 501,
      project: otherProjectID,
      content: 'wrong project',
      tags: [],
      tier: 'semantic',
      status: 'active',
    },
  }))

  await page.goto(`/memory?project=${projectID}&memory=501`)

  await expect(page.getByTestId('memory-content')).toHaveCount(0)
  await expect(page.getByTestId('project-context-select')).toHaveValue(projectID)
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
        project: projectID,
        content: 'older memory omitted by the 500-row project list',
        tags: ['deep-link'],
        tier: 'semantic',
        status: 'active',
      }
    })
  })

  await page.goto(`/memory?project=${projectID}&memory=501`)
  await started
  await page.evaluate((project) => {
    window.history.pushState({}, '', `/memory?project=${project}&memory=101`)
    window.dispatchEvent(new PopStateEvent('popstate'))
  }, projectID)

  await expect(page.getByTestId('memory-row-101')).toHaveAttribute('aria-pressed', 'true')
  release()
  await expect(page.getByTestId('memory-row-101')).toHaveAttribute('aria-pressed', 'true')
  await expect(page.getByTestId('memory-content')).not.toContainText('older memory omitted')
  await expect(page.getByTestId('project-context-select')).toHaveValue(projectID)
})

test('search result actions require explicit memory discriminants', async ({ page }) => {
  await page.goto(`/search?q=behavioral%20rule%20guidance&project=${projectID}`)

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
      await route.fulfill({ json: { observations: [{ id: 701, project: projectID, title: 'older scoped result', type: 'discovery', memory_type: 'context' }] } })
      return
    }
    await route.fulfill({ json: { observations: [{ id: 702, project: projectID, title: 'newer scoped result', type: 'discovery', memory_type: 'context' }] } })
  })

  await page.goto(`/search?q=older&project=${projectID}`)
  await olderStarted
  await page.evaluate((project) => {
    window.history.pushState({}, '', `/search?q=newer&project=${project}`)
    window.dispatchEvent(new PopStateEvent('popstate'))
  }, projectID)

  await expect(page.getByText('newer scoped result')).toBeVisible()
  releaseOlder()
  await expect(page.getByText('newer scoped result')).toBeVisible()
  await expect(page.getByText('older scoped result')).toHaveCount(0)
})

test('legacy all-project document anchor resolves before content and history reads', async ({ page }) => {
  const reads: URL[] = []
  await page.route('**/api/documents/read?**', (route) => {
    const url = new URL(route.request().url())
    reads.push(url)
    return route.fulfill({ json: { id: 901, path: 'legacy.md', project: projectID, version: 1, content: 'Scoped document fixture', content_hash: 'fixture-hash', doc_type: 'markdown' } })
  })
  await page.route('**/api/documents/history?**', (route) => {
    const url = new URL(route.request().url())
    reads.push(url)
    return route.fulfill({ json: { path: 'legacy.md', project: projectID, versions: [{ id: 901, version: 1, content_hash: 'fixture-hash' }] } })
  })
  await page.route('**/api/documents/comments?**', (route) => route.fulfill({ json: { comments: [] } }))
  await page.goto('/documents?project=all&docProject=operator-console&path=legacy.md')
  await expect(page.getByTestId('document-content')).toHaveText('Scoped document fixture')
  await expect(page).toHaveURL(new RegExp(`docProject=${projectID}`))
  expect(reads.length).toBeGreaterThan(1)
  expect(reads.every((url) => url.searchParams.get('project') === projectID)).toBe(true)
  await page.getByRole('button', { name: 'Вернуться к результатам', exact: true }).click()
  expect(new URL(page.url()).searchParams.get('project')).toBe('all')
  expect(new URL(page.url()).searchParams.has('docProject')).toBe(false)
})

for (const path of ['memory', 'documents']) {
  test(`${path} keeps failed project resolution distinct from empty knowledge`, async ({ page }) => {
    const reads: URL[] = []
    page.on('request', request => {
      const url = new URL(request.url())
      if (url.pathname.startsWith('/api/memories') || url.pathname.startsWith('/api/documents')) reads.push(url)
    })
    await page.route('**/api/projects/resolve?**', route => route.fulfill({ status: 503, json: { error: 'Synthetic mapping unavailable' } }))
    await page.goto(`/${path}?project=unknown-retained-project`)
    await expect(page.getByTestId(`${path}-project-error`)).toBeVisible()
    await expect(page.getByTestId('project-context-select')).toHaveValue('')
    expect(reads).toEqual([])
  })
}
