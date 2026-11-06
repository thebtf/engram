import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import test from 'node:test'
import ts from 'typescript'
import * as vue from 'vue'
import * as routing from 'vue-router'
import { createI18n, useI18n } from 'vue-i18n'
import { compileScript, compileTemplate, parse } from '@vue/compiler-sfc'

const root = new URL('../../', import.meta.url)

// Same compiled-SFC/custom-renderer method as auth-session.spec.mjs; no browser or backend.
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
      })]
    },
  }).outputText
  new Function('require', 'module', 'exports', ...Object.keys(globals), code)(require, module, module.exports, ...Object.values(globals))
  return module.exports
}

function node(tag = '', text = '') { return { tag, text, props: {}, children: [], parent: null, focus() { } } }
function text(item) { return item.text + item.children.map(text).join(' ') }
function descendants(item) { return [item, ...item.children.flatMap(descendants)] }
function fixture(t, update = { available: false }, selfcheck = { overall: 'healthy', components: [] }, options = {}) {
  const modules = new Map()
  const calls = []
  let config = { memory: { inject_unified: true } }
  let migrations = { engine: 'synthetic-engine', current_version: '17' }
  const scope = vue.effectScope()
  t.after(() => scope.stop())
  const i18n = options.locale ? createI18n({
    legacy: false, locale: options.locale, fallbackLocale: false,
    messages: { [options.locale]: JSON.parse(readFileSync(new URL(`i18n/locales/${options.locale}.json`, root), 'utf8')) },
  }) : null
  const globals = {
    computed: vue.computed, ref: vue.ref, watch: vue.watch, nextTick: vue.nextTick, onBeforeUnmount: vue.onBeforeUnmount,
    importMeta: { env: { DEV: false } },
    useI18n: i18n ? useI18n : () => ({ t: (key) => key, locale: vue.ref('en') }),
    useRoute: () => ({ fullPath: '/health', path: '/health' }),
    fetch: async (path, init = {}) => {
      assert.equal(init.method || 'GET', 'GET', 'the fixture must never perform a writer action')
      calls.push(path)
      const bodies = {
        '/api/selfcheck': selfcheck,
        '/api/ready': { status: 'ready' },
        '/api/config': config,
        '/api/flags': { items: [{ name: 'synthetic-neighbor', enabled: true, source: 'env', category: 'neighbor' }] },
        '/api/stats/vnext': { embedding: { chunk_count: 73, dimension: 384 }, noise_ratio: 0.125 },
        '/api/vector/metrics': { enabled: true, message: 'synthetic-vector-neighbor' },
        '/api/update/status': { state: 'idle' },
        '/api/update/check': update,
        '/api/migrations': migrations,
      }
      assert.ok(Object.hasOwn(bodies, path), `Unexpected transport request: ${path}`)
      const payload = bodies[path]
      const body = typeof payload === 'function' ? await payload() : payload
      return body instanceof Response ? body : Response.json(body)
    },
  }
  function dependency(name) {
    if (name === 'vue') return vue
    if (name === 'vue-router') return routing
    // Only unrelated browser preference/locale setup is replaced.
    if (name.endsWith('useConsolePreferences')) return preferences
    if (name.endsWith('i18n.config')) return { locales: [{ code: options.locale || 'en' }], setLocale() { } }
    if (name.endsWith('operator-config')) return { operatorConfig: { apiBase: '/api' } }
    const url = new URL(`${name}.ts`, new URL('composables/', root))
    return load(url)
  }
  function load(url) {
    if (!modules.has(url.href)) modules.set(url.href, evaluate(readFileSync(url, 'utf8'), (name) => {
      if (!name.startsWith('.')) return dependency(name)
      if (name.endsWith('operator-config') || name.endsWith('useConsolePreferences') || name.endsWith('i18n.config')) return dependency(name)
      return load(new URL(`${name}.ts`, url))
    }, globals))
    return modules.get(url.href)
  }
  const preferences = { startedLoads: new Set(), density: vue.ref('compact'), theme: vue.ref('dark') }
  const state = scope.run(() => dependency('./useOperatorHealthSettings').useOperatorHealthSettings())
  Object.assign(globals, {
    useOperatorHealthSettings: () => dependency('./useOperatorHealthSettings').useOperatorHealthSettings(),
    useOperatorShellStatus: () => dependency('./useOperatorShell').useOperatorShellStatus(),
    useSettingsModal: () => dependency('./useSettingsModal').useSettingsModal(),
  })
  function mount(path, props = {}) {
    const descriptor = parse(readFileSync(new URL(path, root), 'utf8')).descriptor
    const script = compileScript(descriptor, { id: path })
    const template = compileTemplate({
      source: descriptor.template.content, filename: path, id: path, isTS: true,
      compilerOptions: { bindingMetadata: script.bindings, directiveTransforms: { model: () => ({ props: [] }) } }
    })
    assert.deepEqual(template.errors, [])
    const component = evaluate(script.content, dependency, globals).default
    component.render = evaluate(template.code, dependency, globals).render
    const host = node('root')
    const renderer = vue.createRenderer({
      createElement: (tag) => node(tag), createText: (value) => node('', value), createComment: () => node(),
      patchProp: (item, key, _previous, value) => { item.props[key] = value },
      setText: (item, value) => { item.text = value }, setElementText: (item, value) => { item.children = [node('', value)] },
      parentNode: (item) => item.parent,
      nextSibling: (item) => item.parent?.children[item.parent.children.indexOf(item) + 1] || null,
      querySelector: (selector) => { assert.equal(selector, 'body'); return host },
      insert: (item, parent, anchor) => {
        if (item.parent) item.parent.children = item.parent.children.filter(child => child !== item)
        item.parent = parent
        const index = anchor ? parent.children.indexOf(anchor) : -1
        if (index < 0) parent.children.push(item); else parent.children.splice(index, 0, item)
      },
      remove: (item) => { if (item.parent) item.parent.children = item.parent.children.filter(child => child !== item) },
    })
    const app = renderer.createApp(component, props)
    if (i18n) app.use(i18n)
    for (const name of ['HonestyBadge', 'MutationResultNotice']) app.component(name, { render: () => null })
    app.mount(host)
    t.after(() => app.unmount())
    return host
  }
  return { state, calls, mount, translate: i18n ? i18n.global.t : (key) => key, setUpdate(payload) { update = payload }, setMigrations(payload) { migrations = payload }, setConfig(payload) { config = payload } }
}

