import { expect, test } from '@playwright/test'

const CONSOLE = '11111111-1111-4111-8111-111111111111'
const ALPHA = '22222222-2222-4222-8222-222222222222'

test('project entry keeps session content scoped and archive requires the selected identity', async ({ page }) => {
  await page.goto('/projects?project=operator-console')
  await expect(page.getByTestId(`project-row-${CONSOLE}`)).toBeVisible()
  await page.getByTestId('project-session-501').click()
  await expect(page.getByTestId('project-session-user-prompt')).toContainText('Wire the operator console project surface.')
  await page.locator('.session-detail summary').click()
  await expect(page.getByText('sdk-operator-1')).toBeVisible()
  await expect(page.getByText('balanced')).toBeVisible()

  await page.getByTestId(`project-row-${ALPHA}`).click()
  await expect(page).toHaveURL(new RegExp(`project=${ALPHA}`))
  await expect(page.getByTestId('project-session-user-prompt')).toHaveCount(0)
  await page.getByText('Управление выбранным проектом', { exact: true }).click()
  const input = page.getByRole('textbox', { name: `Введите ID проекта для подтверждения: ${ALPHA}`, exact: true })
  const confirm = page.getByRole('button', { name: 'Подтвердить архивирование', exact: true })
  await expect(confirm).toBeDisabled()
  await input.fill('wrong-project')
  await expect(confirm).toBeDisabled()
  await input.fill(ALPHA)
  await expect(confirm).toBeEnabled()
  const archiveResponse = page.waitForResponse(response => response.request().method() === 'DELETE' && new URL(response.url()).pathname === `/api/projects/${ALPHA}`)
  await confirm.click()
  expect(await (await archiveResponse).json()).toMatchObject({ operation_state: 'completed', readback: { authoritative: true, kind: 'authorized_absence' } })
  await expect(page.getByTestId(`project-row-${ALPHA}`)).toHaveCount(0)
  await expect(page.getByTestId('project-context-select')).toHaveValue('')
  await expect(page.getByTestId('project-session-user-prompt')).toHaveCount(0)
})
