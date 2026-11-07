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

const candidateRows = (start: number, count: number) => Array.from({ length: count }, (_, index) => ({
  id: start + index,
  status: 'pending',
  proposed_content: `Synthetic candidate ${start + index}`,
  confidence: 0.5,
  affected_projects: [],
}))

test('candidate queue follows a continuation beyond 100 and returns to earlier rows', async ({ page }) => {
  const cursors: Array<string | null> = []
  await page.route('**/api/memory/candidates?**', async (route) => {
    const url = new URL(route.request().url())
    const cursor = url.searchParams.get('cursor')
    cursors.push(cursor)
    await route.fulfill({
      json: cursor === 'next-100'
        ? { candidates: candidateRows(101, 2), count: 2, next_cursor: null }
        : { candidates: candidateRows(1, 100), count: 100, next_cursor: 'next-100' }
    })
  })
  await page.goto('/queue?project=all')
  await expect(page.getByTestId('queue-row-1')).toBeVisible()
  await page.locator('#queue-page-size').selectOption('all')
  await expect(page.getByTestId('queue-row-100')).toBeVisible()
  await page.getByRole('button', { name: 'Перейти на следующую страницу' }).click()
  await expect(page.getByTestId('queue-row-101')).toBeVisible()
  expect(cursors).toContain('next-100')
  await expect(page.getByTestId('queue-row-1')).toHaveCount(0)
  await page.getByRole('button', { name: 'Перейти на предыдущую страницу' }).click()
  await expect(page.getByTestId('queue-row-1')).toBeVisible()
})

for (const { status, diagnosis } of [
  { status: 400, diagnosis: 'Сервер Engram отклонил этот запрос.' },
  { status: 503, diagnosis: 'Сервер Engram недоступен.' },
]) {
  test(`continuation ${status} shows its localized diagnosis without exposing cursor or server detail`, async ({ page }) => {
    const cursor = 'distinctive-opaque-cursor-do-not-display'
    const privateBody = 'private-server-detail-do-not-display'
    const requests: Array<{ project: string | null; cursor: string | null }> = []
    await page.route('**/api/memory/candidates?**', async (route) => {
      const url = new URL(route.request().url())
      const current = { project: url.searchParams.get('project'), cursor: url.searchParams.get('cursor') }
      requests.push(current)
      if (current.cursor) {
        await route.fulfill({ status, json: { error: privateBody } })
      } else {
        await route.fulfill({ json: { candidates: candidateRows(1, 100), count: 100, next_cursor: cursor } })
      }
    })
    await page.goto('/queue?project=all')
    await expect(page.getByTestId('queue-row-1')).toBeVisible()
    await page.locator('#queue-page-size').selectOption('all')
    await page.getByRole('button', { name: 'Перейти на следующую страницу' }).click()
    const error = page.locator('.statebar[data-state="error"]')
    await expect(error).toBeVisible()
    await expect(error).toContainText(diagnosis)
    await expect(error).not.toContainText(cursor)
    await expect(error).not.toContainText(privateBody)
    expect(requests[requests.length - 1]).toEqual({ project: 'all', cursor })
  })
}

test('legacy full page is visibly potentially truncated, unlike an exhausted page', async ({ page }) => {
  let legacy = true
  await page.route('**/api/memory/candidates?**', async (route) => {
    await route.fulfill({ json: { candidates: candidateRows(1, 100), count: 100, ...(legacy ? {} : { next_cursor: null }) } })
  })
  await page.goto('/queue?project=all')
  await expect(page.getByTestId('queue-row-1')).toBeVisible()
  const truncation = page.getByText(/(?:неполн|ограничен|усеч|truncat|may be more|more.*available)/i)
  await expect(truncation.first()).toBeVisible()
  legacy = false
  await page.getByRole('button', { name: 'Обновить', exact: true }).click()
  await expect(page.getByTestId('queue-row-1')).toBeVisible()
  await expect(truncation).toHaveCount(0)
})