function updateAnswer(host, consumer, translate = (key) => key) {
  const label = translate(consumer === 'Health' ? 'health.updateAvailable' : 'settings.restart.updateAvailable')
  if (consumer === 'Health') {
    const row = descendants(host).find(item => item.tag === 'div' && item.children.some(child => child.tag === 'span' && text(child) === label))
    assert.ok(row, 'actual compiled Health update row must render')
    return text(row.children.find(child => child.tag === 'strong')).trim()
  }
  const paragraph = descendants(host).find(item => item.tag === 'p' && text(item).includes(label))
  assert.ok(paragraph, 'actual compiled Settings update paragraph must render')
  return text(paragraph.children.filter(child => child.tag === 'code').at(-1)).trim()
}

for (const consumer of ['Health', 'Settings']) {
  for (const [name, payload, expected] of [
    ['successful explicit false', { available: false }, 'common.no'],
    ['successful explicit true', { available: true }, 'common.yes'],
    ['HTTP 200 domain error', { available: false, current_version: 'synthetic', error: 'synthetic release lookup failed', rate_limited: true }, null],
    ['missing availability', {}, null],
    ['invalid string availability', { available: 'false' }, null],
    ['invalid null availability', { available: null }, null],
    ['HTTP unavailable', Response.json({ error: 'synthetic unavailable' }, { status: 503 }), null],
  ]) test(`${consumer}: ${name} uses actual shared result and compiled presentation`, async (t) => {
    const f = fixture(t, payload)
    await f.state.refresh()
    assert.equal(f.calls.filter(path => path === '/api/update/check').length, 1)
    const host = consumer === 'Health' ? f.mount('pages/health.vue') : f.mount('components/SettingsModal.vue', { open: true, activeTab: 'actions' })
    await vue.nextTick()
    const answer = updateAnswer(host, consumer)
    t.diagnostic(JSON.stringify({ consumer, case: name, transportStatus: payload instanceof Response ? payload.status : 200, stateKind: f.state.updateCheckState.value.kind, renderedAnswer: answer }))
    if (expected) assert.equal(answer, expected)
    else assert.ok(answer !== 'common.no' && answer !== 'common.yes', `${name} is unknown, not a successful yes/no answer; rendered ${answer}`)
  })
}

