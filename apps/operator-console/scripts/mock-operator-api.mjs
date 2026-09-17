import { createHash } from 'node:crypto'
import { createServer } from 'node:http'

const port = Number(process.env.PORT || 37993)
const host = process.env.HOST || '127.0.0.1'

function json(res, status, body) {
  const payload = JSON.stringify(body)
  res.writeHead(status, {
    'content-type': 'application/json; charset=utf-8',
    'content-length': Buffer.byteLength(payload),
    'cache-control': 'no-store',
  })
  res.end(payload)
}

function noContent(res) {
  res.writeHead(204, { 'cache-control': 'no-store' })
  res.end()
}

function controlPlaneError(message, code, data) {
  return data === undefined ? { message, code } : { message, code, data }
}

function text(res, status, body) {
  res.writeHead(status, {
    'content-type': 'text/plain; charset=utf-8',
    'content-length': Buffer.byteLength(body),
    'cache-control': 'no-store',
  })
  res.end(body)
}

const config = {
  context: {
    observations: 100,
    max_tokens: 8000,
    session_count: 10,
    relevance_threshold: 0.42,
    obs_types: ['semantic', 'procedural', 'episodic'],
    obs_concepts: ['operator-console', 'settings'],
  },
  memory: {
    inject_unified: true,
    always_inject_limit: 20,
    project_inject_limit: 15,
  },
  storage: {
    vector_strategy: 'hub',
    database_max_conns: 10,
    log_buffer_size: 1024,
  },
  features: {
    telemetry_enabled: true,
    enforce_source_project: true,
  },
}

const desiredConfig = {
  memory: {
    inject_unified: config.memory.inject_unified,
  },
}

const flags = {
  flags: {
    ENGRAM_VNEXT_ENABLED: false,
    ENGRAM_LIFECYCLE_ENABLED: false,
    ENGRAM_VNEXT_F_ENABLED: true,
    ENGRAM_GRAPH_ENABLED: true,
    ENGRAM_ADAPTIVE_ENABLED: false,
    ENGRAM_CRYSTALLIZATION_ENABLED: false,
    ENGRAM_CODE_INTEL_ENABLED: true,
    ENGRAM_V7_PLUG_ENABLED: true,
    ENGRAM_V7_S1_STATE: false,
    ENGRAM_V7_S2_METAMEM: true,
    ENGRAM_V7_S3_AMBIENT: false,
    ENGRAM_V7_S4A_DIRECTIVES_CAPTURE: false,
    ENGRAM_V7_S4B_DIRECTIVES_SURFACING: false,
    ENGRAM_V7_S5_TELEMETRY: false,
    ENGRAM_V7_S6_OUTCOME: false,
    ENGRAM_ENFORCE_SOURCE_PROJECT: true,
    ENGRAM_INJECT_UNIFIED: true,
  },
  items: [
    { name: 'ENGRAM_VNEXT_ENABLED', enabled: false, source: 'env', category: 'vnext', restart_required_to_change: true },
    { name: 'ENGRAM_LIFECYCLE_ENABLED', enabled: false, source: 'env', category: 'vnext', restart_required_to_change: true },
    { name: 'ENGRAM_VNEXT_F_ENABLED', enabled: true, source: 'env', category: 'vnext', restart_required_to_change: true },
    { name: 'ENGRAM_GRAPH_ENABLED', enabled: true, source: 'env', category: 'vnext', restart_required_to_change: true },
    { name: 'ENGRAM_ADAPTIVE_ENABLED', enabled: false, source: 'env', category: 'vnext', restart_required_to_change: true },
    { name: 'ENGRAM_CRYSTALLIZATION_ENABLED', enabled: false, source: 'env', category: 'vnext', restart_required_to_change: true },
    { name: 'ENGRAM_CODE_INTEL_ENABLED', enabled: true, source: 'env', category: 'code-intel', restart_required_to_change: true },
    { name: 'ENGRAM_V7_PLUG_ENABLED', enabled: true, source: 'runtime', category: 'v7', restart_required_to_change: true },
    { name: 'ENGRAM_V7_S1_STATE', enabled: false, source: 'runtime', category: 'v7', restart_required_to_change: true },
    { name: 'ENGRAM_V7_S2_METAMEM', enabled: true, source: 'runtime', category: 'v7', restart_required_to_change: true },
    { name: 'ENGRAM_V7_S3_AMBIENT', enabled: false, source: 'runtime', category: 'v7', restart_required_to_change: true },
    { name: 'ENGRAM_V7_S4A_DIRECTIVES_CAPTURE', enabled: false, source: 'runtime', category: 'v7', restart_required_to_change: true },
    { name: 'ENGRAM_V7_S4B_DIRECTIVES_SURFACING', enabled: false, source: 'runtime', category: 'v7', restart_required_to_change: true },
    { name: 'ENGRAM_V7_S5_TELEMETRY', enabled: false, source: 'runtime', category: 'v7', restart_required_to_change: true },
    { name: 'ENGRAM_V7_S6_OUTCOME', enabled: false, source: 'runtime', category: 'v7', restart_required_to_change: true },
    { name: 'ENGRAM_ENFORCE_SOURCE_PROJECT', enabled: true, source: 'config', category: 'operations', restart_required_to_change: false },
    { name: 'ENGRAM_INJECT_UNIFIED', enabled: true, source: 'config', category: 'memory', restart_required_to_change: true },
  ],
  summary: { total: 17, enabled: 7, disabled: 10 },
  read_only: true,
  apply: {
    supported: true,
    endpoint: 'PATCH /api/config',
    fields: ['features.enforce_source_project', 'memory.inject_unified'],
    reason: 'only allowlisted config-backed fields are writable; env-controlled flags remain read-only',
  },
}

const codeWorkspaces = [
  {
    repository: 'Engram',
    workingCopy: 'main · workstation',
    snapshot: { label: 'Current indexed snapshot', revision: '1a9dad0', publishedAt: '2026-09-17T09:00:00Z' },
    selectionRef: 'mock-workspace-main',
    context: { sourceId: 'mock-source-main', checkoutId: 'mock-checkout-main', viewId: 'mock-view-main', profileId: 'mock-profile-main', generation: 7 },
  },
  {
    repository: 'Engram',
    workingCopy: 'feature/operator-workspace · operator desk',
    snapshot: { label: 'Workspace candidate snapshot', revision: '1a9dad0', publishedAt: '2026-09-17T09:03:00Z' },
    selectionRef: 'mock-workspace-candidate',
    context: { sourceId: 'mock-source-candidate', checkoutId: 'mock-checkout-candidate', viewId: 'mock-view-candidate', profileId: 'mock-profile-candidate', generation: 8 },
  },
  {
    repository: 'Engram',
    workingCopy: 'recovery · offline owner',
    snapshot: null,
    selectionRef: null,
    indexIntentSelectionRef: 'mock-first-index',
    context: null,
  },
]

const codeTabs = new Map()
let codeTabSequence = 41

function codeCatalogResponse() {
  return {
    contexts: codeWorkspaces.map((workspace) => ({
      repository: workspace.repository,
      working_copy: workspace.workingCopy,
      ...(workspace.snapshot === null ? {} : {
        indexed_snapshot: {
          label: workspace.snapshot.label,
          revision: workspace.snapshot.revision,
          published_at: workspace.snapshot.publishedAt,
        },
        selection_ref: workspace.selectionRef,
      }),
      index_intent_available: workspace.snapshot === null,
      ...(workspace.snapshot === null ? { index_intent_selection_ref: workspace.indexIntentSelectionRef } : {}),
    })),
  }
}

function codeResponseContext(workspace) {
  return {
    source_id: workspace.context.sourceId,
    checkout_id: workspace.context.checkoutId,
    view_id: workspace.context.viewId,
    profile_id: workspace.context.profileId,
    generation: workspace.context.generation,
  }
}

function codeItem(workspace, entityKey, path, excerpt) {
  return {
    ref: { source_id: workspace.context.sourceId, view_id: workspace.context.viewId, entity_key: entityKey },
    path,
    span: { byte_start: 0, byte_end: excerpt.length, line_start: 1, line_end: 1 },
    content_digest: `sha256:${entityKey.toLowerCase().replaceAll('.', '-')}`,
    kind: 'function',
    language: 'Go',
    excerpt,
    match_sources: ['lexical'],
    score: 0.91,
  }
}

function codeEnvelope(workspace, items, graph = null, navigation = undefined) {
  return {
    schema: 'engram.code-query/1',
    status: 'ok',
    contexts: [codeResponseContext(workspace)],
    items,
    ...(graph === null ? {} : { graph }),
    ...(navigation === undefined ? {} : { navigation }),
    warnings: [],
    retrieval: { mode: 'lexical' },
    freshness: { state: 'observed_current' },
    coverage: { supported_languages: ['Go'] },
    truncated: false,
  }
}

function selectedCodeWorkspace(bindingID) {
  const selectionRef = codeTabs.get(bindingID)?.selectionRef
  return codeWorkspaces.find((workspace) => workspace.selectionRef === selectionRef) ?? null
}

