import { writeFile } from 'node:fs/promises'
import { expect, test } from '@playwright/test'
import type { Page } from '@playwright/test'
import { readLiveFixture } from './fixture-bootstrap'

async function assertResponsiveShell(page: Page, viewportWidth: number): Promise<Record<string, unknown>> {
  const shell = await page.evaluate(() => {
    const rect = (selector: string) => {
      const element = document.querySelector<HTMLElement>(selector)
      if (!element) throw new Error(`missing ${selector}`)
      const bounds = element.getBoundingClientRect()
      const style = getComputedStyle(element)
      return { left: bounds.left, right: bounds.right, width: bounds.width, height: bounds.height, display: style.display }
    }
    const nav = document.querySelector<HTMLElement>('#primary-navigation')
    if (!nav) throw new Error('missing primary navigation')
    return {
      compact: matchMedia('(max-width: 980px)').matches,
      scrollX,
      scrollWidth: document.documentElement.scrollWidth,
      gridColumns: getComputedStyle(document.querySelector<HTMLElement>('.app')!).gridTemplateColumns,
      nav: { ...rect('#primary-navigation'), ariaHidden: nav.getAttribute('aria-hidden'), inert: nav.inert },
      topbar: rect('.topbar'),
      content: rect('.content'),
      statusbar: rect('.statusbar'),
      menu: rect('.mobile-menu-button'),
      title: rect('.code-page h1'),
    }
  })

  expect(shell.scrollX).toBe(0)
  expect(shell.scrollWidth).toBeLessThanOrEqual(viewportWidth)
  const gridColumns = shell.gridColumns.split(' ').map((column) => Number.parseFloat(column))
  expect(gridColumns.reduce((total, column) => total + column, 0)).toBe(viewportWidth)

  if (viewportWidth <= 980) {
    expect(shell.compact).toBe(true)
    expect(shell.nav.right).toBeLessThanOrEqual(0)
    expect(shell.nav.ariaHidden).toBe('true')
    expect(shell.nav.inert).toBe(true)
    expect(shell.menu.display).not.toBe('none')
    expect(shell.menu.width).toBeGreaterThanOrEqual(44)
    expect(shell.menu.height).toBeGreaterThanOrEqual(44)
    for (const region of [shell.topbar, shell.content, shell.statusbar, shell.title, shell.menu]) {
      expect(region.left).toBeGreaterThanOrEqual(0)
      expect(region.right).toBeLessThanOrEqual(viewportWidth)
    }
  } else {
    expect(shell.compact).toBe(false)
    expect(shell.menu.display).toBe('none')
    expect(shell.nav.left).toBe(0)
    expect(shell.nav.right).toBe(shell.topbar.left)
    for (const region of [shell.topbar, shell.content, shell.statusbar]) {
      expect(region.right).toBeLessThanOrEqual(viewportWidth)
    }
  }
  return shell
}

test('live Workspace shell remains usable across responsive reflow', async ({ browser }, testInfo) => {
  const fixture = await readLiveFixture()
  const apiFailures: Array<{ path: string; status: number }> = []
  const layouts: Array<Record<string, unknown>> = []
  const context = await browser.newContext()
  const page = await context.newPage()
  page.on('response', (response) => {
    const url = new URL(response.url())
    if (url.origin === fixture.frontend.baseUrl && url.pathname.startsWith('/api/') && response.status() >= 400) {
      apiFailures.push({ path: url.pathname, status: response.status() })
    }
  })

  try {
    await page.setViewportSize({ width: 1440, height: 1024 })
    await page.goto(`${fixture.frontend.baseUrl}/`, { waitUntil: 'domcontentloaded' })
    const loginStatus = await page.evaluate(async (credential) => {
      const response = await fetch('/api/auth/user-login', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(credential),
      })
      return response.status
    }, fixture.browserCredential)
    expect(loginStatus).toBe(200)
    await page.getByTestId('overview-workspace-entry').click()
    await expect(page).toHaveURL(/\/code$/)
    await expect(page.getByTestId('code-release-state')).toHaveAttribute('data-state', 'unselected')

    for (const [name, width, height] of [['1440', 1440, 1024], ['980', 980, 900], ['390', 390, 844]] as const) {
      await page.setViewportSize({ width, height })
      layouts.push({ viewport: `${width}x${height}`, shell: await assertResponsiveShell(page, width) })
      if (width === 390) {
        await page.locator('.mobile-menu-button').click()
        await expect(page.locator('#primary-navigation')).toHaveClass(/open/)
        await expect(page.locator('.nav-scrim')).toBeVisible()
        const drawer = await page.locator('#primary-navigation').evaluate((element) => {
          const bounds = element.getBoundingClientRect()
          return { left: bounds.left, right: bounds.right, ariaHidden: element.getAttribute('aria-hidden'), inert: element.inert }
        })
        expect(drawer.left).toBe(0)
        expect(drawer.right).toBeGreaterThan(0)
        expect(drawer.ariaHidden).toBe('false')
        expect(drawer.inert).toBe(false)
        await page.screenshot({ path: testInfo.outputPath('workspace-390-drawer-open.png'), fullPage: true })
        await page.keyboard.press('Escape')
        layouts.push({ viewport: '390x844-after-escape', shell: await assertResponsiveShell(page, width) })
      }
      await page.screenshot({ path: testInfo.outputPath(`workspace-${name}.png`), fullPage: true })
    }

    const cdp = await page.context().newCDPSession(page)
    try {
      await cdp.send('Emulation.setPageScaleFactor', { pageScaleFactor: 2 })
      expect(await page.evaluate(() => window.visualViewport?.scale)).toBe(2)
      layouts.push({ viewport: '390x844@200%', shell: await assertResponsiveShell(page, 390) })
      await page.screenshot({ path: testInfo.outputPath('workspace-200pct.png'), fullPage: true })
    } finally {
      await cdp.send('Emulation.setPageScaleFactor', { pageScaleFactor: 1 })
      await cdp.detach()
    }

    expect(apiFailures).toEqual([])
    const evidence = JSON.stringify({
      evidenceKind: 'real-authenticated-go-postgresql-chromium-shell',
      candidate: fixture.candidate,
      backend: { sourceCommit: fixture.backend.sourceCommit, binarySha256: fixture.backend.binarySha256 },
      browser: { engine: browser.browserType().name(), version: browser.version() },
      layouts,
      apiFailures,
    }, null, 2)
    await writeFile(testInfo.outputPath('workspace-mobile-layout.json'), evidence, 'utf8')
    await testInfo.attach('workspace-mobile-layout', { contentType: 'application/json', body: Buffer.from(evidence) })
  } finally {
    await context.close()
  }
})