for (const boundary of ['pending', 'unavailable', 'live-empty', 'live-components']) {
  test(`Health components: ${boundary} preserves neighboring evidence`, async (t) => {
    const selfcheck = boundary === 'unavailable' ? Response.json({ error: 'synthetic selfcheck unavailable' }, { status: 503 })
      : { overall: 'healthy', components: boundary === 'live-components' ? [{ name: 'synthetic-database', status: 'healthy' }] : [] }
    const f = fixture(t, { available: false }, selfcheck)
    if (boundary !== 'pending') await f.state.refresh()
    else {
      // First load is genuinely pending, not an invented API response or mapped component array.
      assert.equal(f.state.selfcheckState.value.kind, 'pending')
    }
    const host = f.mount('pages/health.vue')
    await vue.nextTick()
    const rendered = text(host)
    const empty = rendered.includes('health.emptyComponents')
    t.diagnostic(JSON.stringify({ boundary, stateKind: f.state.selfcheckState.value.kind, componentCount: f.state.components.value.length, renderedEmpty: empty }))
    if (boundary !== 'pending') {
      assert.ok(rendered.includes('73'), 'live embedding neighbor remains readable')
      assert.ok(rendered.includes('synthetic-vector-neighbor'), 'live vector neighbor remains readable')
      assert.ok(rendered.includes('synthetic-engine'), 'migration evidence remains readable')
      t.diagnostic('PASS: embedding/vector/migration neighbors retained')
    }
    if (boundary === 'pending' || boundary === 'unavailable') {
      const heading = descendants(host).find(item => item.tag === 'h2' && text(item).trim() === 'health.components')
      assert.ok(heading, 'the actual compiled components heading must render')
      let card = heading.parent
      while (card && card.tag !== 'article') card = card.parent
      assert.ok(card, 'the rendered components heading must belong to its card')
      const status = descendants(card).find(item => item.props.role === 'status' || item.props.role === 'alert')
      t.diagnostic(JSON.stringify({ boundary, inCardStatusRole: status?.props.role ?? null, inCardStatusContent: status ? text(status).trim() : null }))
      assert.ok(status, `${boundary} components card must expose an accessible status or alert`)
      const message = text(status).trim()
      assert.ok(message.length > 0, `${boundary} components status must contain readable content`)
      assert.match(message, boundary === 'pending' ? /\bhealth\.state\.pending\b/ : /\bhealth\.state\.(?:error|notLoaded)\b/,
        `${boundary} components status must communicate its state through localized content`)
    }
    if (boundary === 'live-empty') assert.equal(empty, true, 'a real live empty list remains empty')
    else if (boundary === 'live-components') {
      assert.equal(empty, false)
      assert.ok(rendered.includes('synthetic-database'))
    } else assert.equal(empty, false, `${boundary} selfcheck is not a live empty component list`)
  })
}

function deferred() {
  let resolve
  const promise = new Promise((settle) => { resolve = settle })
  return { promise, resolve }
}

function refreshControl(host, consumer, translate = (key) => key) {
  const label = translate(consumer === 'Health' ? 'health.refresh' : 'settings.refresh')
  const buttons = descendants(host).filter(item => item.tag === 'button' && text(item).trim() === label)
  assert.equal(buttons.length, 1, 'the actual consumer must expose one matching read-only Refresh control')
  assert.equal(typeof buttons[0].props.onClick, 'function', 'Refresh must retain its actual compiled handler')
  return buttons[0]
}

