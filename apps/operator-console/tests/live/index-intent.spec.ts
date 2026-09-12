import { writeFile } from 'node:fs/promises'
import { expect, test } from '@playwright/test'
import type { Page } from '@playwright/test'
import { appendBrowserTraffic, readLiveFixture } from './fixture-bootstrap'
import type { LiveFixtureState, RouteTraffic } from './fixture-bootstrap'

type CatalogEntry = {
  source: { label: string }
  checkout: { label: string }
  view: null
  indexIntentAvailable: boolean
  analysisProfileId: string | null
} | null

function catalogEntry(value: unknown): CatalogEntry {
  if (value === null || typeof value !== 'object' || Array.isArray(value)) return null
  const source = Reflect.get(value, 'source')
  const checkout = Reflect.get(value, 'checkout')
  const view = Reflect.get(value, 'view')
  const indexIntentAvailable = Reflect.get(value, 'index_intent_available')
  const profileValue = Reflect.get(value, 'analysis_profile_id')
  if (
    source === null || typeof source !== 'object' || Array.isArray(source)
    || checkout === null || typeof checkout !== 'object' || Array.isArray(checkout)
    || view !== null || typeof indexIntentAvailable !== 'boolean'
  ) return null
  const sourceLabel = Reflect.get(source, 'label')
  const checkoutLabel = Reflect.get(checkout, 'label')
  const analysisProfileId = profileValue === undefined ? null : typeof profileValue === 'string' && profileValue !== '' ? profileValue : null
  if (profileValue !== undefined && analysisProfileId === null || indexIntentAvailable !== (analysisProfileId !== null)) return null
  return typeof sourceLabel === 'string' && sourceLabel !== '' && typeof checkoutLabel === 'string' && checkoutLabel !== ''
    ? { source: { label: sourceLabel }, checkout: { label: checkoutLabel }, view, indexIntentAvailable, analysisProfileId }
    : null
}

function parseFirstIndexTarget(value: string | null): { sourceId: string; checkoutId: string } | null {
  if (value === null || value === '') return null
  let request: unknown
  try {
    request = JSON.parse(value)
  } catch {
    return null
  }
  if (request === null || typeof request !== 'object' || Array.isArray(request)) return null
  const target = Reflect.get(request, 'target')
  if (target === null || typeof target !== 'object' || Array.isArray(target)) return null
  const keys = Object.keys(target).sort()
  const sourceId = Reflect.get(target, 'source_id')
  const checkoutId = Reflect.get(target, 'checkout_id')
  return keys.join(',') === 'checkout_id,source_id' && typeof sourceId === 'string' && sourceId !== '' && typeof checkoutId === 'string' && checkoutId !== ''
    ? { sourceId, checkoutId }
    : null
}

async function login(page: Page, fixture: LiveFixtureState): Promise<void> {
  await page.goto(`${fixture.frontend.baseUrl}/`, { waitUntil: 'domcontentloaded' })
  const status = await page.evaluate(async (credential) => {
    const response = await fetch('/api/auth/user-login', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(credential),
    })
    return response.status
  }, fixture.browserCredential)
  expect(status).toBe(200)
}

