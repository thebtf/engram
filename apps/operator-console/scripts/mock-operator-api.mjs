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
    updated_at: '2026-05-19T10:00:00Z',
    status: 'active',
  },
]

const suppressedMemoryIds = new Set()

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
]

function memoryResponseForProject(project) {
  return memoryRows
    .filter((row) => row.project === project)
    .filter((row) => !suppressedMemoryIds.has(String(row.id)))
    .map((row) => ({ ...row, tags: [...row.tags] }))
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

  if (!path.startsWith('/api')) {
    text(res, 404, 'not found')
    return
  }

  if (req.method === 'OPTIONS') {
    res.writeHead(204)
    res.end()
    return
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
      json(res, 200, { deleted: true, name: cred.name })
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
    json(res, 200, { id: project, removed_at: new Date().toISOString() })
    return
  }

  if (req.method === 'POST' && path === '/api/rules') {
    try {
      const body = await readRequestJson(req)
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
    case '/api/issues':
      json(res, 200, { issues: [] })
      return
    case '/api/vault/credentials':
      json(res, 200, vaultCredentials.map(({ value: _value, ...item }) => item))
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