test('legacy full response keeps all 100 loaded rows reachable by local paging', async ({ page }) => {
  await page.route('**/api/memory/candidates?**', (route) => {
    const limit = Number(new URL(route.request().url()).searchParams.get('limit') || 20)
    const candidates = candidateRows(1, Math.min(100, limit))
    return route.fulfill({ json: { candidates, count: candidates.length } })
  })
  await page.goto('/queue?project=all')
  await expect(page.getByTestId('queue-row-1')).toBeVisible()
  await page.locator('#queue-page-size').selectOption('10')
  await page.getByRole('button', { name: 'Перейти на следующую страницу' }).click()
  await expect(page.getByTestId('queue-row-11')).toBeVisible()
  for (let pageNumber = 2; pageNumber < 10; pageNumber++) {
    await page.getByRole('button', { name: 'Перейти на следующую страницу' }).click()
  }
  await expect(page.getByTestId('queue-row-100')).toBeVisible()
  await expect(page.getByText(/(?:неполн|ограничен|усеч|truncat|may be more|more.*available)/i).first()).toBeVisible()
})

test('null continuation exhausts the loaded page', async ({ page }) => {
  await page.route('**/api/memory/candidates?**', (route) => route.fulfill({
    json: { candidates: candidateRows(1, 2), count: 2, next_cursor: null },
  }))
  await page.goto('/queue?project=all')
  await expect(page.getByTestId('queue-row-1')).toBeVisible()
  await page.locator('#queue-page-size').selectOption('all')
  await expect(page.getByRole('button', { name: 'Перейти на следующую страницу' })).toBeDisabled()
})

test('loaded counts and all-rows option do not claim a corpus total', async ({ page }) => {
  await page.route('**/api/memory/candidates?**', (route) => route.fulfill({
    json: { candidates: candidateRows(1, 100), count: 100, next_cursor: 'next-100' },
  }))
  await page.goto('/queue?project=all')
  await expect(page.getByTestId('queue-row-1')).toBeVisible()
  const scope = /страниц|загруж|page|loaded|неизвест|unknown|—/i
  await expect(page.locator('.queue-brief .metric')).toHaveCount(3)
  for (const metric of await page.locator('.queue-brief .metric').all()) {
    await expect(metric).toContainText(scope)
  }
  await expect(page.locator('.ops-left .cnt')).toContainText(scope)
  await expect(page.locator('.ops-right .cnt')).toContainText(scope)
  await expect(page.locator('#queue-page-size option[value="all"]')).toContainText(scope)
})

for (const malformed of ['', 0, {}, [], false]) {
  test(`candidate queue reports malformed next_cursor ${JSON.stringify(malformed)} as an error`, async ({ page }) => {
    await page.route('**/api/memory/candidates?**', (route) => route.fulfill({
      json: { candidates: candidateRows(1, 1), count: 1, next_cursor: malformed },
    }))
    await page.goto('/queue?project=all')
    await expect(page.locator('.statebar[data-state="error"]')).toBeVisible()
    await expect(page.locator('.statebar[data-state="error"]')).toContainText(/unreadable response|нечитаемый ответ|无法读取的响应/i)
    await expect(page.getByTestId('queue-row-1')).toHaveCount(0)
  })
}

test('select all is visibly page-scoped', async ({ page }) => {
  await page.route('**/api/memory/candidates?**', (route) => route.fulfill({
    json: { candidates: candidateRows(1, 25), count: 25, next_cursor: 'next-100' },
  }))
  await page.goto('/queue?project=all')
  await expect(page.getByTestId('queue-row-1')).toBeVisible()
  await page.locator('#queue-page-size').selectOption('10')
  await expect(page.getByTestId('queue-row-1')).toBeVisible()
  const selectAll = page.getByRole('button', { name: /выделить/i })
  await expect(selectAll).toHaveAccessibleName(/страниц|видим|page|visible/i)
  await selectAll.click()
  await expect(page.getByTestId('queue-row-1').locator('[aria-pressed]')).toHaveAttribute('aria-pressed', 'true')
  await expect(page.getByTestId('queue-row-10').locator('[aria-pressed]')).toHaveAttribute('aria-pressed', 'true')
  await page.getByRole('button', { name: 'Перейти на следующую страницу' }).click()
  await expect(page.getByTestId('queue-row-11').locator('[aria-pressed]')).toHaveAttribute('aria-pressed', 'false')
})

