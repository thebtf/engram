import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import test from 'node:test'
import ts from 'typescript'
import * as vue from 'vue'

const root = new URL('../../', import.meta.url)

// Same in-memory TS/module-cache convention as auth-session; Vue refs and API mapping are real.
function evaluate(source, require, globals) {
  const module = { exports: {} }
  const code = ts.transpileModule(source, {
    compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022 },
    transformers: {
      before: [(context) => (file) => ts.visitNode(file, function visit(node) {
        if (ts.isSourceFile(node)) return ts.factory.updateSourceFile(node, [
          ...node.statements.filter(ts.isImportDeclaration), ...node.statements.filter(statement => !ts.isImportDeclaration(statement)),
        ].map(statement => ts.visitNode(statement, visit)))
        if (ts.isMetaProperty(node) && node.keywordToken === ts.SyntaxKind.ImportKeyword) return ts.factory.createIdentifier('importMeta')
        return ts.visitEachChild(node, visit, context)
      })],
    },
  }).outputText
  new Function('require', 'module', 'exports', ...Object.keys(globals), code)(require, module, module.exports, ...Object.values(globals))
  return module.exports
}

function fixture(t) {
  const modules = new Map()
  const requests = []
  const violations = []
  const scope = vue.effectScope()
  t.after(() => {
    scope.stop()
    assert.deepEqual(violations, [], 'unknown paths and writer actions must fail the fixture, even if production catches them')
    assert.equal(requests.filter(request => !request.settled).length, 0, 'all injected requests must settle')
  })
  const globals = {
    ref: vue.ref, computed: vue.computed, importMeta: { env: { DEV: false } },
    fetch: (path, init = {}) => {
      if ((init.method || 'GET') !== 'GET' || !/^\/api\/access\/users\/(101|202)$/.test(path)) {
        const message = `Forbidden synthetic transport: ${init.method || 'GET'} ${path}`
        violations.push(message)
        throw new Error(message)
      }
      let resolve
      const promise = new Promise(done => { resolve = done })
      const request = {
        path, settled: false,
        respond(body, status = 200) {
          assert.equal(request.settled, false, 'a deferred response can settle only once')
          request.settled = true
          resolve(Response.json(body, { status }))
        },
      }
      requests.push(request)
      return promise
    },
  }
  const preferences = { startedLoads: new Set() }
  function dependency(name) {
    if (name === 'vue') return vue
    if (name.endsWith('useConsolePreferences')) return preferences
    if (name.endsWith('operator-config')) return { operatorConfig: { apiBase: '/api' } }
    assert.ok(['./useOperatorAccess', './useOperatorApi', './useApi'].includes(name), `Unexpected module: ${name}`)
    if (!modules.has(name)) modules.set(name, evaluate(readFileSync(new URL(`composables/${name.slice(2)}.ts`, root), 'utf8'), dependency, globals))
    return modules.get(name)
  }
  const create = () => scope.run(() => dependency('./useOperatorAccess').useOperatorAccess())
  const state = create()
  function open(userID, instance = state) {
    const count = requests.length
    const done = instance.openUser(userID)
    assert.equal(requests.length, count + 1, 'public openUser must reach the injected real API boundary')
    const request = requests.at(-1)
    assert.equal(request.path, `/api/access/users/${userID}`)
    return { done, request }
  }
  return { state, create, open, requests }
}

function payload(userID, revision = 'current') {
  return {
    user: { id: userID, email: `user-${userID}@example.test`, role: 'viewer', disabled: false, created_at: revision },
    sessions: [], invitations_created: [], invitations_used: [], audit: [],
  }
}

function snapshot(state) {
  return JSON.parse(JSON.stringify({ selected: state.selectedUserID.value, data: state.drilldown.value, state: state.drilldownState.value }))
}

async function succeed(operation, userID, revision = 'current') {
  operation.request.respond(payload(userID, revision))
  await operation.done
}