function refreshPresentation(host, consumer) {
  return {
    answer: updateAnswer(host, consumer),
    refreshDisabled: Boolean(refreshControl(host, consumer).props.disabled),
    ...(consumer === 'Health' ? {
      pendingVisible: descendants(host).some(item => item.tag === 'div' && text(item).trim() === 'health.state.pending'),
    } : {}),
  }
}

for (const consumer of ['Health', 'Settings']) {
  for (const initialAvailable of [false, true]) {
    for (const completion of ['success', 'domain-error']) {
      test(`${consumer} refresh: live ${initialAvailable ? 'yes' : 'no'} waits honestly for ${completion}`, { timeout: 5000 }, async (t) => {
        const f = fixture(t, { available: initialAvailable })
        await f.state.refresh()
        const host = consumer === 'Health' ? f.mount('pages/health.vue') : f.mount('components/SettingsModal.vue', { open: true, activeTab: 'actions' })
        await vue.nextTick()
        assert.deepEqual(refreshPresentation(host, consumer), {
          answer: initialAvailable ? 'common.yes' : 'common.no',
          refreshDisabled: false,
          ...(consumer === 'Health' ? { pendingVisible: false } : {}),
        }, 'the initial successful answer and enabled Refresh must actually render')

        const entered = deferred()
        const response = deferred()
        const migrationsEntered = deferred()
        const migrationsResponse = deferred()
        const nextUpdate = completion === 'success' ? { available: !initialAvailable } : {
          available: false, error: 'synthetic refresh release lookup failed',
        }
        f.setUpdate(() => {
          entered.resolve()
          return response.promise
        })
        f.setMigrations(() => {
          migrationsEntered.resolve()
          return migrationsResponse.promise
        })
        const refreshRun = refreshControl(host, consumer).props.onClick()
        let waiting
        let afterUpdate
        try {
          await entered.promise
          await vue.nextTick()
          waiting = refreshPresentation(host, consumer)
          response.resolve(nextUpdate)
          await migrationsEntered.promise
          await vue.nextTick()
          afterUpdate = refreshPresentation(host, consumer)
        } finally {
          response.resolve(nextUpdate)
          migrationsResponse.resolve({ engine: 'synthetic-engine', current_version: '17' })
          await refreshRun
          await vue.nextTick()
        }

        const settled = refreshPresentation(host, consumer)
        assert.equal(f.calls.filter(path => path === '/api/update/check').length, 2, 'only initial load and the user Refresh may check updates')
        assert.equal(f.calls.filter(path => path === '/api/migrations').length, 2, 'the same user Refresh must reach and finish its late migrations request')
        t.diagnostic(JSON.stringify({ consumer, initialAvailable, completion, transportStatus: 200, waiting, afterUpdate, settled, settledKind: f.state.updateCheckState.value.kind }))
        assert.deepEqual(settled, {
          answer: completion === 'domain-error' ? '—' : initialAvailable ? 'common.no' : 'common.yes',
          refreshDisabled: false,
          ...(consumer === 'Health' ? { pendingVisible: false } : {}),
        }, 'success or error must replace the old answer and restore the actual Refresh control')
        assert.deepEqual({ waiting, afterUpdate }, {
          waiting: {
            answer: '—',
            refreshDisabled: true,
            ...(consumer === 'Health' ? { pendingVisible: true } : {}),
          },
          afterUpdate: {
            answer: completion === 'domain-error' ? '—' : initialAvailable ? 'common.no' : 'common.yes',
            refreshDisabled: true,
            ...(consumer === 'Health' ? { pendingVisible: true } : {}),
          },
        }, 'Refresh must hide the unresolved old answer and remain busy until its late migrations response finishes')
      })
    }
  }
}

