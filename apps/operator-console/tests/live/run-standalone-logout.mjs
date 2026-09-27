import { execFileSync, spawnSync } from 'node:child_process'
import { randomBytes } from 'node:crypto'
import { createServer } from 'node:net'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'

const root = dirname(fileURLToPath(import.meta.url))
const project = `engram-logout-${process.pid}-${randomBytes(4).toString('hex')}`
const [serverImage, consoleImage] = process.argv.slice(2)
if (!serverImage || !consoleImage) throw new Error('usage: node tests/live/run-standalone-logout.mjs <server-image> <console-image>')
const port = () => new Promise((resolve, reject) => {
  const server = createServer()
  server.once('error', reject)
  server.listen(0, '127.0.0.1', () => {
    const address = server.address()
    server.close(() => resolve(address.port))
  })
})
const [backendPort, consolePort] = await Promise.all([port(), port()])
const backend = `http://127.0.0.1:${backendPort}`
const frontend = `http://127.0.0.1:${consolePort}`
const email = `standalone-${project}@fixture.invalid`
const password = `Local-${randomBytes(24).toString('base64url')}`
const env = {
  ...process.env,
  FIXTURE_DB_PASSWORD: randomBytes(24).toString('hex'),
  FIXTURE_ADMIN_TOKEN: randomBytes(32).toString('base64url'),
  FIXTURE_SERVER_IMAGE: serverImage,
  FIXTURE_CONSOLE_IMAGE: consoleImage,
  FIXTURE_BACKEND_PORT: String(backendPort),
  FIXTURE_CONSOLE_PORT: String(consolePort),
  FIXTURE_PUBLIC_ORIGIN: process.argv[4] === '--invalid-config' ? 'console.example.test' : process.argv[4] === '--invalid-scheme' ? 'ftp://console.example.test' : frontend,
}
const compose = (args) => execFileSync('docker', ['compose', '-p', project, '-f', join(root, 'standalone-logout.compose.yml'), ...args], { env, stdio: 'pipe', timeout: 180_000 })
try {
  compose(['up', '-d', '--wait'])
  if (process.argv[4] === '--invalid-config' || process.argv[4] === '--invalid-scheme') {
    const ready = await fetch(`${frontend}/api/ready`)
    const denied = await fetch(`${frontend}/api/auth/logout`, { method: 'POST', headers: { Origin: frontend } })
    if (ready.status !== 200 || denied.status !== 500) throw new Error(`invalid public origin: ready ${ready.status}, logout ${denied.status}`)
    console.log('invalid public origin: ready 200; logout 500')
  } else {
    const setup = await fetch(`${backend}/api/auth/setup`, {
      method: 'POST',
      headers: { Authorization: `Bearer ${env.FIXTURE_ADMIN_TOKEN}`, 'Content-Type': 'application/json' },
      body: JSON.stringify({ email, password }),
    })
    if (setup.status !== 201) throw new Error(`local admin setup: HTTP ${setup.status}`)
    const result = spawnSync(process.execPath, [join(root, '../../node_modules/@playwright/test/cli.js'), 'test', '--config', 'playwright.logout.config.ts'], {
      cwd: join(root, '../..'),
      env: {
        ...env,
        ENGRAM_LOGOUT_BROWSER_BASE_URL: frontend,
        ENGRAM_LOGOUT_BROWSER_BACKEND_URL: backend,
        ENGRAM_LOGOUT_BROWSER_EMAIL: email,
        ENGRAM_LOGOUT_BROWSER_PASSWORD: password,
        ENGRAM_LOGOUT_BROWSER_CDP: process.env.ENGRAM_LOGOUT_BROWSER_CDP || '',
      },
      stdio: 'inherit',
      timeout: 120_000,
    })
    if (result.error) throw result.error
    if (result.status !== 0) process.exitCode = result.status || 1
    const responses = await Promise.all(Array.from({ length: 350 }, (_, index) => fetch(`${frontend}/api/ready`, {
      headers: {
        'X-Forwarded-For': `198.51.100.${index % 200}`,
        'X-Real-IP': `203.0.113.${index % 200}`,
        'True-Client-IP': `192.0.2.${index % 200}`,
      },
    }).then((response) => response.status)))
    if (!responses.includes(429) || responses.some((status) => status !== 200 && status !== 429)) {
      throw new Error(`backend limiter spoof probe: ${responses.filter((status) => status === 200).length} allowed, ${responses.filter((status) => status === 429).length} limited`)
    }
    console.log(`backend limiter: ${responses.filter((status) => status === 429).length} spoofed-client requests limited`)
  }
} catch (error) {
  const logs = compose(['logs', '--no-color', '--tail', '20', 'server', 'console']).toString()
  console.error(logs.replaceAll(env.FIXTURE_DB_PASSWORD, '[redacted]').replaceAll(env.FIXTURE_ADMIN_TOKEN, '[redacted]'))
  throw error
} finally {
  compose(['down', '--volumes'])
  console.log(`standalone fixture ${project}: stopped and removed`)
}
