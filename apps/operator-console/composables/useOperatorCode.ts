import { onBeforeUnmount, onMounted, ref } from 'vue'
import { operatorApiUrl } from './useOperatorApi'

export type CodeBootstrapPhase = 'idle' | 'binding' | 'ready' | 'collision' | 'ambiguous' | 'reload-pending' | 'denied' | 'error'
export type CodePresentationKind = 'idle' | 'loading' | 'ready' | 'empty' | 'partial' | 'stale' | 'denied' | 'unsupported' | 'timeout' | 'offline' | 'error'

export type CodeCatalogState = 'idle' | 'loading' | 'ready' | 'empty' | 'denied' | 'unavailable' | 'offline'

export interface CodeResponseContext {
  sourceId: string
  checkoutId: string
  viewId: string
  profileId: string
  generation: number
}

export interface CodeSafeContext {
  source: string
  checkout: string
  view: string
  context: CodeResponseContext
}

export interface IndexIntentTarget {
  sourceId: string
  checkoutId: string
}

export interface CodeCatalogEntry {
  source: { id: string; label: string }
  checkout: { id: string; label: string }
  view: CodeSafeContext | null
  indexIntentAvailable: boolean
  indexIntentTarget: IndexIntentTarget | null
}

export interface CodeBootstrapEvidence {
  navigationType: string
  openerBefore: boolean
  openerAfter: boolean | null
  transition: string
}

export interface CodeSpan {
  byteStart: number
  byteEnd: number
  lineStart: number
  lineEnd: number
}

export interface CodeEntityRef {
  sourceId: string
  viewId: string
  entityKey: string
}

export interface CodeSourceDescriptor {
  entityKey: string
  span: CodeSpan
  contentDigest: string
}

export interface CodeItem {
  ref: CodeEntityRef
  path: string
  span: CodeSpan
  contentDigest: string
  kind: string
  language: string
  excerpt: string
  matchSources: string[]
  score: number | null
}

export interface CodeGraphEdge {
  from: CodeEntityRef
  to: CodeEntityRef
  relation: string
  evidenceKind: string
  explanation: string | null
}

export interface CodeGraph {
  nodes: CodeEntityRef[]
  edges: CodeGraphEdge[]
  stopReason: string
}

export interface CodeGraphNavigationNode {
  ref: CodeEntityRef
  source: CodeSourceDescriptor | null
}

export interface CodeGraphNavigation {
  nodes: CodeGraphNavigationNode[]
}

export interface CodeEnvelope {
  status: 'ok' | 'empty' | 'partial' | 'stale' | 'unavailable' | 'context_required' | 'forbidden'
  context: CodeResponseContext | null
  items: CodeItem[]
  graph: CodeGraph | null
  navigation: CodeGraphNavigation | null
  warnings: string[]
  retrievalMode: string | null
  freshnessState: string | null
  continuation: string | null
}

export interface CodeStatus {
  totalChunks: number
  embeddedChunks: number
  coverage: string
  freshnessState: string | null
}

export interface CodePresentationState {
  kind: CodePresentationKind
  message: string
}
export interface CodeGraphOptions {
  direction: 'incoming' | 'outgoing' | 'both'
  relations: string[]
}
export type IndexIntentKind = 'reindex' | 'reconcile'
export type IndexIntentState = 'submitted' | 'queued' | 'acknowledged' | 'running' | 'completed' | 'unavailable' | 'failed'


interface CodeSearchRequest {
  query: string
  limit: number
  pathPrefix: string
  languages: string[]
}

interface CodeGraphRequest {
  target: CodeEntityRef
  options: CodeGraphOptions
}
interface IndexIntentBase<State extends IndexIntentState> {
  intentRef: string
  state: State
  attempt: number
  retryable: boolean
  createdAt: string
  updatedAt: string
}

interface IndexIntentResult {
  viewRef: string
  generation: number
}

type IndexIntentRecord =
  | IndexIntentBase<'submitted'>
  | IndexIntentBase<'queued'>
  | IndexIntentBase<'acknowledged'>
  | IndexIntentBase<'running'>
  | (IndexIntentBase<'completed'> & { result: IndexIntentResult | null })
  | IndexIntentBase<'unavailable'>
  | IndexIntentBase<'failed'>

interface IndexIntentLifecyclePresentation<State extends IndexIntentState> {
  kind: State
  attempt: number | null
}

export type IndexIntentPresentationState =
  | { kind: 'idle' | 'loading' | 'error' | 'denied' | 'offline'; attempt: null }
  | IndexIntentLifecyclePresentation<'submitted'>
  | IndexIntentLifecyclePresentation<'queued'>
  | IndexIntentLifecyclePresentation<'acknowledged'>
  | IndexIntentLifecyclePresentation<'running'>
  | IndexIntentLifecyclePresentation<'completed'>
  | IndexIntentLifecyclePresentation<'unavailable'>
  | IndexIntentLifecyclePresentation<'failed'>

interface CodeBinding {
  tabBindingId: string
  documentProof: string
  resumeNonce: string
  reloadToken: string
}

interface CodeResumePair {
  tabBindingId: string
  resumeNonce: string
  reloadToken: string
}

interface CodeTransition {
  state: 'TAB_BINDING_READY' | 'TAB_BINDING_COLLISION' | 'TAB_BOOTSTRAP_AMBIGUOUS' | 'RELOAD_PENDING'
  binding: CodeBinding | null
}

type CodeApiResult =
  | { kind: 'success'; status: number; body: unknown }
  | { kind: 'denied'; status: number }
  | { kind: 'unsupported'; status: number }
  | { kind: 'timeout'; status: number }
  | { kind: 'offline'; status: number }
  | { kind: 'error'; status: number }

const RESUME_STORAGE_KEY = 'engram.operator-code.resume.v1'
const CONTEXT_SELECTION_STORAGE_KEY = 'engram.operator-code.context-selection.v1'
const REQUEST_TIMEOUT_MS = 30_000
const INDEX_INTENT_STORAGE_KEY = 'engram.operator-code.index-intent.v1'
const INDEX_INTENT_POLL_DELAY_MS = 1_000
const INDEX_INTENT_MAX_POLLS = 30

function text(value: unknown): string | null {
  if (typeof value !== 'string') return null
  const normalized = value.trim()
  return normalized === '' ? null : normalized
}

function finiteNumber(value: unknown): number | null {
  return typeof value === 'number' && Number.isFinite(value) ? value : null
}

function textList(value: unknown): string[] | null {
  if (!Array.isArray(value)) return null
  const values: string[] = []
  for (const item of value) {
    const parsed = text(item)
    if (parsed === null) return null
    values.push(parsed)
  }
  return values
}

function parseEntityRef(value: unknown): CodeEntityRef | null {
  if (value === null || typeof value !== 'object' || Array.isArray(value)) return null
  const sourceId = text(Reflect.get(value, 'source_id'))
  const viewId = text(Reflect.get(value, 'view_id'))
  const entityKey = text(Reflect.get(value, 'entity_key'))
  if (sourceId === null || viewId === null || entityKey === null) return null
  return { sourceId, viewId, entityKey }
}