async function handleCodeRequest(req, res, path) {
  const body = req.method === 'GET' ? {} : await readRequestJson(req)
  if (req.method === 'POST' && path === '/api/code/tabs/handshake') {
    const tabBindingId = `60000000-0000-4000-8000-${String(codeTabSequence).padStart(12, '0')}`
    codeTabSequence += 1
    codeTabs.set(tabBindingId, { documentProof: `mock-proof-${codeTabSequence}`, selectionRef: null })
    json(res, 200, { state: 'TAB_BINDING_READY', tab_binding_id: tabBindingId, document_proof: `mock-proof-${codeTabSequence}`, resume_nonce: `mock-resume-${codeTabSequence}`, reload_token: `mock-reload-${codeTabSequence}` })
    return true
  }
  if (req.method === 'POST' && path === '/api/code/tabs/resume') {
    const tab = codeTabs.get(body.tab_binding_id)
    if (!tab) {
      json(res, 409, { error: 'binding unavailable' })
      return true
    }
    json(res, 200, { state: 'TAB_BINDING_READY', tab_binding_id: body.tab_binding_id, document_proof: tab.documentProof, resume_nonce: body.resume_nonce, reload_token: `mock-reload-${body.tab_binding_id}` })
    return true
  }
  if (req.method === 'POST' && path === '/api/code/contexts') {
    json(res, 200, codeCatalogResponse())
    return true
  }
  const pinMatch = path.match(/^\/api\/code\/tabs\/([^/]+)\/context$/)
  if (req.method === 'PUT' && pinMatch) {
    const workspace = codeWorkspaces.find((candidate) => candidate.selectionRef === body.selection_ref)
    const tab = codeTabs.get(pinMatch[1])
    if (!tab || workspace === undefined || workspace.snapshot === null) {
      json(res, 403, { error: 'selection denied' })
      return true
    }
    tab.selectionRef = workspace.selectionRef
    noContent(res)
    return true
  }
  const leaseMatch = path.match(/^\/api\/code\/tabs\/([^/]+)\/lease$/)
  if (req.method === 'PUT' && leaseMatch && codeTabs.has(leaseMatch[1])) {
    noContent(res)
    return true
  }
  const closeMatch = path.match(/^\/api\/code\/tabs\/([^/]+)$/)
  if (req.method === 'DELETE' && closeMatch) {
    codeTabs.delete(closeMatch[1])
    noContent(res)
    return true
  }

  const headerBinding = req.headers['x-engram-tab-binding-id']
  const tabBindingId = typeof body.tab_binding_id === 'string' ? body.tab_binding_id : typeof headerBinding === 'string' ? headerBinding : ''
  const workspace = selectedCodeWorkspace(tabBindingId)
  if (workspace === null) {
    json(res, 403, { error: 'no selected workspace' })
    return true
  }
  const item = codeItem(workspace, 'workspace.Run', 'internal/workspace/run.go', 'func Run(ctx context.Context) error { return nil }')
  const neighbor = codeItem(workspace, 'workspace.Validate', 'internal/workspace/validate.go', 'func Validate(ctx context.Context) error { return nil }')
  if (req.method === 'POST' && path === '/api/code/status') {
    json(res, 200, { total_chunks: 64, embedded_chunks: 64, embedding: { Coverage: 'complete' }, freshness: { state: 'observed_current' } })
    return true
  }
  if (req.method === 'POST' && (path === '/api/code/structure' || path === '/api/code/search')) {
    json(res, 200, codeEnvelope(workspace, [item, neighbor]))
    return true
  }
  if (req.method === 'POST' && path === '/api/code/graph') {
    const graph = {
      nodes: [item.ref, neighbor.ref],
      edges: [{ from: item.ref, to: neighbor.ref, relation: 'calls', evidence_kind: 'reference_site', explanation: 'Derived from the selected snapshot.' }],
      stop_reason: 'complete',
    }
    const navigationRef = (source) => ({
      entity: source.ref,
      context_ref: {
        source_id: workspace.context.sourceId,
        checkout_id: workspace.context.checkoutId,
        view_id: workspace.context.viewId,
        analysis_profile_id: workspace.context.profileId,
        generation: workspace.context.generation,
      },
      source_state: 'available',
      source_read: { entity_key: source.ref.entity_key, span: source.span, content_digest: source.content_digest },
    })
    json(res, 200, codeEnvelope(workspace, [], graph, {
      nodes: [navigationRef(item), navigationRef(neighbor)],
      edges: [{ from: navigationRef(item), to: navigationRef(neighbor), relation: 'calls', evidence_kind: 'reference_site' }],
    }))
    return true
  }
  if (req.method === 'POST' && path === '/api/code/source') {
    json(res, 200, codeEnvelope(workspace, [body.entity_key === neighbor.ref.entity_key ? neighbor : item]))
    return true
  }
  if (req.method === 'POST' && path === '/api/code/index-intents') {
    json(res, 202, { intent_ref: 'mock-index-intent', state: 'queued', attempt: 1, retryable: false, created_at: '2026-09-17T09:05:00Z', updated_at: '2026-09-17T09:05:00Z' })
    return true
  }
  if (req.method === 'GET' && path === '/api/code/index-intents/mock-index-intent') {
    json(res, 200, { intent_ref: 'mock-index-intent', state: 'queued', attempt: 1, retryable: false, created_at: '2026-09-17T09:05:00Z', updated_at: '2026-09-17T09:05:00Z' })
    return true
  }
  return false
}

const migrations = {
  engine: 'gormigrate',
  table: 'migrations',
  current_version: '151_behavioral_rules_enabled',
  applied_count: 3,
  applied_ids: ['001_init_schema', '150_config_patch_receipts', '151_behavioral_rules_enabled'],
  dirty_supported: false,
  applied_at_supported: false,
}

const memoryRows = [
  {
    id: 101,
    project: 'operator-console',
    content: 'operator console = data plane: manage memory PRODUCT, config one Settings tab',
    tags: ['product', 'operator-console'],
    tier: 'semantic',
    epistemic_type: 'fact',
    confidence: 0.92,
    citation_count: 12,
    injection_count: 15,
    owner_principal: 'agent/alice',
    owner_principal_kind: 'agent',
    agent_visibility: 'shared',
    domain: 'operator-console',
    updated_at: '2026-06-22T10:00:00Z',
    status: 'active',
  },
  {
    id: 102,
    project: 'operator-console',
    content: 'stale recall hits should be suppressed when they no longer help the current roadmap',
    tags: ['moderation', 'noise'],
    tier: 'episodic',
    epistemic_type: 'observation',
    confidence: 0.61,
    citation_count: 0,
    injection_count: 28,
    owner_principal: 'agent/alice',
    owner_principal_kind: 'agent',
    agent_visibility: 'shared',
    domain: 'memory-lab',
    updated_at: '2026-05-19T10:00:00Z',
    status: 'active',
  },
]

const deepLinkMemoryRows = [
  {
    id: 501,
    project: 'operator-console',
    content: 'older memory omitted by the 500-row project list',
    tags: ['deep-link'],
    tier: 'semantic',
    epistemic_type: 'fact',
    confidence: 0.9,
    citation_count: 1,
    injection_count: 1,
    updated_at: '2026-01-01T10:00:00Z',
    status: 'active',
  },
]

const searchGuidanceRows = [
  {
    id: 303,
    project: 'operator-console',
    type: 'behavioral_rule',
    memory_type: 'guidance',
    title: 'behavioral rule guidance: preserve explicit memory evidence',
    narrative: 'Guidance results are not memory records and cannot open the memory page.',
    content: 'behavioral rule guidance: preserve explicit memory evidence',
    similarity: 0.88,
    created_at: '2026-06-23T10:00:00Z',
  },
]

const suppressedMemoryIds = new Set()
const memoryStates = new Map(memoryRows.map((row) => [String(row.id), { status: row.status, version: 1 }]))
let memorySelectionVersion = 0
let memorySelection = { kind: 'none', selection_version: memorySelectionVersion, targets: [] }

let projectIds = ['operator-console', 'project-alpha']

let sessionRows = [
  {
    id: 501,
    claude_session_id: 'sess-operator-1',
    sdk_session_id: { String: 'sdk-operator-1', Valid: true },
    project: 'operator-console',
    status: 'active',
    started_at: '2026-06-23T08:00:00Z',
    completed_at: { String: '', Valid: false },
    prompt_counter: 7,
    injection_strategy: { String: 'balanced', Valid: true },
    outcome: { String: 'running', Valid: true },
    outcome_reason: { String: '', Valid: false },
    worker_port: { Int64: 37777, Valid: true },
    user_prompt: { String: 'Wire the operator console project surface.', Valid: true },
  },
  {
    id: 502,
    claude_session_id: 'sess-alpha-1',
    sdk_session_id: { String: 'sdk-alpha-1', Valid: true },
    project: 'project-alpha',
    status: 'completed',
    started_at: '2026-06-22T08:00:00Z',
    completed_at: { String: '2026-06-22T09:00:00Z', Valid: true },
    prompt_counter: 3,
    injection_strategy: { String: 'quiet', Valid: true },
    outcome: { String: 'done', Valid: true },
    outcome_reason: { String: 'fixture complete', Valid: true },
    worker_port: { Int64: 37778, Valid: true },
    user_prompt: { String: 'Fixture project session.', Valid: true },
  },
]

