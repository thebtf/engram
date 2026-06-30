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
const healthSettingsPath = join(root, 'composables', 'useOperatorHealthSettings.ts')
const domainRegistryPath = join(root, 'composables', 'useOperatorDomainRegistry.ts')
const overviewComposablePath = join(root, 'composables', 'useOperatorOverview.ts')
const overviewPagePath = join(root, 'pages', 'index.vue')
const memoryPagePath = join(root, 'pages', 'memory.vue')
const settingsModalPath = join(root, 'components', 'SettingsModal.vue')
const issuesPagePath = join(root, 'pages', 'issues.vue')
const queuePagePath = join(root, 'pages', 'queue.vue')
const queueComposablePath = join(root, 'composables', 'useOperatorQueue.ts')
const rulesPagePath = join(root, 'pages', 'rules.vue')
const rulesComposablePath = join(root, 'composables', 'useOperatorRules.ts')
const projectsPagePath = join(root, 'pages', 'projects.vue')
const projectsComposablePath = join(root, 'composables', 'useOperatorProjects.ts')
const pageSizePath = join(root, 'composables', 'usePersistentPageSize.ts')
const mockOperatorApiPath = join(root, 'scripts', 'mock-operator-api.mjs')
const chunkReloadPluginPath = join(root, 'plugins', 'chunk-reload.client.ts')
const ruLocalePath = join(root, 'i18n', 'locales', 'ru.json')
const enLocalePath = join(root, 'i18n', 'locales', 'en.json')
const zhLocalePath = join(root, 'i18n', 'locales', 'zh.json')

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

