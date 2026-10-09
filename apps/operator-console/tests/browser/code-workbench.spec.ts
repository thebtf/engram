import { expect, test } from '@playwright/test'
import type { Page } from '@playwright/test'

const binding = { state: 'TAB_BINDING_READY', tab_binding_id: '60000000-0000-4000-8000-000000000041', document_proof: 'proof', resume_nonce: 'resume', reload_token: 'reload' }
const span = { byte_start: 0, byte_end: 12, line_start: 1, line_end: 1 }

async function workbench(page: Page) {
  let selected = 'a'
  let catalogCopies = ['a', 'b']
  const intent = { intent_ref: 'intent-a', state: 'queued', attempt: 1, retryable: false, created_at: '2026-10-09T00:00:00Z', updated_at: '2026-10-09T00:00:00Z' }
  let failure: { path: string; status: number } | null = null
  let releaseSource: (() => void) | null = null
  let holdSource = false
  let sourceHeld = false
  const reads: Array<{ path: string; view: string; body: Record<string, unknown> }> = []
  await page.route('**/api/auth/me', route => route.fulfill({ json: { auth_disabled: true } }))
  await page.route('**/api/code/**', async route => {
    const path = new URL(route.request().url()).pathname
    if (failure?.path === path) return route.fulfill({ status: failure.status })
    if (path.endsWith('/tabs/handshake') || path.endsWith('/tabs/resume')) return route.fulfill({ json: { ...binding, state: route.request().postDataJSON().ambiguous ? 'TAB_BOOTSTRAP_AMBIGUOUS' : 'TAB_BINDING_READY' } })
    if (path.endsWith('/contexts')) return route.fulfill({ json: { contexts: catalogCopies.map(copy => ({ source_ref: 'repository', checkout_ref: copy, repository: 'Repository', working_copy: `Copy ${copy}`, indexed_snapshot: { label: `Snapshot ${copy}` }, view_ref: `view-${copy}`, selection_ref: copy, index_intent_available: false })) } })
    if (path.endsWith('/context')) { selected = route.request().postDataJSON().selection_ref; return route.fulfill({ status: 204 }) }
    if (path.endsWith('/status')) return route.fulfill({ json: { total_chunks: 1, embedded_chunks: 1, embedding: { coverage: 'complete' }, freshness: { state: 'observed_current' } } })
    if (path.endsWith('/index-intents')) return route.fulfill({ status: 202, json: intent })
    if (path.endsWith('/index-intents/intent-a')) return route.fulfill({ json: intent })
    if (['structure', 'search', 'graph', 'source'].some(action => path.endsWith(`/${action}`))) {
      const copy = selected
      const context = { source_id: 'source', checkout_id: copy, view_id: `view-${copy}`, profile_id: 'profile', generation: 1 }
      const ref = { source_id: context.source_id, view_id: context.view_id, entity_key: `implementation-${copy}` }
      const descriptor = { entity_key: ref.entity_key, span, content_digest: `digest-${copy}` }
      const item = { ref, path: `src/${copy}.ts`, span, content_digest: descriptor.content_digest, kind: 'function', language: 'typescript', excerpt: `${copy}()`, match_sources: ['lexical'], score: 1 }
      const node = { entity: ref, context_ref: { ...context, analysis_profile_id: context.profile_id }, source_state: 'available', source_read: descriptor }
      reads.push({ path, view: context.view_id, body: route.request().postDataJSON() })
      if (path.endsWith('/source') && holdSource) await new Promise<void>(resolve => { sourceHeld = true; releaseSource = resolve })
      return route.fulfill({ json: { schema: 'engram.code-query/1', status: 'ok', contexts: [context], items: [item], warnings: [], retrieval: { mode: 'lexical' }, freshness: { state: 'observed_current' }, coverage: {}, truncated: false, ...(path.endsWith('/graph') ? { graph: { nodes: [ref], edges: [], stop_reason: 'complete' }, navigation: { nodes: [node], edges: [] } } : {}) } })
    }
    return route.fulfill({ status: 204 })
  })
  await page.goto('/code')
  await page.getByTestId('code-context-working-copy').selectOption('a')
  await page.getByTestId('code-context-snapshot').selectOption('a')
  await page.getByTestId('code-pin-context').press('Enter')
  await expect(page.getByTestId('code-structure-results')).toBeVisible()
  await page.getByTestId('code-query-input').fill('implementation')
  await page.getByTestId('code-query-input').press('Enter')
  await expect(page.getByTestId('code-search-results')).toBeVisible()
  return {
    reads,
    fail(path: string, status: number) { failure = { path, status } },
    publishOnlyB() { catalogCopies = ['b'] },
    recover() { failure = null; holdSource = false },
    hold() { holdSource = true },
    held() { return sourceHeld },
    release() { releaseSource?.() },
  }
}