const candidateFixtureRows = [
  {
    id: 301,
    status: 'pending',
    proposed_content: 'candidate queue REST bridge should mirror MCP promotion semantics',
    proposed_promotion_target: 'semantic',
    proposed_tier: 'semantic',
    proposed_epistemic_type: 'decision',
    source_session_id: 'sess-301',
    confidence: 0.88,
    recurrence_count: 4,
    fingerprint: 'fp301',
    created_at: '2026-06-23T08:00:00Z',
    updated_at: '2026-06-23T08:00:00Z',
    review_after: '2026-06-24T08:00:00Z',
    evidence_handles: ['session:sess-301'],
    affected_projects: ['operator-console'],
    privacy_scope: 'project',
  },
  {
    id: 302,
    status: 'pending',
    proposed_content: 'low-confidence recall noise needs an operator decision before promotion',
    proposed_promotion_target: 'episodic',
    proposed_tier: 'episodic',
    proposed_epistemic_type: 'observation',
    source_session_id: 'sess-302',
    confidence: 0.62,
    recurrence_count: 1,
    fingerprint: 'fp302',
    created_at: '2026-06-23T09:00:00Z',
    updated_at: '2026-06-23T09:00:00Z',
    review_after: '2026-06-24T09:00:00Z',
    evidence_handles: ['session:sess-302'],
    affected_projects: ['operator-console'],
    privacy_scope: 'project',
  },
]

let candidateRows = candidateFixtureRows.map((row) => ({
  ...row,
  evidence_handles: [...row.evidence_handles],
  affected_projects: [...row.affected_projects],
}))
let candidateSelectionVersion = 0
let candidateSelection = { domain: 'queue', kind: 'none', selection_version: candidateSelectionVersion, targets: [] }

let ruleRows = [
  {
    id: 401,
    project: '',
    content: 'operator console rules are live data; disabled rows remain visible but do not inject',
    priority: 40,
    version: 1,
    enabled: true,
    edited_by: 'mock-operator-api',
    created_at: '2026-06-22T10:00:00Z',
    updated_at: '2026-06-22T10:00:00Z',
  },
  {
    id: 402,
    project: 'operator-console',
    content: 'temporarily disabled guidance stays recoverable from the control plane',
    priority: 30,
    version: 3,
    enabled: false,
    edited_by: 'mock-operator-api',
    created_at: '2026-06-21T10:00:00Z',
    updated_at: '2026-06-22T11:00:00Z',
  },
]

let ruleSelectionVersion = 0
let ruleSelection = { domain: 'rules', kind: 'none', selection_version: ruleSelectionVersion }

let domainRows = [
  {
    domain: 'memory-lab',
    owner_principal: 'agent/alice',
    owner_principal_kind: 'agent',
    mode: 'warn',
    created_at: '2026-06-23T08:30:00Z',
    updated_at: '2026-06-23T08:30:00Z',
  },
  {
    domain: 'operator-console',
    owner_principal: 'service/dashboard',
    owner_principal_kind: 'service',
    mode: 'reject',
    created_at: '2026-06-23T08:45:00Z',
    updated_at: '2026-06-23T08:45:00Z',
  },
]

const vaultCredentials = [
  {
    id: 1,
    name: 'shared-token',
    project: 'alpha',
    scope: 'project',
    created_at: '2026-06-21T10:00:00Z',
    value: 'alpha-secret-value',
  },
  {
    id: 2,
    name: 'shared-token',
    project: 'beta',
    scope: 'project',
    created_at: '2026-06-22T10:00:00Z',
    value: 'beta-secret-value',
  },
  {
    id: 3,
    name: 'orphaned-token',
    project: 'legacy',
    scope: 'project',
    created_at: '2026-06-20T10:00:00Z',
    value: 'orphaned-secret-value',
    orphaned: true,
  },
]
let issueRows = [
  {
    id: 701,
    title: 'Issue mutations require current readback',
    body: 'The operator must see the postcondition, not only an accepted receipt.',
    status: 'open',
    priority: 'high',
    type: 'bug',
    source_project: 'operator-console',
    target_project: 'engram',
    source_project_display_name: 'Operator Console',
    target_project_display_name: 'Engram',
    labels: ['operator-created'],
    created_at: '2026-09-10T10:00:00Z',
    updated_at: '2026-09-10T10:00:00Z',
  },
]
let issueComments = []
let bookJobs = []
let documentRows = []

function nextBookID() {
  return Math.max(0, ...bookJobs.map((row) => row.id)) + 1
}

function nextDocumentID() {
  return Math.max(0, ...documentRows.map((row) => row.id)) + 1
}

function documentHash(content) {
  return createHash('sha256').update(content).digest('hex')
}

function documentListResponse(url) {
  const project = (url.searchParams.get('project') || '').trim()
  const docType = (url.searchParams.get('doc_type') || '').trim()
  const pathPrefix = (url.searchParams.get('path_prefix') || '').trim()
  const requestedLimit = Number(url.searchParams.get('limit') || 50)
  const limit = Number.isFinite(requestedLimit) && requestedLimit > 0 ? Math.min(requestedLimit, 200) : 50
  const latestByPath = new Map()
  for (const row of documentRows) {
    if (row.project !== project || (docType && row.doc_type !== docType) || (pathPrefix && !row.path.startsWith(pathPrefix))) continue
    if (!latestByPath.has(row.path) || latestByPath.get(row.path).version < row.version) latestByPath.set(row.path, row)
  }
  const documents = [...latestByPath.values()]
    .sort((left, right) => left.path.localeCompare(right.path))
    .slice(0, limit)
    .map(({ content, content_hash, metadata, ...row }) => ({ ...row }))
  return { documents, project, ...(docType ? { doc_type: docType } : {}), ...(pathPrefix ? { path_prefix: pathPrefix } : {}), count: documents.length, limit }
}

function documentHistoryResponse(url) {
  const path = (url.searchParams.get('path') || '').trim()
  const project = (url.searchParams.get('project') || '').trim()
  const requestedLimit = Number(url.searchParams.get('limit') || 0)
  const limit = Number.isFinite(requestedLimit) && requestedLimit > 0 ? Math.min(requestedLimit, 200) : 0
  const versions = documentRows
    .filter((row) => row.path === path && row.project === project)
    .sort((left, right) => right.version - left.version)
    .slice(0, limit || undefined)
    .map(({ path: _path, project: _project, content, doc_type, metadata, ...row }) => ({ ...row }))
  return { path, project, versions, count: versions.length }
}

function currentMemory(row) {
  const state = memoryStates.get(String(row.id)) || { status: row.status, version: 1 }
  return { ...row, tags: [...row.tags], status: state.status, version: state.version }
}

function memoryResponseForProject(project) {
  return memoryRows
    .filter((row) => row.project === project)
    .filter((row) => !suppressedMemoryIds.has(String(row.id)))
    .map(currentMemory)
}

function principalMemoryResponse(url) {
  const principal = (url.searchParams.get('principal') || '').trim()
  const principalKind = (url.searchParams.get('principal_kind') || 'agent').trim()
  const project = (url.searchParams.get('project') || '').trim()
  const domain = (url.searchParams.get('domain') || '').trim()
  const visibility = (url.searchParams.get('visibility') || 'all').trim()
  const includePrivate = url.searchParams.get('include_private') === 'true'
  const requestedLimit = Number(url.searchParams.get('limit') || 10)
  const limit = Number.isFinite(requestedLimit) && requestedLimit > 0 ? Math.min(10, requestedLimit) : 10

  if (!principal) {
    return {
      principal,
      principal_kind: principalKind,
      project,
      domain,
      items: [],
      hidden_count: 0,
      audit: { durable: false, action: 'principal_memory_query' },
      audit_status: 'not_required',
    }
  }

  const rows = memoryRows
    .filter((row) => !suppressedMemoryIds.has(String(row.id)))
    .filter((row) => row.owner_principal === principal)
    .filter((row) => !project || row.project === project)
    .filter((row) => !domain || row.domain === domain)
    .filter((row) => visibility === 'all' || row.agent_visibility === visibility)

  const visible = rows
    .filter((row) => includePrivate || row.agent_visibility !== 'private')
    .slice(0, limit)
    .map((row) => ({
      id: row.id,
      project: row.project,
      content: row.content,
      tags: [...row.tags],
      owner_principal: row.owner_principal,
      owner_principal_kind: row.owner_principal_kind,
      agent_visibility: row.agent_visibility,
      domain: row.domain,
      confidence: row.confidence,
      created_at: row.updated_at,
    }))

  return {
    principal,
    principal_kind: principalKind,
    project,
    domain,
    items: visible,
    hidden_count: rows.length - visible.length,
    audit: { durable: false, action: 'principal_memory_query' },
    audit_status: 'not_required',
  }
}

