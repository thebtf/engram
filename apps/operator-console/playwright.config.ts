import { defineConfig, devices } from '@playwright/test'

const appPort = process.env.OPERATOR_CONSOLE_SMOKE_PORT || '37992'
const apiPort = process.env.OPERATOR_CONSOLE_SMOKE_API_PORT || '37993'
const appUrl = `http://127.0.0.1:${appPort}`
const apiUrl = `http://127.0.0.1:${apiPort}`

export default defineConfig({
  testDir: './tests/browser',
  timeout: 30_000,
  expect: { timeout: 5_000 },
  fullyParallel: false,
  reporter: process.env.CI ? [['list']] : 'list',
  use: {
    baseURL: appUrl,
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
  webServer: [
    {
      name: 'mock-operator-api',
      command: 'node scripts/mock-operator-api.mjs',
      url: `${apiUrl}/api/ready`,
      timeout: 30_000,
      reuseExistingServer: !process.env.CI,
      env: {
        PORT: apiPort,
        HOST: '127.0.0.1',
      },
      stdout: 'pipe',
      stderr: 'pipe',
    },
    {
      name: 'operator-console-preview',
      command: 'node .output/server/index.mjs',
      url: `${appUrl}/settings`,
      timeout: 60_000,
      reuseExistingServer: !process.env.CI,
      env: {
        PORT: appPort,
        HOST: '127.0.0.1',
        NUXT_OPERATOR_API_TARGET: apiUrl,
        NUXT_PUBLIC_API_DISPLAY_HOST: `127.0.0.1:${appPort}`,
      },
      stdout: 'pipe',
      stderr: 'pipe',
    },
  ],
})