function parseSpan(value: unknown): CodeSpan | null {
  if (value === null || typeof value !== 'object' || Array.isArray(value)) return null
  const byteStart = finiteNumber(Reflect.get(value, 'byte_start'))
  const byteEnd = finiteNumber(Reflect.get(value, 'byte_end'))
  const lineStart = finiteNumber(Reflect.get(value, 'line_start'))
  const lineEnd = finiteNumber(Reflect.get(value, 'line_end'))
  if (
    byteStart === null || byteEnd === null || lineStart === null || lineEnd === null
    || !Number.isInteger(byteStart) || !Number.isInteger(byteEnd)
    || !Number.isInteger(lineStart) || !Number.isInteger(lineEnd)
    || byteStart < 0 || byteEnd <= byteStart || lineStart < 1 || lineEnd < lineStart
  ) return null
  return { byteStart, byteEnd, lineStart, lineEnd }
}

function parseItem(value: unknown): CodeItem | null {
  if (value === null || typeof value !== 'object' || Array.isArray(value)) return null
  const ref = parseEntityRef(Reflect.get(value, 'ref'))
  const path = text(Reflect.get(value, 'path'))
  const span = parseSpan(Reflect.get(value, 'span'))
  const contentDigest = text(Reflect.get(value, 'content_digest'))
  const kind = text(Reflect.get(value, 'kind'))
  const language = text(Reflect.get(value, 'language'))
  const excerptValue = Reflect.get(value, 'excerpt')
  const excerpt = typeof excerptValue === 'string' ? excerptValue : null
  const matchSources = textList(Reflect.get(value, 'match_sources'))
  const scoreValue = Reflect.get(value, 'score')
  const score = scoreValue === undefined || scoreValue === null ? null : finiteNumber(scoreValue)
  if (
    ref === null || path === null || span === null || contentDigest === null || kind === null
    || language === null || excerpt === null || matchSources === null || (scoreValue !== undefined && scoreValue !== null && score === null)
  ) return null
  return { ref, path, span, contentDigest, kind, language, excerpt, matchSources, score }
}

function parseResponseContext(value: unknown): CodeResponseContext | null {
  if (value === null || typeof value !== 'object' || Array.isArray(value)) return null
  const sourceId = text(Reflect.get(value, 'source_id'))
  const checkoutId = text(Reflect.get(value, 'checkout_id'))
  const viewId = text(Reflect.get(value, 'view_id'))
  const profileId = text(Reflect.get(value, 'profile_id'))
  const generation = finiteNumber(Reflect.get(value, 'generation'))
  if (
    sourceId === null || checkoutId === null || viewId === null || profileId === null || generation === null
    || !Number.isInteger(generation) || generation < 1
  ) return null
  return { sourceId, checkoutId, viewId, profileId, generation }
}

function entityRefKey(ref: CodeEntityRef): string {
  return `${ref.sourceId}\u0000${ref.viewId}\u0000${ref.entityKey}`
}

function contextKey(context: CodeResponseContext): string {
  return `${context.sourceId}\u0000${context.checkoutId}\u0000${context.viewId}\u0000${context.profileId}\u0000${context.generation}`
}

function parseCatalogContext(value: unknown): CodeResponseContext | null {
  if (value === null || typeof value !== 'object' || Array.isArray(value)) return null
  const sourceId = text(Reflect.get(value, 'source_id'))
  const checkoutId = text(Reflect.get(value, 'checkout_id'))
  const viewId = text(Reflect.get(value, 'view_id'))
  const profileId = text(Reflect.get(value, 'analysis_profile_id'))
  const generation = finiteNumber(Reflect.get(value, 'generation'))
  if (
    sourceId === null || checkoutId === null || viewId === null || profileId === null || generation === null
    || !Number.isInteger(generation) || generation < 1
  ) return null
  return { sourceId, checkoutId, viewId, profileId, generation }
}

function parseCatalogLabel(value: unknown): { id: string; label: string } | null {
  if (value === null || typeof value !== 'object' || Array.isArray(value)) return null
  const id = text(Reflect.get(value, 'id'))
  const label = text(Reflect.get(value, 'label'))
  return id === null || label === null ? null : { id, label }
}

function parseCatalogEntry(value: unknown): CodeCatalogEntry | null {
  if (value === null || typeof value !== 'object' || Array.isArray(value)) return null
  const source = parseCatalogLabel(Reflect.get(value, 'source'))
  const checkout = parseCatalogLabel(Reflect.get(value, 'checkout'))
  const indexIntentAvailable = Reflect.get(value, 'index_intent_available')
  const profileValue = Reflect.get(value, 'analysis_profile_id')
  const profileId = profileValue === undefined ? null : text(profileValue)
  const viewValue = Reflect.get(value, 'view')
  if (source === null || checkout === null || typeof indexIntentAvailable !== 'boolean') return null
  if (viewValue === null) {
    if (indexIntentAvailable !== (profileId !== null)) return null
    return {
      source,
      checkout,
      view: null,
      indexIntentAvailable,
      indexIntentTarget: profileId === null ? null : { sourceId: source.id, checkoutId: checkout.id },
    }
  }
  if (profileValue !== undefined || viewValue === undefined || typeof viewValue !== 'object' || Array.isArray(viewValue)) return null
  const context = parseCatalogContext(Reflect.get(viewValue, 'context_ref'))
  const view = text(Reflect.get(viewValue, 'label'))
  if (context === null || view === null || context.sourceId !== source.id || context.checkoutId !== checkout.id) return null
  return { source, checkout, view: { source: source.label, checkout: checkout.label, view, context }, indexIntentAvailable, indexIntentTarget: null }
}

function parseCatalog(value: unknown): CodeCatalogEntry[] | null {
  if (value === null || typeof value !== 'object' || Array.isArray(value)) return null
  const values = Reflect.get(value, 'contexts')
  if (!Array.isArray(values)) return null
  const entries: CodeCatalogEntry[] = []
  const contexts = new Set<string>()
  for (const value of values) {
    const entry = parseCatalogEntry(value)
    if (entry === null) return null
    if (entry.view !== null) {
      const key = contextKey(entry.view.context)
      if (contexts.has(key)) return null
      contexts.add(key)
    }
    entries.push(entry)
  }
  return entries
}

function parseGraph(value: unknown): CodeGraph | null {
  if (value === null || typeof value !== 'object' || Array.isArray(value)) return null
  const nodeValues = Reflect.get(value, 'nodes')
  const edgeValues = Reflect.get(value, 'edges')
  if (!Array.isArray(nodeValues) || !Array.isArray(edgeValues)) return null
  const nodes: CodeEntityRef[] = []
  const nodeKeys = new Set<string>()
  for (const rawNode of nodeValues) {
    const node = parseEntityRef(rawNode)
    if (node === null || nodeKeys.has(entityRefKey(node))) return null
    nodeKeys.add(entityRefKey(node))
    nodes.push(node)
  }
  const edges: CodeGraphEdge[] = []
  for (const rawEdge of edgeValues) {
    if (rawEdge === null || typeof rawEdge !== 'object' || Array.isArray(rawEdge)) return null
    const from = parseEntityRef(Reflect.get(rawEdge, 'from'))
    const to = parseEntityRef(Reflect.get(rawEdge, 'to'))
    const relation = text(Reflect.get(rawEdge, 'relation'))
    const evidenceKind = text(Reflect.get(rawEdge, 'evidence_kind'))
    const explanationValue = Reflect.get(rawEdge, 'explanation')
    const explanation = explanationValue === undefined || explanationValue === null ? null : text(explanationValue)
    if (
      from === null || to === null || relation === null || evidenceKind === null
      || !nodeKeys.has(entityRefKey(from)) || !nodeKeys.has(entityRefKey(to))
      || (explanationValue !== undefined && explanationValue !== null && explanation === null)
    ) return null
    edges.push({ from, to, relation, evidenceKind, explanation })
  }
  const stopReason = text(Reflect.get(value, 'stop_reason'))
  return stopReason === null ? null : { nodes, edges, stopReason }
}