function candidateResponse(project, status, limit) {
  const normalizedProject = project === 'all' ? '' : project
  const rows = candidateRows
    .filter((row) => row.status === status)
    .filter((row) => !normalizedProject || row.affected_projects.includes(normalizedProject))
    .slice(0, limit)
    .map((row) => ({
      ...row,
      evidence_handles: [...row.evidence_handles],
      affected_projects: [...row.affected_projects],
    }))
  return {
    candidates: rows,
    count: rows.length,
    project: project || 'all',
    status,
    limit,
  }
}
function saveMemorySelection(body) {
  const requested = body?.selection
  if (!requested || requested.kind !== 'explicit' || !Array.isArray(requested.targets) || !requested.targets.length) return null
  const targets = requested.targets.map((target) => {
    const id = String(target?.id || '')
    const row = memoryRows.find((item) => String(item.id) === id)
    const state = memoryStates.get(id)
    if (!row || !state || (target.expected_version !== undefined && target.expected_version !== state.version)) return null
    return { id, expected_version: state.version }
  })
  if (targets.some((target) => target === null)) return null
  memorySelection = { kind: 'explicit', selection_version: ++memorySelectionVersion, targets }
  return { selection: { kind: memorySelection.kind, selection_version: memorySelection.selection_version } }
}

function applyMemorySelectionOperation(body) {
  if (!body || !['suppress', 'unsuppress', 'archive'].includes(body.action) || body.selection?.kind !== memorySelection.kind || body.selection?.selection_version !== memorySelection.selection_version || memorySelection.kind !== 'explicit') return null
  const current = []
  for (const target of memorySelection.targets) {
    const state = memoryStates.get(target.id)
    if (!state) return null
    const status = body.action === 'suppress' ? 'flagged' : body.action === 'archive' ? 'archived' : 'active'
    const next = { status, version: state.version + 1 }
    memoryStates.set(target.id, next)
    if (body.action === 'suppress') suppressedMemoryIds.add(target.id)
    else suppressedMemoryIds.delete(target.id)
    current.push({ id: target.id, ...next })
  }
  return {
    request_id: body.request_id,
    operation_state: 'completed',
    item_results: current.map((state) => ({ target_id: state.id, outcome: 'committed', observed_version: state.version })),
    readback: { authoritative: true, kind: 'current', current_state: current },
  }
}

function saveCandidateSelection(body) {
  const requested = body?.selection
  if (!body || body.domain !== 'queue' || !requested || requested.kind !== 'explicit' || !Array.isArray(requested.targets) || !requested.targets.length) return null
  const targets = requested.targets.map((target) => {
    const id = String(target?.id || '')
    return candidateRows.some((row) => String(row.id) === id && row.status === 'pending') ? { id } : null
  })
  if (targets.some((target) => target === null)) return null
  candidateSelection = { domain: 'queue', kind: 'explicit', selection_version: ++candidateSelectionVersion, targets }
  return { selection: { ...candidateSelection, targets: candidateSelection.targets.map((target) => ({ ...target })) } }
}

function applyCandidateSelectionOperation(body) {
  if (!body || !['promote', 'reject', 'supersede'].includes(body.action) || body.selection?.kind !== candidateSelection.kind || body.selection?.selection_version !== candidateSelection.selection_version || candidateSelection.kind !== 'explicit') return null
  const status = body.action === 'promote' ? 'promoted' : body.action === 'reject' ? 'rejected' : 'superseded'
  const targetIDs = new Set(candidateSelection.targets.map((target) => target.id))
  if ([...targetIDs].some((id) => !candidateRows.some((row) => String(row.id) === id && row.status === 'pending'))) return null
  candidateRows = candidateRows.map((row) => !targetIDs.has(String(row.id))
    ? row
    : { ...row, status, updated_at: new Date().toISOString() })
  const current = [...targetIDs].map((candidate_id) => ({ candidate_id, candidate_status: status }))
  return {
    request_id: body.request_id,
    operation_state: 'completed',
    item_results: current.map((state) => ({ target_id: state.candidate_id, outcome: 'committed' })),
    readback: { authoritative: true, kind: 'current', current_state: current.length === 1 ? current[0] : current },
  }
}


function cloneRule(row) {
  return {
    ...row,
    project: row.project || undefined,
  }
}

function ruleResponse(url) {
  const limitRaw = Number(url.searchParams.get('limit') || 100)
  const limit = Number.isFinite(limitRaw) && limitRaw > 0 ? limitRaw : 100
  const all = url.searchParams.get('all') === 'true'
  const project = (url.searchParams.get('project') || '').trim()
  const rows = ruleRows
    .filter((row) => {
      if (all) return true
      if (!project) return !row.project
      return !row.project || row.project === project
    })
    .slice()
    .sort((left, right) => {
      if (left.priority !== right.priority) return right.priority - left.priority
      return right.created_at.localeCompare(left.created_at)
    })
    .slice(0, limit)
    .map(cloneRule)

  return rows
}

function ruleSelectionSnapshot(selection) {
  return { selection: { ...selection, ...(selection.targets ? { targets: selection.targets.map((target) => ({ ...target })) } : {}) } }
}

function saveRuleSelection(body) {
  if (!body || body.domain !== 'rules' || !body.selection || typeof body.selection !== 'object') return null
  const selection = body.selection
  if (selection.kind === 'none') {
    ruleSelection = { domain: 'rules', kind: 'none', selection_version: ++ruleSelectionVersion }
    return ruleSelectionSnapshot(ruleSelection)
  }
  if (selection.kind !== 'explicit' || !Array.isArray(selection.targets) || !selection.targets.length) return null

  const targets = selection.targets.map((target) => {
    const id = Number(target?.id)
    const rule = ruleRows.find((row) => row.id === id)
    if (!Number.isInteger(id) || !rule) return null
    return { id: String(id), expected_version: Number.isInteger(target.expected_version) ? target.expected_version : rule.version }
  })
  if (targets.some((target) => target === null)) return null
  ruleSelection = { domain: 'rules', kind: 'explicit', selection_version: ++ruleSelectionVersion, targets }
  return ruleSelectionSnapshot(ruleSelection)
}

function ruleSelectionPage(body) {
  if (!body || body.domain !== 'rules' || !body.filter || typeof body.filter.scope !== 'string') return null
  const scope = body.filter.scope
  const rows = ruleRows.filter((row) => scope === 'all' || (scope === 'global' ? !row.project : !row.project || row.project === scope))
  return {
    filter_fingerprint: `sha256:${createHash('sha256').update(`mock-rules-filter:${scope}`).digest('hex')}`,
    cursor: `mock-rules-page-${scope}`,
    next_cursor: '',
    targets: rows.map((row) => ({ id: String(row.id), expected_version: row.version })),
    total: rows.length,
  }
}

function applyRuleSelectionOperation(body) {
  if (!body || !['enable', 'disable'].includes(body.action) || body.selection?.selection_version !== ruleSelection.selection_version || ruleSelection.kind !== 'explicit') return null
  const enabled = body.action === 'enable'
  const now = new Date().toISOString()
  const targets = ruleSelection.targets.map((target) => Number(target.id))
  if (targets.some((id) => !ruleRows.some((row) => row.id === id))) return null

  ruleRows = ruleRows.map((row) => !targets.includes(row.id)
    ? row
    : { ...row, enabled, version: row.version + 1, updated_at: now })
  const updated = ruleRows.filter((row) => targets.includes(row.id))
  const item_results = updated.map((row) => ({
    target_id: row.id,
    outcome: 'committed',
    observed_version: row.version,
    readback: { authoritative: true, kind: 'current', current_state: cloneRule(row), current_version: row.version },
  }))
  const current_state = updated.length === 1 ? cloneRule(updated[0]) : updated.map(cloneRule)
  return {
    request_id: body.request_id,
    operation_state: 'completed',
    item_results,
    readback: { authoritative: true, kind: 'current', current_state, ...(updated.length === 1 ? { current_version: updated[0].version } : {}) },
  }
}

function nextRuleId() {
  return Math.max(400, ...ruleRows.map((row) => row.id)) + 1
}

function domainRegistryResponse() {
  return {
    domains: domainRows
      .slice()
      .sort((a, b) => a.domain.localeCompare(b.domain))
      .map((row) => ({ ...row })),
  }
}

function validDomainPayload(body) {
  return (
    body &&
    typeof body.owner_principal === 'string' &&
    body.owner_principal.trim() &&
    ['human', 'agent', 'service'].includes(body.owner_principal_kind) &&
    ['off', 'warn', 'reject'].includes(body.mode)
  )
}