test('keyboard mobile result → relation → same-View source → back stays reachable without page overflow', async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 844 })
  const state = await workbench(page)
  await expect(page.locator('.structure-panel')).toHaveCount(0)
  await page.getByTestId('code-search-explore').press('Enter')
  await expect(page.getByTestId('code-graph-heading')).toBeFocused()
  await page.getByTestId('code-graph-node').press('Space')
  await page.getByTestId('code-graph-source').press('Enter')
  await expect(page.locator('#code-work-source h3')).toBeFocused()
  await expect(page.getByTestId('code-source-result')).toHaveText('a()')
  expect(state.reads.filter(read => /\/(search|graph|source)$/.test(read.path)).map(read => read.view)).toEqual(['view-a', 'view-a', 'view-a'])
  expect(state.reads.find(read => read.path.endsWith('/source'))?.body).toMatchObject({ entity_key: 'implementation-a', content_digest: 'digest-a', span })
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true)
  await page.keyboard.press('Escape')
  await expect(page.getByTestId('code-graph-heading')).toBeFocused()
  await page.getByTestId('code-panel-back').press('Enter')
  await expect(page.locator('#code-work-results h3')).toBeFocused()
  await expect(page.getByTestId('code-query-input')).toHaveValue('implementation')
  await expect(page.getByTestId('code-search-results')).toBeVisible()
})

test('a working-copy switch atomically conceals results, relations and pending source, including its late response', async ({ page }) => {
  const state = await workbench(page)
  await page.getByTestId('code-search-explore').click()
  await expect(page.getByTestId('code-graph-results')).toBeVisible()
  state.hold()
  await page.getByTestId('code-search-source').click()
  await expect.poll(state.held).toBe(true)
  await page.locator('a[href="/memory"]').first().click()
  await expect(page).toHaveURL(/\/memory$/)
  await page.locator('a[href="/code"]').first().click()
  await expect(page).toHaveURL(/\/code$/)
  await expect(page.getByTestId('code-context-pinned')).toContainText('Snapshot a')
  await page.getByTestId('code-context-working-copy').selectOption('b')
  await expect(page.getByTestId('code-context-pinned')).toHaveCount(0)
  for (const id of ['code-search-results', 'code-graph-results', 'code-source-result', 'code-selected-result']) await expect(page.getByTestId(id)).toHaveCount(0)
  await page.getByTestId('code-context-snapshot').selectOption('b')
  await page.getByTestId('code-pin-context').click()
  await expect(page.getByTestId('code-structure-results')).toContainText('src/b.ts')
  const response = page.waitForResponse(response => new URL(response.url()).pathname === '/api/code/source')
  state.release()
  await (await response).finished()
  await expect(page.getByTestId('code-source-result')).toHaveCount(0)
  await expect(page.locator('.result-grid')).not.toContainText('a()')
})

