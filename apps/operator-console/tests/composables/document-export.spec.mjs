import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import test from 'node:test'
import { fileURLToPath } from 'node:url'
import { compileScript, parse } from '@vue/compiler-sfc'
import ts from 'typescript'
import * as vue from 'vue'

const pagePath = new URL('../../pages/documents.vue', import.meta.url)
const apiPath = new URL('../../composables/useApi.ts', import.meta.url)
const artifact = {
  downloadUrl: '/api/documents/exports/b857ebf7-c1cf-4a1f-a733-465ee492d3cb',
  filename: 'synthetic-export.json', contentType: 'application/json', byteLength: 32,
}
const document = { id: '7', path: 'synthetic.md', project: 'canonical-test', version: 1, content: 'Synthetic fixture only.' }

function evaluateModule(source, require, globals = {}) {
  const module = { exports: {} }
  const code = ts.transpileModule(source, {
    compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022 },
  }).outputText
  new Function('require', 'module', 'exports', ...Object.keys(globals), code)(require, module, module.exports, ...Object.values(globals))
  return module.exports
}

const { parseMutationResponse } = evaluateModule(readFileSync(apiPath, 'utf8'), () => { throw new Error('Unexpected mutation-parser dependency') })

async function operationResult(status, body) {
  const request = { requestId: 'synthetic-request', action: 'document-export', intent: { action: 'export', target: { documentId: 7, version: 1 } } }
  const mutation = await parseMutationResponse(request, new Response(JSON.stringify(body), {
    status, headers: { 'content-type': 'application/json' },
  }), () => undefined)
  return { mutation, artifact }
}

const verified = () => operationResult(200, {
  operation_state: 'completed',
  readback: { authoritative: true, kind: 'non_disclosing', operation_status: 'export_ready' },
})

async function mountConsumer(t) {
  const project = vue.ref('canonical-test')
  const route = vue.reactive({ query: {}, path: '/documents' })
  const primaryDocument = vue.ref(null)
  const primaryVersion = vue.ref(null)
  const primaryState = vue.ref({ kind: 'empty' })
  let operation = verified
  const docs = {
    documents: [], history: [], comments: [],
    primaryVersion, primaryDocument, primaryState,
    secondaryVersion: vue.ref(null), secondaryDocument: vue.ref(null),
    documentsState: vue.ref({ kind: 'empty' }), historyState: vue.ref({ kind: 'empty' }),
    secondaryState: vue.ref({ kind: 'empty' }), commentsState: vue.ref({ kind: 'empty' }),
    total: vue.ref(0), hasMore: vue.ref(false),
    refresh: async () => { },
    clearDocumentDetail: () => { primaryDocument.value = null; primaryVersion.value = null },
    openDocument: async () => { },
    selectPrimaryVersion: async (version) => { primaryVersion.value = version; primaryDocument.value = { ...document, version } },
    selectSecondaryVersion: async () => { },
    runDocumentSelectionOperation: () => operation(),
  }
  const context = { project, to: (path) => ({ path, query: {} }), remember: () => { } }
  const descriptor = parse(readFileSync(pagePath, 'utf8'), { filename: fileURLToPath(pagePath) }).descriptor
  const script = compileScript(descriptor, { id: 'document-export-consumer' })
  const component = evaluateModule(script.content, (name) => {
    if (name === 'vue') return vue
    if (name.endsWith('useOperatorDocuments')) return { useOperatorDocuments: () => docs }
    if (name.endsWith('useOperatorProjectContext')) return { useOperatorProjectContext: () => context, projectName: (id) => id }
    if (name.endsWith('useOperatorApi')) return { operatorApiUrl: (path) => path, operatorDiagnosisKey: () => 'synthetic-error' }
    throw new Error(`Unexpected page dependency: ${name}`)
  }, { useI18n: () => ({ t: (key) => key, locale: vue.ref('en') }), useRoute: () => route, useRouter: () => ({ push: async () => { } }) }).default
  component.render = () => null
  const createNode = () => ({ children: [], parent: null })
  const renderer = vue.createRenderer({
    createElement: createNode, createText: createNode, createComment: createNode,
    setText: () => { }, setElementText: () => { }, patchProp: () => { },
    parentNode: (node) => node.parent, nextSibling: () => null,
    insert: (node, parent) => { node.parent = parent; parent.children.push(node) },
    remove: (node) => { if (node.parent) node.parent.children = node.parent.children.filter((child) => child !== node) },
  })
  const app = renderer.createApp(component)
  const instance = app.mount(createNode())
  t.after(() => app.unmount())
  await vue.nextTick()
  primaryDocument.value = { ...document }
  primaryVersion.value = 1
  primaryState.value = { kind: 'live' }
  await vue.nextTick()
  return { state: instance.$.setupState, route, project, primaryDocument, primaryVersion, primaryState, setOperation: (next) => { operation = next } }
}

for (const [label, status, body] of [
  ['accepted', 202, { operation_state: 'accepted' }],
  ['unverified completion', 200, { operation_state: 'completed' }],
]) {
  test(`export consumer withholds an artifact after ${label}`, async (t) => {
    const consumer = await mountConsumer(t)
    consumer.setOperation(() => operationResult(status, body))
    await consumer.state.exportDocument()
    assert.equal(consumer.state.mutationResult.kind, 'committed_verification_pending')
    assert.equal(consumer.state.exportArtifact, null)
  })
}

test('new export clears a prior verified artifact before waiting and keeps it hidden on failure', async (t) => {
  const consumer = await mountConsumer(t)
  await consumer.state.exportDocument()
  assert.deepEqual(consumer.state.exportArtifact, artifact)
  let reject
  consumer.setOperation(() => new Promise((_, fail) => { reject = fail }))
  const attempt = consumer.state.exportDocument()
  assert.equal(consumer.state.exportArtifact, null)
  reject(new Error('Synthetic transport failure'))
  await attempt
  assert.equal(consumer.state.exportArtifact, null)
})

for (const changedTarget of ['scope', 'path', 'version', 'reader error']) {
  test(`verified export disappears on ${changedTarget} change and a late result cannot restore it`, async (t) => {
    const consumer = await mountConsumer(t)
    await consumer.state.exportDocument()
    assert.deepEqual(consumer.state.exportArtifact, artifact)
    let resolve
    consumer.setOperation(() => new Promise((accept) => { resolve = accept }))
    const attempt = consumer.state.exportDocument()
    if (changedTarget === 'scope') consumer.project.value = 'other-canonical-test'
    if (changedTarget === 'path') consumer.route.query.path = 'other-synthetic.md'
    if (changedTarget === 'version') consumer.primaryVersion.value = 2
    if (changedTarget === 'reader error') consumer.primaryState.value = { kind: 'error' }
    await vue.nextTick()
    assert.equal(consumer.state.exportArtifact, null)
    resolve(await verified())
    await attempt
    assert.equal(consumer.state.exportArtifact, null)
  })
}