function syncConfigFlags() {
  flags.flags.ENGRAM_ENFORCE_SOURCE_PROJECT = Boolean(config.features.enforce_source_project)
  const sourceProject = flags.items.find((item) => item.name === 'ENGRAM_ENFORCE_SOURCE_PROJECT')
  if (sourceProject) sourceProject.enabled = flags.flags.ENGRAM_ENFORCE_SOURCE_PROJECT

  flags.flags.ENGRAM_INJECT_UNIFIED = Boolean(config.memory.inject_unified)
  const injectUnified = flags.items.find((item) => item.name === 'ENGRAM_INJECT_UNIFIED')
  if (injectUnified) injectUnified.enabled = flags.flags.ENGRAM_INJECT_UNIFIED
}

function pendingRestartItems() {
  const pending = []
  if (config.memory.inject_unified !== desiredConfig.memory.inject_unified) {
    pending.push({
      field: 'memory.inject_unified',
      effective: config.memory.inject_unified,
      desired: desiredConfig.memory.inject_unified,
      reason: 'requires_restart',
    })
  }
  return pending
}

function configResponse() {
  const pending_restart = pendingRestartItems()
  return {
    context: {
      ...config.context,
      obs_types: [...config.context.obs_types],
      obs_concepts: [...config.context.obs_concepts],
    },
    memory: { ...config.memory },
    storage: { ...config.storage },
    features: { ...config.features },
    lifecycle: {
      restart_required: pending_restart.length > 0,
      pending_restart,
      apply: {
        supported: false,
        reason: 'generic restart/apply endpoint is not available',
      },
    },
  }
}

function readRequestJson(req) {
  return new Promise((resolve, reject) => {
    let body = ''
    req.setEncoding('utf8')
    req.on('data', (chunk) => { body += chunk })
    req.on('end', () => {
      try {
        resolve(body.trim() ? JSON.parse(body) : {})
      } catch (error) {
        reject(error)
      }
    })
    req.on('error', reject)
  })
}

