import assert from 'node:assert/strict'
import { spawn } from 'node:child_process'
import { once } from 'node:events'
import { createServer, request } from 'node:http'
import test from 'node:test'
import { fileURLToPath } from 'node:url'

function listen(server) {
  server.listen(0, '127.0.0.1')
  return once(server, 'listening').then(() => server.address().port)
}

async function proxyFixture(t) {
  const received = []
  const backend = createServer(async (req, res) => {
    let body = ''
    for await (const chunk of req) body += chunk
    received.push({ method: req.method, url: req.url, headers: req.headers, body })
    res.writeHead(200, { 'content-type': 'application/json', 'set-cookie': 'engram_auth=fixture-response; HttpOnly; Path=/' })
    res.end('{"status":"ok"}')
  })
  const backendPort = await listen(backend)
  t.after(() => new Promise((resolve, reject) => backend.close(error => error ? reject(error) : resolve())))
  const reservation = createServer()
  const port = await listen(reservation)
  await new Promise((resolve, reject) => reservation.close(error => error ? reject(error) : resolve()))
  const child = spawn(process.execPath, [fileURLToPath(new URL('./static-host.mjs', import.meta.url))], {
    cwd: new URL('../', import.meta.url),
    env: { ...process.env, HOST: '127.0.0.1', PORT: String(port), ENGRAM_OPERATOR_API_TARGET: `http://127.0.0.1:${backendPort}/api` },
    stdio: ['ignore', 'pipe', 'pipe'],
  })
  let output = ''
  child.stdout.on('data', chunk => { output += chunk })
  child.stderr.on('data', chunk => { output += chunk })
  t.after(async () => {
    if (child.exitCode === null && child.signalCode === null) {
      const exited = once(child, 'exit')
      child.kill()
      await exited
    }
  })
  await new Promise((resolve, reject) => {
    const timer = setTimeout(() => reject(new Error(`static host did not become ready: ${output}`)), 5000)
    child.once('error', error => { clearTimeout(timer); reject(error) })
    child.once('exit', code => { clearTimeout(timer); reject(new Error(`static host exited ${code}: ${output}`)) })
    child.stdout.on('data', () => {
      if (output.includes('Operator console listening on')) { clearTimeout(timer); resolve() }
    })
  })
  return {
    received,
    send(headers) {
      return new Promise((resolve, reject) => {
        const req = request(`http://127.0.0.1:${port}/api/auth/user-logout?fixture=1`, { method: 'POST', headers }, res => {
          let body = ''
          res.on('data', chunk => { body += chunk })
          res.on('end', () => resolve({ status: res.statusCode, headers: res.headers, body }))
        })
        req.on('error', reject)
        req.end('{"fixture":true}')
      })
    },
  }
}

test('static host preserves legitimate browser credentials and API flow', { timeout: 10000 }, async t => {
  const fixture = await proxyFixture(t)
  const headers = {
    authorization: 'Bearer ' + 'static-host-test-only',
    'x-auth-token': 'fixture-keycard',
    cookie: 'engram_auth=fixture-session',
    origin: 'http://localhost:5173',
    'sec-fetch-site': 'same-site',
    'content-type': 'application/json',
    'x-engram-request-id': 'fixture-request',
    'x-engram-tab-binding-id': 'fixture-tab',
    'x-engram-document-proof': 'fixture-proof',
  }
  const result = await fixture.send(headers)
  assert.equal(result.status, 200)
  assert.equal(result.body, '{"status":"ok"}')
  assert.deepEqual(result.headers['set-cookie'], ['engram_auth=fixture-response; HttpOnly; Path=/'])
  assert.equal(fixture.received.length, 1)
  const actual = fixture.received[0]
  assert.equal(actual.method, 'POST')
  assert.equal(actual.url, '/api/auth/user-logout?fixture=1')
  assert.equal(actual.body, '{"fixture":true}')
  for (const [key, value] of Object.entries(headers)) assert.equal(actual.headers[key], value, `${key} must reach the worker`)
})

test('static host does not relay client identity or forged peer headers', { timeout: 10000 }, async t => {
  const fixture = await proxyFixture(t)
  const forged = {
    'x-authentik-email': 'forged@example.test',
    'x-authentik-username': 'forged-operator',
    'x-authentik-groups': 'admins',
    'x-authentik-uid': 'forged-user',
    'x-authentik-entitlements': 'forged-admin-entitlement',
    'true-client-ip': '192.0.2.90',
    'x-forwarded-server': 'forged-server',
    'x-real-ip': '192.0.2.1',
    'x-forwarded-for': '192.0.2.1',
    'x-forwarded-host': 'forged.example.test',
    'x-forwarded-proto': 'https',
    forwarded: 'for=192.0.2.1;host=forged.example.test;proto=https',
  }
  assert.equal((await fixture.send(forged)).status, 200)
  assert.equal(fixture.received.length, 1)
  const leaked = Object.entries(forged).filter(([key, value]) => fixture.received[0].headers[key] === value).map(([key]) => key)
  assert.deepEqual(leaked, [], 'public console must not promote client identity or peer claims into trusted upstream headers')
  assert.equal(fixture.received[0].headers['x-forwarded-for'], '127.0.0.1', 'forwarded client address must derive from the actual console socket, not incoming claims')
})
