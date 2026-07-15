import { expect, test } from '@playwright/test'

test('mobile and tablet navigation stays reachable and settings groups stay selectable', async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 1024 })
  await page.goto('/')
  await expect(page.locator('#primary-navigation')).toBeVisible()
  await expect(page.locator('.mobile-menu-button')).toBeHidden()

  await page.setViewportSize({ width: 390, height: 844 })
  await page.goto('/')

  const trigger = page.locator('.mobile-menu-button')
  await expect(trigger).toBeVisible()
  await expect(trigger).toHaveAttribute('aria-expanded', 'false')
  await trigger.click()
  await expect(trigger).toHaveAttribute('aria-expanded', 'true')

  const nav = page.locator('#primary-navigation')
  await expect(nav).toBeVisible()
  await expect(nav.getByRole('link', { name: 'Обзор' })).toBeVisible()
  await page.keyboard.press('Escape')
  await expect(trigger).toHaveAttribute('aria-expanded', 'false')
  await expect(trigger).toBeFocused()

  await page.setViewportSize({ width: 980, height: 900 })
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