function parseSourceDescriptor(value: unknown): CodeSourceDescriptor | null {
  if (value === null || typeof value !== 'object' || Array.isArray(value)) return null
  const entityKey = text(Reflect.get(value, 'entity_key'))
  const span = parseSpan(Reflect.get(value, 'span'))
  const contentDigest = text(Reflect.get(value, 'content_digest'))
  return entityKey === null || span === null || contentDigest === null ? null : { entityKey, span, contentDigest }
}

function parseGraphNavigation(value: unknown, context: CodeResponseContext, graph: CodeGraph): CodeGraphNavigation | null {
  if (value === null || typeof value !== 'object' || Array.isArray(value)) return null
  const nodeValues = Reflect.get(value, 'nodes')
  const edgeValues = Reflect.get(value, 'edges')
  if (!Array.isArray(nodeValues) || !Array.isArray(edgeValues)) return null
  const graphNodes = new Set(graph.nodes.map(entityRefKey))
  const nodes: CodeGraphNavigationNode[] = []
  const navigationNodes = new Set<string>()
  for (const value of nodeValues) {
    if (value === null || typeof value !== 'object' || Array.isArray(value)) return null
    const ref = parseEntityRef(Reflect.get(value, 'entity'))
    const nodeContext = parseCatalogContext(Reflect.get(value, 'context_ref'))
    const sourceState = text(Reflect.get(value, 'source_state'))
    const sourceValue = Reflect.get(value, 'source_read')
    const source = sourceValue === undefined || sourceValue === null ? null : parseSourceDescriptor(sourceValue)
    if (
      ref === null || nodeContext === null || !sameView(context, nodeContext) || !graphNodes.has(entityRefKey(ref))
      || navigationNodes.has(entityRefKey(ref)) || (sourceState !== 'available' && sourceState !== 'unavailable')
      || (sourceState === 'available' && (source === null || source.entityKey !== ref.entityKey))
      || (sourceState === 'unavailable' && source !== null)
    ) return null
    navigationNodes.add(entityRefKey(ref))
    nodes.push({ ref, source })
  }
  if (navigationNodes.size !== graphNodes.size) return null
  for (const value of edgeValues) {
    if (value === null || typeof value !== 'object' || Array.isArray(value)) return null
    const fromValue = Reflect.get(value, 'from')
    const toValue = Reflect.get(value, 'to')
    const from = fromValue !== null && typeof fromValue === 'object' && !Array.isArray(fromValue) ? parseEntityRef(Reflect.get(fromValue, 'entity')) : null
    const to = toValue !== null && typeof toValue === 'object' && !Array.isArray(toValue) ? parseEntityRef(Reflect.get(toValue, 'entity')) : null
    if (from === null || to === null || !navigationNodes.has(entityRefKey(from)) || !navigationNodes.has(entityRefKey(to)) || text(Reflect.get(value, 'relation')) === null || text(Reflect.get(value, 'evidence_kind')) === null) return null
  }
  return { nodes }
}

function parseEnvelope(value: unknown): CodeEnvelope | null {
  if (value === null || typeof value !== 'object' || Array.isArray(value)) return null
  if (Reflect.get(value, 'schema') !== 'engram.code-query/1') return null
  const status = text(Reflect.get(value, 'status'))
  if (
    status !== 'ok' && status !== 'empty' && status !== 'partial' && status !== 'stale'
    && status !== 'unavailable' && status !== 'context_required' && status !== 'forbidden'
  ) return null
  const contextsValue = Reflect.get(value, 'contexts')
  const contexts: CodeResponseContext[] = []
  if (contextsValue !== undefined) {
    if (!Array.isArray(contextsValue)) return null
    for (const rawContext of contextsValue) {
      const context = parseResponseContext(rawContext)
      if (context === null) return null
      contexts.push(context)
    }
  }
  const itemsValue = Reflect.get(value, 'items')
  const items: CodeItem[] = []
  if (itemsValue !== undefined) {
    if (!Array.isArray(itemsValue)) return null
    for (const rawItem of itemsValue) {
      const item = parseItem(rawItem)
      if (item === null) return null
      items.push(item)
    }
  }
  const graphValue = Reflect.get(value, 'graph')
  const graph = graphValue === undefined || graphValue === null ? null : parseGraph(graphValue)
  if (graphValue !== undefined && graphValue !== null && graph === null) return null
  const warningsValue = Reflect.get(value, 'warnings')
  const warnings = warningsValue === undefined ? [] : textList(warningsValue)
  if (warnings === null) return null
  const retrieval = Reflect.get(value, 'retrieval')
  const retrievalMode = retrieval !== null && typeof retrieval === 'object' && !Array.isArray(retrieval)
    ? text(Reflect.get(retrieval, 'mode'))
    : null
  const freshness = Reflect.get(value, 'freshness')
  const freshnessState = freshness !== null && typeof freshness === 'object' && !Array.isArray(freshness)
    ? text(Reflect.get(freshness, 'state'))
    : null
  const contextual = status === 'ok' || status === 'empty' || status === 'partial' || status === 'stale'
  const continuationValue = Reflect.get(value, 'continuation')
  const continuation = continuationValue === undefined || continuationValue === null ? null : text(continuationValue)
  const truncated = Reflect.get(value, 'truncated')
  const coverage = Reflect.get(value, 'coverage')
  if (continuationValue !== undefined && continuationValue !== null && continuation === null) return null
  if (contextual && (
    contextsValue === undefined || contexts.length !== 1 || itemsValue === undefined || warningsValue === undefined
    || retrievalMode === null || freshnessState === null || coverage === null || typeof coverage !== 'object' || Array.isArray(coverage)
    || typeof truncated !== 'boolean' || truncated !== (continuation !== null)
  )) return null
  const context = contexts[0] ?? null
  if (context !== null && (
    !items.every((item) => item.ref.sourceId === context.sourceId && item.ref.viewId === context.viewId)
    || graph !== null && !graph.nodes.every((node) => node.sourceId === context.sourceId && node.viewId === context.viewId)
  )) return null
  const navigationValue = Reflect.get(value, 'navigation')
  const navigation = navigationValue === undefined ? null : context === null || graph === null ? null : parseGraphNavigation(navigationValue, context, graph)
  if (navigationValue !== undefined && navigation === null) return null
  if (!contextual && (
    contextsValue !== undefined || freshness !== undefined || retrieval !== undefined || coverage !== undefined
    || itemsValue !== undefined || graphValue !== undefined || truncated !== undefined || warningsValue !== undefined
    || continuationValue !== undefined || navigationValue !== undefined
  )) return null
  return {
    status,
    context,
    items,
    graph,
    navigation,
    warnings,
    retrievalMode,
    freshnessState,
    continuation,
  }
}
function parseTransition(value: unknown): CodeTransition | null {
  if (value === null || typeof value !== 'object' || Array.isArray(value)) return null
  const state = text(Reflect.get(value, 'state'))
  if (state === 'RELOAD_PENDING') return { state, binding: null }
  if (state !== 'TAB_BINDING_READY' && state !== 'TAB_BINDING_COLLISION' && state !== 'TAB_BOOTSTRAP_AMBIGUOUS') return null
  const tabBindingId = text(Reflect.get(value, 'tab_binding_id'))
  const documentProof = text(Reflect.get(value, 'document_proof'))
  const resumeNonce = text(Reflect.get(value, 'resume_nonce'))
  const reloadToken = text(Reflect.get(value, 'reload_token'))
  if (tabBindingId === null || documentProof === null || resumeNonce === null || reloadToken === null) return null
  return { state, binding: { tabBindingId, documentProof, resumeNonce, reloadToken } }
}