test('overview model health is live endpoint data, not a mustbuild mock chart', () => {
  const compatibilitySource = read(compatibilityPath)
  const overviewComposableSource = read(overviewComposablePath)
  const overviewPageSource = read(overviewPagePath)

  assert.match(compatibilitySource, /interface ApiModelHealthResponse/, 'model-health payload must be typed')
  assert.match(compatibilitySource, /export const useModelsState\b/, 'model-health must expose a stateful live seam')
  assert.match(compatibilitySource, /fetchJson<ApiModelHealthResponse>\('\/api\/model-health'\)/, 'model-health must fetch the live REST endpoint')
  assert.doesNotMatch(compatibilitySource, /fallback\/gpt-4-1-mini/, 'model-health must not keep old hardcoded mock model rows')
  assert.doesNotMatch(overviewComposableSource, /unsupportedOperatorAction\(\s*['"]model-health['"]/, 'Overview must not represent model health as mustbuild after the endpoint exists')
  assert.match(overviewComposableSource, /useModelsState/, 'Overview must consume the live model-health state seam')
  assert.match(overviewPageSource, /\/api\/model-health/, 'Overview chart must show model-health endpoint evidence')
  assert.match(overviewPageSource, /overview\.modelHealth\.ok/, 'Model-health chart labels must be keyed for i18n')
  assert.match(overviewPageSource, /overview\.modelHealth\.errorShort/, 'Model-health errors must be visible and keyed')
  assert.doesNotMatch(overviewPageSource, /modelHealthGap/, 'Overview page must not render stale modelHealthGap data')
})

test('settings models tab is live read-only model health, not a fake model editor', () => {
  const compatibilitySource = read(compatibilityPath)
  const settingsModalSource = read(settingsModalPath)
  const mockApiSource = read(mockOperatorApiPath)
  const localeSources = [read(ruLocalePath), read(enLocalePath), read(zhLocalePath)]

  assert.match(compatibilitySource, /interface ApiModelsResponse/, 'model registry payload must be typed')
  assert.match(compatibilitySource, /export const useModelRegistryState\b/, 'model registry must expose a stateful live seam')
  assert.match(compatibilitySource, /fetchJson<ApiModelsResponse>\('\/api\/models'\)/, 'model registry must fetch the live REST endpoint')
  assert.match(compatibilitySource, /Array\.isArray\(payload\?\.models\)/, 'model registry must tolerate malformed non-array models payloads')
  assert.match(settingsModalSource, /useModelRegistryState/, 'Settings modal must consume the model registry seam')
  assert.match(settingsModalSource, /useModelsState/, 'Settings modal must consume the live model-health seam')
  assert.match(settingsModalSource, /watch\(\[open,\s*activeTab\]/, 'Settings modal must refresh model surfaces when the Models tab is opened')
  assert.match(settingsModalSource, /\{\s*id:\s*['"]models['"][\s\S]*kind:\s*['"]models['"][\s\S]*cls:\s*['"]live['"][\s\S]*evidence:\s*['"]GET \/api\/model-health['"]/, 'Models tab must be live model-health, not mustbuild')
  assert.doesNotMatch(settingsModalSource, /\{\s*id:\s*['"]models['"][\s\S]*kind:\s*['"]mustbuild['"][\s\S]*evidence:\s*['"]GET \/api\/models['"]/, 'Models tab must not remain a mustbuild placeholder')
  assert.match(settingsModalSource, /selectedTab\.kind === ['"]models['"]/, 'Settings modal must render a dedicated Models tab body')
  assert.match(settingsModalSource, /GET \/api\/model-health/, 'Models body must show model-health endpoint evidence')
  assert.match(settingsModalSource, /GET \/api\/models/, 'Models body must show model registry endpoint evidence')
  assert.match(settingsModalSource, /GET \/api\/model-credentials · GET \/api\/model-bindings · POST \/api\/models/, 'Unbuilt model mutations must stay explicit mustbuild evidence')
  assert.doesNotMatch(settingsModalSource, /data-edit-model|data-delete-cred|openModelModal|modelForm/, 'Settings modal must not expose mockup-only model mutation controls')
  assert.match(mockApiSource, /case ['"]\/api\/models['"]:/, 'Browser smoke mock must expose the live-empty /api/models endpoint')

  for (const localeSource of localeSources) {
    assert.match(localeSource, /"models":\s*\{/, 'Settings model copy must be keyed in every locale')
    assert.match(localeSource, /"health":\s*\{/, 'Settings model health copy must be keyed in every locale')
    assert.match(localeSource, /"registry":\s*\{/, 'Settings model registry copy must be keyed in every locale')
    assert.match(localeSource, /"next":\s*\{/, 'Settings model mustbuild-next copy must be keyed in every locale')
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
  const previewBody = functionBody(source, 'payloadPreview')
  const parseBody = functionBody(source, 'parseMemoryArray')
  const loaderBody = functionBody(source, 'loadMemoryRows')

  assert.match(loaderBody, /operatorFetchJson<unknown>/, 'Memory Lab must fetch project memory payloads through the operator API seam')
  assert.match(loaderBody, /parseMemoryArray\(payload/, 'Memory Lab must validate every project payload before mapping rows')
  assert.match(loaderBody, /Promise\.all\(projects\.map/, 'Memory Lab must fetch per-project memory payloads concurrently')
  assert.match(parseBody, /throw\s+/, 'parseMemoryArray must throw on malformed or non-array payloads')
  assert.match(parseBody, /let parsed: unknown/, 'parseMemoryArray must keep JSON.parse try scope narrow')
  assert.doesNotMatch(parseBody, /return\s+\[\]/, 'parseMemoryArray must not convert malformed payloads into an empty Memory page')
  assert.match(previewBody, /JSON\.stringify\(payload\)/, 'payload preview must serialize object payloads with useful bounded JSON')
  assert.doesNotMatch(previewBody, /Object\.prototype\.toString\.call\(payload\)/, 'payload preview must not degrade objects to [object Object]')
})

test('memory page-size contract offers persisted bounded all mode', () => {
  const pageSizeSource = read(pageSizePath)
  const memoryPageSource = read(memoryPagePath)
  const issuesPageSource = read(issuesPagePath)
  const memoryLabSource = read(memoryLabPath)

  assert.match(pageSizeSource, /10\s*\|\s*25\s*\|\s*50\s*\|\s*['"]all['"]/, 'page-size type must use 10/25/50/all values')
  assert.match(pageSizeSource, /\[10,\s*25,\s*50,\s*['"]all['"]\]/, 'page-size options must include all as a real value')
  assert.match(pageSizeSource, /engram\.operatorConsole\.memory\.pageSize/, 'memory page-size preference must use the CR-006 storage key')
  assert.doesNotMatch(pageSizeSource, /engram\.console\.pageSizes/, 'memory page-size preference must not be hidden in the legacy grouped key')
  assert.match(pageSizeSource, /export function usePersistentPageSize\(key: string, initial: OperatorPageSize = 10\)/, 'page-size helper must require an explicit storage key for future pages')
  assert.doesNotMatch(pageSizeSource, /function isOperatorPageSize/, 'page-size helper must not keep unused local validator aliases')
  assert.match(pageSizeSource, /size\s*===\s*['"]all['"]/, 'all must resolve explicitly, not through numeric sentinel math')
  assert.doesNotMatch(memoryPageSource, /v-model\.number="pageSize"/, 'page-size select must not coerce all into a number')
  assert.match(memoryPageSource, /t\(['"]memory\.allRows['"]\)/, 'the all option label must be localized')
  assert.doesNotMatch(issuesPageSource, /v-model\.number="pageSize"/, 'shared page-size consumers must not coerce all into a number')
  assert.doesNotMatch(issuesPageSource, /size\s*===\s*0/, 'shared page-size consumers must not keep the old numeric all sentinel')
  assert.match(issuesPageSource, /size\s*===\s*['"]all['"]\s*\?\s*t\(['"]issues\.list\.allRows['"]\)/, 'Issues all option label must be localized for the shared all sentinel')
  assert.match(memoryLabSource, /limit=500|MEMORY_LIST_LIMIT\s*=\s*500/, 'Memory Lab must request no more than the current server cap for all-mode readiness')
  assert.doesNotMatch(memoryLabSource, /limit=200/, 'Memory Lab must not keep the old 200-row cap after all-mode support')
})

test('principal memory surface follows the approved T008 contract', () => {
  const memoryPageSource = read(memoryPagePath)
  const memoryLabSource = read(memoryLabPath)
  const mockOperatorApiSource = read(mockOperatorApiPath)
  const localeSources = [read(enLocalePath), read(ruLocalePath), read(zhLocalePath)]

  assert.match(memoryLabSource, /export interface PrincipalMemoryScope/, 'Principal memory scope must be a typed UI contract')
  assert.match(memoryLabSource, /export function principalMemoryQueryPath/, 'Principal memory query URL construction must be reusable and testable')
  assert.match(memoryLabSource, /PRINCIPAL_CURRENT_PROJECT\s*=\s*['"]current['"]/, 'Principal memory current-project scope must use a non-empty UI sentinel')
  assert.match(memoryLabSource, /project:\s*['"]all['"]/, 'Principal memory default project scope must stay all; current requires a concrete page project')
  assert.match(memoryLabSource, /principalMemoryQueryPath\(scope: PrincipalMemoryScope,\s*currentProject = ['"]['"]\)/, 'Principal memory query path must accept the current page project for current-project scope')
  assert.doesNotMatch(memoryLabSource, /return clean\(currentProject\) \|\| ['"]all['"]/, 'Current-project scope must not silently widen to all when currentProject is empty')
  assert.match(memoryLabSource, /resolvedCurrentProject && resolvedCurrentProject !== ['"]all['"] \? resolvedCurrentProject : ['"]['"]/, 'Current-project scope must resolve only to a concrete project')
  assert.match(memoryLabSource, /Current project scope needs a concrete project before querying\./, 'Current-project scope must gate instead of issuing an unscoped query when no concrete project is active')
  assert.match(memoryLabSource, /normalized\.includePrivate\s*\|\|\s*normalized\.visibility === ['"]private['"]/, 'Private visibility must request include_private instead of returning hidden-only private results')
  assert.match(memoryLabSource, /\/api\/memories\/principal/, 'Principal memory surface must bind to the live T006 REST route')
  assert.match(memoryLabSource, /export function useOperatorPrincipalMemorySurface\(currentProject\?: Ref<string>\)/, 'Principal memory surface must have a dedicated composable with current-project binding')
  assert.match(memoryLabSource, /briefState:\s*ComputedRef<OperatorLoadState<OperatorPrincipalMemoryBrief>>/, 'Brief panel state must be a typed honest load state')
  assert.match(memoryLabSource, /MCP get_memory_brief/, 'Brief panel must name the MCP-only T007 bridge until browser REST exists')
  assert.match(memoryLabSource, /mustBuildState<OperatorPrincipalMemoryBrief>/, 'Browser brief panel must render mustbuild instead of faking a live fetch')
  assert.match(memoryLabSource, /riskyConfirmation/, 'Cross-principal widening must have an explicit risky-confirm branch')

  assert.match(memoryPageSource, /useOperatorPrincipalMemorySurface\(project\)/, 'Memory page must bind principal current-project scope to the active project filter')
  assert.doesNotMatch(memoryPageSource, /<option value="">\{\{ t\('memory\.principal\.projects\.current'\) \}\}<\/option>/, 'Principal current-project option must not use an empty value that becomes an unscoped backend query')
  for (const testId of [
    'principal-memory-surface',
    'principal-state-banner',
    'principal-select',
    'domain-select',
    'project-scope',
    'refresh',
    'brief-refresh',
    'attribution-toggle',
    'principal-knowledge-summary',
    'principal-brief-panel',
  ]) {
    assert.match(memoryPageSource, new RegExp(`data-testid="${testId}"`), `Memory page must render ${testId}`)
  }

  for (const state of ['pending', 'live', 'empty', 'gated', 'mustbuild', 'stale', 'error', 'risky-confirm']) {
    assert.match(memoryPageSource, new RegExp(`memory\\.principal\\.state\\.${state}`), `Principal surface must render ${state} copy`)
  }

  assert.match(mockOperatorApiSource, /case '\/api\/memories\/principal':[\s\S]*principalMemoryResponse\(url\)/, 'Mock operator API must serve /api/memories/principal for browser smoke')

  for (const localeSource of localeSources) {
    assert.match(localeSource, /"principal":\s*\{/, 'Principal memory copy must be keyed in every locale')
    assert.doesNotMatch(localeSource, /keep this record in prompts|оставить запись в подсказках|保留到提示词/, 'Touched memory copy must not keep prompt-retention semantics')
  }
})

test('candidate review queue is a live gated surface, not a SectionStub', () => {
  const queuePageSource = read(queuePagePath)
  const queueComposableSource = read(queueComposablePath)
  const overviewComposableSource = read(overviewComposablePath)
  const overviewPageSource = read(overviewPagePath)

  assert.doesNotMatch(queuePageSource, /<SectionStub\b/, 'Queue page must not remain an inert SectionStub after the REST bridge exists')
  assert.match(queueComposableSource, /QUEUE_FLAG\s*=\s*['"]ENGRAM_VNEXT_F_ENABLED['"]/, 'Queue composable must name the vNext-F flag gate')
  assert.match(queueComposableSource, /operatorFetchJson<ApiFlags>\('\/api\/flags'/, 'Queue composable must check flags before fetching candidates')
  assert.match(queueComposableSource, /gatedState\(evidence,\s*QUEUE_FLAG/, 'Queue composable must represent flag-off as gated, not empty')
  assert.match(queueComposableSource, /QUEUE_ALL_PROJECTS_API\s*=\s*['"]all['"]/, 'Queue composable must expose an all-project REST query for unscoped candidates')
  assert.match(queueComposableSource, /selectedProject\.value\s*===\s*QUEUE_ALL_PROJECTS\s*\?\s*QUEUE_ALL_PROJECTS_API\s*:\s*selectedProject\.value/, 'Queue composable must translate the all-project UI sentinel before hitting the live endpoint')
  assert.match(queueComposableSource, /\/api\/memory\/candidates\?project=\$\{encodeURIComponent\(apiProject\)\}&status=\$\{QUEUE_STATUS\}&limit=\$\{QUEUE_LIMIT\}/, 'Queue composable must fetch the live candidate list endpoint')
  assert.match(queueComposableSource, /\/api\/memory\/candidates\/\$\{encodeURIComponent\(id\)\}\/\$\{action\}/, 'Queue actions must use live candidate action endpoints')
  assert.match(queueComposableSource, /runOperatorMutation<CandidateActionReceipt>/, 'Queue actions must use the rollback-capable mutation seam')
  assert.match(queuePageSource, /usePersistentPageSize\('engram\.operatorConsole\.queue\.pageSize'/, 'Queue page-size preference must be persisted under its own key')
  assert.match(queuePageSource, /size === 'all' \? t\('queue\.allRows'\)/, 'Queue page must render the shared all page-size label through i18n')
  assert.match(queuePageSource, /HonestyBadge/, 'Queue page must render design honesty classification evidence')
  assert.match(overviewComposableSource, /useOperatorQueue/, 'Overview must consume the live queue seam after the endpoint exists')
  assert.doesNotMatch(overviewComposableSource, /queueGap/, 'Overview must not keep the stale queueGap once the queue is wired')
  assert.match(overviewPageSource, /overview\.attention\.queueLive/, 'Overview attention must distinguish live queue count from gated copy')
})

test('behavioral rules enabled toggle is live endpoint-backed and remains recoverable', () => {
  const compatibilitySource = read(compatibilityPath)
  const rulesPageSource = read(rulesPagePath)
  const rulesComposableSource = read(rulesComposablePath)
  const mockOperatorApiSource = read(mockOperatorApiPath)

  assert.match(compatibilitySource, /enabled\?: boolean/, 'Rule compatibility payload must carry the server enabled field')
  assert.match(compatibilitySource, /enabled: row\.enabled !== false/, 'Rule compatibility mapper must default legacy rows to enabled')
  assert.match(rulesComposableSource, /toggleRuleEnabled:\s*\(id: number, enabled: boolean\) => Promise<unknown>/, 'Rules composable must expose a typed enabled toggle action')
  assert.match(rulesComposableSource, /action:\s*['"]rule-enable-toggle['"]/, 'Rule enabled toggle must be a real mutation action')
  assert.match(rulesComposableSource, /endpointEvidence\(`\/api\/rules\/\$\{id\}\/enabled`,\s*['"]rule-enable-toggle['"]\)/, 'Rule enabled toggle must carry PATCH /api/rules/{id}/enabled evidence')
  assert.ok(rulesComposableSource.includes('operatorFetchJson<ApiRuleRow>(`/api/rules/${id}/enabled`'), 'Rule enabled toggle must call the live enabled endpoint')
  assert.match(rulesComposableSource, /replaceArray\(rowsState\.value, rowsState\.value\.map\(\(row\) => row\.id === id \? \{ \.\.\.row, enabled \} : row\)\)/, 'Rule enabled toggle must update the visible row optimistically')
  assert.doesNotMatch(rulesComposableSource, /enableGap/, 'Rule enabled toggle must not remain an unsupported mustbuild gap')
  assert.doesNotMatch(rulesComposableSource, /unsupportedOperatorAction\(\s*['"]rule-enable-toggle['"]/, 'Rule enabled toggle must not be represented as an unsupported action')

  assert.match(rulesPageSource, /const isToggling = ref\(false\)/, 'Rules page must keep a local in-flight guard for toggle mutations')
  assert.match(rulesPageSource, /if \(pending\.value \|\| isToggling\.value\) return/, 'Rules page toggle handler must reject concurrent toggle clicks')
  assert.match(rulesPageSource, /@click="toggleRule\(rule\)"/, 'Rules page switch must call the live toggle handler')
  assert.match(rulesPageSource, /:aria-checked="String\(rule\.enabled\)"/, 'Rules page switch must expose true state to assistive tech')
  assert.match(rulesPageSource, /:disabled="pending \|\| isToggling"/, 'Rules page switch must be disabled while a toggle mutation is locally in flight')
  assert.match(rulesPageSource, /data-testid="`rule-enable-toggle-\$\{rule\.id\}`"/, 'Rules page switch must expose a stable browser-smoke selector')
  assert.match(rulesPageSource, /data-testid="`rule-status-\$\{rule\.id\}`"/, 'Rules page status chip must expose a stable browser-smoke selector')
  assert.match(rulesPageSource, /rules\.detail\.enabled/, 'Rules page enabled label must be i18n-keyed')
  assert.match(rulesPageSource, /rules\.detail\.disabled/, 'Rules page disabled label must be i18n-keyed')
  assert.doesNotMatch(rulesPageSource, /disabled\s+role="switch"/, 'Rules page must not render the enabled switch as inert')

  assert.match(mockOperatorApiSource, /let ruleRows = \[/, 'Mock operator API must keep stateful rule rows for browser smoke')
  assert.match(mockOperatorApiSource, /path\.match\(\/\^\\\/api\\\/rules\\\/\(\[\^\/\]\+\)\\\/enabled\$\/\)/, 'Mock operator API must implement PATCH /api/rules/{id}/enabled')
  assert.match(mockOperatorApiSource, /typeof body\.enabled !== 'boolean'/, 'Mock rule enabled route must reject missing enabled state')
  assert.match(mockOperatorApiSource, /enabled: body\.enabled/, 'Mock rule enabled route must persist the enabled state')
  assert.match(mockOperatorApiSource, /case '\/api\/rules':[\s\S]*ruleResponse\(url\)/, 'Mock operator API must serve rule rows through GET /api/rules')
})

test('memory detail actions keep mustbuild descriptors while live delete and audit are endpoint-backed', () => {
  const memoryPageSource = read(memoryPagePath)
  const memoryLabSource = read(memoryLabPath)
  const mockOperatorApiSource = read(mockOperatorApiPath)

  assert.doesNotMatch(memoryPageSource, /storeCopy/, 'uncontracted store-copy controls must not be visible in Memory detail')
  assert.match(memoryPageSource, /suppressOpened/, 'Memory detail may expose hide-as-noise only through the live suppress handler')
  assert.match(memoryPageSource, /suppressMemory\(memory\.id\)/, 'Memory detail suppress must call the composable live suppress action')
  assert.match(memoryPageSource, /suppressMemories\(selectedIds\.value\)/, 'Memory bulk suppress must call the composable live bulk suppress action')
  assert.match(memoryPageSource, /data-testid="memory-suppress-action"/, 'Memory suppress browser smoke must use a stable non-localized selector')
  assert.match(memoryPageSource, /deleteOpened/, 'Memory detail may expose delete only through the live delete handler')
  assert.match(memoryPageSource, /deleteMemory\(memory\.id\)/, 'Memory detail delete must call the composable live delete action')
  assert.match(memoryLabSource, /deleteMemory:\s*\(id:\s*string\)\s*=>\s*Promise<OperatorMutationResult<unknown>>/, 'Memory Lab delete must expose a typed mutation result')
  assert.match(memoryLabSource, /endpointEvidence\(`\/api\/memories\/\$\{id\}`,\s*['"]memory-delete['"]\)/, 'Memory delete must carry the live REST endpoint as evidence')
  assert.match(memoryLabSource, /suppressMemory:\s*\(id:\s*string,\s*reason\?:\s*string\)\s*=>\s*Promise<OperatorMutationResult<MemoryActionReceipt>>/, 'Memory Lab suppress must expose a typed mutation result')
  assert.match(memoryLabSource, /endpointEvidence\(`\/api\/memories\/\$\{id\}\/suppress`,\s*['"]memory-suppress['"]\)/, 'Memory suppress must carry the live REST endpoint as evidence')
  assert.match(memoryLabSource, /endpointEvidence\('\/api\/memories\/suppress',\s*['"]memory-bulk-suppress['"]/, 'Memory bulk suppress must carry the live bulk REST endpoint as evidence')
  assert.match(memoryLabSource, /operatorFetchJson<MemoryActionReceipt\[\]>\('\/api\/memories\/suppress'/, 'Memory bulk suppress must avoid client-side Promise.all fanout')
  assert.doesNotMatch(memoryLabSource, /Promise\.all\(uniqueIds\.map/, 'Memory bulk suppress must not fan out partial row mutations from the browser')
  assert.doesNotMatch(memoryLabSource, /memory-hide-noise/, 'hide-as-noise must not remain in mustbuild action gaps after the live REST bridge exists')
  assert.match(memoryPageSource, /memory-audit-panel/, 'Memory detail must expose a stable audit panel selector')
  assert.match(memoryPageSource, /auditMemory\(memory\.id\)/, 'Memory detail audit must call the composable live audit action')
  assert.match(memoryLabSource, /auditMemory:\s*\(id:\s*string,\s*limit\?:\s*number\)\s*=>\s*Promise<OperatorLoadState<MemoryAuditResponse>>/, 'Memory Lab audit must expose a typed load state')
  assert.match(memoryLabSource, /const normalizedLimit = Number\.isInteger\(limit\) && limit > 0 \? Math\.min\(200, limit\) : 10/, 'Memory audit must normalize browser-provided limits before constructing the endpoint URL')
  assert.match(memoryLabSource, /\/api\/memories\/\$\{encodeURIComponent\(id\)\}\/audit\?limit=\$\{normalizedLimit\}/, 'Memory audit must carry the live REST endpoint as evidence with the normalized limit')
  assert.match(mockOperatorApiSource, /if \(!Number\.isInteger\(id\) \|\| id <= 0\) \{[\s\S]*json\(res,\s*400,\s*\{\s*error:\s*['"]invalid memory id['"]\s*\}/, 'Mock memory audit must reject invalid memory IDs like the live handler')
  assert.match(mockOperatorApiSource, /if \(!Number\.isInteger\(limit\) \|\| limit <= 0 \|\| limit > 200\) \{[\s\S]*json\(res,\s*400,\s*\{\s*error:\s*['"]invalid limit['"]\s*\}/, 'Mock memory audit must reject invalid limits like the live handler')
  assert.match(mockOperatorApiSource, /const row = memoryRows\.find\(\(item\) => item\.id === id\)/, 'Mock memory audit must use numeric memory ID matching before returning audit rows')
  assert.doesNotMatch(memoryLabSource, /unsupportedOperatorAction\(\s*['"]memory-audit['"]/, 'Memory audit must not remain a mustbuild descriptor after the endpoint exists')
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
  assert.match(memoryLabSource, /export const memoryActionGaps:\s*readonly MemoryActionGap\[\]/, 'Memory action descriptors must be exported readonly typed data')
  assert.match(memoryLabSource, /actionGaps:\s*readonly MemoryActionGap\[\]/, 'Memory Lab return type must expose capability descriptors')
  assert.match(memoryLabSource, /actionGaps:\s*memoryActionGaps/, 'Memory Lab must return the exported descriptor data')
  assert.match(memoryPageSource, /action\.labelKey/, 'Memory page must consume descriptor labels rather than duplicating copy')
  assert.match(memoryPageSource, /action\.badgeKey/, 'Memory page must consume descriptor badge labels rather than duplicating copy')
  assert.match(memoryPageSource, /action\.evidence\.endpoint/, 'Memory page must consume descriptor evidence rather than duplicating endpoints')
})

test('settings feature flags are live read-only data, not a mustbuild fake control', () => {
  const healthSettingsSource = read(healthSettingsPath)
  const settingsModalSource = read(settingsModalPath)

  assert.match(healthSettingsSource, /interface ApiFlags/, 'Settings seam must type the live feature-flag payload')
  assert.match(healthSettingsSource, /endpointEvidence\('\/api\/flags',\s*['"]flags['"]\)/, 'Feature flags must carry live endpoint evidence')
  assert.match(healthSettingsSource, /loadOperatorJson<ApiFlags>\('\/api\/flags'/, 'Settings seam must fetch feature flags from the real endpoint')
  assert.match(healthSettingsSource, /flagsState:\s*ComputedRef<OperatorLoadState<ApiFlags>>/, 'Feature flag state must be exposed to the modal')
  assert.match(healthSettingsSource, /flagItems:\s*ComputedRef<ApiFlagItem\[\]>/, 'Feature flag items must be exposed to the modal')
  assert.doesNotMatch(healthSettingsSource, /flagsGap/, 'GET /api/flags must no longer be represented as a mustbuild gap')
  assert.doesNotMatch(healthSettingsSource, /['"]flags-read['"]/, 'Feature flag reads must not remain an unsupported action')

  assert.match(settingsModalSource, /v-for="item in flagItems"/, 'Settings modal must render live flag rows from data')
  assert.match(settingsModalSource, /settings\.flags\.title/, 'Feature flag copy must be keyed for i18n')
  assert.match(settingsModalSource, /flagsState\.kind === 'error'/, 'Feature flag load errors must be visible')
  assert.doesNotMatch(settingsModalSource, /flagsGap/, 'Settings modal must not render GET /api/flags as a mustbuild gap')
})

test('health flags by area are live read-only endpoint state', () => {
  const healthSettingsSource = read(healthSettingsPath)
  const healthPageSource = read(join(root, 'pages', 'health.vue'))
  const paritySource = read(join(root, 'PARITY.json'))

  assert.match(healthSettingsSource, /interface OperatorFlagGroup/, 'Health seam must expose grouped feature-flag state')
  assert.match(healthSettingsSource, /flagGroups:\s*ComputedRef<OperatorFlagGroup\[\]>/, 'Feature flag groups must be exposed to the Health page')
  assert.match(healthSettingsSource, /const flagGroups = computed/, 'Feature flag groups must be derived from live flag items')
  assert.match(healthSettingsSource, /restart_required_to_change/, 'Feature flag groups must preserve restart-required evidence')
  assert.match(healthSettingsSource, /a\.category\.localeCompare\(b\.category\)/, 'Feature flag groups must render in stable category order')

  assert.match(healthPageSource, /flagsState/, 'Health page must read feature-flag load state')
  assert.match(healthPageSource, /flagGroups/, 'Health page must render grouped feature flags')
  assert.match(healthPageSource, /health\.flagsByArea/, 'Health flag card title must be i18n-keyed')
  assert.match(healthPageSource, /\/api\/flags/, 'Health flag card must show live endpoint evidence')
  assert.match(healthPageSource, /flagsState\.kind !== 'live'/, 'Health flag card must keep a real not-loaded state')
  assert.match(healthPageSource, /v-for="flagGroup in flagGroups"/, 'Health flag card must render one row per feature-flag area')
  assert.match(healthPageSource, /health\.flags\.summary/, 'Health flag row summary must be i18n-keyed')
  assert.match(healthPageSource, /health\.flags\.evidence/, 'Health flag evidence badge must be i18n-keyed')
  assert.doesNotMatch(healthPageSource, /v-for="item in flagItems"/, 'Health page must not duplicate the Settings raw flag control list')
  assert.doesNotMatch(paritySource, /flags-by-area is live in Settings modal via GET \/api\/flags but not promoted into this page/, 'PARITY health row must not keep the closed flags-by-area gap')
})

test('health migrations state is live endpoint-backed and no longer mustbuild', () => {
  const healthSettingsSource = read(healthSettingsPath)
  const healthPageSource = read(join(root, 'pages', 'health.vue'))
  const mockOperatorApiSource = read(mockOperatorApiPath)
  const paritySource = read(join(root, 'PARITY.json'))

  assert.match(healthSettingsSource, /interface ApiMigrationState/, 'Health seam must type the migration-state payload')
  assert.match(healthSettingsSource, /endpointEvidence\('\/api\/migrations',\s*['"]migrations['"]\)/, 'Migration state must carry live endpoint evidence')
  assert.match(healthSettingsSource, /loadOperatorJson<ApiMigrationState>\('\/api\/migrations'/, 'Health seam must fetch migration state from the real endpoint')
  assert.match(healthSettingsSource, /migrationsState:\s*ComputedRef<OperatorLoadState<ApiMigrationState>>/, 'Migration state must be exposed to the Health page')
  assert.match(healthSettingsSource, /migrationMetrics:\s*ComputedRef<OperatorHealthMetric\[\]>/, 'Health seam must expose migration metrics')
  assert.match(healthPageSource, /health\.migrations/, 'Health page migration card title must be i18n-keyed')
  assert.match(healthPageSource, /\/api\/migrations/, 'Health page must show the live migration endpoint evidence')
  assert.match(healthPageSource, /migrationsState\.kind === 'live'/, 'Health page must classify migration state from live load state')
  assert.match(healthPageSource, /const migrationMessage = computed/, 'Health page must compute migration helper copy from load state')
  assert.match(healthPageSource, /migrationsState\.value\.kind !== 'live'[\s\S]*health\.state\.notLoaded/, 'Health page must reserve not-loaded copy for non-live migration state')
  assert.match(healthPageSource, /migrationsState\.value\.data\.dirty_supported === false/, 'Health page must show dirty unsupported copy only when the live payload says so')
  assert.match(
    mockOperatorApiSource,
    /case '\/api\/migrations':[\s\S]*?json\(res,\s*200,\s*migrations\)[\s\S]*?return/,
    'Mock operator API must serve migration state for smoke',
  )
  assert.doesNotMatch(paritySource, /migrations \(mustbuild\)/, 'PARITY health row must not keep migrations as a mustbuild gap')
})

test('settings config save is live allowlisted PATCH with restart receipt', () => {
  const healthSettingsSource = read(healthSettingsPath)
  const settingsModalSource = read(settingsModalPath)

  assert.match(healthSettingsSource, /interface ApiConfigPatch/, 'Settings save seam must type the PATCH request')
  assert.match(healthSettingsSource, /interface ApiConfigPatchReceipt/, 'Settings save seam must type the restart receipt')
  assert.match(healthSettingsSource, /interface ApiConfigPendingRestart/, 'Settings seam must type pending restart lifecycle rows')
  assert.match(healthSettingsSource, /configPendingRestart:\s*ComputedRef<ApiConfigPendingRestart\[\]>/, 'Settings seam must expose pending restart lifecycle rows')
  assert.match(healthSettingsSource, /saveConfig:\s*\(patch: ApiConfigPatch\)/, 'Settings composable must expose a typed saveConfig action')
  assert.match(healthSettingsSource, /operatorFetchJson<ApiConfigPatchReceipt>\('\/api\/config',\s*\{[\s\S]*method:\s*'PATCH'/, 'Settings save must call PATCH /api/config')
  assert.match(healthSettingsSource, /configSaveEvidence = endpointEvidence\('\/api\/config',\s*'config-save'\)/, 'Settings save must carry live endpoint evidence')
  assert.doesNotMatch(healthSettingsSource, /configSaveGap/, 'Settings save must not remain an unsupported mustbuild action')

  assert.match(settingsModalSource, /saveRuntimeConfig/, 'Settings modal must expose a save action')
  assert.match(settingsModalSource, /configSaveResult\.data\.restart_required/, 'Settings modal must render restart-required receipt state')
  assert.match(settingsModalSource, /v-for="item in configPendingRestart"/, 'Settings modal must render pending restart lifecycle rows')
  assert.match(settingsModalSource, /settings\.lifecycle\.effective/, 'Pending lifecycle effective label must be keyed for i18n')
  assert.match(settingsModalSource, /settings\.lifecycle\.desired/, 'Pending lifecycle desired label must be keyed for i18n')
  assert.match(settingsModalSource, /settings\.save\.success/, 'Settings save copy must be keyed for i18n')
  assert.doesNotMatch(settingsModalSource, /settings\.gaps\.configSave/, 'Settings modal must not render the old config-save mustbuild gap')
})

test('settings domain registry is live GET PUT DELETE control-plane surface', () => {
  const domainRegistrySource = read(domainRegistryPath)
  const settingsModalSource = read(settingsModalPath)
  const mockOperatorApiSource = read(mockOperatorApiPath)

  assert.match(domainRegistrySource, /endpointEvidence\('\/api\/memory-domains',\s*['"]memory-domain-registry['"]\)/, 'Domain registry list must carry live endpoint evidence')
  assert.match(domainRegistrySource, /loadOperatorJson<ApiMemoryDomainsListResponse>\('\/api\/memory-domains'/, 'Domain registry must load the live list endpoint')
  assert.match(domainRegistrySource, /function assertDomain\(value: string\)/, 'Domain registry mutations must reject empty domains before constructing endpoint URLs')
  assert.match(domainRegistrySource, /retry:\s*\{[\s\S]*source:\s*['"]memory-domain-registry['"][\s\S]*run:\s*async\s*\(\)\s*=>\s*\{[\s\S]*await refreshDomains\(\)[\s\S]*return domainStateRef\.value/, 'Domain registry error retry must return normalized OperatorMemoryDomain state')
  assert.match(domainRegistrySource, /operatorFetchJson<ApiMemoryDomain>\(endpoint,\s*\{[\s\S]*method:\s*'PUT'/, 'Domain registry upsert must call PUT /api/memory-domains/{domain}')
  assert.match(domainRegistrySource, /operatorFetchJson<ApiMemoryDomainDeleteReceipt>\(endpoint,\s*\{\s*method:\s*'DELETE'\s*\}/, 'Domain registry delete must call DELETE /api/memory-domains/{domain}')
  assert.match(domainRegistrySource, /runOperatorMutation/, 'Domain registry writes must use the rollback-capable mutation seam')
  assert.match(domainRegistrySource, /DOMAIN_OWNER_KINDS\s*=\s*\['human',\s*'agent',\s*'service'\]/, 'Domain registry owner kinds must mirror server validation')
  assert.match(domainRegistrySource, /DOMAIN_OWNER_MODES\s*=\s*\['off',\s*'warn',\s*'reject'\]/, 'Domain registry modes must mirror server validation')

  assert.match(settingsModalSource, /id:\s*['"]domains['"][\s\S]*kind:\s*['"]domains['"][\s\S]*cls:\s*['"]live['"]/, 'Settings must expose domain registry as a live tab')
  assert.match(settingsModalSource, /selectedTab\.kind === 'domains'/, 'Settings modal must render a dedicated domain registry branch')
  assert.match(settingsModalSource, /domainDeleteConfirm\.value !== domain/, 'Domain delete must require a confirmation click')
  assert.match(settingsModalSource, /editingDomain\s*=\s*ref<string \| null>\(null\)/, 'Settings modal must track edit mode for existing domain rows')
  assert.match(settingsModalSource, /:disabled="Boolean\(editingDomain\)"/, 'Settings modal must lock the domain key while editing an existing row')
  assert.match(settingsModalSource, /t\('settings\.domains\.count',\s*domainCount\)/, 'Domain count must use Vue I18n plural choice instead of named interpolation only')
  assert.match(settingsModalSource, /:disabled="Boolean\(domainDeleteInFlight\)"/, 'Domain delete buttons must lock as a group while a delete mutation is in flight')
  assert.match(settingsModalSource, /settings\.domains\.rows\.deleteHelp/, 'Domain delete copy must clarify that memories are not deleted')
  assert.match(settingsModalSource, /domainListEvidence\.endpoint/, 'Domain registry tab must show live endpoint evidence')
  assert.doesNotMatch(settingsModalSource, /GET \/api\/memory-domains['"],\s*kind:\s*['"]mustbuild['"]/, 'Domain registry must not be represented as mustbuild after the backend seam exists')

  assert.match(mockOperatorApiSource, /function controlPlaneError\(message, code, data\)/, 'Mock operator API domain route errors must follow the control-plane error envelope')
  assert.match(mockOperatorApiSource, /try\s*\{[\s\S]*decodeURIComponent\(domainMatch\[1\]\)\.trim\(\)[\s\S]*\}\s*catch\s*\{[\s\S]*controlPlaneError\('invalid domain encoding', 400\)/, 'Mock operator API must safely reject malformed encoded domain paths')
  assert.doesNotMatch(mockOperatorApiSource, /json\(res,\s*(?:400|404),\s*\{\s*error:\s*['"][^'"]*domain/, 'Mock operator API domain route must not use legacy { error } responses')
})

test('projects control plane archives projects through typed soft-delete confirmation', () => {
  const projectsPageSource = read(projectsPagePath)
  const projectsComposableSource = read(projectsComposablePath)
  const mockOperatorApiSource = read(mockOperatorApiPath)

  assert.match(projectsComposableSource, /action:\s*['"]project-archive['"]/, 'Project removal must be represented as archive/soft-delete in the mutation seam')
  assert.match(projectsComposableSource, /const endpoint = `\/api\/projects\/\$\{encodeURIComponent\(project\)\}`/, 'Project archive evidence must use the same encoded endpoint as the mutation request')
  assert.match(projectsComposableSource, /operatorFetchJson\(endpoint,\s*jsonInit\('DELETE'\),\s*['"]projects-delete['"]\)/, 'Project archive must call the live DELETE /api/projects/{id} endpoint')
  assert.match(projectsComposableSource, /type ApiNullableString = string \| \{ String\?: string; Valid\?: boolean \}/, 'Session detail mapper must accept Go sql.NullString JSON from the live endpoint')
  assert.match(projectsComposableSource, /type ApiNullableInt = number \| string \| \{ Int64\?: number \| string \| null; Valid\?: boolean \}/, 'Session detail mapper must accept Go sql.NullInt64 JSON from the live endpoint')
  assert.match(projectsComposableSource, /function nullableString\(/, 'Session detail mapper must normalize nullable string payloads before rendering')
  assert.match(projectsComposableSource, /function nullableIntString\(/, 'Session detail mapper must normalize nullable integer payloads before rendering')
  assert.match(projectsComposableSource, /value\.Int64 === undefined \|\| value\.Int64 === null/, 'Session detail mapper must not render null Int64 values as the literal string null')
  assert.match(projectsComposableSource, /sessionRouteGap/, 'Projects page must keep route-decision timeline as the honest mustbuild gap')
  assert.doesNotMatch(projectsComposableSource, /sessionStrategyGap/, 'Session strategy readback must not remain classified as mustbuild once the live detail endpoint exposes injection_strategy')

  assert.match(projectsPageSource, /function toggleArchiveProject\(project: string\)/, 'Projects page must separate opening the archive confirmation from submitting it')
  assert.match(projectsPageSource, /async function submitArchiveProject\(project: string\)/, 'Projects page must submit project archive through a dedicated mutation handler')
  assert.match(projectsPageSource, /projectArchiveTarget/, 'Projects page must track the currently expanded archive confirmation')
  assert.match(projectsPageSource, /projectArchiveInput\.value !== project/, 'Project archive must require typed project-id confirmation')
  assert.match(projectsPageSource, /projectArchivePending/, 'Project archive must lock the action while the mutation is in flight')
  assert.match(projectsPageSource, /projects\.archive\.body/, 'Project archive copy must explain the soft-delete semantics through i18n')
  assert.match(projectsPageSource, /projectArchiveEndpoint\(project\.id\)/, 'Project archive confirmation must expose endpoint evidence')
  assert.match(projectsPageSource, /@keydown\.enter\.prevent="submitArchiveProject\(project\.id\)"/, 'Project archive Enter key must submit instead of toggling the confirmation')
  assert.match(projectsPageSource, /@click="submitArchiveProject\(project\.id\)"/, 'Project archive confirm button must submit instead of toggling the confirmation')
  assert.match(projectsPageSource, /Boolean\(projectArchivePending\)/, 'Project archive confirm controls must be disabled while any archive mutation is pending')
  assert.match(projectsPageSource, /data-testid="`project-archive-input-\$\{project\.id\}`"/, 'Project archive browser smoke must have a stable input selector')
  assert.match(projectsPageSource, /data-testid="`project-session-\$\{session\.id\}`"/, 'Session browser smoke must have a stable row selector')
  assert.match(projectsPageSource, /projects\.detail\.userPrompt/, 'Session detail must render the live user_prompt field')
  assert.match(projectsPageSource, /projects\.detail\.outcomeReason/, 'Session detail must render the live outcome_reason field')
  assert.match(projectsPageSource, /sessionRouteGap\.evidence\.endpoint/, 'Session detail must show route-decision mustbuild endpoint evidence')
  assert.doesNotMatch(projectsPageSource, /confirmDeleteProject/, 'Projects page must not keep delete-named handlers for archive semantics')

  assert.match(mockOperatorApiSource, /let projectIds = \['operator-console', 'project-alpha'\]/, 'Mock API must expose multiple projects for archive smoke coverage')
  assert.match(mockOperatorApiSource, /sdk_session_id:\s*\{\s*String:\s*'sdk-operator-1',\s*Valid:\s*true\s*\}/, 'Mock API must exercise Go sql.NullString-shaped session detail fields')
  assert.match(mockOperatorApiSource, /worker_port:\s*\{\s*Int64:\s*37777,\s*Valid:\s*true\s*\}/, 'Mock API must exercise Go sql.NullInt64-shaped session detail fields')
  assert.match(mockOperatorApiSource, /const projectDeleteMatch = path\.match\(\/\^\\\/api\\\/projects\\\/\(\[\^\/\]\+\)\$\/\)/, 'Mock API must implement DELETE /api/projects/{id} for browser smoke')
  assert.match(mockOperatorApiSource, /req\.method === 'DELETE' && projectDeleteMatch/, 'Mock project archive route must be DELETE-only')
  assert.match(mockOperatorApiSource, /removed_at:\s*new Date\(\)\.toISOString\(\)/, 'Mock project archive must return removed_at like the live server')
  assert.match(mockOperatorApiSource, /case '\/api\/sessions':/, 'Mock API must expose session detail lookup')
  assert.match(mockOperatorApiSource, /claudeSessionId/, 'Mock session detail must use the same claudeSessionId query seam as the page')
})

test('Nuxt UI color-mode auto-registration stays disabled', () => {
  const source = read(nuxtConfigPath)

  assert.match(source, /ui:\s*{[\s\S]*colorMode:\s*false/, 'Nuxt UI color-mode auto-registration must remain disabled')
  assert.match(source, /@nuxtjs\/color-mode/, 'the app still owns theme classes through @nuxtjs/color-mode')
})

test('operator console recovers from stale Nuxt chunk errors after deploy', () => {
  const configSource = read(nuxtConfigPath)
  const source = read(chunkReloadPluginPath)

  assert.match(configSource, /emitRouteChunkError:\s*['"]automatic-immediate['"]/, 'Nuxt must immediately recover from route chunk failures after deploy')
  assert.doesNotMatch(source, /reloadNuxtApp/, 'chunk reload plugin must use URL replacement instead of reloading a possibly stale document')
  assert.match(source, /app:chunkError/, 'Nuxt app chunk errors must be handled explicitly')
  assert.match(source, /vite:preloadError/, 'Vite preload errors must be handled explicitly')
  assert.match(source, /unhandledrejection/, 'dynamic-import promise rejections must be handled explicitly')
  assert.match(source, /window\.addEventListener\(['"]error['"],[\s\S]*\}, true\)/, 'module script error events must be handled in capture phase before Nuxt renders a 500 page')
  assert.match(source, /failed to fetch dynamically imported module/, 'browser dynamic import error text must be recognized')
  assert.match(source, /failed to load module script/, 'Chrome module script failure text must be recognized')
  assert.match(source, /isNuxtModuleScriptFailure/, 'filename-only Nuxt module script failures must be handled separately from text-pattern matching')
  assert.match(source, /function isNuxtModuleScriptFailure[\s\S]*event\.filename[\s\S]*_nuxt/, 'filename-only Nuxt chunk errors must recognize /_nuxt/*.js URLs')
  assert.match(source, /RELOAD_TTL_MS\s*=\s*30_000/, 'reload guard must bound repeated reload attempts')
  assert.match(source, /sessionStorage\.setItem\(RELOAD_KEY/, 'reload guard must persist a short-lived retry marker')
  assert.match(source, /RELOAD_QUERY_PARAM\s*=\s*['"]engram_chunk_reload['"]/, 'reload guard must have a storage-disabled URL fallback')
  assert.match(source, /url\.searchParams\.set\(RELOAD_QUERY_PARAM,\s*String\(Date\.now\(\)\)\)/, 'reload recovery must cache-bust the replacement URL')
  assert.match(source, /window\.location\.replace\(url\.toString\(\)\)/, 'chunk recovery must replace the URL instead of looping reloads')
})
