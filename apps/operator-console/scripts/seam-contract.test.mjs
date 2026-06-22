import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'
import test from 'node:test'

const root = join(dirname(fileURLToPath(import.meta.url)), '..')
const nuxtConfigPath = join(root, 'nuxt.config.ts')
const seamPath = join(root, 'composables', 'useOperatorApi.ts')
const compatibilityPath = join(root, 'composables', 'useMockData.ts')
const memoryLabPath = join(root, 'composables', 'useOperatorMemoryLab.ts')
const memoryPagePath = join(root, 'pages', 'memory.vue')
const pageSizePath = join(root, 'composables', 'usePersistentPageSize.ts')

function read(path) {
  return readFileSync(path, 'utf8')
}

function functionBody(source, name) {
  const start = source.indexOf(`function ${name}`)
  assert.notEqual(start, -1, `${name} must be declared as a function`)

  const paramsOpen = source.indexOf('(', start)
  assert.notEqual(paramsOpen, -1, `${name} must declare parameters`)

  let parenDepth = 0
  let paramsClose = -1
  for (let index = paramsOpen; index < source.length; index += 1) {
    const char = source[index]
    if (char === '(') parenDepth += 1
    if (char === ')') {
      parenDepth -= 1
      if (parenDepth === 0) {
        paramsClose = index
        break
      }
    }
  }

  assert.notEqual(paramsClose, -1, `${name} parameter list is not balanced`)
  const open = source.indexOf('{', paramsClose)
  assert.notEqual(open, -1, `${name} must have a body`)

  let depth = 0
  for (let index = open; index < source.length; index += 1) {
    const char = source[index]
    if (char === '{') depth += 1
    if (char === '}') {
      depth -= 1
      if (depth === 0) return source.slice(open, index + 1)
    }
  }

  assert.fail(`${name} body is not balanced`)
}

test('load-state union covers every honest operator surface state', () => {
  const source = read(seamPath)
  const required = ['pending', 'error', 'empty', 'gated', 'stale', 'mustbuild', 'live']

  for (const state of required) {
    assert.match(source, new RegExp(`['"]${state}['"]`), `missing load state ${state}`)
  }

  assert.match(source, /export\s+type\s+OperatorLoadKind/, 'OperatorLoadKind must be exported')
  assert.match(source, /export\s+type\s+OperatorLoadState/, 'OperatorLoadState union must be exported')

  for (const factory of ['pendingState', 'errorState', 'emptyState', 'gatedState', 'staleState', 'mustBuildState', 'liveState']) {
    assert.match(source, new RegExp(`export function ${factory}\\b`), `${factory} must be exported`)
  }
})

