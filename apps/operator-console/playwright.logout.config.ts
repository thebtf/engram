import { defineConfig, devices } from '@playwright/test'

export default defineConfig({
  testDir: './tests/live',
  testMatch: 'auth-logout.spec.ts',
  timeout: 60_000,
  use: { ...devices['Desktop Chrome'], locale: 'ru-RU' },
})