test('Shared refresh overlap: older Health completion cannot replace the Settings recheck or clear its busy controls', { timeout: 5000 }, async (t) => {
  const f = fixture(t, { available: false })
  await f.state.refresh()
  const health = f.mount('pages/health.vue')
  const settings = f.mount('components/SettingsModal.vue', { open: true, activeTab: 'actions' })
  await vue.nextTick()
  const presentation = () => ({ Health: refreshPresentation(health, 'Health'), Settings: refreshPresentation(settings, 'Settings') })
  const olderEntered = deferred()
  const olderResponse = deferred()
  const newerEntered = deferred()
  const newerResponse = deferred()
  const newerMigrationsEntered = deferred()
  const newerMigrationsResponse = deferred()
  let updateCalls = 0
  let migrationCalls = 0
  f.setUpdate(() => {
    const entered = updateCalls++ === 0 ? olderEntered : newerEntered
    entered.resolve()
    return updateCalls === 1 ? olderResponse.promise : newerResponse.promise
  })
  f.setMigrations(() => {
    if (migrationCalls++ !== 0) return { engine: 'synthetic-engine', current_version: '17' }
    newerMigrationsEntered.resolve()
    return newerMigrationsResponse.promise
  })
  const olderRun = refreshControl(health, 'Health').props.onClick()
  let newerRun
  let atNewerMigrations
  let afterOlderCompletion
  try {
    await olderEntered.promise
    // Settings post-save/recheck can call the public refresh while a UI refresh is busy.
    newerRun = f.state.refresh()
    await newerEntered.promise
    newerResponse.resolve({ available: false })
    await newerMigrationsEntered.promise
    await vue.nextTick()
    atNewerMigrations = presentation()
    olderResponse.resolve({ available: true })
    await olderRun
    await vue.nextTick()
    afterOlderCompletion = presentation()
  } finally {
    olderResponse.resolve({ available: true })
    newerResponse.resolve({ available: false })
    newerMigrationsResponse.resolve({ engine: 'synthetic-engine', current_version: '17' })
    await Promise.all([olderRun, newerRun])
    await vue.nextTick()
  }
  const settled = presentation()
  assert.equal(updateCalls, 2, 'a later programmatic recheck must not be swallowed by single-flight')
  t.diagnostic(JSON.stringify({ consumer: 'Shared', atNewerMigrations, afterOlderCompletion, settled, updateCalls }))
  const busy = {
    Health: { answer: 'common.no', refreshDisabled: true, pendingVisible: true },
    Settings: { answer: 'common.no', refreshDisabled: true },
  }
  assert.deepEqual({ atNewerMigrations, afterOlderCompletion, settled }, {
    atNewerMigrations: busy,
    afterOlderCompletion: busy,
    settled: {
      Health: { answer: 'common.no', refreshDisabled: false, pendingVisible: false },
      Settings: { answer: 'common.no', refreshDisabled: false },
    },
  }, 'both actual consumers must retain the latest recheck result and busy ownership through its final response')
})