function parseStatus(value: unknown): CodeStatus | null {
  if (value === null || typeof value !== 'object' || Array.isArray(value)) return null
  const totalChunks = finiteNumber(Reflect.get(value, 'total_chunks'))
  const embeddedChunks = finiteNumber(Reflect.get(value, 'embedded_chunks'))
  const embedding = Reflect.get(value, 'embedding')
  const coverage = embedding !== null && typeof embedding === 'object' && !Array.isArray(embedding)
    ? text(Reflect.get(embedding, 'Coverage'))
    : null
  const freshness = Reflect.get(value, 'freshness')
  const freshnessState = freshness !== null && typeof freshness === 'object' && !Array.isArray(freshness)
    ? text(Reflect.get(freshness, 'state'))
    : null
  if (
    totalChunks === null || embeddedChunks === null || coverage === null
    || !Number.isInteger(totalChunks) || !Number.isInteger(embeddedChunks)
    || totalChunks < 0 || embeddedChunks < 0 || embeddedChunks > totalChunks
  ) return null
  return { totalChunks, embeddedChunks, coverage, freshnessState }
}

function timestamp(value: unknown): string | null {
  const parsed = text(value)
  return parsed === null || Number.isNaN(Date.parse(parsed)) ? null : parsed
}

function parseIndexIntentResult(value: unknown): IndexIntentResult | null {
  if (value === null || typeof value !== 'object' || Array.isArray(value)) return null
  const viewRef = text(Reflect.get(value, 'view_ref'))
  const generation = finiteNumber(Reflect.get(value, 'generation'))
  if (viewRef === null || generation === null || !Number.isInteger(generation) || generation < 1) return null
  return { viewRef, generation }
}

function parseIndexIntent(value: unknown): IndexIntentRecord | null {
  if (value === null || typeof value !== 'object' || Array.isArray(value)) return null
  const intentRef = text(Reflect.get(value, 'intent_ref'))
  const state = text(Reflect.get(value, 'state'))
  const attempt = finiteNumber(Reflect.get(value, 'attempt'))
  const retryable = Reflect.get(value, 'retryable')
  const createdAt = timestamp(Reflect.get(value, 'created_at'))
  const updatedAt = timestamp(Reflect.get(value, 'updated_at'))
  if (
    intentRef === null || attempt === null || !Number.isInteger(attempt) || attempt < 0
    || typeof retryable !== 'boolean' || createdAt === null || updatedAt === null
  ) return null
  const resultValue = Reflect.get(value, 'result')
  const base = { intentRef, attempt, retryable, createdAt, updatedAt }
  switch (state) {
    case 'submitted':
    case 'queued':
    case 'acknowledged':
    case 'running':
    case 'unavailable':
    case 'failed':
      if (resultValue !== undefined && resultValue !== null || retryable !== (state === 'unavailable')) return null
      return { ...base, state }
    case 'completed': {
      if (retryable) return null
      const result = resultValue === undefined || resultValue === null ? null : parseIndexIntentResult(resultValue)
      if (resultValue !== undefined && resultValue !== null && result === null) return null
      return { ...base, state, result }
    }
    default:
      return null
  }
}

function parseIndexIntentAcknowledgement(value: unknown): Extract<IndexIntentRecord, { state: 'submitted' | 'queued' }> | null {
  const intent = parseIndexIntent(value)
  return intent !== null && (intent.state === 'submitted' || intent.state === 'queued') ? intent : null
}

function indexIntentNotice(kind: 'idle' | 'loading' | 'error' | 'denied' | 'offline'): IndexIntentPresentationState {
  return { kind, attempt: null }
}

function indexIntentPresentation(intent: IndexIntentRecord): IndexIntentPresentationState {
  const attempt = intent.attempt > 0 ? intent.attempt : null
  switch (intent.state) {
    case 'submitted':
    case 'queued':
    case 'acknowledged':
    case 'running':
    case 'unavailable':
    case 'failed':
    case 'completed':
      return { kind: intent.state, attempt }
    default: {
      const exhaustive: never = intent
      return exhaustive
    }
  }
}

interface IndexIntentResume {
  requestRef: string
  kind: IndexIntentKind
  target?: IndexIntentTarget
  intentRef?: string
}


function presentation(kind: CodePresentationKind, message: string): CodePresentationState {
  return { kind, message }
}

function presentationFromEnvelope(envelope: CodeEnvelope): CodePresentationState {
  switch (envelope.status) {
    case 'ok': return presentation('ready', 'Released result is available for the pinned view.')
    case 'empty': return presentation('empty', 'The released query completed with no matches.')
    case 'partial': return presentation('partial', 'The server released a bounded partial result.')
    case 'stale': return presentation('stale', 'The server released this result with stale-view evidence.')
    case 'forbidden': return presentation('denied', 'The server denied this request without contextual content.')
    case 'context_required': return presentation('unsupported', 'The server requires a current pinned context.')
    case 'unavailable': return presentation('error', 'The server could not release this contextual response.')
  }
}

function sameView(left: CodeResponseContext | null, right: CodeResponseContext | null): boolean {
  return left !== null && right !== null
    && left.sourceId === right.sourceId
    && left.checkoutId === right.checkoutId
    && left.viewId === right.viewId
    && left.profileId === right.profileId
    && left.generation === right.generation
}

function navigationType(): string {
  const entry = globalThis.performance?.getEntriesByType('navigation')[0]
  return entry instanceof PerformanceNavigationTiming ? entry.type : 'unknown'
}

function requestId(): string | null {
  return typeof globalThis.crypto?.randomUUID === 'function' ? globalThis.crypto.randomUUID() : null
}

function loadResumePair(): CodeResumePair | null {
  try {
    const raw = sessionStorage.getItem(RESUME_STORAGE_KEY)
    if (raw === null) return null
    const parsed: unknown = JSON.parse(raw)
    if (parsed === null || typeof parsed !== 'object' || Array.isArray(parsed)) return null
    const tabBindingId = text(Reflect.get(parsed, 'tabBindingId'))
    const resumeNonce = text(Reflect.get(parsed, 'resumeNonce'))
    const reloadToken = text(Reflect.get(parsed, 'reloadToken'))
    if (tabBindingId === null || resumeNonce === null || reloadToken === null) return null
    return { tabBindingId, resumeNonce, reloadToken }
  } catch {
    return null
  }
}