const server = createServer(async (req, res) => {
  const url = new URL(req.url || '/', `http://${host}:${port}`)
  const path = url.pathname.replace(/\/+$/, '') || '/'
  const origin = req.headers.origin
  const allowedOrigin = `http://127.0.0.1:${process.env.OPERATOR_CONSOLE_SMOKE_PORT || '37992'}`
  if (origin === allowedOrigin) {
    res.setHeader('Access-Control-Allow-Origin', origin)
    res.setHeader('Access-Control-Allow-Credentials', 'true')
    res.setHeader('Access-Control-Allow-Headers', 'Content-Type, X-Engram-Request-ID')
    res.setHeader('Access-Control-Allow-Methods', 'GET, POST, PATCH, PUT, DELETE, OPTIONS')
  }

  if (!path.startsWith('/api')) {
    text(res, 404, 'not found')
    return
  }

  if (req.method === 'OPTIONS') {
    res.writeHead(204)
    res.end()
    return
  }

  if (path.startsWith('/api/code/')) {
    try {
      if (await handleCodeRequest(req, res, path)) return
    } catch (error) {
      json(res, 400, { error: error instanceof Error ? error.message : String(error) })
      return
    }
  }

  if (req.method === 'POST' && (path === '/api/restart' || path === '/api/update/restart')) {
    json(res, 202, { ok: true, state: 'accepted' })
    return
  }

  if (req.method === 'PATCH' && path === '/api/config') {
    try {
      const patch = await readRequestJson(req)
      const changed = []
      if (patch.features && Object.hasOwn(patch.features, 'enforce_source_project')) {
        config.features.enforce_source_project = Boolean(patch.features.enforce_source_project)
        changed.push('enforce_source_project')
      }
      if (patch.memory && Object.hasOwn(patch.memory, 'inject_unified')) {
        desiredConfig.memory.inject_unified = Boolean(patch.memory.inject_unified)
        changed.push('inject_unified (requires restart)')
      }
      syncConfigFlags()
      const responseConfig = configResponse()
      const pendingRestartFields = responseConfig.lifecycle.pending_restart.map((item) => item.field)
      json(res, 200, {
        success: true,
        applied: true,
        audit_logged: true,
        changed,
        restart_required: responseConfig.lifecycle.restart_required,
        restart_required_fields: pendingRestartFields,
        config: responseConfig,
      })
    } catch (error) {
      json(res, 400, { error: error instanceof Error ? error.message : String(error) })
    }
    return
  }

  if (req.method === 'POST' && path === '/api/memories/selection') {
    try {
      const saved = saveMemorySelection(await readRequestJson(req))
      if (!saved) {
        json(res, 400, { error: 'invalid memory selection' })
        return
      }
      json(res, 200, saved)
    } catch (error) {
      json(res, 400, { error: error instanceof Error ? error.message : String(error) })
    }
    return
  }

  if (req.method === 'POST' && path === '/api/memories/operations') {
    try {
      const result = applyMemorySelectionOperation(await readRequestJson(req))
      if (!result) {
        json(res, 400, { error: 'invalid memory operation' })
        return
      }
      json(res, 200, result)
    } catch (error) {
      json(res, 400, { error: error instanceof Error ? error.message : String(error) })
    }
    return
  }

  if (req.method === 'POST' && path === '/api/memories/suppress') {
    try {
      const body = await readRequestJson(req)
      const ids = Array.isArray(body.ids) ? [...new Set(body.ids.map((id) => Number(id)))] : []
      if (!ids.length || ids.some((id) => !Number.isInteger(id) || id <= 0)) {
        json(res, 400, { error: 'invalid memory ids' })
        return
      }
      const missing = ids.some((id) => !memoryRows.some((row) => row.id === id) || suppressedMemoryIds.has(String(id)))
      if (missing) {
        json(res, 404, { error: 'memory not found' })
        return
      }
      const reason = typeof body.reason === 'string' ? body.reason.trim() : ''
      ids.forEach((id) => suppressedMemoryIds.add(String(id)))
      json(res, 200, ids.map((id) => ({ status: 'ok', action: 'suppress', id, reason })))
    } catch (error) {
      json(res, 400, { error: error instanceof Error ? error.message : String(error) })
    }
    return
  }

  const suppressMatch = path.match(/^\/api\/memories\/([^/]+)\/suppress$/)
  if (req.method === 'POST' && suppressMatch) {
    const id = suppressMatch[1]
    const exists = memoryRows.some((row) => String(row.id) === id)
    if (!exists || suppressedMemoryIds.has(id)) {
      json(res, 404, { error: 'memory not found' })
      return
    }

    let reason = ''
    try {
      const body = await readRequestJson(req)
      reason = typeof body.reason === 'string' ? body.reason.trim() : ''
    } catch {
      reason = ''
    }
    suppressedMemoryIds.add(id)
    json(res, 200, { status: 'ok', action: 'suppress', id: Number(id), reason })
    return
  }

  const deleteMatch = path.match(/^\/api\/memories\/([^/]+)$/)
  if (req.method === 'DELETE' && deleteMatch) {
    const id = deleteMatch[1]
    const exists = memoryRows.some((row) => String(row.id) === id)
    if (!exists || suppressedMemoryIds.has(id)) {
      json(res, 404, { error: 'memory not found' })
      return
    }
    suppressedMemoryIds.add(id)
    json(res, 200, { status: 'ok' })
    return
  }

  const memoryGetMatch = path.match(/^\/api\/memories\/(-?\d+)$/)
  if (req.method === 'GET' && memoryGetMatch) {
    const id = Number(memoryGetMatch[1])
    if (!Number.isInteger(id) || id <= 0) {
      json(res, 400, { error: 'invalid memory id' })
      return
    }
    const row = [...memoryRows, ...deepLinkMemoryRows].find((item) => item.id === id)
    if (!row) {
      json(res, 404, { error: 'memory not found' })
      return
    }
    json(res, 200, currentMemory(row))
    return
  }

  const memoryAuditMatch = path.match(/^\/api\/memories\/([^/]+)\/audit$/)
  if (req.method === 'GET' && memoryAuditMatch) {
    const idRaw = memoryAuditMatch[1]
    const id = Number(idRaw)
    if (!Number.isInteger(id) || id <= 0) {
      json(res, 400, { error: 'invalid memory id' })
      return
    }

    const rawLimit = url.searchParams.get('limit') ?? ''
    if (rawLimit.trim() !== '') {
      const limit = Number(rawLimit)
      if (!Number.isInteger(limit) || limit <= 0 || limit > 200) {
        json(res, 400, { error: 'invalid limit' })
        return
      }
    }

    const idKey = String(id)
    const row = memoryRows.find((item) => item.id === id)
    if (!row || suppressedMemoryIds.has(idKey)) {
      json(res, 404, { error: 'memory not found' })
      return
    }
    json(res, 200, {
      memory_id: id,
      entries: [
        {
          id: 7000 + id,
          memory_id: id,
          action: 'memory.store',
          actor: 'mock-operator-api',
          reason: 'browser smoke fixture',
          before_state_present: false,
          after_state_present: true,
          created_at: row.updated_at,
        },
      ],
    })
    return
  }

  const candidateActionMatch = path.match(/^\/api\/memory\/candidates\/([^/]+)\/(promote|reject|supersede)$/)
  if (req.method === 'POST' && candidateActionMatch) {
    const id = Number(candidateActionMatch[1])
    const action = candidateActionMatch[2]
    const rowIndex = candidateRows.findIndex((item) => item.id === id)
    const row = candidateRows[rowIndex]
    if (!row) {
      json(res, 404, { error: 'candidate not found' })
      return
    }
    if (row.status !== 'pending') {
      json(res, 409, { error: 'candidate is not pending' })
      return
    }

    if (action === 'reject') {
      try {
        await readRequestJson(req)
      } catch {
        json(res, 400, { error: 'invalid JSON body' })
        return
      }
    }

    const nextStatus = action === 'promote' ? 'promoted' : action === 'reject' ? 'rejected' : 'superseded'
    const memoryId = action === 'promote' ? 9100 + id : undefined
    const updatedRow = {
      ...row,
      status: nextStatus,
      promoted_memory_id: memoryId ?? row.promoted_memory_id,
    }
    candidateRows = candidateRows.map((item, index) => index === rowIndex ? updatedRow : item)
    json(res, 200, {
      action,
      candidate_id: updatedRow.id,
      candidate_status: updatedRow.status,
      memory_id: memoryId,
      promoted_memory_id: updatedRow.promoted_memory_id,
    })
    return
  }

  const domainMatch = path.match(/^\/api\/memory-domains\/([^/]+)$/)
  if ((req.method === 'PUT' || req.method === 'DELETE') && domainMatch) {
    let domain = ''
    try {
      domain = decodeURIComponent(domainMatch[1]).trim()
    } catch {
      json(res, 400, controlPlaneError('invalid domain encoding', 400))
      return
    }
    if (!domain) {
      json(res, 400, controlPlaneError('domain must not be empty', 400))
      return
    }

    if (req.method === 'DELETE') {
      const index = domainRows.findIndex((row) => row.domain === domain)
      if (index < 0) {
        json(res, 404, controlPlaneError('domain owner not found', 404))
        return
      }
      domainRows.splice(index, 1)
      json(res, 200, { deleted: true, domain })
      return
    }

    try {
      const body = await readRequestJson(req)
      if (!validDomainPayload(body)) {
        json(res, 400, controlPlaneError('invalid domain owner payload', 400))
        return
      }
      const now = new Date().toISOString()
      const existing = domainRows.find((row) => row.domain === domain)
      const row = {
        domain,
        owner_principal: body.owner_principal.trim(),
        owner_principal_kind: body.owner_principal_kind,
        mode: body.mode,
        created_at: existing?.created_at || now,
        updated_at: now,
      }
      domainRows = existing ? domainRows.map((item) => item.domain === domain ? row : item) : [...domainRows, row]
      json(res, 200, row)
    } catch (error) {
      json(res, 400, controlPlaneError(error instanceof Error ? error.message : String(error), 400))
    }
    return
  }

  if (req.method === 'POST' && path === '/api/books') {
    try {
      const body = await readRequestJson(req)
      const sourceRef = typeof body.source_ref === 'string' ? body.source_ref.trim() : ''
      if (!sourceRef) {
        json(res, 400, { error: 'source_ref required' })
        return
      }
      const now = new Date().toISOString()
      const row = {
        id: nextBookID(),
        status: 'pending',
        source_ref: sourceRef,
        error: '',
        created_at: now,
        updated_at: now,
        documents_path_prefix: '',
        documents_link: '/documents',
      }
      bookJobs.push(row)
      json(res, 202, row)
    } catch (error) {
      json(res, 400, { error: error instanceof Error ? error.message : String(error) })
    }
    return
  }

  const bookStatusMatch = path.match(/^\/api\/books\/(\d+)\/status$/)
  if (req.method === 'GET' && bookStatusMatch) {
    const id = Number(bookStatusMatch[1])
    const index = bookJobs.findIndex((row) => row.id === id)
    if (index < 0) {
      json(res, 404, { error: 'book job not found' })
      return
    }
    const current = bookJobs[index]
    if (current.status === 'pending') {
      bookJobs[index] = {
        ...current,
        status: 'done',
        updated_at: new Date().toISOString(),
        documents_path_prefix: `books/jobs/${id}/`,
      }
    }
    json(res, 200, bookJobs[index])
    return
  }

  if (req.method === 'POST' && path === '/api/documents') {
    try {
      const body = await readRequestJson(req)
      const pathValue = typeof body.path === 'string' ? body.path.trim() : ''
      const project = typeof body.project === 'string' ? body.project.trim() : ''
      const content = typeof body.content === 'string' ? body.content : ''
      if (!pathValue || !project || !content.trim()) {
        json(res, 400, { error: !pathValue ? 'path is required' : !project ? 'project is required' : 'content is required' })
        return
      }
      const now = new Date().toISOString()
      const latest = documentRows
        .filter((row) => row.path === pathValue && row.project === project)
        .reduce((max, row) => Math.max(max, row.version), 0)
      const row = {
        id: nextDocumentID(),
        path: pathValue,
        project,
        content,
        content_hash: documentHash(content),
        doc_type: typeof body.doc_type === 'string' && body.doc_type.trim() ? body.doc_type.trim() : 'markdown',
        metadata: typeof body.metadata === 'string' && body.metadata.trim() ? body.metadata.trim() : '{}',
        author: typeof body.author === 'string' && body.author.trim() ? body.author.trim() : 'operator',
        version: latest + 1,
        created_at: now,
      }
      documentRows.push(row)
      json(res, 201, { id: row.id, path: row.path, project: row.project, message: 'document created' })
    } catch (error) {
      json(res, 400, { error: error instanceof Error ? error.message : String(error) })
    }
    return
  }

  if (req.method === 'DELETE' && path === '/api/vault/orphaned-credentials') {
    const deleted = vaultCredentials.filter((credential) => credential.orphaned).length
    for (let index = vaultCredentials.length - 1; index >= 0; index -= 1) {
      if (vaultCredentials[index].orphaned) vaultCredentials.splice(index, 1)
    }
    json(res, 200, { status: 'ok', deleted })
    return
  }

  const vaultCredentialMatch = path.match(/^\/api\/vault\/credentials\/([^/]+)$/)
  if (vaultCredentialMatch) {
    const name = decodeURIComponent(vaultCredentialMatch[1])
    const project = url.searchParams.get('project') || ''
    const cred = vaultCredentials.find((item) => item.name === name && (item.project || '') === project)
    if (!cred) {
      json(res, 404, { error: 'credential not found' })
      return
    }
    if (req.method === 'GET') {
      json(res, 200, { name: cred.name, value: cred.value, scope: cred.scope })
      return
    }
    if (req.method === 'DELETE') {
      const index = vaultCredentials.findIndex((item) => item.id === cred.id)
      if (index >= 0) vaultCredentials.splice(index, 1)
      json(res, 200, {
        operation_state: 'completed',
        readback: { authoritative: true, kind: 'authorized_absence' },
      })
      return
    }
  }

  const projectDeleteMatch = path.match(/^\/api\/projects\/([^/]+)$/)
  if (req.method === 'DELETE' && projectDeleteMatch) {
    let project = ''
    try {
      project = decodeURIComponent(projectDeleteMatch[1]).trim()
    } catch {
      json(res, 400, { error: 'invalid project encoding' })
      return
    }

    const index = projectIds.indexOf(project)
    if (index < 0) {
      json(res, 404, { error: 'project not found or already deleted' })
      return
    }

    projectIds.splice(index, 1)
    sessionRows = sessionRows.filter((row) => row.project !== project)
    json(res, 200, {
      operation_state: 'completed',
      readback: { authoritative: true, kind: 'authorized_absence' },
    })
    return
  }

  if (req.method === 'POST' && path === '/api/collections/selection/current') {
    try {
      const body = await readRequestJson(req)
      if (body.domain !== 'rules') {
        json(res, 400, { error: 'rules selection domain is required' })
        return
      }
      json(res, 200, ruleSelectionSnapshot(ruleSelection))
    } catch (error) {
      json(res, 400, { error: error instanceof Error ? error.message : String(error) })
    }
    return
  }

  if (req.method === 'POST' && path === '/api/collections/selection') {
    try {
      const body = await readRequestJson(req)
      const saved = body.domain === 'queue' ? saveCandidateSelection(body) : saveRuleSelection(body)
      if (!saved) {
        json(res, 400, { error: 'invalid collection selection' })
        return
      }
      json(res, 200, saved)
    } catch (error) {
      json(res, 400, { error: error instanceof Error ? error.message : String(error) })
    }
    return
  }

  if (req.method === 'POST' && path === '/api/memory/candidates/operations') {
    try {
      const result = applyCandidateSelectionOperation(await readRequestJson(req))
      if (!result) {
        json(res, 400, { error: 'invalid candidate operation' })
        return
      }
      json(res, 200, result)
    } catch (error) {
      json(res, 400, { error: error instanceof Error ? error.message : String(error) })
    }
    return
  }

  if (req.method === 'POST' && path === '/api/collections/selection/page') {
    try {
      const page = ruleSelectionPage(await readRequestJson(req))
      if (!page) {
        json(res, 400, { error: 'invalid rules selection page' })
        return
      }
      json(res, 200, page)
    } catch (error) {
      json(res, 400, { error: error instanceof Error ? error.message : String(error) })
    }
    return
  }

  if (req.method === 'POST' && path === '/api/rules') {
    try {
      const body = await readRequestJson(req)
      const result = applyRuleSelectionOperation(body)
      if (result) {
        json(res, 200, result)
        return
      }
      const content = typeof body.content === 'string' ? body.content.trim() : ''
      if (!content) {
        json(res, 400, { error: 'content is required' })
        return
      }

      const now = new Date().toISOString()
      const project = typeof body.project === 'string' ? body.project.trim() : ''
      const priority = typeof body.priority === 'number' ? body.priority : 0
      const row = {
        id: nextRuleId(),
        project,
        content,
        priority,
        version: 1,
        enabled: true,
        edited_by: typeof body.edited_by === 'string' ? body.edited_by.trim() : 'operator-console',
        created_at: now,
        updated_at: now,
      }
      ruleRows = [row, ...ruleRows]
      json(res, 201, cloneRule(row))
    } catch (error) {
      json(res, 400, { error: error instanceof Error ? error.message : String(error) })
    }
    return
  }


  const ruleEnabledMatch = path.match(/^\/api\/rules\/([^/]+)\/enabled$/)
  if (req.method === 'PATCH' && ruleEnabledMatch) {
    const id = Number(ruleEnabledMatch[1])
    if (!Number.isInteger(id) || id <= 0) {
      json(res, 400, { error: 'invalid rule id' })
      return
    }
    const rowIndex = ruleRows.findIndex((row) => row.id === id)
    if (rowIndex < 0) {
      json(res, 404, { error: 'rule not found' })
      return
    }

    try {
      const body = await readRequestJson(req)
      if (typeof body.enabled !== 'boolean') {
        json(res, 400, { error: 'enabled is required' })
        return
      }
      const updated = {
        ...ruleRows[rowIndex],
        enabled: body.enabled,
        edited_by: typeof body.edited_by === 'string' ? body.edited_by.trim() : ruleRows[rowIndex].edited_by,
        version: ruleRows[rowIndex].version + 1,
        updated_at: new Date().toISOString(),
      }
      ruleRows = ruleRows.map((row, index) => index === rowIndex ? updated : row)
      json(res, 200, cloneRule(updated))
    } catch (error) {
      json(res, 400, { error: error instanceof Error ? error.message : String(error) })
    }
    return
  }

  const ruleMatch = path.match(/^\/api\/rules\/([^/]+)$/)
  if ((req.method === 'PATCH' || req.method === 'DELETE') && ruleMatch) {
    const id = Number(ruleMatch[1])
    if (!Number.isInteger(id) || id <= 0) {
      json(res, 400, { error: 'invalid rule id' })
      return
    }
    const rowIndex = ruleRows.findIndex((row) => row.id === id)
    if (rowIndex < 0) {
      json(res, 404, { error: 'rule not found' })
      return
    }

    if (req.method === 'DELETE') {
      ruleRows = ruleRows.filter((row) => row.id !== id)
      json(res, 200, { deleted: id })
      return
    }

    try {
      const body = await readRequestJson(req)
      const current = ruleRows[rowIndex]
      let content = current.content
      if (Object.hasOwn(body, 'content')) {
        content = typeof body.content === 'string' ? body.content.trim() : ''
        if (!content) {
          json(res, 400, { error: 'content must not be empty' })
          return
        }
      }
      const updated = {
        ...current,
        content,
        priority: typeof body.priority === 'number' ? body.priority : current.priority,
        edited_by: typeof body.edited_by === 'string' ? body.edited_by.trim() : current.edited_by,
        version: current.version + 1,
        updated_at: new Date().toISOString(),
      }
      ruleRows = ruleRows.map((row, index) => index === rowIndex ? updated : row)
      json(res, 200, cloneRule(updated))
    } catch (error) {
      json(res, 400, { error: error instanceof Error ? error.message : String(error) })
    }
    return
  }

  if (req.method === 'POST' && path === '/api/issues/acknowledge') {
    try {
      const body = await readRequestJson(req)
      const ids = Array.isArray(body.ids) ? [...new Set(body.ids.map(Number))] : []
      if (!ids.length || ids.some((id) => !Number.isSafeInteger(id)) || ids.some((id) => !issueRows.some((row) => row.id === id))) {
        json(res, 400, { error: 'invalid issue acknowledgement' })
        return
      }
      const now = new Date().toISOString()
      issueRows = issueRows.map((row) => ids.includes(row.id) ? { ...row, status: 'acknowledged', updated_at: now } : row)
      json(res, 200, { acknowledged: ids })
    } catch (error) {
      json(res, 400, { error: error instanceof Error ? error.message : String(error) })
    }
    return
  }

  if (req.method === 'POST' && path === '/api/issues') {
    try {
      const body = await readRequestJson(req)
      const title = typeof body.title === 'string' ? body.title.trim() : ''
      const target = typeof body.target_project === 'string' ? body.target_project.trim() : ''
      if (!title || !target) {
        json(res, 400, { error: !title ? 'title is required' : 'target_project is required' })
        return
      }
      const now = new Date().toISOString()
      const row = {
        id: Math.max(700, ...issueRows.map((issue) => issue.id)) + 1,
        title,
        body: typeof body.body === 'string' ? body.body : '',
        status: 'open',
        priority: typeof body.priority === 'string' ? body.priority : 'medium',
        type: typeof body.type === 'string' ? body.type : 'task',
        source_project: typeof body.source_project === 'string' && body.source_project.trim() ? body.source_project.trim() : 'operator-console',
        target_project: target,
        source_project_display_name: '',
        target_project_display_name: '',
        labels: Array.isArray(body.labels) ? body.labels.filter((label) => typeof label === 'string') : [],
        created_at: now,
        updated_at: now,
      }
      issueRows.push(row)
      json(res, 201, { id: row.id, message: 'issue created' })
    } catch (error) {
      json(res, 400, { error: error instanceof Error ? error.message : String(error) })
    }
    return
  }

  const issueMatch = path.match(/^\/api\/issues\/(\d+)$/)
  if (issueMatch) {
    const id = Number(issueMatch[1])
    const index = issueRows.findIndex((row) => row.id === id)
    if (index < 0) {
      json(res, 404, { error: 'issue not found' })
      return
    }
    if (req.method === 'GET') {
      const issue = issueRows[index]
      const comments = issueComments.filter((comment) => comment.issue_id === id).map((comment) => ({ ...comment }))
      json(res, 200, {
        issue: { ...issue, labels: [...issue.labels] },
        comments,
        comment_count: comments.length,
        source_project_display_name: issue.source_project_display_name || issue.source_project,
        target_project_display_name: issue.target_project_display_name || issue.target_project,
      })
      return
    }
    if (req.method === 'PATCH') {
      try {
        const body = await readRequestJson(req)
        const current = issueRows[index]
        const updated = {
          ...current,
          ...(typeof body.title === 'string' ? { title: body.title } : {}),
          ...(typeof body.body === 'string' ? { body: body.body } : {}),
          ...(typeof body.priority === 'string' ? { priority: body.priority } : {}),
          ...(typeof body.type === 'string' ? { type: body.type } : {}),
          ...(typeof body.status === 'string' ? { status: body.status } : {}),
          ...(Array.isArray(body.labels) && body.labels.every((label) => typeof label === 'string') ? { labels: [...body.labels] } : {}),
          updated_at: new Date().toISOString(),
        }
        issueRows = issueRows.map((row, rowIndex) => rowIndex === index ? updated : row)
        if (typeof body.comment === 'string' && body.comment.trim()) {
          issueComments.push({
            id: Math.max(0, ...issueComments.map((comment) => comment.id)) + 1,
            issue_id: id,
            author_project: typeof body.source_project === 'string' ? body.source_project : 'dashboard',
            author_agent: typeof body.source_agent === 'string' ? body.source_agent : 'operator-console',
            body: body.comment,
            created_at: updated.updated_at,
          })
        }
        json(res, 200, { message: 'issue updated' })
      } catch (error) {
        json(res, 400, { error: error instanceof Error ? error.message : String(error) })
      }
      return
    }
    if (req.method === 'DELETE') {
      issueRows = issueRows.filter((row) => row.id !== id)
      issueComments = issueComments.filter((comment) => comment.issue_id !== id)
      res.writeHead(204)
      res.end()
      return
    }
  }

  if (req.method !== 'GET') {
    json(res, 405, { error: 'method not allowed' })
    return
  }

  switch (path) {
    case '/api/auth/me':
      json(res, 200, {
        authenticated: false,
        auth_disabled: true,
        user: { name: 'admin', initials: 'A', role: 'admin' },
      })
      return
    case '/api/ready':
      json(res, 200, { status: 'ok', ready: true })
      return
    case '/api/selfcheck':
      json(res, 200, {
        overall: 'ok',
        version: 'v6.29.0-smoke',
        uptime: '1m',
        components: [
          { name: 'Worker Service', status: 'healthy' },
          { name: 'PostgreSQL', status: 'healthy' },
        ],
      })
      return
    case '/api/config':
      json(res, 200, configResponse())
      return
    case '/api/flags':
      json(res, 200, flags)
      return
    case '/api/migrations':
      json(res, 200, migrations)
      return
    case '/api/stats':
      json(res, 200, {
        session_count: 0,
        connected_clients: 1,
        retrieval_requests: 0,
        context_injections: 0,
      })
      return
    case '/api/stats/vnext':
      json(res, 200, {
        injection_count: 0,
        citation_count: 0,
        uncited_count: 0,
        noise_ratio: 0,
        outcomes: {
          total_sessions: 0,
          unrecorded_sessions: 0,
          unrecorded_fraction: 0,
        },
        embedding: {
          chunk_count: 0,
          memories_with_chunks: 0,
          active_memory_count: 0,
          dimension: 1536,
          embedding_coverage: 1,
          model: 'smoke-embedding',
        },
      })
      return
    case '/api/models':
      json(res, 200, {
        models: [],
        default: null,
        current: '',
      })
      return
    case '/api/model-health':
      json(res, 200, {
        generated_at: new Date().toISOString(),
        rows: [
          {
            id: 'recall/embedder',
            role: 'embedding',
            provider: 'OpenAI-compatible embeddings',
            model: 'smoke-embedding',
            health: 'standby',
            source: 'settings',
            endpoint: '/v1/embeddings',
            message: 'Embedding client is initialized, but this snapshot does not probe the endpoint.',
            evidence: ['model_settings.embedder.url', 'model_settings.embedder.model'],
            configured: true,
            secret_set: false,
          },
          {
            id: 'recall/reranker',
            role: 'reranker',
            provider: 'Cohere-compatible rerank',
            model: 'bge-reranker',
            health: 'standby',
            source: 'absent',
            endpoint: '/v1/rerank',
            message: 'Reranker URL is not configured; recall keeps fusion order.',
            evidence: ['ENGRAM_RERANK_URL', 'model_settings.reranker.url'],
            configured: false,
            secret_set: false,
          },
          {
            id: 'ops/llm',
            role: 'llm',
            provider: 'OpenAI-compatible chat',
            model: 'chat-default',
            health: 'standby',
            source: 'absent',
            endpoint: '/v1/chat/completions',
            message: 'LLM URL is not configured; crystallization and on-demand LLM flows stay disabled.',
            evidence: ['ENGRAM_LLM_URL'],
            configured: false,
            secret_set: false,
          },
        ],
        summary: { total: 3, ok: 0, standby: 3, degraded: 0, configured: 1 },
      })
      return
    case '/api/vector/metrics':
      json(res, 200, {
        enabled: true,
        stats: {
          chunk_count: 0,
          memories_with_chunks: 0,
          dimension: 1536,
          model: 'smoke-embedding',
        },
      })
      return
    case '/api/update/status':
      json(res, 200, { state: 'idle', progress: 0, message: 'idle' })
      return
    case '/api/update/check':
      json(res, 200, {
        current_version: 'v6.29.0-smoke',
        latest_version: 'v6.29.0-smoke',
        available: false,
      })
      return
    case '/api/issues/tracked-projects':
      json(res, 200, { projects: [...new Set(issueRows.flatMap((issue) => [issue.source_project, issue.target_project]))].sort(), count: issueRows.length })
      return
    case '/api/issues':
      json(res, 200, {
        issues: issueRows.map((issue) => ({
          ...issue,
          labels: [...issue.labels],
          comment_count: issueComments.filter((comment) => comment.issue_id === issue.id).length,
        })),
        total: issueRows.length,
        project_names: {},
      })
      return
    case '/api/context/search':
      {
        const project = (url.searchParams.get('project') || '').trim()
        const query = (url.searchParams.get('query') || '').trim()
        if (!project || !query) {
          json(res, 400, { error: 'project and query required' })
          return
        }
        const observations = [
          ...[...memoryRows, ...deepLinkMemoryRows]
            .filter((row) => row.project === project && !suppressedMemoryIds.has(String(row.id)))
            .filter((row) => row.content.toLocaleLowerCase().includes(query.toLocaleLowerCase()))
            .map((row) => ({
              id: row.id,
              project: row.project,
              type: 'discovery',
              memory_type: 'context',
              title: row.content,
              narrative: row.content,
              content: row.content,
              similarity: 0.92,
              created_at: row.updated_at,
            })),
          ...searchGuidanceRows.filter((row) => row.project === project && row.content.toLocaleLowerCase().includes(query.toLocaleLowerCase())),
        ].slice(0, 20)
        json(res, 200, { project, query, intent: 'search', observations, threshold: 0, max_results: 20, total_results: observations.length })
      }
      return
    case '/api/documents':
      {
        const project = (url.searchParams.get('project') || '').trim()
        if (!project) {
          json(res, 400, { error: 'project query parameter is required' })
          return
        }
        json(res, 200, documentListResponse(url))
      }
      return
    case '/api/documents/comments':
      {
        const documentID = Number((url.searchParams.get('document_id') || '').trim())
        if (!Number.isInteger(documentID) || documentID <= 0) {
          json(res, 400, { error: 'document_id is required' })
          return
        }
        if (!documentRows.some((row) => row.id === documentID)) {
          json(res, 404, { error: 'document not found' })
          return
        }
        json(res, 200, { comments: [], count: 0, document_id: documentID })
      }
      return
    case '/api/documents/history':
      {
        const pathValue = (url.searchParams.get('path') || '').trim()
        const project = (url.searchParams.get('project') || '').trim()
        if (!pathValue || !project) {
          json(res, 400, { error: !pathValue ? 'path query parameter is required' : 'project query parameter is required' })
          return
        }
        json(res, 200, documentHistoryResponse(url))
      }
      return
    case '/api/documents/read':
      {
        const pathValue = (url.searchParams.get('path') || '').trim()
        const project = (url.searchParams.get('project') || '').trim()
        const rawVersion = (url.searchParams.get('version') || '').trim()
        if (!pathValue || !project) {
          json(res, 400, { error: !pathValue ? 'path query parameter is required' : 'project query parameter is required' })
          return
        }
        const version = rawVersion ? Number(rawVersion) : null
        if (rawVersion && (!Number.isInteger(version) || version <= 0)) {
          json(res, 400, { error: `invalid version ${JSON.stringify(rawVersion)}` })
          return
        }
        const rows = documentRows.filter((row) => row.path === pathValue && row.project === project)
        const row = version === null
          ? rows.reduce((latest, item) => !latest || item.version > latest.version ? item : latest, null)
          : rows.find((item) => item.version === version)
        if (!row) {
          json(res, 404, { error: 'document not found' })
          return
        }
        json(res, 200, { ...row })
      }
      return
    case '/api/vault/credentials':
      json(res, 200, vaultCredentials.map(({ value: _value, orphaned: _orphaned, ...item }) => item))
      return
    case '/api/vault/status':
      json(res, 200, { key_configured: true, fingerprint: 'abcddcba11223344', key_source: 'mock', credential_count: vaultCredentials.length })
      return
    case '/api/sessions/list':
      {
        const project = url.searchParams.get('project') || ''
        const requestedLimit = Number(url.searchParams.get('limit') || 100)
        const limit = Number.isFinite(requestedLimit) && requestedLimit > 0 ? requestedLimit : 100
        const rows = sessionRows
          .filter((row) => !project || row.project === project)
          .slice(0, limit)
        json(res, 200, { sessions: rows, total: rows.length, limit, offset: 0 })
      }
      return
    case '/api/sessions':
      {
        const claudeSessionId = url.searchParams.get('claudeSessionId') || ''
        const row = sessionRows.find((item) => item.claude_session_id === claudeSessionId)
        if (!row) {
          json(res, 404, { error: 'session not found' })
          return
        }
        json(res, 200, row)
      }
      return
    case '/api/rules':
      json(res, 200, ruleResponse(url))
      return
    case '/api/projects':
      json(res, 200, [...projectIds])
      return
    case '/api/memories/principal':
      json(res, 200, principalMemoryResponse(url))
      return
    case '/api/memories':
      json(res, 200, memoryResponseForProject(url.searchParams.get('project') || 'operator-console'))
      return
    case '/api/memory/candidates':
      json(res, 200, candidateResponse(
        url.searchParams.get('project') || 'operator-console',
        url.searchParams.get('status') || 'pending',
        Number(url.searchParams.get('limit') || 100),
      ))
      return
    case '/api/memory-domains':
      json(res, 200, domainRegistryResponse())
      return
    default:
      json(res, 404, { error: 'not found' })
      return
  }
})

server.listen(port, host, () => {
  console.log(`mock operator api listening on http://${host}:${port}`)
})