test('Settings config overlap: an older config response cannot replace a completed post-save recheck snapshot', { timeout: 5000 }, async (t) => {
  const f = fixture(t)
  const previousConfig = { memory: { inject_unified: true }, features: { enforce_source_project: false } }
  const newerConfig = { memory: { inject_unified: true }, features: { enforce_source_project: true } }
  f.setConfig(previousConfig)
  await f.state.refresh()
  const settings = f.mount('components/SettingsModal.vue', { open: true, activeTab: 'runtime' })
  await vue.nextTick()
  const label = 'features.enforce_source_project'
  function presentation() {
    const section = descendants(settings).find(item => item.tag === 'section' && item.children.some(child => child.tag === 'div' && text(child).trim() === 'settings.configSnapshot'))
    assert.ok(section, 'the actual compiled Settings runtime config snapshot must render')
    const rows = descendants(section).filter(item => item.tag === 'div' && item.children.some(child => child.tag === 'span' && text(child).trim() === label))
    assert.equal(rows.length, 1, 'the actual snapshot must expose one matching feature label/value row')
    const value = rows[0].children.find(child => child.tag === 'strong')
    assert.ok(value, 'the snapshot value must render beside its real feature label')
    return {
      label,
      value: text(value).trim(),
      pendingVisible: descendants(settings).some(item => item.tag === 'div' && text(item).trim() === 'settings.state.pending'),
    }
  }
  const initial = presentation()
  assert.deepEqual(initial, { label, value: 'false', pendingVisible: false })
  const olderEntered = deferred()
  const olderResponse = deferred()
  let configCalls = 0
  f.setConfig(() => {
    if (configCalls++ !== 0) return newerConfig
    olderEntered.resolve()
    return olderResponse.promise
  })
  const olderRun = f.state.refresh()
  let newerRun
  let whileOlderConfigHeld
  let afterNewerCompletion
  try {
    await olderEntered.promise
    await vue.nextTick()
    whileOlderConfigHeld = presentation()
    newerRun = f.state.refresh()
    await newerRun
    await vue.nextTick()
    afterNewerCompletion = presentation()
  } finally {
    olderResponse.resolve(previousConfig)
    await Promise.all([olderRun, newerRun])
    await vue.nextTick()
  }
  const afterOlderCompletion = presentation()
  assert.equal(configCalls, 2, 'the later public post-save recheck must issue its own config read')
  assert.equal(f.calls.filter(path => path === '/api/config').length, 3, 'only initial load and the two public refreshes may read config')
  t.diagnostic(JSON.stringify({ consumer: 'Settings runtime config', initial, whileOlderConfigHeld, afterNewerCompletion, afterOlderCompletion, configCalls }))
  assert.deepEqual({ whileOlderConfigHeld, afterNewerCompletion, afterOlderCompletion }, {
    whileOlderConfigHeld: { label, value: 'false', pendingVisible: true },
    afterNewerCompletion: { label, value: 'true', pendingVisible: false },
    afterOlderCompletion: { label, value: 'true', pendingVisible: false },
  }, 'the actual runtime snapshot must retain the completed newer config after the older response arrives')
})

for (const [boundary, selfcheck] of [
  ['missing', { overall: 'healthy' }],
  ['null', { overall: 'healthy', components: null }],
]) {
  test(`Health components: live ${boundary} list is unknown, not server-returned empty`, async (t) => {
    const f = fixture(t, { available: false }, selfcheck)
    await f.state.refresh()
    assert.equal(f.calls.filter(path => path === '/api/selfcheck').length, 1)
    assert.equal(f.state.selfcheckState.value.kind, 'live', 'the successful selfcheck must reach the actual live state')
    assert.equal(Object.hasOwn(f.state.selfcheckState.value.data, 'components'), boundary === 'null', 'the transport must preserve missing versus explicit null')
    if (boundary === 'null') assert.equal(f.state.selfcheckState.value.data.components, null)
    const host = f.mount('pages/health.vue')
    await vue.nextTick()
    const heading = descendants(host).find(item => item.tag === 'h2' && text(item).trim() === 'health.components')
    assert.ok(heading, 'the actual compiled components heading must render')
    let card = heading.parent
    while (card && card.tag !== 'article') card = card.parent
    assert.ok(card, 'the rendered components heading must belong to its card')
    const empty = text(card).includes('health.emptyComponents')
    const status = descendants(card).find(item => item.props.role === 'status' || item.props.role === 'alert')
    const message = status ? text(status).trim() : ''
    t.diagnostic(JSON.stringify({ boundary, transportStatus: 200, stateKind: f.state.selfcheckState.value.kind, componentCount: f.state.components.value.length, renderedEmpty: empty, inCardStatusRole: status?.props.role ?? null, inCardStatusContent: message }))
    assert.equal(empty, false, `a live selfcheck with components ${boundary} is not a server-returned empty component list`)
    assert.ok(status, `live ${boundary} components card must expose an accessible status or alert`)
    assert.ok(message.length > 0, `live ${boundary} components status must contain readable content`)
    assert.match(message, /\bhealth\.state\.(?:unknown|notLoaded)\b/, `live ${boundary} components status must communicate unknown or not-loaded state through localized content`)
  })
}