test('fetch seam uses runtime config, same-origin default, retryable source errors, and real fetch failure checks', () => {
  const source = read(seamPath)

  assert.match(source, /DEFAULT_OPERATOR_API_BASE\s*=\s*['"]\/api['"]/, 'same-origin /api must be the default base')
  assert.match(source, /useRuntimeConfig\(\)\.public\.apiBase/, 'public apiBase runtime config must drive the wrapper')
  assert.match(source, /credentials:\s*['"]include['"]/, 'fetch must keep operator cookies')
  assert.match(source, /if\s*\(\s*!response\.ok\s*\)/, 'wrapper must reject HTTP failures')
  assert.match(source, /status:\s*response\.status/, 'HTTP status must be captured')
  assert.match(source, /source:/, 'source metadata must be carried')
  assert.match(source, /retry/, 'retry metadata must be carried')
})

test('fetch seam reads bodies safely instead of throwing browser-native JSON parse errors', () => {
  const source = read(seamPath)
  const compatibility = read(compatibilityPath)
  const operatorBody = functionBody(source, 'operatorFetchJson')
  const bodyDetailBody = functionBody(source, 'bodyDetail')
  const responseReaderBody = functionBody(source, 'readOperatorResponseText')
  const compatibilityBody = functionBody(compatibility, 'fetchJson')

  for (const [label, body] of [['useMockData fetchJson', compatibilityBody]]) {
    assert.match(body, /response\.text\(\)/, `${label} must read response body as text before parsing`)
    assert.match(body, /if\s*\(\s*!text\.trim\(\)\s*\)/, `${label} must tolerate empty 200 JSON bodies`)
    assert.match(body, /JSON\.parse\(text\)/, `${label} must parse JSON after the empty-body guard`)
    assert.doesNotMatch(body, /response\.json\(\)/, `${label} must not throw native Response.json parse errors`)
  }

  assert.match(operatorBody, /readOperatorResponseText\(response, path, source, method\)/, 'operatorFetchJson must read body through the safe reader')
  assert.match(responseReaderBody, /response\.text\(\)/, 'safe response reader must use response.text()')
  assert.match(operatorBody, /if\s*\(\s*!text\.trim\(\)\s*\)/, 'operatorFetchJson must tolerate empty 200 JSON bodies')
  assert.match(operatorBody, /JSON\.parse\(text\)/, 'operatorFetchJson must parse JSON after the empty-body guard')
  assert.doesNotMatch(operatorBody, /response\.json\(\)/, 'operatorFetchJson must not throw native Response.json parse errors')
  assert.match(operatorBody, /bodyDetail\(text\)/, 'HTTP errors must include response body details when present')
  assert.match(bodyDetailBody, /HTML error response/, 'HTML error pages must be summarized instead of rendered into the operator UI')
  assert.match(operatorBody, /Invalid JSON from/, 'invalid JSON must be reported with endpoint context')
})

test('mutation seam supports success, rollback, refresh, and honest mustbuild unsupported actions', () => {
  const source = read(seamPath)
  const unsupportedBody = functionBody(source, 'unsupportedOperatorAction')

  assert.match(source, /export\s+async\s+function\s+runOperatorMutation\b/, 'runOperatorMutation must be exported')
  assert.match(source, /snapshot/, 'mutation must allow snapshot capture')
  assert.match(source, /rollback/, 'mutation must expose rollback result/metadata')
  assert.match(source, /refresh/, 'mutation must support post-action refresh')
  assert.match(source, /kind:\s*['"]success['"]/, 'success result kind must exist')
  assert.match(source, /kind:\s*['"]rollback['"]/, 'rollback result kind must exist')

  assert.match(unsupportedBody, /kind:\s*['"]mustbuild['"]/, 'unsupported actions must be mustbuild descriptors')
  assert.match(unsupportedBody, /operable:\s*false/, 'unsupported actions must not be operable')
  assert.doesNotMatch(unsupportedBody, /\bexecute\s*:/, 'unsupported action must not expose a no-op executable')
})

test('useMockData remains an import-compatible ownership map for page CRs', () => {
  const source = read(compatibilityPath)

  assert.match(source, /OPERATOR_DATA_AREAS/, 'compatibility seam must publish area ownership')
  for (const exportedName of [
    'useMemories',
    'useRules',
    'useIssues',
    'useCreds',
    'useVaultStatus',
    'useProjects',
    'useHealthSnapshot',
    'useServerConfigSnapshot',
    'useServerInfo',
  ]) {
    assert.match(source, new RegExp(`export const ${exportedName}\\b`), `${exportedName} compatibility export must remain`)
  }
})

test('memory compatibility export uses the Memory Lab live state seam', () => {
  const source = read(compatibilityPath)
  const memoryExport = source.match(/export const useMemories = \(\) => \{[\s\S]*?\n\}/)

  assert.ok(memoryExport, 'useMemories compatibility export must exist')
  assert.match(source, /useOperatorMemoryLab/, 'useMockData must import the Memory Lab live seam')
  assert.match(memoryExport[0], /useOperatorMemoryLab\(\)\.rows/, 'useMemories must delegate to Memory Lab rows')
  assert.doesNotMatch(memoryExport[0], /live:memories/, 'useMemories must not maintain a separate stale memory cache')
})

test('memory lab malformed project payloads are errors, not empty memory', () => {
  const source = read(memoryLabPath)
  const parseBody = functionBody(source, 'parseMemoryArray')
  const loaderBody = functionBody(source, 'loadMemoryRows')

  assert.match(loaderBody, /operatorFetchJson<unknown>/, 'Memory Lab must fetch project memory payloads through the operator API seam')
  assert.match(loaderBody, /parseMemoryArray\(payload/, 'Memory Lab must validate every project payload before mapping rows')
  assert.match(parseBody, /throw\s+/, 'parseMemoryArray must throw on malformed or non-array payloads')
  assert.doesNotMatch(parseBody, /return\s+\[\]/, 'parseMemoryArray must not convert malformed payloads into an empty Memory page')
})

test('memory page-size contract offers persisted bounded all mode', () => {
  const pageSizeSource = read(pageSizePath)
  const memoryPageSource = read(memoryPagePath)
  const memoryLabSource = read(memoryLabPath)

  assert.match(pageSizeSource, /10\s*\|\s*25\s*\|\s*50\s*\|\s*['"]all['"]/, 'page-size type must use 10/25/50/all values')
  assert.match(pageSizeSource, /\[10,\s*25,\s*50,\s*['"]all['"]\]/, 'page-size options must include all as a real value')
  assert.match(pageSizeSource, /engram\.operatorConsole\.memory\.pageSize/, 'memory page-size preference must use the CR-006 storage key')
  assert.doesNotMatch(pageSizeSource, /engram\.console\.pageSizes/, 'memory page-size preference must not be hidden in the legacy grouped key')
  assert.match(pageSizeSource, /size\s*===\s*['"]all['"]/, 'all must resolve explicitly, not through numeric sentinel math')
  assert.doesNotMatch(memoryPageSource, /v-model\.number="pageSize"/, 'page-size select must not coerce all into a number')
  assert.match(memoryPageSource, /t\(['"]memory\.allRows['"]\)/, 'the all option label must be localized')
  assert.match(memoryLabSource, /limit=500|MEMORY_LIST_LIMIT\s*=\s*500/, 'Memory Lab must request no more than the current server cap for all-mode readiness')
  assert.doesNotMatch(memoryLabSource, /limit=200/, 'Memory Lab must not keep the old 200-row cap after all-mode support')
})

test('memory detail actions are data-backed mustbuild capabilities, not hardcoded fake-live controls', () => {
  const memoryPageSource = read(memoryPagePath)
  const memoryLabSource = read(memoryLabPath)

  assert.doesNotMatch(memoryPageSource, /storeCopy|deleteOpened/, 'uncontracted store/delete controls must not be visible in Memory detail')
  assert.match(memoryLabSource, /memoryActionGaps|actionGaps/, 'Memory Lab must expose action capability descriptors')
  assert.match(memoryLabSource, /unsupportedOperatorAction\(/, 'Memory actions without browser endpoints must use unsupported action descriptors')
  assert.match(memoryPageSource, /v-for="action in actionGaps"/, 'Memory detail buttons must render from action capability data')
  assert.match(memoryPageSource, /:title="action\.evidence\.endpoint"/, 'Memory detail actions must carry endpoint/tool evidence')
  assert.doesNotMatch(memoryPageSource, /<button class="act" disabled>\{\{ t\('memory\.detail\.actions\.hideNoise'\) \}\}/, 'mustbuild action buttons must not be duplicated as hardcoded literals')
})

test('memory capability evidence is an exported reusable typed data contract', () => {
  const seamSource = read(seamPath)
  const memoryPageSource = read(memoryPagePath)
  const memoryLabSource = read(memoryLabPath)
  const unsupportedType = seamSource.match(/export interface OperatorUnsupportedAction \{[\s\S]*?\n\}/)

  assert.ok(unsupportedType, 'OperatorUnsupportedAction must remain exported as the reusable capability base')
  assert.match(unsupportedType[0], /kind:\s*['"]mustbuild['"]/, 'unsupported capability base must carry the honesty class')
  assert.match(unsupportedType[0], /operable:\s*false/, 'unsupported capability base must be non-operable by construction')
  assert.match(unsupportedType[0], /evidence:\s*OperatorEndpointEvidence/, 'unsupported capability base must carry endpoint/tool evidence')
  assert.match(memoryLabSource, /export interface MemoryActionGap extends OperatorUnsupportedAction/, 'Memory action descriptors must extend the reusable capability base')
  assert.match(memoryLabSource, /export const memoryActionGaps:\s*MemoryActionGap\[\]/, 'Memory action descriptors must be exported typed data')
  assert.match(memoryLabSource, /actionGaps:\s*MemoryActionGap\[\]/, 'Memory Lab return type must expose capability descriptors')
  assert.match(memoryLabSource, /actionGaps:\s*memoryActionGaps/, 'Memory Lab must return the exported descriptor data')
  assert.match(memoryPageSource, /action\.labelKey/, 'Memory page must consume descriptor labels rather than duplicating copy')
  assert.match(memoryPageSource, /action\.badgeKey/, 'Memory page must consume descriptor badge labels rather than duplicating copy')
  assert.match(memoryPageSource, /action\.evidence\.endpoint/, 'Memory page must consume descriptor evidence rather than duplicating endpoints')
})

test('Nuxt UI color-mode auto-registration stays disabled', () => {
  const source = read(nuxtConfigPath)

  assert.match(source, /ui:\s*{[\s\S]*colorMode:\s*false/, 'Nuxt UI color-mode auto-registration must remain disabled')
  assert.match(source, /@nuxtjs\/color-mode/, 'the app still owns theme classes through @nuxtjs/color-mode')
})
