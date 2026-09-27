import assert from 'node:assert/strict'
import { createServer, request } from 'node:http'
import { once } from 'node:events'
import test from 'node:test'
import { createApp, createRouter, toNodeListener } from 'h3'

async function listen(server) {
  server.listen(0, '127.0.0.1')
  await once(server, 'listening')
  return `http://127.0.0.1:${server.address().port}`
}

test('operator-web API proxy never relays client-provided Authentik identity', async (t) => {
  const requests = []
  const upstream = createServer((req, res) => {
    requests.push({ url: req.url, headers: { ...req.headers } })
    if (req.url === '/api/ready') {
      res.end('ready')
    } else if (req.url === '/api/auth/login' && req.method === 'POST') {
      res.setHeader('set-cookie', 'engram_auth=real-cookie; Path=/; HttpOnly')
      res.end('logged in')
    } else if (req.url === '/api/auth/logout' && req.method === 'POST') {
      const expectedOrigin = req.headers['x-forwarded-proto'] + '://' + req.headers['x-forwarded-host']
      res.statusCode = req.headers.origin === expectedOrigin ? 200 : 403
      res.setHeader('set-cookie', 'engram_auth=; Path=/; Max-Age=0')
      res.end('logged out')
    } else {
      res.statusCode = 401
      res.end('unauthorized')
    }
  })
  const upstreamURL = await listen(upstream)
  t.after(() => upstream.close())

  const originalHandler = globalThis.defineEventHandler
  const originalConfig = globalThis.useRuntimeConfig
  globalThis.defineEventHandler = (handler) => handler
  globalThis.useRuntimeConfig = () => ({ engramApiTarget: upstreamURL, engramPublicOrigin: origin })
  const router = createRouter()
  router.add('/api/**:path', (await import('../server/routes/api/[...path].ts')).default)
  const consoleServer = createServer(toNodeListener(createApp().use(router)))
  const origin = await listen(consoleServer)
  t.after(() => {
    globalThis.defineEventHandler = originalHandler
    globalThis.useRuntimeConfig = originalConfig
    consoleServer.close()
  })

  const login = await fetch(`${origin}/api/auth/login`, { method: 'POST', headers: { Origin: origin } })
  assert.equal(login.status, 200)
  assert.match(login.headers.get('set-cookie'), /engram_auth=real-cookie/)

  const response = await fetch(`${origin}/api/auth/me?session=check`, {
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
  assert.equal(response.status, 401)
  const observed = requests.at(-1)
  assert.equal(observed.url, '/api/auth/me?session=check')
  assert.equal(Object.keys(observed.headers).filter((name) => name.toLowerCase().startsWith('x-authentik-')).length, 0)
  assert.equal(observed.headers.cookie, 'engram_auth=real-cookie')
  assert.equal(observed.headers.origin, origin)
  assert.equal(observed.headers['x-forwarded-host'], new URL(origin).host)
  assert.equal(observed.headers['x-forwarded-proto'], 'http')
  assert.equal(observed.headers['x-forwarded-for'], '127.0.0.1')
  assert.equal(observed.headers['x-real-ip'], '127.0.0.1')
  assert.equal(observed.headers['true-client-ip'], '127.0.0.1')

  const logout = await fetch(`${origin}/api/auth/logout`, { method: 'POST', headers: { Origin: origin, Cookie: 'engram_auth=real-cookie' } })
  assert.equal(logout.status, 200)
  assert.match(logout.headers.get('set-cookie'), /engram_auth=;/)
  assert.equal((await fetch(`${origin}/api/auth/me`)).status, 401)

  const sent = requests.length
  const hostileOrigin = await fetch(`${origin}/api/auth/logout`, { method: 'POST', headers: { Origin: 'http://forged.example.test', 'X-Authentik-Email': 'admin@example.test' } })
  assert.equal(hostileOrigin.status, 403)
  assert.equal(requests.length, sent + 1)
  const hostileHost = await new Promise((resolve, reject) => {
    const req = request(`${origin}/api/auth/logout`, { method: 'POST', headers: { Host: 'forged.example.test', Origin: origin } }, (res) => {
      res.resume()
      res.on('end', () => resolve(res.statusCode))
    })
    req.on('error', reject)
    req.end()
  })
  assert.equal(hostileHost, 403)
  assert.equal(requests.length, sent + 1)

  globalThis.useRuntimeConfig = () => ({ engramApiTarget: upstreamURL, engramPublicOrigin: 'http://invalid.example.test/path' })
  assert.equal((await fetch(`${origin}/api/auth/logout`, { method: 'POST' })).status, 500)
  assert.equal(requests.length, sent + 1)
  assert.equal((await fetch(`${origin}/api/ready`, { headers: { Host: 'forged.example.test' } })).status, 200)
  assert.equal(requests.at(-1).url, '/api/ready')

  globalThis.useRuntimeConfig = () => ({ engramApiTarget: upstreamURL })
  assert.equal((await fetch(`${origin}/api/auth/me`)).status, 500)
  assert.equal((await fetch(`${origin}/api/ready`)).status, 200)
  assert.equal(requests.at(-1).url, '/api/ready')
})
