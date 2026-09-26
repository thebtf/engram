import { expect, test } from '@playwright/test'
import type { Page } from '@playwright/test'

test.describe('mock interaction-only: responsive navigation and localization', () => {
  test('queue nav stays active while flags are unknown or unavailable, but shows an explicit off gate', async ({ page }) => {
    let failFlags: (() => void) | undefined
    await page.route('**/api/flags', async (route) => {
      await new Promise<void>((resolve) => { failFlags = resolve })
      await route.abort()
    })

    await page.goto('/')
    const queue = page.locator('#primary-navigation a[href="/queue"]')
    await expect(queue).toBeVisible()
    await expect(queue).not.toHaveClass(/tomb/)
    await expect(queue.locator('.ndot')).toHaveAttribute('data-s', 'active')
    await expect.poll(() => Boolean(failFlags)).toBe(true)
    const failedFlags = page.waitForEvent('requestfailed', (request) => new URL(request.url()).pathname === '/api/flags')
    failFlags!()
    await failedFlags
    await expect(queue).not.toHaveClass(/tomb/)
    await expect(queue.locator('.ndot')).toHaveAttribute('data-s', 'active')

    await page.unroute('**/api/flags')
    await page.route('**/api/flags', (route) => route.fulfill({ json: { flags: { ENGRAM_VNEXT_F_ENABLED: false } } }))
    await page.reload()
    await expect(queue.locator('.ndot')).toHaveAttribute('data-s', 'gated')
    await expect(queue.locator('.flag')).toHaveText('ENGRAM_VNEXT_F_ENABLED')
  })

  test('only queue and Overview fetch candidate content, not Search navigation', async ({ page }) => {
    const candidateRequests: string[] = []
    page.on('request', (request) => {
      if (new URL(request.url()).pathname === '/api/memory/candidates') candidateRequests.push(request.url())
    })

    await page.goto('/search')
    await expect(page.locator('#primary-navigation a[href="/queue"]')).toBeVisible()
    expect(candidateRequests).toEqual([])

    await page.goto('/queue')
    await expect.poll(() => candidateRequests.length).toBeGreaterThan(0)
    candidateRequests.length = 0

    await page.goto('/')
    await expect.poll(() => candidateRequests.length).toBeGreaterThan(0)
  })

  test('mobile and tablet navigation stays reachable and settings groups stay selectable', async ({ page }) => {
    await page.setViewportSize({ width: 1440, height: 1024 })
    await page.goto('/')
    await expect(page.locator('#primary-navigation')).toBeVisible()
    await expect(page.locator('.mobile-menu-button')).toBeHidden()
    await page.setViewportSize({ width: 390, height: 844 })
    await expectClosedMobileShell(page, 390)

    const mobileTrigger = page.locator('.mobile-menu-button')
    await mobileTrigger.click()
    const mobileNav = page.locator('#primary-navigation')
    await expect(mobileNav).toHaveClass(/open/)
    const scrim = page.getByRole('button', { name: 'Закрыть меню', exact: true })
    await expect(scrim).toBeVisible()
    expect(await mobileNav.evaluate((element) => element.getBoundingClientRect().left)).toBe(0)
    await scrim.click({ position: { x: 380, y: 400 } })
    await expectClosedMobileShell(page, 390)
    await expect(mobileTrigger).toBeFocused()

    await page.setViewportSize({ width: 980, height: 900 })
    await page.goto('/')
    const trigger = page.locator('.mobile-menu-button')
    await expect(trigger).toBeVisible()
    await expect(trigger).toHaveAttribute('aria-expanded', 'false')
    await trigger.click()

    const nav = page.locator('#primary-navigation')
    await expect(trigger).toHaveAttribute('aria-expanded', 'true')
    await expect(nav.getByRole('link', { name: 'Обзор' })).toBeVisible()
    await nav.getByRole('link', { name: 'Обзор' }).focus()
    await expect(nav.getByRole('link', { name: 'Обзор' })).toBeFocused()
    await page.keyboard.press('Escape')
    await expect(trigger).toHaveAttribute('aria-expanded', 'false')
    await expect(trigger).toBeFocused()

    await page.setViewportSize({ width: 390, height: 844 })
    await page.goto('/settings')
    const dialog = page.getByRole('dialog', { name: 'Настройки сервера' })
    await expect(dialog).toBeVisible()
    const flags = dialog.getByRole('button', { name: 'Флаги сервера' })
    await flags.scrollIntoViewIfNeeded()
    await expect(flags).toBeVisible()
    await flags.click()
    await expect(dialog.getByRole('heading', { name: 'Флаги сервера' })).toBeVisible()
    await dialog.getByRole('button', { name: 'Доступ', exact: true }).click()
    await expect(dialog.getByRole('link', { name: 'Открыть «Доступ»', exact: true })).toBeVisible()
  })

  test('hard reload, 200% zoom, RU/EN, and zh keep navigation and Settings keyboard-accessible', async ({ page }) => {
    await page.setViewportSize({ width: 1440, height: 1024 })
    await page.goto('/graph')
    await expect(page.getByTestId('shell-memory-count')).toBeVisible()
    await expect(page.getByRole('heading', { name: 'Редактирование графа знаний упразднено', exact: true })).toBeVisible()
    await page.reload({ waitUntil: 'domcontentloaded' })
    await expect(page).toHaveURL(/\/graph$/)
    await expect(page.getByRole('link', { name: 'Рабочее место', exact: true })).toBeVisible()

    await page.setViewportSize({ width: 640, height: 512 })
    await page.goto('/settings')
    const dialog = page.getByRole('dialog')
    await expect(dialog).toHaveAccessibleName('Настройки сервера')
    await expect(dialog).toBeVisible()
    await expect(page.locator('.settings-head .close')).toBeFocused()
    await expectNoHorizontalOverflow(page)

    await expect(page.locator('html')).toHaveAttribute('lang', 'ru')
    await dialog.getByRole('button', { name: 'English' }).click()
    await expect(page.locator('html')).toHaveAttribute('lang', 'en')
    await page.keyboard.press('Escape')
    await expect(dialog).toBeHidden()
    const englishMenu = page.getByRole('button', { name: 'Menu', exact: true })
    await englishMenu.click()
    await expect(page.getByRole('link', { name: 'Workspace', exact: true })).toBeVisible()
    await page.keyboard.press('Escape')

    await page.goto('/settings')
    await expect(dialog).toBeVisible()
    await dialog.getByRole('button', { name: '中文' }).click()
    await expect(page.locator('html')).toHaveAttribute('lang', 'zh-Hans')
    await page.keyboard.press('Escape')
    const chineseMenu = page.getByRole('button', { name: '菜单', exact: true })
    await chineseMenu.click()
    const workspace = page.getByRole('link', { name: '工作区', exact: true })
    await expect(workspace).toBeVisible()
    await workspace.focus()
    await expect(workspace).toBeFocused()
  })
})

