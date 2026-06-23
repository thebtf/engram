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
    case '/api/projects':
      json(res, 200, [])
      return
    case '/api/memories':
      json(res, 200, [])
      return
    default:
      json(res, 404, { error: 'not found' })
      return
  }
})

server.listen(port, host, () => {
  console.log(`mock operator api listening on http://${host}:${port}`)
})