function persistResumePair(binding: CodeBinding): boolean {
  try {
    sessionStorage.setItem(RESUME_STORAGE_KEY, JSON.stringify({
      tabBindingId: binding.tabBindingId,
      resumeNonce: binding.resumeNonce,
      reloadToken: binding.reloadToken,
    }))
    return true
  } catch {
    return false
  }
}

function clearResumePair(): void {
  try {
    sessionStorage.removeItem(RESUME_STORAGE_KEY)
  } catch {
    // Storage failure removes only reload convenience; it never creates an authorization fallback.
  }
}
function loadContextSelection(): string | null {
  try {
    return text(sessionStorage.getItem(CONTEXT_SELECTION_STORAGE_KEY))
  } catch {
    return null
  }
}

function persistContextSelection(context: CodeSafeContext): boolean {
  try {
    sessionStorage.setItem(CONTEXT_SELECTION_STORAGE_KEY, contextKey(context.context))
    return true
  } catch {
    return false
  }
}

function clearContextSelection(): void {
  try {
    sessionStorage.removeItem(CONTEXT_SELECTION_STORAGE_KEY)
  } catch {
    // Selection storage is a convenience only; it never grants or restores a pin.
  }
}


function loadIndexIntentResume(): IndexIntentResume | null {
  try {
    const raw = sessionStorage.getItem(INDEX_INTENT_STORAGE_KEY)
    if (raw === null) return null
    const parsed: unknown = JSON.parse(raw)
    if (parsed === null || typeof parsed !== 'object' || Array.isArray(parsed)) return null
    const requestRef = text(Reflect.get(parsed, 'requestRef'))
    const kind = Reflect.get(parsed, 'kind')
    const targetValue = Reflect.get(parsed, 'target')
    const target = targetValue === undefined ? undefined : parseStoredIndexIntentTarget(targetValue)
    const intentRefValue = Reflect.get(parsed, 'intentRef')
    if (requestRef === null || (kind !== 'reindex' && kind !== 'reconcile') || target === null) return null
    if (intentRefValue === undefined) return target === undefined ? { requestRef, kind } : { requestRef, kind, target }
    const intentRef = text(intentRefValue)
    return intentRef === null ? null : target === undefined ? { requestRef, kind, intentRef } : { requestRef, kind, target, intentRef }
  } catch {
    return null
  }
}
function parseStoredIndexIntentTarget(value: unknown): IndexIntentTarget | null {
  if (value === null || typeof value !== 'object' || Array.isArray(value)) return null
  const sourceId = text(Reflect.get(value, 'sourceId'))
  const checkoutId = text(Reflect.get(value, 'checkoutId'))
  return sourceId === null || checkoutId === null ? null : { sourceId, checkoutId }
}


function persistIndexIntentResume(value: IndexIntentResume): boolean {
  try {
    sessionStorage.setItem(INDEX_INTENT_STORAGE_KEY, JSON.stringify({
      requestRef: value.requestRef,
      kind: value.kind,
      ...(value.target === undefined ? {} : { target: value.target }),
      ...(value.intentRef === undefined ? {} : { intentRef: value.intentRef }),
    }))
    return true
  } catch {
    return false
  }
}

function clearIndexIntentResume(): void {
  try {
    sessionStorage.removeItem(INDEX_INTENT_STORAGE_KEY)
  } catch {
    // Storage retains no authority; failing to clear it only prevents convenience cleanup.
  }
}