test('S4 live first-index action reflects the real daemon target fixture', async ({ browser }, testInfo) => {
  const fixture = await readLiveFixture()
  const context = await browser.newContext()
  const page = await context.newPage()
  const traffic: RouteTraffic[] = []
  const statusHeaderChecks: boolean[] = []
  let firstIndexSubmitted = false
  let fixtureSeam: string | null = null

  page.on('request', (request) => {
    const url = new URL(request.url())
    if (url.origin !== fixture.frontend.baseUrl || !url.pathname.startsWith('/api/code/index-intents')) return
    if (request.method() === 'GET') {
      const headers = request.headers()
      statusHeaderChecks.push(
        typeof headers['x-engram-tab-binding-id'] === 'string' && headers['x-engram-tab-binding-id'] !== ''
        && typeof headers['x-engram-document-proof'] === 'string' && headers['x-engram-document-proof'] !== ''
        && request.postData() === null,
      )
    }
  })
  page.on('response', (response) => {
    const url = new URL(response.url())
    if (url.origin === fixture.frontend.baseUrl && url.pathname.startsWith('/api/code/index-intents')) {
      traffic.push({ origin: 'browser', method: response.request().method(), path: '/api/code/index-intents', status: response.status(), at: new Date().toISOString() })
    }
  })

  try {
    expect(fixture.mock.prohibited).toBe(true)
    expect(fixture.mock.liveConfigContainsMockCommand).toBe(false)
    expect(fixture.candidate.commit).toBe(fixture.backend.sourceCommit)

    await login(page, fixture)
    const catalogResponse = page.waitForResponse((response) => {
      const url = new URL(response.url())
      return response.request().method() === 'POST' && url.pathname === '/api/code/contexts'
    })
    await page.goto(`${fixture.frontend.baseUrl}/code`, { waitUntil: 'domcontentloaded' })
    const catalog = await catalogResponse
    expect(catalog.status()).toBe(200)
    const catalogBody: unknown = await catalog.json()
    const entries = catalogBody !== null && typeof catalogBody === 'object' && !Array.isArray(catalogBody)
      ? Reflect.get(catalogBody, 'contexts')
      : null
    expect(Array.isArray(entries)).toBe(true)
    const noView = Array.isArray(entries)
      ? entries.map(catalogEntry).find((entry) => entry !== null && entry.source.label.includes(`${fixture.fixtureId}-c`)) ?? null
      : null
    expect(noView).not.toBeNull()
    if (noView === null) throw new Error('live fixture did not expose its intentionally unpublished checkout')

    const firstIndexAction = page.getByTestId('code-request-first-index')
    if (noView.indexIntentAvailable && noView.analysisProfileId !== null) {
      await expect(firstIndexAction).toHaveCount(1)
      const submitResponse = page.waitForResponse((response) => {
        const url = new URL(response.url())
        return response.request().method() === 'POST' && url.pathname === '/api/code/index-intents'
      })
      await firstIndexAction.click()
      const response = await submitResponse
      expect(response.status()).toBe(202)
      const target = parseFirstIndexTarget(response.request().postData())
      expect(target).not.toBeNull()
      expect(response.request().postData()).not.toContain('analysis_profile_id')
      await expect(page.getByTestId('code-context-pinned')).toHaveCount(0)
      await expect(page.getByTestId('index-intent-check-status')).toBeVisible()
      const statusResponse = page.waitForResponse((candidate) => {
        const url = new URL(candidate.url())
        return candidate.request().method() === 'GET' && /^\/api\/code\/index-intents\/[^/]+$/.test(url.pathname)
      })
      await page.getByTestId('index-intent-check-status').click()
      expect((await statusResponse).status()).toBe(200)
      expect(statusHeaderChecks.length).toBeGreaterThan(0)
      expect(statusHeaderChecks.every(Boolean)).toBe(true)
      firstIndexSubmitted = true
    } else {
      await expect(firstIndexAction).toHaveCount(0)
      await expect(page.getByTestId('code-context-index-affordance').filter({ hasText: `${fixture.fixtureId}-c` }))
        .toContainText('no single fresh daemon target')
      fixtureSeam = 'The live fixture provisions Views directly and starts no authenticated daemon PollCodeIndexIntents loop, so the server has no fresh no-View IndexIntentTargetRegistry advertisement to exercise first-index admission.'
    }
  } finally {
    const state = await appendBrowserTraffic(traffic)
    const evidence = JSON.stringify({
      evidenceKind: 'real-authenticated-go-postgresql-browser-first-index',
      candidate: state.candidate,
      backend: { sourceCommit: state.backend.sourceCommit, binarySha256: state.backend.binarySha256 },
      browser: { engine: browser.browserType().name(), version: browser.version() },
      browserHTTP: 'real registered Go routes; no page-level API mock or SQL lifecycle fabrication',
      firstIndexSubmitted,
      fixtureSeam,
      traffic: state.traffic,
    }, null, 2)
    await writeFile(testInfo.outputPath('s4-live-index-intent.json'), evidence, 'utf8')
    await testInfo.attach('s4-live-index-intent', { contentType: 'application/json', body: Buffer.from(evidence) })
    await context.close()
  }
})
