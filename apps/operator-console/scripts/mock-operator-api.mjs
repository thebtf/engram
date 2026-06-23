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

const candidateRows = [
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
  const rows = candidateRows
    .filter((row) => row.status === status)
    .filter((row) => row.affected_projects.includes(project))
    .slice(0, limit)
    .map((row) => ({
      ...row,
      evidence_handles: [...row.evidence_handles],
      affected_projects: [...row.affected_projects],
    }))
  return {
    candidates: rows,
    count: rows.length,
    project,
    status,
    limit,
  }
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

  const candidateActionMatch = path.match(/^\/api\/memory\/candidates\/([^/]+)\/(promote|reject|supersede)$/)
  if (req.method === 'POST' && candidateActionMatch) {
    const id = Number(candidateActionMatch[1])
    const action = candidateActionMatch[2]
    const row = candidateRows.find((item) => item.id === id)
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

    row.status = action === 'promote' ? 'promoted' : action === 'reject' ? 'rejected' : 'superseded'
    const memoryId = action === 'promote' ? 9100 + id : undefined
    if (memoryId !== undefined) row.promoted_memory_id = memoryId
    json(res, 200, {
      action,
      candidate_id: row.id,
      candidate_status: row.status,
      memory_id: memoryId,
      promoted_memory_id: row.promoted_memory_id,
    })
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
      json(res, 200, { sessions: [], total: 0 })
      return
    case '/api/rules':
      json(res, 200, [])
      return
    case '/api/projects':
      json(res, 200, ['operator-console'])
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
    default:
      json(res, 404, { error: 'not found' })
      return
  }
})

server.listen(port, host, () => {
  console.log(`mock operator api listening on http://${host}:${port}`)
})