async function expectClosedMobileShell(page: Page, viewportWidth: number): Promise<void> {
  await expect.poll(() => page.locator('#primary-navigation').evaluate((element) => ({
    ariaHidden: element.getAttribute('aria-hidden'),
    inert: element.inert,
  }))).toEqual({ ariaHidden: 'true', inert: true })
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
      gridWidth: parseFloat(getComputedStyle(document.querySelector<HTMLElement>('.app')!).gridTemplateColumns),
      nav: { ...rect('#primary-navigation'), ariaHidden: nav.getAttribute('aria-hidden'), inert: nav.inert },
      topbar: rect('.topbar'),
      content: rect('.content'),
      statusbar: rect('.statusbar'),
      menu: rect('.mobile-menu-button'),
      title: rect('.content h1'),
    }
  })

  expect(shell.compact).toBe(true)
  expect(shell.scrollX).toBe(0)
  expect(shell.scrollWidth).toBeLessThanOrEqual(viewportWidth)
  expect(shell.gridWidth).toBe(viewportWidth)
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
}

async function expectNoHorizontalOverflow(page: Page): Promise<void> {
  const overflow = await page.evaluate(() => document.documentElement.scrollWidth - window.innerWidth)
  expect(overflow).toBeLessThanOrEqual(1)
}