test('selection is cleared between first, middle, last and refreshed local pages', async ({ page }) => {
  await page.route('**/api/memory/candidates?**', (route) => route.fulfill({
    json: { candidates: candidateRows(1, 25), count: 25, next_cursor: null },
  }))
  await page.goto('/queue?project=all')
  await expect(page.getByTestId('queue-row-1')).toBeVisible()
  await page.locator('#queue-page-size').selectOption('10')
  await expect(page.getByTestId('queue-row-1')).toBeVisible()
  const select = page.getByRole('button', { name: /выделить/i })
  await select.click()
  await expect(page.getByTestId('queue-row-10').locator('[aria-pressed]')).toHaveAttribute('aria-pressed', 'true')
  await page.getByRole('button', { name: 'Перейти на следующую страницу' }).click()
  await expect(page.getByTestId('queue-row-11').locator('[aria-pressed]')).toHaveAttribute('aria-pressed', 'false')
  await select.click()
  await expect(page.getByTestId('queue-row-20').locator('[aria-pressed]')).toHaveAttribute('aria-pressed', 'true')
  await page.getByRole('button', { name: 'Перейти на следующую страницу' }).click()
  await expect(page.getByTestId('queue-row-21').locator('[aria-pressed]')).toHaveAttribute('aria-pressed', 'false')
  await select.click()
  await expect(page.getByTestId('queue-row-25').locator('[aria-pressed]')).toHaveAttribute('aria-pressed', 'true')
  await page.getByRole('button', { name: 'Обновить', exact: true }).click()
  await expect(page.getByTestId('queue-row-1').locator('[aria-pressed]')).toHaveAttribute('aria-pressed', 'false')
})

test('row action after continuation submits its clicked ID without the navigation cursor', async ({ page }) => {
  const selections: unknown[] = []
  const operations: unknown[] = []
  await page.route('**/api/memory/candidates?**', (route) => {
    const cursor = new URL(route.request().url()).searchParams.get('cursor')
    return route.fulfill({
      json: cursor
        ? { candidates: candidateRows(101, 2), count: 2, next_cursor: null }
        : { candidates: candidateRows(1, 100), count: 100, next_cursor: 'next-100' }
    })
  })
  await page.route('**/api/collections/selection', async (route) => {
    selections.push(route.request().postDataJSON())
    await route.fulfill({ json: { selection: { domain: 'queue', kind: 'explicit', selection_version: 1, targets: [{ id: '101' }] } } })
  })
  await page.route('**/api/memory/candidates/operations', (route) => {
    operations.push(route.request().postDataJSON())
    return route.fulfill({ status: 503, json: { error: 'synthetic refusal' } })
  })
  await page.goto('/queue?project=all')
  await expect(page.getByTestId('queue-row-1')).toBeVisible()
  await page.locator('#queue-page-size').selectOption('all')
  await expect(page.getByTestId('queue-row-100')).toBeVisible()
  await page.getByRole('button', { name: 'Перейти на следующую страницу' }).click()
  await expect(page.getByTestId('queue-row-101')).toBeVisible()
  await page.getByTestId('queue-row-102').locator('[aria-pressed]').click()
  await page.getByTestId('queue-action-reject-101').click()
  await page.getByTestId('queue-action-reject-101').click()
  await expect.poll(() => selections.length).toBe(1)
  expect(selections[0]).toEqual({ domain: 'queue', selection: { kind: 'explicit', targets: [{ id: '101' }] } })
  await expect.poll(() => operations.length).toBe(1)
  expect(operations[0]).toEqual({
    request_id: expect.any(String),
    action: 'reject',
    selection: { kind: 'explicit', selection_version: 1 },
    reason: 'operator rejected candidate',
  })
})

test('row action submits only its clicked visible ID as an explicit selection', async ({ page }) => {
  const selections: unknown[] = []
  await page.route('**/api/memory/candidates?**', (route) => route.fulfill({
    json: { candidates: candidateRows(1, 25), count: 25, next_cursor: 'next-100' },
  }))
  await page.route('**/api/collections/selection', async (route) => {
    selections.push(route.request().postDataJSON())
    await route.fulfill({ json: { selection: { domain: 'queue', kind: 'explicit', selection_version: 1, targets: [{ id: '1' }] } } })
  })
  await page.route('**/api/memory/candidates/operations', (route) => route.fulfill({ status: 503, json: { error: 'synthetic refusal' } }))
  await page.goto('/queue?project=all')
  await expect(page.getByTestId('queue-row-1')).toBeVisible()
  await page.getByTestId('queue-row-2').locator('[aria-pressed]').click()
  await page.getByTestId('queue-action-reject-1').click()
  await page.getByTestId('queue-action-reject-1').click()
  await expect.poll(() => selections.length).toBe(1)
  expect(selections[0]).toEqual({ domain: 'queue', selection: { kind: 'explicit', targets: [{ id: '1' }] } })
})