function updateRegion(host, consumer, translate) {
  const label = translate(consumer === 'Health' ? 'health.update' : 'settings.restart.updateAvailable')
  const anchor = descendants(host).find(item => consumer === 'Health'
    ? item.tag === 'h2' && text(item).trim() === label
    : item.tag === 'p' && text(item).includes(label))
  assert.ok(anchor, `${consumer}: update region must render`)
  let region = anchor.parent
  while (region && region.tag !== (consumer === 'Health' ? 'article' : 'section')) region = region.parent
  assert.ok(region, `${consumer}: update region must have its own container`)
  return region
}

function updateNotices(region) {
  return descendants(region).filter(item => ['status', 'alert'].includes(item.props.role)
    || ['polite', 'assertive'].includes(item.props['aria-live']))
    .map(item => text(item).trim())
}

for (const consumer of ['Health', 'Settings']) {
  for (const [failure, payload, diagnosisKey] of [
    ['domain failure', { available: false, error: 'private provider detail' }, null],
    ['HTTP 503', () => Response.json({ error: 'private provider detail' }, { status: 503 }), 'errors.diagnosis.server-unavailable'],
    ['HTTP 401', () => Response.json({ error: 'private provider detail' }, { status: 401 }), 'errors.diagnosis.unauthorized'],
    ['HTTP 403', () => Response.json({ error: 'private provider detail' }, { status: 403 }), 'errors.diagnosis.forbidden'],
  ]) test(`${consumer}: ${failure} is accessible and clears after refresh`, async (t) => {
    const f = fixture(t, { available: true }, undefined, { locale: 'en' })
    await f.state.refresh()
    const host = consumer === 'Health' ? f.mount('pages/health.vue') : f.mount('components/SettingsModal.vue', { open: true, activeTab: 'actions' })
    await vue.nextTick()
    f.setUpdate(payload)
    await refreshControl(host, consumer, f.translate).props.onClick()
    await vue.nextTick()
    const failed = {
      kind: f.state.updateCheckState.value.kind,
      answer: updateAnswer(host, consumer, f.translate),
      notices: updateNotices(updateRegion(host, consumer, f.translate)),
      rawVisible: text(host).includes('private provider detail'),
      disabled: Boolean(refreshControl(host, consumer, f.translate).props.disabled),
    }
    f.setUpdate({ available: false })
    await refreshControl(host, consumer, f.translate).props.onClick()
    await vue.nextTick()
    const recovered = {
      kind: f.state.updateCheckState.value.kind,
      answer: updateAnswer(host, consumer, f.translate),
      notices: updateNotices(updateRegion(host, consumer, f.translate)),
      disabled: Boolean(refreshControl(host, consumer, f.translate).props.disabled),
    }
    t.diagnostic(JSON.stringify({ consumer, failure, failed, recovered }))
    const issues = []
    if (failed.kind !== 'error' || failed.answer !== '—') issues.push('failed availability must remain unknown')
    if (!failed.notices.length) issues.push('update failure needs an accessible notice')
    for (const notice of failed.notices) {
      if (!notice || /\b(?:health|settings|errors)\.[\w.-]+/.test(notice)) issues.push('notice must contain translated user-visible text')
      if (diagnosisKey && !notice.includes(f.translate(diagnosisKey))) issues.push(`notice must explain ${diagnosisKey}`)
      if (!diagnosisKey && !/update/i.test(notice)) issues.push('domain failure must explain the unavailable update check')
    }
    if (failed.rawVisible || failed.disabled) issues.push('provider detail must remain private and Refresh enabled')
    if (recovered.kind !== 'live' || recovered.answer !== f.translate('common.no') || recovered.notices.length || recovered.disabled) issues.push('successful refresh must clear the failure and show the actual answer')
    assert.equal(f.calls.filter(path => path === '/api/update/check').length, 3)
    assert.deepEqual(issues, [], `${consumer}: ${failure} must be explained accessibly and clear on success`)
  })
}

