import { expect, test } from '@playwright/test'
import type { Page } from '@playwright/test'
import { appendBrowserTraffic, readLiveFixture } from './fixture-bootstrap'
import type { RouteTraffic } from './fixture-bootstrap'

type RuleSeed = {
  content: string
  priority: number
  project?: string
}

type BrowserResponse = {
  status: number
  text: string
}

async function requestJSON(page: Page, path: string, method: 'GET' | 'POST' | 'PATCH', body?: unknown): Promise<BrowserResponse> {
  return page.evaluate(async ({ requestBody, requestMethod, requestPath }) => {
    const response = await fetch(requestPath, {
      method: requestMethod,
      headers: requestBody === undefined ? undefined : { 'Content-Type': 'application/json' },
      body: requestBody === undefined ? undefined : JSON.stringify(requestBody),
    })
    return { status: response.status, text: await response.text() }
  }, { requestBody: body, requestMethod: method, requestPath: path })
}

async function seedRules(page: Page, rows: RuleSeed[]): Promise<void> {
  for (let offset = 0; offset < rows.length; offset += 20) {
    const batch = rows.slice(offset, offset + 20)
    const statuses = await page.evaluate(async (rules) => Promise.all(rules.map(async (rule) => {
      const response = await fetch('/api/rules', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ ...rule, edited_by: 't026-live-fixture' }),
      })
      return response.status
    })), batch)
    expect(statuses).toEqual(batch.map(() => 201))
  }
}