test('a rejected switch restores historical A authority and its intent without exposing A bodies under B selection', async ({ page }) => {
  const state = await workbench(page)
  await page.getByTestId('code-search-explore').click()
  await expect(page.getByTestId('code-graph-results')).toBeVisible()
  await page.getByTestId('code-search-source').click()
  await expect(page.getByTestId('code-source-result')).toHaveText('a()')
  await page.getByTestId('index-intent-reindex').click()
  await expect(page.getByTestId('index-intent-state')).toHaveAttribute('data-state', 'queued')
  const saved = await page.evaluate(() => ({ pin: sessionStorage.getItem('engram.operator-code.view-candidate.v2'), intent: sessionStorage.getItem('engram.operator-code.index-intent.v1') }))
  state.publishOnlyB()
  await page.locator('.context-picker .actions button').first().click()
  await expect(page.getByTestId('code-context-snapshot').locator('option[value="a"]')).toHaveCount(0)
  await page.getByTestId('code-context-working-copy').selectOption('b')
  await page.getByTestId('code-context-snapshot').selectOption('b')
  for (const id of ['code-context-pinned', 'code-search-results', 'code-graph-results', 'code-source-result', 'index-intent-status']) await expect(page.getByTestId(id)).toHaveCount(0)
  expect(await page.evaluate(() => ({ pin: sessionStorage.getItem('engram.operator-code.view-candidate.v2'), intent: sessionStorage.getItem('engram.operator-code.index-intent.v1') }))).toEqual(saved)
  state.fail(`/api/code/tabs/${binding.tab_binding_id}/context`, 403)
  await page.getByTestId('code-pin-context').click()
  await expect(page.locator('main.code-page')).toHaveAttribute('data-catalog-state', 'switch-rejected')
  await expect(page.getByTestId('code-context-pinned')).toContainText('Snapshot a')
  await expect(page.getByTestId('code-context-working-copy')).not.toHaveValue('b')
  await expect(page.getByTestId('code-structure-results')).toContainText('src/a.ts')
  await expect(page.getByTestId('code-source-result')).toHaveCount(0)
  await expect(page.getByTestId('index-intent-state')).toHaveAttribute('data-state', 'queued')
  expect(await page.evaluate(() => ({ pin: sessionStorage.getItem('engram.operator-code.view-candidate.v2'), intent: sessionStorage.getItem('engram.operator-code.index-intent.v1') }))).toEqual(saved)
  await page.locator('a[href="/memory"]').first().click()
  await expect(page).toHaveURL(/\/memory$/)
  await page.locator('a[href="/code"]').first().click()
  await expect(page).toHaveURL(/\/code$/)
  await expect(page.getByTestId('code-context-pinned')).toContainText('Snapshot a')
  await expect(page.getByTestId('code-structure-results')).toContainText('src/a.ts')
  state.recover()
  await page.getByTestId('code-context-working-copy').selectOption('b')
  await page.getByTestId('code-context-snapshot').selectOption('b')
  await page.getByTestId('code-pin-context').click()
  await expect(page.getByTestId('code-context-pinned')).toContainText('Snapshot b')
  await expect(page.getByTestId('code-structure-results')).toContainText('src/b.ts')
  expect(await page.evaluate(() => sessionStorage.getItem('engram.operator-code.index-intent.v1'))).toBeNull()
})

test('actual-current409 clears its authority and requires explicit catalogue selection and confirmation', async ({ page }) => {
  const state = await workbench(page)
  await page.getByTestId('code-search-source').click()
  await expect(page.getByTestId('code-source-result')).toBeVisible()
  state.fail('/api/code/source', 409)
  await page.getByTestId('code-search-source').click()
  await expect(page.locator('main.code-page')).toHaveAttribute('data-catalog-state', 'snapshot-rejected')
  await expect(page.getByTestId('code-context-pinned')).toHaveCount(0)
  await expect(page.getByTestId('code-release-state')).toHaveAttribute('data-state', 'unselected')
  await expect(page.locator('.result-grid')).toHaveCount(0)
  await expect(page.getByTestId('code-pin-context')).toBeDisabled()
  expect(await page.evaluate(() => sessionStorage.getItem('engram.operator-code.view-candidate.v2'))).toBeNull()
  state.recover()
  await page.locator('.context-picker .actions button').first().click()
  await page.getByTestId('code-context-working-copy').selectOption('b')
  await page.getByTestId('code-context-snapshot').selectOption('b')
  await expect(page.getByTestId('code-context-pinned')).toHaveCount(0)
  await page.getByTestId('code-pin-context').click()
  await expect(page.getByTestId('code-structure-results')).toContainText('src/b.ts')
  await expect(page.getByTestId('code-source-result')).toHaveCount(0)
})
