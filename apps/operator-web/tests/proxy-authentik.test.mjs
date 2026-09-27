import assert from 'node:assert/strict'
import { createServer } from 'node:http'
import { once } from 'node:events'
import test from 'node:test'
import { createApp, createRouter, toNodeListener } from 'h3'

async function listen(server) {
  server.listen(0, '127.0.0.1')
  await once(server, 'listening')
  return `http://127.0.0.1:${server.address().port}`
}

test('operator-web API proxy never relays client-provided Authentik identity', async (t) => {
  let observed
  const upstream = createServer((req, res) => {
    observed = req.headers
    res.end('upstream')
  })
  const upstreamURL = await listen(upstream)
  t.after(() => upstream.close())

  const originalHandler = globalThis.defineEventHandler
  const originalConfig = globalThis.useRuntimeConfig
  globalThis.defineEventHandler = (handler) => handler
  globalThis.useRuntimeConfig = () => ({ engramApiTarget: upstreamURL })
  const router = createRouter()
  router.add('/api/**:path', (await import('../server/routes/api/[...path].ts')).default)
  const consoleServer = createServer(toNodeListener(createApp().use(router)))
  const origin = await listen(consoleServer)
  t.after(() => {
    globalThis.defineEventHandler = originalHandler
    globalThis.useRuntimeConfig = originalConfig
    consoleServer.close()
  })

  const response = await fetch(`${origin}/api/auth/me`, {
    headers: {
      'X-Authentik-Email': 'admin@example.test',
      'x-AuThEnTiK-NaMe': 'Forged Admin',
      'X-Authentik-Groups': 'admins',
      Cookie: 'engram_auth=real-cookie',
      Origin: origin,
      'X-Forwarded-For': '198.51.100.2',
    },
  })
  assert.equal(response.status, 200)
  assert.ok(observed, 'upstream must observe the proxy request')
  assert.equal(Object.keys(observed).filter((name) => name.toLowerCase().startsWith('x-authentik-')).length, 0)
  assert.equal(observed.cookie, 'engram_auth=real-cookie')
  assert.equal(observed.origin, origin)
  assert.equal(observed['x-forwarded-for'], '198.51.100.2')
})
