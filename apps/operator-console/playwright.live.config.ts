import { defineConfig, devices } from '@playwright/test'

export default defineConfig({
  testDir: './tests/live',
  testMatch: /(?:route-trace\.ts|honest-shell\.spec\.ts|operator-code-explorer\.spec\.ts|mutation-result\.spec\.ts)/,
  globalSetup: './tests/live/fixture-bootstrap.ts',
  timeout: 120_000,
  expect: { timeout: 10_000 },
  fullyParallel: false,
  workers: 1,
  reporter: process.env.CI ? [['list']] : 'list',
  use: {
    locale: 'ru-RU',
    trace: 'retain-on-failure',
    screenshot: 'only-on-failure',
  },
  projects: [
    {
      name: 'chromium',
      use: { ...devices['Desktop Chrome'] },
    },
  ],
})
