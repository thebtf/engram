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

const server = createServer((req, res) => {
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
      json(res, 200, config)
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
      json(res, 200, {})
  }
})

server.listen(port, host, () => {
  console.log(`mock operator api listening on http://${host}:${port}`)
})