export function useOperatorCode() {
  const bootstrapPhase = ref<CodeBootstrapPhase>('idle')
  const bootstrapEvidence = ref<CodeBootstrapEvidence>({ navigationType: 'unknown', openerBefore: false, openerAfter: null, transition: 'idle' })
  const binding = ref<CodeBinding | null>(null)
  const contextCatalog = ref<CodeCatalogEntry[]>([])
  const contextState = ref<CodeCatalogState>('idle')
  const contextCandidate = ref<CodeSafeContext | null>(null)
  const pinnedContext = ref<CodeSafeContext | null>(null)
  const status = ref<CodeStatus | null>(null)
  const searchEnvelope = ref<CodeEnvelope | null>(null)
  const graphEnvelope = ref<CodeEnvelope | null>(null)
  const sourceEnvelope = ref<CodeEnvelope | null>(null)
  const activeSearchRequest = ref<CodeSearchRequest | null>(null)
  const activeGraphRequest = ref<CodeGraphRequest | null>(null)
  const searchContinuationNotice = ref<'denied' | 'unavailable' | null>(null)
  const searchState = ref<CodePresentationState>(presentation('idle', 'Pin an authorized view before searching.'))
  const graphState = ref<CodePresentationState>(presentation('idle', 'Choose a released search result to explore relationships.'))
  const sourceState = ref<CodePresentationState>(presentation('idle', 'Choose a released search result to read an exact source span.'))
  const pending = ref(false)
  const indexIntentState = ref<IndexIntentPresentationState>(indexIntentNotice('idle'))
  const indexIntentPending = ref(false)
  const indexIntentResume = ref<IndexIntentResume | null>(loadIndexIntentResume())
  let indexIntentPollTimer: number | null = null
  let indexIntentPollGeneration = 0
  let indexIntentPollCount = 0

  function bindingPayload(extra: Record<string, unknown> = {}): Record<string, unknown> | null {
    if (binding.value === null) return null
    return { tab_binding_id: binding.value.tabBindingId, document_proof: binding.value.documentProof, ...extra }
  }

  async function request(path: string, method: string, body?: Record<string, unknown>, extraHeaders: Record<string, string> = {}): Promise<CodeApiResult> {
    const id = requestId()
    if (id === null) return { kind: 'error', status: 0 }
    if (!navigator.onLine) return { kind: 'offline', status: 0 }
    const controller = new AbortController()
    const timeout = window.setTimeout(() => controller.abort(), REQUEST_TIMEOUT_MS)
    const headers: Record<string, string> = { 'X-Engram-Request-ID': id, ...extraHeaders }
    if (body !== undefined) headers['Content-Type'] = 'application/json'
    try {
      const response = await fetch(operatorApiUrl(path), {
        method,
        credentials: 'include',
        headers,
        ...(body === undefined ? {} : { body: JSON.stringify(body) }),
        signal: controller.signal,
      })
      if (response.status === 204) return { kind: 'success', status: response.status, body: undefined }
      if (!response.ok) {
        if (response.status === 401 || response.status === 403) return { kind: 'denied', status: response.status }
        if (response.status === 408) return { kind: 'timeout', status: response.status }
        if (response.status === 404 || response.status === 405 || response.status === 409 || response.status === 422) return { kind: 'unsupported', status: response.status }
        return { kind: 'error', status: response.status }
      }
      const raw = await response.text()
      if (raw.trim() === '') return { kind: 'success', status: response.status, body: undefined }
      try {
        const parsed: unknown = JSON.parse(raw)
        return { kind: 'success', status: response.status, body: parsed }
      } catch {
        return { kind: 'error', status: response.status }
      }
    } catch (error) {
      return error instanceof DOMException && error.name === 'AbortError'
        ? { kind: 'timeout', status: 0 }
        : { kind: 'offline', status: 0 }
    } finally {
      window.clearTimeout(timeout)
    }
  }

  function clearContextualResults(): void {
    status.value = null
    searchEnvelope.value = null
    graphEnvelope.value = null
    sourceEnvelope.value = null
    activeSearchRequest.value = null
    activeGraphRequest.value = null
    searchContinuationNotice.value = null
    searchState.value = presentation('idle', 'Pin an authorized view before searching.')
    graphState.value = presentation('idle', 'Choose a released search result to explore relationships.')
    sourceState.value = presentation('idle', 'Choose a released search result to read an exact source span.')
  }

  function stopIndexIntentPolling(): void {
    indexIntentPollGeneration += 1
    if (indexIntentPollTimer !== null) window.clearTimeout(indexIntentPollTimer)
    indexIntentPollTimer = null
    indexIntentPollCount = 0
  }

  function clearIndexIntent(): void {
    stopIndexIntentPolling()
    indexIntentResume.value = null
    clearIndexIntentResume()
    indexIntentPending.value = false
    indexIntentState.value = indexIntentNotice('idle')
  }

  function indexIntentFailure(result: Exclude<CodeApiResult, { kind: 'success' }>): IndexIntentPresentationState {
    switch (result.kind) {
      case 'denied': return indexIntentNotice('denied')
      case 'offline': return indexIntentNotice('offline')
      case 'timeout':
      case 'unsupported':
      case 'error':
        return indexIntentNotice('error')
      default: {
        const exhaustive: never = result
        return exhaustive
      }
    }
  }


  async function applyIndexIntent(intent: IndexIntentRecord): Promise<void> {
    indexIntentState.value = indexIntentPresentation(intent)
    if (intent.state === 'submitted' || intent.state === 'queued' || intent.state === 'acknowledged' || intent.state === 'running') {
      scheduleIndexIntentPoll()
      return
    }
    stopIndexIntentPolling()
    if (intent.state === 'completed') await discoverContext()
  }

  function scheduleIndexIntentPoll(): void {
    const resume = indexIntentResume.value
    if (resume?.intentRef === undefined || indexIntentPollTimer !== null || indexIntentPollCount >= INDEX_INTENT_MAX_POLLS) {
      if (indexIntentPollCount >= INDEX_INTENT_MAX_POLLS) {
        indexIntentState.value = indexIntentNotice('error')
      }
      return
    }
    const generation = indexIntentPollGeneration
    indexIntentPollTimer = window.setTimeout(() => {
      indexIntentPollTimer = null
      indexIntentPollCount += 1
      void loadIndexIntent(generation)
    }, INDEX_INTENT_POLL_DELAY_MS)
  }

  async function loadIndexIntent(pollGeneration?: number): Promise<void> {
    const requestGeneration = pollGeneration ?? indexIntentPollGeneration
    const current = binding.value
    const resume = indexIntentResume.value
    if (current === null || resume?.intentRef === undefined || indexIntentPending.value) return
    indexIntentPending.value = true
    const result = await request(`/code/index-intents/${encodeURIComponent(resume.intentRef)}`, 'GET', undefined, {
      'X-Engram-Tab-Binding-ID': current.tabBindingId,
      'X-Engram-Document-Proof': current.documentProof,
    })
    if (requestGeneration !== indexIntentPollGeneration) return
    indexIntentPending.value = false
    if (result.kind !== 'success') {
      stopIndexIntentPolling()
      indexIntentState.value = indexIntentFailure(result)
      return
    }
    const intent = parseIndexIntent(result.body)
    if (intent === null || intent.intentRef !== resume.intentRef) {
      stopIndexIntentPolling()
      indexIntentState.value = indexIntentNotice('error')
      return
    }
    await applyIndexIntent(intent)
  }

  async function submitIndexIntent(kind: IndexIntentKind, target?: IndexIntentTarget): Promise<void> {
    if (binding.value === null || target === undefined && pinnedContext.value === null || indexIntentPending.value || pending.value) return
    const existing = indexIntentResume.value
    const requestRef = existing !== null && existing.kind === kind && existing.intentRef === undefined
      && (existing.target === undefined || target === undefined ? existing.target === target : existing.target.sourceId === target.sourceId && existing.target.checkoutId === target.checkoutId)
      ? existing.requestRef
      : requestId()
    if (requestRef === null) {
      indexIntentState.value = indexIntentNotice('error')
      return
    }
    const resume: IndexIntentResume = target === undefined ? { requestRef, kind } : { requestRef, kind, target }
    if (!persistIndexIntentResume(resume)) {
      indexIntentState.value = indexIntentNotice('error')
      return
    }
    stopIndexIntentPolling()
    const requestGeneration = indexIntentPollGeneration
    indexIntentResume.value = resume
    indexIntentState.value = indexIntentNotice('loading')
    indexIntentPending.value = true
    const payload = bindingPayload({
      request_ref: requestRef,
      kind,
      ...(target === undefined ? {} : { target: { source_id: target.sourceId, checkout_id: target.checkoutId } }),
    })
    let result: CodeApiResult
    if (payload === null) {
      result = { kind: 'error', status: 0 }
    } else {
      result = await request('/code/index-intents', 'POST', payload)
    }
    if (requestGeneration !== indexIntentPollGeneration) return
    indexIntentPending.value = false
    if (result.kind !== 'success' || result.status !== 202) {
      stopIndexIntentPolling()
      indexIntentState.value = result.kind === 'success' ? indexIntentNotice('error') : indexIntentFailure(result)
      return
    }
    const intent = parseIndexIntentAcknowledgement(result.body)
    if (intent === null) {
      stopIndexIntentPolling()
      indexIntentState.value = indexIntentNotice('error')
      return
    }
    const reconciled: IndexIntentResume = { ...resume, intentRef: intent.intentRef }
    indexIntentResume.value = reconciled
    persistIndexIntentResume(reconciled)
    await applyIndexIntent(intent)
  }

  async function retryIndexIntent(): Promise<void> {
    const current = binding.value
    const resume = indexIntentResume.value
    if (current === null || resume?.intentRef === undefined || indexIntentState.value.kind !== 'unavailable' || indexIntentPending.value || pending.value) return
    stopIndexIntentPolling()
    const requestGeneration = indexIntentPollGeneration
    indexIntentState.value = indexIntentNotice('loading')
    indexIntentPending.value = true
    const payload = bindingPayload()
    if (payload === null) {
      indexIntentPending.value = false
      indexIntentState.value = indexIntentNotice('error')
      return
    }
    const result = await request(`/code/index-intents/${encodeURIComponent(resume.intentRef)}/retry`, 'POST', payload)
    if (requestGeneration !== indexIntentPollGeneration) return
    indexIntentPending.value = false
    if (result.kind !== 'success' || result.status !== 202) {
      indexIntentState.value = result.kind === 'success' ? indexIntentNotice('error') : indexIntentFailure(result)
      return
    }
    const intent = parseIndexIntentAcknowledgement(result.body)
    if (intent === null || intent.intentRef !== resume.intentRef) {
      indexIntentState.value = indexIntentNotice('error')
      return
    }
    await applyIndexIntent(intent)
  }

  async function refreshIndexIntent(): Promise<void> {
    const resume = indexIntentResume.value
    if (resume === null || indexIntentPending.value) return
    stopIndexIntentPolling()
    if (resume.intentRef === undefined) {
      await submitIndexIntent(resume.kind, resume.target)
      return
    }
    await loadIndexIntent()
  }

  async function reconcileIndexIntent(): Promise<void> {
    const resume = indexIntentResume.value
    if (resume === null) return
    if (resume.intentRef === undefined) {
      await submitIndexIntent(resume.kind, resume.target)
      return
    }
    await loadIndexIntent()
  }

  function applyTransition(transition: CodeTransition, evidence: CodeBootstrapEvidence): boolean {
    const previousBinding = binding.value
    const replaced = transition.state === 'TAB_BINDING_COLLISION' || transition.state === 'TAB_BOOTSTRAP_AMBIGUOUS'
      || previousBinding !== null && previousBinding.tabBindingId !== transition.binding?.tabBindingId
    if (replaced) {
      clearIndexIntent()
      clearContextSelection()
    }
    bootstrapEvidence.value = { ...evidence, transition: transition.state }
    binding.value = transition.binding
    contextCatalog.value = []
    contextState.value = 'idle'
    contextCandidate.value = null
    pinnedContext.value = null
    clearContextualResults()
    if (transition.state === 'RELOAD_PENDING') {
      bootstrapPhase.value = 'reload-pending'
      return false
    }
    if (transition.binding === null || !persistResumePair(transition.binding)) {
      binding.value = null
      clearResumePair()
      bootstrapPhase.value = 'ambiguous'
      return false
    }
    bootstrapPhase.value = transition.state === 'TAB_BINDING_COLLISION'
      ? 'collision'
      : transition.state === 'TAB_BOOTSTRAP_AMBIGUOUS'
        ? 'ambiguous'
        : 'ready'
    return true
  }

  async function handshake(documentNonce: string, copied: CodeResumePair | null, ambiguous: boolean, evidence: CodeBootstrapEvidence): Promise<boolean> {
    const body: Record<string, unknown> = { document_nonce: documentNonce }
    if (copied !== null) {
      body.copied_tab_binding_id = copied.tabBindingId
      body.copied_resume_nonce = copied.resumeNonce
    }
    if (ambiguous) body.ambiguous = true
    const result = await request('/code/tabs/handshake', 'POST', body)
    if (result.kind !== 'success') {
      bootstrapPhase.value = result.kind === 'denied' ? 'denied' : 'error'
      return false
    }
    const transition = parseTransition(result.body)
    if (transition === null) {
      bootstrapPhase.value = 'error'
      return false
    }
    return applyTransition(transition, evidence)
  }

  async function resume(documentNonce: string, pair: CodeResumePair, evidence: CodeBootstrapEvidence): Promise<boolean> {
    const result = await request('/code/tabs/resume', 'POST', {
      tab_binding_id: pair.tabBindingId,
      resume_nonce: pair.resumeNonce,
      reload_token: pair.reloadToken,
      document_nonce: documentNonce,
    })
    if (result.kind !== 'success') {
      clearResumePair()
      bootstrapPhase.value = result.kind === 'denied' ? 'denied' : 'error'
      return false
    }
    const transition = parseTransition(result.body)
    if (transition === null) {
      clearResumePair()
      bootstrapPhase.value = 'error'
      return false
    }
    return applyTransition(transition, evidence)
  }

  async function discoverContext(): Promise<void> {
    const payload = bindingPayload()
    if (payload === null) return
    contextState.value = 'loading'
    pending.value = true
    const result = await request('/code/contexts', 'POST', payload)
    pending.value = false
    if (result.kind !== 'success') {
      contextState.value = result.kind === 'denied' ? 'denied' : result.kind === 'offline' ? 'offline' : 'unavailable'
      return
    }
    const catalog = parseCatalog(result.body)
    if (catalog === null) {
      contextState.value = 'unavailable'
      return
    }
    contextCatalog.value = catalog
    contextState.value = catalog.length === 0 ? 'empty' : 'ready'
    const selectedKey = contextCandidate.value === null ? null : contextKey(contextCandidate.value.context)
    if (selectedKey !== null) contextCandidate.value = catalog.flatMap((entry) => entry.view === null ? [] : [entry.view]).find((entry) => contextKey(entry.context) === selectedKey) ?? null
    if (pinnedContext.value !== null && !catalog.some((entry) => entry.view !== null && sameView(entry.view.context, pinnedContext.value?.context ?? null))) {
      pinnedContext.value = null
      clearContextualResults()
      clearIndexIntent()
      clearContextSelection()
    }
  }

  function selectContext(context: CodeSafeContext): void {
    contextCandidate.value = context
    persistContextSelection(context)
  }

  function restoreContextSelection(): boolean {
    const stored = loadContextSelection()
    if (stored === null) return false
    const selected = contextCatalog.value.flatMap((entry) => entry.view === null ? [] : [entry.view]).find((entry) => contextKey(entry.context) === stored) ?? null
    if (selected === null) return false
    contextCandidate.value = selected
    return true
  }

  async function initialize(): Promise<void> {
    if (pending.value) return
    bootstrapPhase.value = 'binding'
    const documentNonce = requestId()
    if (documentNonce === null) {
      bootstrapPhase.value = 'error'
      return
    }
    const navType = navigationType()
    const openerBefore = window.opener !== null
    let openerAfter: boolean | null = openerBefore ? false : null
    if (openerBefore) {
      clearResumePair()
      try {
        window.opener = null
      } catch {
        // The readback below, not assignment success, controls the transition.
      }
      openerAfter = window.opener === null
    }
    const evidence: CodeBootstrapEvidence = { navigationType: navType, openerBefore, openerAfter, transition: 'initializing' }
    const pair = openerBefore ? null : loadResumePair()
    const resumingPinnedBinding = !openerBefore && navType === 'reload' && pair !== null
    if (!resumingPinnedBinding) clearIndexIntent()
    const established = openerBefore && openerAfter !== true
      ? await handshake(documentNonce, null, true, evidence)
      : resumingPinnedBinding
        ? await resume(documentNonce, pair, evidence)
        : navType === 'navigate'
          ? await handshake(documentNonce, pair, false, evidence)
          : await handshake(documentNonce, null, true, evidence)
    if (!established) return
    await discoverContext()
    if (resumingPinnedBinding && restoreContextSelection()) {
      const restored = contextCandidate.value
      if (restored !== null && await refreshStatus(true)) {
        pinnedContext.value = restored
        await reconcileIndexIntent()
      }
    }
  }

  async function pinContext(): Promise<void> {
    const selected = contextCandidate.value
    if (binding.value === null || selected === null || pending.value) return
    pending.value = true
    const result = await request(`/code/tabs/${encodeURIComponent(binding.value.tabBindingId)}/context`, 'PUT', {
      document_proof: binding.value.documentProof,
      context_ref: {
        source_id: selected.context.sourceId,
        checkout_id: selected.context.checkoutId,
        view_id: selected.context.viewId,
        analysis_profile_id: selected.context.profileId,
        generation: selected.context.generation,
      },
    })
    pending.value = false
    if (result.kind !== 'success' || result.status !== 204) {
      contextState.value = result.kind === 'denied' ? 'denied' : result.kind === 'offline' ? 'offline' : 'unavailable'
      return
    }
    clearIndexIntent()
    pinnedContext.value = selected
    persistContextSelection(selected)
    clearContextualResults()
    await refreshStatus()
  }

  async function refreshStatus(allowRestoredPin = false): Promise<boolean> {
    const payload = bindingPayload()
    if (payload === null || (!allowRestoredPin && pinnedContext.value === null)) return false
    pending.value = true
    const result = await request('/code/status', 'POST', payload)
    pending.value = false
    status.value = result.kind === 'success' ? parseStatus(result.body) : null
    return status.value !== null
  }

  async function requestSearch(searchRequest: CodeSearchRequest, continuation: string | null): Promise<void> {
    const pinned = pinnedContext.value
    const payload = bindingPayload({
      query: searchRequest.query,
      limit: searchRequest.limit,
      path_prefix: searchRequest.pathPrefix,
      languages: searchRequest.languages,
      ...(continuation === null ? {} : { continuation }),
    })
    if (payload === null || pinned === null) return
    pending.value = true
    searchState.value = presentation('loading', 'Waiting for the server to release the search result.')
    searchEnvelope.value = null
    graphEnvelope.value = null
    sourceEnvelope.value = null
    searchContinuationNotice.value = null
    const result = await request('/code/search', 'POST', payload)
    pending.value = false
    if (result.kind !== 'success') {
      if (continuation !== null && result.kind === 'denied') searchContinuationNotice.value = 'denied'
      if (continuation !== null && result.kind === 'error') searchContinuationNotice.value = 'unavailable'
      searchState.value = presentation(result.kind, 'No contextual search body was released.')
      return
    }
    const envelope = parseEnvelope(result.body)
    if (envelope === null || !sameView(pinned.context, envelope.context)) {
      searchState.value = presentation('error', 'The search response did not prove the pinned View, so no result was rendered.')
      return
    }
    activeSearchRequest.value = searchRequest
    searchEnvelope.value = envelope
    searchState.value = presentationFromEnvelope(envelope)
  }

  async function search(query: string): Promise<void> {
    const normalized = query.trim()
    if (normalized === '') return
    await requestSearch({ query: normalized, limit: 10, pathPrefix: '', languages: [] }, null)
  }

  async function continueSearch(): Promise<void> {
    const searchRequest = activeSearchRequest.value
    const continuation = searchEnvelope.value?.continuation ?? null
    if (searchRequest === null || continuation === null) return
    await requestSearch(searchRequest, continuation)
  }

  async function requestGraph(target: CodeEntityRef, options: CodeGraphOptions, continuation: string | null = null): Promise<void> {
    const payload = bindingPayload({
      action: 'neighbors',
      target: { entity_key: target.entityKey },
      direction: options.direction,
      relations: options.relations,
      max_depth: 2,
      max_nodes: 24,
      max_edges: 48,
      ...(continuation === null ? {} : { continuation }),
    })
    const pinned = pinnedContext.value
    const activeSearch = searchEnvelope.value
    if (payload === null || pinned === null || activeSearch === null || !sameView(pinned.context, activeSearch.context)) return
    pending.value = true
    graphState.value = presentation('loading', 'Waiting for the server to release graph evidence.')
    const result = await request('/code/graph', 'POST', payload)
    pending.value = false
    if (result.kind !== 'success') {
      graphEnvelope.value = null
      activeGraphRequest.value = null
      graphState.value = presentation(result.kind, 'No relationship facts were released.')
      return
    }
    const envelope = parseEnvelope(result.body)
    if (envelope === null || envelope.navigation === null || !sameView(pinned.context, envelope.context)) {
      graphEnvelope.value = null
      activeGraphRequest.value = null
      graphState.value = presentation('error', 'The graph response did not prove navigation inside the pinned View, so it was concealed.')
      return
    }
    activeGraphRequest.value = { target, options }
    graphEnvelope.value = envelope
    graphState.value = presentationFromEnvelope(envelope)
  }

  async function explore(item: CodeItem, options: CodeGraphOptions = { direction: 'both', relations: [] }, continuation: string | null = null): Promise<void> {
    await requestGraph(item.ref, options, continuation)
  }

  async function continueGraph(target: CodeEntityRef | null = null): Promise<void> {
    const request = activeGraphRequest.value
    const graph = graphEnvelope.value
    if (request === null || graph === null) return
    if (target !== null) {
      const pinned = pinnedContext.value
      if (
        pinned === null || target.sourceId !== pinned.context.sourceId || target.viewId !== pinned.context.viewId
        || graph.navigation === null || !graph.navigation.nodes.some((node) => entityRefKey(node.ref) === entityRefKey(target))
      ) return
      await requestGraph(target, request.options)
      return
    }
    if (graph.continuation === null) return
    await requestGraph(request.target, request.options, graph.continuation)
  }

  async function readSource(descriptor: CodeSourceDescriptor): Promise<void> {
    const payload = bindingPayload({
      entity_key: descriptor.entityKey,
      span: { byte_start: descriptor.span.byteStart, byte_end: descriptor.span.byteEnd, line_start: descriptor.span.lineStart, line_end: descriptor.span.lineEnd },
      content_digest: descriptor.contentDigest,
    })
    const pinned = pinnedContext.value
    if (payload === null || pinned === null) return
    pending.value = true
    sourceState.value = presentation('loading', 'Waiting for the server to release the exact source span.')
    const result = await request('/code/source', 'POST', payload)
    pending.value = false
    if (result.kind !== 'success') {
      sourceEnvelope.value = null
      sourceState.value = presentation(result.kind, 'No source body was released.')
      return
    }
    const envelope = parseEnvelope(result.body)
    if (envelope === null || !sameView(pinned.context, envelope.context)) {
      sourceEnvelope.value = null
      sourceState.value = presentation('error', 'The source response did not prove the pinned view, so no body was rendered.')
      return
    }
    sourceEnvelope.value = envelope
    sourceState.value = presentationFromEnvelope(envelope)
  }

  function closeBinding(): void {
    const current = binding.value
    if (current !== null) void request(`/code/tabs/${encodeURIComponent(current.tabBindingId)}`, 'DELETE', { document_proof: current.documentProof })
  }

  onMounted(() => {
    window.addEventListener('pagehide', closeBinding, { once: true })
  })

  onBeforeUnmount(() => {
    stopIndexIntentPolling()
    window.removeEventListener('pagehide', closeBinding)
  })

  return {
    bootstrapPhase,
    bootstrapEvidence,
    contextCatalog,
    contextState,
    contextCandidate,
    pinnedContext,
    status,
    searchEnvelope,
    graphEnvelope,
    sourceEnvelope,
    searchContinuationNotice,
    searchState,
    graphState,
    sourceState,
    pending,
    indexIntentState,
    indexIntentPending,
    initialize,
    discoverContext,
    selectContext,
    pinContext,
    refreshStatus,
    submitIndexIntent,
    retryIndexIntent,
    refreshIndexIntent,
    search,
    continueSearch,
    explore,
    readSource,
    continueGraph,
  }
}