test('T026 live Rules UI retains safety state and server-authorized outcomes', async ({ page }, testInfo) => {
  const fixture = await readLiveFixture()
  const traffic: RouteTraffic[] = []
  const requestBodies: Array<{ method: string; path: string; body: string | null }> = []
  let reorderBody: string | null | undefined
  const marker = `t026-${fixture.fixtureId}`
  const bulkProject = `${marker}-bulk`
  const globalRules = [
    { content: `${marker} global first`, priority: 100_002 },
    { content: `${marker} global second`, priority: 100_001 },
  ]

  page.on('request', (request) => {
    const url = new URL(request.url())
    if (url.origin !== fixture.frontend.baseUrl || !url.pathname.startsWith('/api/')) return
    if (url.pathname === '/api/rules') {
      requestBodies.push({ method: request.method(), path: url.pathname, body: request.postData() })
    }
  })
  page.on('response', (response) => {
    const url = new URL(response.url())
    if (url.origin === fixture.frontend.baseUrl && url.pathname.startsWith('/api/')) {
      traffic.push({
        origin: 'browser',
        method: response.request().method(),
        path: url.pathname,
        status: response.status(),
        at: new Date().toISOString(),
      })
    }
  })

  try {
    expect(fixture.candidate.commit).toBe(fixture.backend.sourceCommit)
    expect(fixture.mock.prohibited).toBe(true)

    const shell = await page.goto(`${fixture.frontend.baseUrl}/rules`, { waitUntil: 'domcontentloaded' })
    expect(shell?.status()).toBe(200)
    expect((await requestJSON(page, '/api/auth/user-login', 'POST', fixture.browserCredential)).status).toBe(200)

    await seedRules(page, [
      ...globalRules,
      ...Array.from({ length: 201 }, (_, index) => ({
        project: bulkProject,
        content: `${marker} paged rule ${index + 1}`,
        priority: 50_000 - index,
      })),
    ])

    await page.reload({ waitUntil: 'networkidle' })
    await page.getByTestId('rules-selection-load-page').click()
    await expect(page.getByTestId('rules-selection-page-info')).toContainText('203')
    await expect(page.getByTestId('rules-selection-page-info')).toContainText(/ещё результаты|More results|更多结果/)

    await page.getByTestId('rules-selection-page').click()
    await expect(page.getByTestId('rules-selection-kind')).toContainText(/Текущая страница|Current page|当前页/)
    await expect(page.getByTestId('rules-selection-version')).toContainText(/v\d+/)

    const pageHeader = page.getByTestId('rules-page-selection')
    await expect(pageHeader).toHaveAttribute('aria-checked', 'true')
    await page.locator('.rule-check').first().click()
    await expect(pageHeader).toHaveAttribute('aria-checked', 'mixed')
    await expect(pageHeader).toHaveJSProperty('indeterminate', true)
    await pageHeader.focus()
    await page.keyboard.press('Space')
    await expect(pageHeader).toHaveAttribute('aria-checked', 'true')
    await expect(pageHeader).toHaveJSProperty('indeterminate', false)

    await page.getByTestId('rules-selection-freeze').click()
    await expect(page.getByTestId('rules-selection-kind')).toContainText(/Замороженный фильтр|Frozen filter|冻结/)
    await expect(page.getByTestId('rules-selection-frozen-info')).toContainText('203')
    await page.locator('.rule-check').first().click()
    await expect(page.getByTestId('rules-selection-frozen-info')).toContainText('202')
    const frozenInfo = page.getByTestId('rules-selection-frozen-info')
    const disableSelected = page.getByRole('button', { name: /Выключить выбранные|Disable selected|禁用已选规则/ })


    await page.locator('.scope-filter select').selectOption('global')
    await expect(page.getByTestId('rules-selection-kind')).toContainText(/Замороженный фильтр|Frozen filter|冻结/)
    await expect(frozenInfo).toContainText('202')
    await expect(page.getByTestId('rules-selection-reconfirm')).toContainText(/filter_changed/)
    await expect(disableSelected).toBeDisabled()
    await page.getByTestId('rules-selection-load-page').click()
    await expect(page.getByTestId('rules-selection-page-info')).toContainText('2')
    await page.getByTestId('rules-selection-page').click()

    const firstEdit = page.getByRole('button', { name: /Править|Edit|编辑/ }).first()
    await firstEdit.click()
    await expect(page.getByText(/Нет изменений для применения|No changes to apply|没有可应用的更改/)).toBeVisible()
    const editor = page.locator('.rule-editor textarea')
    await editor.fill(`${marker} field changed by selection`)
    await page.getByRole('button', { name: /Сохранить|Save|保存/ }).click()
    await expect(page.getByTestId('rules-operation-readbacks')).toContainText(/включено|enabled|已启用/)
    await expect(page.getByTestId('rules-operation-readbacks')).toContainText(/v\d+/)

    await page.getByTestId('rules-selection-load-page').click()
    await page.getByTestId('rules-selection-page').click()
    const firstRuleID = await page.locator('.rule-row').first().evaluate((row) => {
      const label = row.querySelector('.rule-check')?.getAttribute('aria-label') || ''
      const match = label.match(/(\d+)/)
      return match?.[1] || ''
    })
    expect(firstRuleID).not.toBe('')
    expect((await requestJSON(page, `/api/rules/${firstRuleID}`, 'PATCH', { content: `${marker} externally changed` })).status).toBe(200)

    await page.getByRole('button', { name: /Выключить выбранные|Disable selected|禁用已选规则/ }).click()
    await expect(page.getByTestId('mutation-result')).toHaveAttribute('data-kind', 'conflict')
    await expect(page.getByTestId('rules-selection-kind')).toContainText(/Текущая страница|Current page|当前页/)

    await page.getByTestId('rules-selection-load-page').click()
    await page.getByTestId('rules-selection-page').click()
    await page.getByRole('button', { name: /Выключить выбранные|Disable selected|禁用已选规则/ }).click()
    await expect(page.getByTestId('rules-operation-readbacks')).toContainText(/выключено|disabled|已禁用/)

    await page.getByTestId('rules-selection-load-page').click()
    await page.getByTestId('rules-selection-page').click()
    const firstGrip = page.locator('.rule-grip').first()
    await expect(firstGrip).toBeEnabled()
    await firstGrip.focus()
    await page.keyboard.press('ArrowDown')
    await expect(page.getByTestId('rules-operation-readbacks')).toHaveCount(1)

    const mutationRequests = requestBodies.filter((request) => request.method === 'POST' && request.path === '/api/rules')
    reorderBody = mutationRequests.find((request) => request.body?.includes('"action":"reorder"'))?.body
    expect(reorderBody).toEqual(expect.stringContaining('"action":"reorder"'))
    expect(reorderBody).toEqual(expect.stringContaining('"selection_version":'))
    expect(requestBodies.some((request) => request.method === 'PATCH')).toBe(false)

    const deleteButton = page.getByRole('button', { name: /Удалить|Delete|删除/ }).first()
    await deleteButton.click()
    await page.getByRole('button', { name: /Подтвердить|Confirm|确认/ }).click()
    await expect(page.getByTestId('rules-operation-readbacks')).toContainText(/отсутствие|authorized absence|授权不存在/)
    await expect(page.getByTestId('rules-operation-readbacks')).not.toContainText(marker)
    expect(traffic.filter((entry) => entry.path.toLowerCase().includes('book'))).toEqual([])
  } finally {
    const state = await appendBrowserTraffic(traffic)
    await testInfo.attach('t026-rules-selection-safety-live', {
      contentType: 'application/json',
      body: Buffer.from(JSON.stringify({
        evidenceKind: 'real-authenticated-go-postgresql-browser',
        candidate: state.candidate,
        backend: { sourceCommit: state.backend.sourceCommit, binarySha256: state.backend.binarySha256 },
        frontend: { buildEntry: state.frontend.buildEntry, buildEntrySha256: state.frontend.buildEntrySha256 },
        assertedSafetyBranches: [
          'candidate-bound backend identity',
          'page selection over two hundred rules',
          'frozen selection exclusion and retained filter-change reconfirmation',
          'stale selected-rule version conflict retaining UI selection',
          'authorized current-state and destructive-absence readbacks',
          'selection-version-bearing reorder request',
          'no Book endpoint traffic',
        ],
        reorderRequestBody: reorderBody,
        bookTraffic: state.traffic.filter((entry) => entry.path.toLowerCase().includes('book')),
        traffic: state.traffic,
      }, null, 2)),
    })
  }
})