for (const status of [200, 503]) {
  test(`late continuation ${status} from the previous project cannot replace current rows or selection`, async ({ page }) => {
    let release!: () => void
    let deliveredResponse!: () => void
    const delayed = new Promise<void>((resolve) => { release = resolve })
    const delivered = new Promise<void>((resolve) => { deliveredResponse = resolve })
    let continuationRequested = false
    await page.route('**/api/memory/candidates?**', async (route) => {
      const url = new URL(route.request().url())
      if (url.searchParams.get('cursor') === 'next-100') {
        continuationRequested = true
        await delayed
        try {
          await route.fulfill({ status, json: status === 200 ? { candidates: candidateRows(101, 1), count: 1, next_cursor: null } : { error: 'synthetic failure' } })
        } finally {
          deliveredResponse()
        }
      } else if (url.searchParams.get('project') === 'all') {
        await route.fulfill({ json: { candidates: candidateRows(1, 100), count: 100, next_cursor: 'next-100' } })
      } else {
        await route.fulfill({ json: { candidates: candidateRows(501, 1), count: 1, next_cursor: null } })
      }
    })
    try {
      await page.goto('/queue?project=all')
      await expect(page.getByTestId('queue-row-1')).toBeVisible()
      await page.locator('#queue-page-size').selectOption('all')
      await page.getByRole('button', { name: 'Перейти на следующую страницу' }).click()
      await expect.poll(() => continuationRequested).toBe(true)
      const projectSelect = page.getByTestId('project-context-select')
      const project = await projectSelect.locator('option').evaluateAll((options) => options.map((option) => (option as HTMLOptionElement).value).find((value) => value && value !== 'all'))
      expect(project).toBeTruthy()
      await projectSelect.selectOption(project!)
      await expect(page.getByTestId('queue-row-501')).toBeVisible()
      await page.getByTestId('queue-row-501').locator('[aria-pressed]').click()
      release()
      await delivered
      await page.evaluate(() => new Promise<void>((resolve) => requestAnimationFrame(() => requestAnimationFrame(() => resolve()))))
      await expect(page.getByTestId('queue-row-501').locator('[aria-pressed]')).toHaveAttribute('aria-pressed', 'true')
      await expect(page.getByTestId('queue-row-101')).toHaveCount(0)
      await expect(page.locator('.statebar[data-state="error"]')).toHaveCount(0)
    } finally {
      release()
    }
  })
}

