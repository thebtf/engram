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

test('Nitro API proxy drops forged Authentik identity without losing cookies or public forwarding', async (t) => {
  let observed
  const upstream = createServer((req, res) => {
    observed = req.headers
    res.setHeader('content-type', 'application/json')
    res.end(JSON.stringify({ received: true }))
  })
  const upstreamURL = await listen(upstream)
  t.after(() => upstream.close())

  const originalHandler = globalThis.defineEventHandler
  const originalConfig = globalThis.useRuntimeConfig
  globalThis.defineEventHandler = (handler) => handler
  const router = createRouter()
  const proxy = (await import('../server/routes/api/[...path].ts')).default
  router.add('/api/**:path', proxy)
  const consoleServer = createServer(toNodeListener(createApp().use(router)))
  const origin = await listen(consoleServer)
  globalThis.useRuntimeConfig = () => ({ operatorApiTarget: upstreamURL, operatorPublicOrigin: origin })
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
      'X-Forwarded-Host': 'forged.example.test',
      'X-Forwarded-Proto': 'https',
      'X-Forwarded-For': '198.51.100.2',
      'X-Real-IP': '198.51.100.2',
      'True-Client-IP': '198.51.100.2',
    },
  })
  assert.equal(response.status, 200)
  assert.ok(observed, 'the upstream must observe the actual proxy request')
  assert.equal(Object.keys(observed).filter((name) => name.toLowerCase().startsWith('x-authentik-')).length, 0)
  assert.equal(observed.cookie, 'engram_auth=real-cookie')
  assert.equal(observed.origin, origin)
  assert.equal(observed['x-forwarded-host'], new URL(origin).host)
  assert.equal(observed['x-forwarded-proto'], 'http')
  assert.equal(observed['x-forwarded-for'], '127.0.0.1')
  assert.equal(observed['x-real-ip'], '127.0.0.1')
  assert.equal(observed['true-client-ip'], '127.0.0.1')
})