function runtimeFailurePresentation(host, translate) {
  const settingsPrefix = translate('settings.state.error', { message: 'SENTINEL' }).split('SENTINEL')[0]
  const updateLabel = 'Update check'
  const diagnosis = translate('errors.diagnosis.forbidden')
  return {
    settingsErrors: descendants(host).filter(item => item.tag === 'div' && item.children.every(child => !child.tag) && text(item).trim().startsWith(settingsPrefix)).map(item => text(item).trim()),
    updateAlerts: descendants(host).filter(item => item.props.role === 'alert' && text(item).includes(updateLabel) && text(item).includes(diagnosis)).map(item => text(item).trim()),
  }
}

for (const configFails of [false, true]) {
  test(`Settings Runtime: update HTTP 403 has its own accessible alert with config ${configFails ? 'unavailable' : 'loaded'}`, async (t) => {
    const f = fixture(t, () => Response.json({ error: 'private update detail' }, { status: 403 }), undefined, { locale: 'en' })
    if (configFails) f.setConfig(() => Response.json({ error: 'private config detail' }, { status: 503 }))
    await f.state.refresh()
    const host = f.mount('components/SettingsModal.vue', { open: true, activeTab: 'runtime' })
    await vue.nextTick()
    const actual = runtimeFailurePresentation(host, f.translate)
    t.diagnostic(JSON.stringify({ configFails, actual }))
    assert.deepEqual({ settingsErrorCount: actual.settingsErrors.length, updateAlerts: actual.updateAlerts }, {
      settingsErrorCount: configFails ? 1 : 0,
      updateAlerts: [`Update check: ${f.translate('errors.diagnosis.forbidden')}`],
    }, 'Runtime must independently report a settings load failure and an accessible localized update failure')
  })
}

test('Settings Runtime: update recovery clears only its alert while config remains unavailable', async (t) => {
  const f = fixture(t, () => Response.json({ error: 'private update detail' }, { status: 403 }), undefined, { locale: 'en' })
  f.setConfig(() => Response.json({ error: 'private config detail' }, { status: 503 }))
  await f.state.refresh()
  const host = f.mount('components/SettingsModal.vue', { open: true, activeTab: 'runtime' })
  await vue.nextTick()
  const failed = runtimeFailurePresentation(host, f.translate)
  f.setUpdate({ available: false })
  await f.state.refresh()
  await vue.nextTick()
  const recovered = runtimeFailurePresentation(host, f.translate)
  t.diagnostic(JSON.stringify({ failed, recovered }))
  assert.equal(failed.settingsErrors.length, 1, 'the config failure must remain visible as a localized settings error')
  assert.deepEqual({ failed, recovered }, {
    failed: { settingsErrors: failed.settingsErrors, updateAlerts: [`Update check: ${f.translate('errors.diagnosis.forbidden')}`] },
    recovered: { settingsErrors: failed.settingsErrors, updateAlerts: [] },
  }, 'successful update refresh must clear its alert without hiding or changing the continuing config failure')
})

for (const [locale, label, misleading] of [
  ['en', 'Update check', 'Update available'],
  ['ru', 'Проверка обновлений', 'Доступно обновление'],
  ['zh', '更新检查', '有可用更新'],
]) {
  for (const surface of ['Health', 'Settings Actions', 'Settings Runtime']) {
    test(`${surface}: ${locale} update failure uses a neutral alert label`, async (t) => {
      const f = fixture(t, () => Response.json({ error: 'private provider detail' }, { status: 403 }), undefined, { locale })
      await f.state.refresh()
      const host = surface === 'Health'
        ? f.mount('pages/health.vue')
        : f.mount('components/SettingsModal.vue', { open: true, activeTab: surface === 'Settings Actions' ? 'actions' : 'runtime' })
      await vue.nextTick()
      const alerts = descendants(host).filter(item => item.props.role === 'alert').map(item => text(item).trim())
      assert.deepEqual(alerts, [`${label}: ${f.translate('errors.diagnosis.forbidden')}`])
      assert.ok(alerts.every(alert => !alert.includes(misleading)), 'an unknown result must not assert an update is available')
      if (surface !== 'Settings Runtime') assert.equal(updateAnswer(host, surface === 'Health' ? 'Health' : 'Settings', f.translate), '—')
    })
  }
}