for (const destination of ['project', 'leave-queue'] as const) {
  test(`late 401 after ${destination} does not lock the current authorized screen`, async ({ page }, testInfo) => {
    let release!: () => void
    let settled!: () => void
    const delayed = new Promise<void>((resolve) => { release = resolve })
    const routeSettled = new Promise<void>((resolve) => { settled = resolve })
    let continuationRequested = false
    let browserOutcome = ''
    const isOldContinuation = (url: string) => new URL(url).searchParams.get('cursor') === 'old-scope-cursor'
    page.on('response', (response) => {
      if (isOldContinuation(response.url())) browserOutcome = `response:${response.status()}`
    })
    page.on('requestfailed', (request) => {
      if (isOldContinuation(request.url())) browserOutcome = `requestfailed:${request.failure()?.errorText || 'unknown'}`
    })
    await page.route('**/api/auth/me', (route) => route.fulfill({ json: { authenticated: true, auth_disabled: false } }))
    await page.route('**/api/memory/candidates?**', async (route) => {
      const url = new URL(route.request().url())
      if (url.searchParams.get('cursor') === 'old-scope-cursor') {
        continuationRequested = true
        await delayed
        try {
          await route.fulfill({ status: 401, json: { error: 'expired old request' } }).catch(() => { })
        } finally {
          settled()
        }
      } else if (url.searchParams.get('project') === 'all') {
        await route.fulfill({ json: { candidates: candidateRows(1, 100), count: 100, next_cursor: 'old-scope-cursor' } })
      } else {
        await route.fulfill({ json: { candidates: candidateRows(501, 1), count: 1, next_cursor: null } })
      }
    })
    try {
      await page.goto('/queue?project=all')
      await expect(page.locator('.identity')).toHaveAttribute('data-auth', 'authenticated')
      await expect(page.getByTestId('queue-row-1')).toBeVisible()
      await page.locator('#queue-page-size').selectOption('all')
      await expect(page.getByTestId('queue-row-100')).toBeVisible()
      await page.getByRole('button', { name: 'Перейти на следующую страницу' }).click()
      await expect.poll(() => continuationRequested).toBe(true)
      if (destination === 'project') {
        const projectSelect = page.getByTestId('project-context-select')
        const project = await projectSelect.locator('option').evaluateAll((options) => options.map((option) => (option as HTMLOptionElement).value).find((value) => value && value !== 'all'))
        expect(project).toBeTruthy()
        await projectSelect.selectOption(project!)
        await expect(page.getByTestId('queue-row-501')).toBeVisible()
        await page.getByTestId('queue-row-501').locator('[aria-pressed]').click()
      } else {
        await page.locator('a[href^="/memory"]').first().click()
        await expect(page).toHaveURL(/\/memory/)
        await expect(page.locator('h1').first()).toBeVisible()
      }
      release()
      await routeSettled
      await expect.poll(() => browserOutcome).toMatch(/^(response:401|requestfailed:)/)
      testInfo.annotations.push({ type: 'old-continuation-outcome', description: browserOutcome })
      await page.waitForTimeout(100)
      await expect(page.locator('.identity')).toHaveAttribute('data-auth', 'authenticated')
      if (destination === 'project') {
        await expect(page.getByTestId('queue-row-501').locator('[aria-pressed]')).toHaveAttribute('aria-pressed', 'true')
        await expect(page.locator('.statebar[data-state="error"]')).toHaveCount(0)
      } else {
        await expect(page).toHaveURL(/\/memory/)
        await expect(page.locator('h1').first()).toBeVisible()
      }
    } finally {
      release()
    }
  })
}

test('session expiry locks the queue without automatically retrying flags or publishing a late page', async ({ page }) => {
  let expired = false
  let expiredFlags = 0
  let continuationRequested = false
  let release!: () => void
  let delivered!: () => void
  const delayed = new Promise<void>((resolve) => { release = resolve })
  const completed = new Promise<void>((resolve) => { delivered = resolve })
  await page.route('**/api/auth/me', (route) => route.fulfill({ json: { authenticated: true, auth_disabled: false } }))
  await page.route('**/api/flags', (route) => {
    if (!expired) return route.fulfill({ json: { flags: { ENGRAM_VNEXT_F_ENABLED: true } } })
    expiredFlags++
    return route.fulfill({ status: expiredFlags <= 3 ? 401 : 503, json: { error: 'session expired' } })
  })
  await page.route('**/api/memory/candidates?**', async (route) => {
    if (new URL(route.request().url()).searchParams.has('cursor')) {
      continuationRequested = true
      await delayed
      try {
        await route.fulfill({ json: { candidates: candidateRows(101, 1), count: 1, next_cursor: null } }).catch(() => { })
      } finally {
        delivered()
      }
      return
    }
    await route.fulfill({ json: { candidates: candidateRows(1, 100), count: 100, next_cursor: 'next-100' } })
  })
  try {
    await page.goto('/queue?project=all')
    await expect(page.getByTestId('queue-row-1')).toBeVisible()
    await page.locator('#queue-page-size').selectOption('all')
    await expect(page.getByTestId('queue-row-100')).toBeVisible()
    await page.getByRole('button', { name: 'Перейти на следующую страницу' }).click()
    await expect.poll(() => continuationRequested).toBe(true)
    expired = true
    await page.getByRole('button', { name: 'Обновить', exact: true }).click()
    await expect(page.locator('.identity')).toHaveAttribute('data-auth', 'locked')
    release()
    await completed
    await page.waitForTimeout(500)
    await expect(page.getByTestId('queue-row-1')).toHaveCount(0)
    await expect(page.getByTestId('queue-row-101')).toHaveCount(0)
    expect(expiredFlags).toBe(1)
  } finally {
    release()
  }
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