for (const outcome of ['success', 'error']) {
  test(`a late ${outcome} cannot replace the latest selected user's live result`, async (t) => {
    const f = fixture(t)
    const older = f.open(101)
    const current = f.open(202)
    await succeed(current, 202)
    const expected = snapshot(f.state)
    assert.equal(expected.data.user.id, 202)
    assert.equal(expected.state.kind, 'live')
    if (outcome === 'success') await succeed(older, 101, 'obsolete')
    else { older.request.respond({ error: 'synthetic unavailable' }, 503); await older.done }
    assert.deepEqual(snapshot(f.state), expected, 'obsolete completion must not change current data or state')
  })

  test(`closing the panel invalidates a pending ${outcome}`, async (t) => {
    const f = fixture(t)
    const pending = f.open(101)
    f.state.closeUser()
    const closed = snapshot(f.state)
    assert.equal(closed.selected, null)
    assert.equal(closed.data.user, null)
    assert.equal(closed.state.kind, 'live')
    if (outcome === 'success') await succeed(pending, 101)
    else { pending.request.respond({ error: 'synthetic unavailable' }, 503); await pending.done }
    assert.deepEqual(snapshot(f.state), closed, 'closed data and state must remain empty after late completion')
  })

  test(`same-user close/reopen across composable instances rejects the old ${outcome} (ABA)`, async (t) => {
    const f = fixture(t)
    const other = f.create()
    assert.equal(other.selectedUserID, f.state.selectedUserID, 'both instances share the selection ref')
    const older = f.open(101)
    other.closeUser()
    const reopened = f.open(101, other)
    await succeed(reopened, 101, 'reopened')
    const expected = snapshot(other)
    assert.equal(expected.data.user.createdAt, 'reopened')
    if (outcome === 'success') await succeed(older, 101, 'before-close')
    else { older.request.respond({ error: 'synthetic unavailable' }, 503); await older.done }
    assert.deepEqual(snapshot(f.state), expected, 'request lifetime must be shared, not scoped to a composable instance or user ID')
  })
}

test('selecting a different user never attributes the previous snapshot to the pending selection', async (t) => {
  const f = fixture(t)
  await succeed(f.open(101), 101)
  const next = f.open(202)
  const pending = snapshot(f.state)
  await succeed(next, 202)
  assert.equal(pending.selected, 202)
  assert.equal(pending.state.kind, 'pending')
  assert.equal(pending.data.user, null, 'new selection must not expose the previous user through drilldown')
  assert.equal(pending.state.data?.user ?? null, null, 'pending load-state snapshot must not carry the previous user')
  for (const key of ['sessions', 'invitationsCreated', 'invitationsUsed', 'audit']) {
    assert.deepEqual(pending.data[key], [])
    assert.deepEqual(pending.state.data?.[key] ?? [], [])
  }
})

test('same-user refresh retains its proven snapshot and maps the current error with a working retry', async (t) => {
  const f = fixture(t)
  await succeed(f.open(101), 101, 'proven')
  const proven = snapshot(f.state).data
  assert.equal(proven.user.email, 'user-101@example.test')
  assert.equal(proven.user.createdAt, 'proven')
  const refresh = f.open(101)
  const pending = snapshot(f.state)
  refresh.request.respond({ error: 'synthetic unavailable' }, 503)
  await refresh.done
  assert.equal(pending.state.kind, 'pending')
  assert.deepEqual(pending.data, proven)
  assert.deepEqual(pending.state.data, proven)
  const failed = f.state.drilldownState.value
  assert.equal(failed.kind, 'error')
  assert.equal(failed.error.status, 503)
  assert.equal(failed.error.category, 'server-unavailable')
  assert.equal(failed.error.source, 'access-user-drilldown')
  assert.equal(failed.error.path, '/api/access/users/101')
  assert.equal(failed.error.method, 'GET')
  assert.equal(failed.error.retryable, true)
  assert.deepEqual(snapshot(f.state).data, proven)
  assert.deepEqual(JSON.parse(JSON.stringify(failed.data)), proven)
  const count = f.requests.length
  const retry = failed.retry.run()
  assert.equal(f.requests.length, count + 1)
  const request = f.requests.at(-1)
  assert.equal(request.path, '/api/access/users/101')
  const retryPending = snapshot(f.state)
  request.respond(payload(101, 'recovered'))
  const recovered = await retry
  assert.equal(retryPending.state.kind, 'pending')
  assert.equal(recovered.kind, 'live')
  assert.equal(recovered.data.user.createdAt, 'recovered')
  assert.equal(f.state.drilldown.value.user.createdAt, 'recovered')
})
