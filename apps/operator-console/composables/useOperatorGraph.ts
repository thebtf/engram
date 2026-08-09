import { onScopeDispose, ref, type ComputedRef, type Ref } from 'vue'
import type { OperatorDiagnosisCategory, OperatorLoadState, OperatorSourceError } from './useOperatorApi'
import {
  emptyState,
  endpointEvidence,
  errorState,
  gatedState,
  liveState,
  operatorApiUrl,
  operatorErrorDiagnosis,
  operatorFetchJson,
  pendingState,
} from './useOperatorApi'

const GRAPH_FLAG = 'ENGRAM_GRAPH_ENABLED'
const GRAPH_LIMIT = 200
export const GRAPH_DEFAULT_TRAVERSE_DEPTH = 2
export const GRAPH_DEFAULT_PATH_DEPTH = 3

export const GRAPH_NODE_TYPES = [
  'project', 'repo', 'skill', 'agent', 'rule', 'hook', 'session',
  'file', 'consumer', 'decision', 'claim', 'bug', 'feature',
] as const

export const GRAPH_EDGE_TYPES = [
  'uses', 'depends_on', 'supersedes', 'contradicts', 'caused', 'fixed_by',
  'learned_from', 'promoted_to', 'belongs_to', 'imports', 'modifies',
  'blocked_by', 'avoids', 'succeeded_by', 'synonym_of', 'same_concept_as',
] as const

interface ApiFlags {
  flags?: Record<string, boolean>
}

interface ApiGraphNode {
  id: number | string
  node_type?: string
  external_ref?: string
  project?: string
  privacy_scope?: string
  metadata?: string | null
  created_at?: string
  updated_at?: string
}

interface ApiGraphNodesResponse {
  nodes?: ApiGraphNode[]
  count?: number
}

interface ApiGraphEdge {
  id: number | string
  edge_type?: string
  weight?: number
  reasoning?: string
  source_session_id?: string
  source_type?: string
  target_type?: string
  source_id?: number | string | null
  target_id?: number | string | null
  node_source_id?: number | string | null
  node_target_id?: number | string | null
  created_at?: string
}

interface ApiGraphEdgesResponse {
  edges?: ApiGraphEdge[]
  count?: number
}

interface ApiGraphTraverseStep {
  edge_id?: number | string
  source_id?: number | string
  target_id?: number | string
  edge_type?: string
  weight?: number
  reasoning?: string
  depth?: number
}

interface ApiGraphTraverseResponse {
  results?: ApiGraphTraverseStep[]
  count?: number
}

interface ApiGraphPathResponse {
  path?: ApiGraphTraverseStep[]
  found?: boolean
  hops?: number
}

interface GraphErrorPayload {
  error?: {
    code?: string
    message?: string
  }
}

export interface OperatorGraphNode {
  id: string
  nodeType: string
  externalRef: string
  project: string
  privacyScope: string
  metadata: string
  createdAt: string
  updatedAt: string
}

export interface OperatorGraphEdge {
  id: string
  edgeType: string
  weight: number
  reasoning: string
  sourceSessionId: string
  sourceType: string
  targetType: string
  sourceID?: string
  targetID?: string
  nodeSourceID?: string
  nodeTargetID?: string
  createdAt: string
}

export interface OperatorGraphTraverseStep {
  edgeID: string
  sourceID: string
  targetID: string
  edgeType: string
  weight: number
  reasoning: string
  depth: number
}

export interface OperatorGraphPathResult {
  found: boolean
  hops: number
  path: OperatorGraphTraverseStep[]
}

export interface GraphMutationError {
  status?: number
  /** Typed transport diagnosis for locale-key mapping at the presentation boundary. */
  category?: OperatorDiagnosisCategory
  code: string
  endpoint: string
  method: string
  message: string
}

export interface GraphActionResult {
  ok: boolean
  error?: GraphMutationError
}

export interface CreateGraphNodeInput {
  nodeType: string
  externalRef: string
  project: string
  privacyScope?: string
}

export interface CreateGraphEdgeInput {
  sourceNodeID: string
  targetNodeID: string
  edgeType: string
  reasoning?: string
}

export interface DeleteGraphNodeInput {
  nodeID: string
  cascade: boolean
}

class GraphApiError extends Error {
  readonly status?: number
  readonly category: OperatorDiagnosisCategory
  readonly code: string
  readonly endpoint: string
  readonly method: string
  readonly retryable: boolean

  constructor(meta: GraphMutationError & { category: OperatorDiagnosisCategory; retryable: boolean }) {
    super(meta.message)
    this.name = 'GraphApiError'
    this.status = meta.status
    this.category = meta.category
    this.code = meta.code
    this.endpoint = meta.endpoint
    this.method = meta.method
    this.retryable = meta.retryable
  }
}

function replaceArray<T>(target: T[], next: readonly T[]) {
  target.splice(0, target.length, ...next)
}

function jsonInit(method: 'POST' | 'DELETE', body?: unknown): RequestInit {
  const init: RequestInit = { method }
  if (body !== undefined) {
    init.headers = { 'Content-Type': 'application/json' }
    init.body = JSON.stringify(body)
  }
  return init
}

function isRetryableStatus(status?: number) {
  return status === undefined || status === 0 || status === 408 || status === 429 || status >= 500
}

function compactText(text: string) {
  return text.trim().replace(/\s+/g, ' ').slice(0, 220)
}

function graphErrorFromThrown(error: unknown, fallback: Omit<GraphMutationError, 'message' | 'code'>): GraphMutationError {
  if (error instanceof GraphApiError) {
    return {
      status: error.status,
      category: error.category,
      code: error.code,
      endpoint: error.endpoint,
      method: error.method,
      message: error.message,
    }
  }

  return {
    ...fallback,
    category: 'unreachable',
    code: 'graph_request_failed',
    message: error instanceof Error ? error.message : String(error),
  }
}

function graphSourceError(error: unknown, fallback: Omit<OperatorSourceError, 'message' | 'retryable'>): OperatorSourceError {
  if (error instanceof GraphApiError) {
    return {
      message: error.message,
      category: error.category,
      status: error.status,
      source: fallback.source,
      path: error.endpoint,
      method: error.method,
      retryable: error.retryable,
    }
  }

  return {
    message: error instanceof Error ? error.message : String(error),
    category: 'unreachable',
    source: fallback.source,
    path: fallback.path,
    method: fallback.method,
    retryable: true,
  }
}

async function fetchGraphJson<T>(path: string, init: RequestInit = {}): Promise<T> {
  const method = String(init.method || 'GET').toUpperCase()
  let response: Response
  try {
    response = await fetch(operatorApiUrl(path), {
      ...init,
      credentials: 'include',
    })
  } catch (error) {
    throw new GraphApiError({
      category: 'unreachable',
      code: 'graph_request_failed',
      endpoint: path,
      method,
      message: error instanceof Error ? error.message : String(error),
      retryable: true,
    })
  }

  const text = await response.text()
  if (!response.ok) {
    let payload: GraphErrorPayload | undefined
    if (text.trim()) {
      try {
        payload = JSON.parse(text) as GraphErrorPayload
      } catch {
        payload = undefined
      }
    }
    const code = payload?.error?.code || 'graph_request_failed'
    const category = operatorErrorDiagnosis(response.status)
    throw new GraphApiError({
      status: response.status,
      category,
      code,
      endpoint: path,
      method,
      message: `${method} ${path} failed with status ${response.status}`,
      retryable: isRetryableStatus(response.status),
    })
  }

  if (!text.trim()) {
    return undefined as T
  }
  return JSON.parse(text) as T
}

function mapNode(row: ApiGraphNode): OperatorGraphNode {
  return {
    id: String(row.id),
    nodeType: row.node_type || 'skill',
    externalRef: row.external_ref || '',
    project: row.project || '',
    privacyScope: row.privacy_scope || 'project',
    metadata: typeof row.metadata === 'string' ? row.metadata : '',
    createdAt: row.created_at || '',
    updatedAt: row.updated_at || '',
  }
}

function parseNodes(payload: ApiGraphNodesResponse, path: string): OperatorGraphNode[] {
  if (!payload || typeof payload !== 'object' || !Array.isArray(payload.nodes)) {
    throw new GraphApiError({
      category: 'invalid-response',
      code: 'invalid_graph_payload',
      endpoint: path,
      method: 'GET',
      message: `Invalid graph nodes payload from ${path}`,
      retryable: false,
    })
  }
  return payload.nodes.map(mapNode)
}

function mapEdge(row: ApiGraphEdge): OperatorGraphEdge {
  return {
    id: String(row.id),
    edgeType: row.edge_type || 'uses',
    weight: typeof row.weight === 'number' ? row.weight : 1,
    reasoning: row.reasoning || '',
    sourceSessionId: row.source_session_id || '',
    sourceType: row.source_type || 'memory',
    targetType: row.target_type || 'memory',
    sourceID: row.source_id === undefined || row.source_id === null ? undefined : String(row.source_id),
    targetID: row.target_id === undefined || row.target_id === null ? undefined : String(row.target_id),
    nodeSourceID: row.node_source_id === undefined || row.node_source_id === null ? undefined : String(row.node_source_id),
    nodeTargetID: row.node_target_id === undefined || row.node_target_id === null ? undefined : String(row.node_target_id),
    createdAt: row.created_at || '',
  }
}

function parseEdges(payload: ApiGraphEdgesResponse, path: string): OperatorGraphEdge[] {
  if (!payload || typeof payload !== 'object' || !Array.isArray(payload.edges)) {
    throw new GraphApiError({
      category: 'invalid-response',
      code: 'invalid_graph_payload',
      endpoint: path,
      method: 'GET',
      message: `Invalid graph edges payload from ${path}`,
      retryable: false,
    })
  }
  return payload.edges.map(mapEdge)
}

function mapTraverseStep(row: ApiGraphTraverseStep): OperatorGraphTraverseStep {
  return {
    edgeID: String(row.edge_id || ''),
    sourceID: String(row.source_id || ''),
    targetID: String(row.target_id || ''),
    edgeType: row.edge_type || 'uses',
    weight: typeof row.weight === 'number' ? row.weight : 1,
    reasoning: row.reasoning || '',
    depth: typeof row.depth === 'number' ? row.depth : 0,
  }
}

function parseTraverse(payload: ApiGraphTraverseResponse | null | undefined, path: string): OperatorGraphTraverseStep[] {
  if (payload == null) {
    return []
  }
  if (typeof payload !== 'object') {
    throw new GraphApiError({
      category: 'invalid-response',
      code: 'invalid_graph_payload',
      endpoint: path,
      method: 'GET',
      message: `Invalid graph traversal payload from ${path}`,
      retryable: false,
    })
  }
  if (payload.results == null) {
    return []
  }
  if (!Array.isArray(payload.results)) {
    throw new GraphApiError({
      category: 'invalid-response',
      code: 'invalid_graph_payload',
      endpoint: path,
      method: 'GET',
      message: `Invalid graph traversal payload from ${path}`,
      retryable: false,
    })
  }
  return payload.results.map(mapTraverseStep)
}

function parsePath(payload: ApiGraphPathResponse | null | undefined, path: string): OperatorGraphPathResult {
  if (payload == null) {
    return { found: false, hops: 0, path: [] }
  }
  if (typeof payload !== 'object') {
    throw new GraphApiError({
      category: 'invalid-response',
      code: 'invalid_graph_payload',
      endpoint: path,
      method: 'GET',
      message: `Invalid graph path payload from ${path}`,
      retryable: false,
    })
  }
  const rawPath = payload.path == null ? [] : payload.path
  if (!Array.isArray(rawPath)) {
    throw new GraphApiError({
      category: 'invalid-response',
      code: 'invalid_graph_payload',
      endpoint: path,
      method: 'GET',
      message: `Invalid graph path payload from ${path}`,
      retryable: false,
    })
  }
  return {
    found: payload.found === true,
    hops: typeof payload.hops === 'number' ? payload.hops : rawPath.length,
    path: rawPath.map(mapTraverseStep),
  }
}

function startOnce(key: string, run: () => Promise<void>) {
  const started = useState<boolean>(`live:${key}:started`, () => false)
  if (import.meta.client && !started.value) {
    started.value = true
    void run().catch((error) => {
      if (import.meta.dev) {
        console.warn(`[useOperatorGraph] ${key} live load failed`, error)
      }
    })
  }
  return started
}

export function useOperatorGraph(): {
  projects: string[]
  selectedProject: Ref<string>
  nodes: OperatorGraphNode[]
  connectedEdges: OperatorGraphEdge[]
  selectedNodeID: Ref<string | null>
  nodesState: ComputedRef<OperatorLoadState<OperatorGraphNode[]>>
  hasProvenSnapshot: ComputedRef<boolean>
  edgesState: ComputedRef<OperatorLoadState<OperatorGraphEdge[]>>
  traverseResults: Ref<OperatorGraphTraverseStep[]>
  traverseBusy: Ref<boolean>
  traverseError: Ref<GraphMutationError | null>
  pathResult: Ref<OperatorGraphPathResult | null>
  pathBusy: Ref<boolean>
  pathError: Ref<GraphMutationError | null>
  lastMutationError: Ref<GraphMutationError | null>
  refresh: () => Promise<void>
  refreshConnectedEdges: (nodeID?: string | null) => Promise<void>
  createNode: (input: CreateGraphNodeInput) => Promise<GraphActionResult>
  createEdge: (input: CreateGraphEdgeInput) => Promise<GraphActionResult>
  deleteEdge: (edgeID: string) => Promise<GraphActionResult>
  deleteNode: (input: DeleteGraphNodeInput) => Promise<GraphActionResult>
  invalidateMutations: () => void
  traverseMemory: (memoryID: string, depth?: number) => Promise<void>
  findPath: (sourceID: string, targetID: string, maxDepth?: number) => Promise<void>
} {
  const nodesEvidence = endpointEvidence('/api/graph/nodes?project={project}', 'operator-graph', { flag: GRAPH_FLAG })
  const edgesEvidence = endpointEvidence('/api/graph/edges?node_id={id}&direction=both', 'operator-graph', { flag: GRAPH_FLAG })
  const nodesRows = useState<OperatorGraphNode[]>('live:graph:nodes', () => [])
  const edgesRows = useState<OperatorGraphEdge[]>('live:graph:edges', () => [])
  const projectsState = useState<string[]>('live:graph:projects', () => [])
  const selectedProject = useState<string>('live:graph:selected-project', () => '')
  const selectedNodeID = useState<string | null>('live:graph:selected-node-id', () => null)
  const nodesStateRef = useState<OperatorLoadState<OperatorGraphNode[]>>('live:graph:nodes:state', () => pendingState(nodesEvidence, nodesRows.value))
  const hasProvenSnapshotValue = useState<boolean>('live:graph:has-proven-snapshot', () => false)
  const edgesStateRef = useState<OperatorLoadState<OperatorGraphEdge[]>>('live:graph:edges:state', () => emptyState(edgesEvidence, edgesRows.value))
  const traverseResults = useState<OperatorGraphTraverseStep[]>('live:graph:traverse-results', () => [])
  const traverseBusy = useState<boolean>('live:graph:traverse-busy', () => false)
  const traverseError = useState<GraphMutationError | null>('live:graph:traverse-error', () => null)
  const pathResult = useState<OperatorGraphPathResult | null>('live:graph:path-result', () => null)
  const pathBusy = useState<boolean>('live:graph:path-busy', () => false)
  const pathError = useState<GraphMutationError | null>('live:graph:path-error', () => null)
  const lastMutationError = useState<GraphMutationError | null>('live:graph:last-mutation-error', () => null)
  const refreshGeneration = ref(0)
  const edgesGeneration = ref(0)
  const traverseGeneration = ref(0)
  const pathGeneration = ref(0)
  const mutationGeneration = ref(0)
  let scopeActive = true

  const nodesState = computed(() => nodesStateRef.value)
  const hasProvenSnapshot = computed(() => hasProvenSnapshotValue.value)
  const edgesState = computed(() => edgesStateRef.value)

  function begin(generation: Ref<number>) {
    generation.value += 1
    return generation.value
  }

  function owns(generation: Ref<number>, run: number) {
    return scopeActive && generation.value === run
  }

  function invalidateMutations() {
    begin(mutationGeneration)
    lastMutationError.value = null
  }

  async function refreshConnectedEdges(nodeID = selectedNodeID.value) {
    const run = begin(edgesGeneration)
    if (!nodeID) {
      if (!owns(edgesGeneration, run)) return
      replaceArray(edgesRows.value, [])
      edgesStateRef.value = emptyState(edgesEvidence, edgesRows.value)
      return
    }
    if (nodesStateRef.value.kind === 'gated' || nodesStateRef.value.kind === 'pending' || selectedNodeID.value !== nodeID) return
    const path = `/api/graph/edges?node_id=${encodeURIComponent(nodeID)}&direction=both`
    edgesStateRef.value = pendingState(edgesEvidence, edgesRows.value)
    try {
      const payload = await fetchGraphJson<ApiGraphEdgesResponse>(path)
      const edges = parseEdges(payload, path)
      if (!owns(edgesGeneration, run) || selectedNodeID.value !== nodeID) return
      replaceArray(edgesRows.value, edges)
      edgesStateRef.value = edges.length ? liveState(edgesEvidence, edgesRows.value) : emptyState(edgesEvidence, edgesRows.value)
    } catch (error) {
      if (!owns(edgesGeneration, run) || selectedNodeID.value !== nodeID) return
      const mapped = graphSourceError(error, {
        source: 'operator-graph-edges',
        path,
        method: 'GET',
      })
      edgesStateRef.value = errorState(edgesEvidence, mapped, {
        source: 'operator-graph-edges',
        run: async () => {
          await refreshConnectedEdges(nodeID)
          return edgesStateRef.value
        },
      }, edgesRows.value)
    }
  }

  async function refresh() {
    const run = begin(refreshGeneration)
    begin(edgesGeneration)
    begin(traverseGeneration)
    begin(pathGeneration)
    traverseBusy.value = false
    traverseError.value = null
    replaceArray(traverseResults.value, [])
    pathBusy.value = false
    pathError.value = null
    pathResult.value = null
    nodesStateRef.value = pendingState(nodesEvidence, nodesRows.value)
    try {
      const flags = await operatorFetchJson<ApiFlags>('/api/flags', undefined, 'operator-graph-flags')
      if (!owns(refreshGeneration, run)) return
      if (flags.flags?.[GRAPH_FLAG] !== true) {
        begin(edgesGeneration)
        replaceArray(nodesRows.value, [])
        replaceArray(edgesRows.value, [])
        nodesStateRef.value = gatedState(nodesEvidence, GRAPH_FLAG, 'Knowledge graph is disabled by the graph feature flag.', nodesRows.value)
        edgesStateRef.value = gatedState(edgesEvidence, GRAPH_FLAG, 'Knowledge graph is disabled by the graph feature flag.', edgesRows.value)
        return
      }

      const projects = await operatorFetchJson<string[]>('/api/projects', undefined, 'operator-graph-projects')
      if (!owns(refreshGeneration, run)) return
      const nextProjects = Array.isArray(projects) ? projects.filter(Boolean).sort() : []
      replaceArray(projectsState.value, nextProjects)
      if (!selectedProject.value || !projectsState.value.includes(selectedProject.value)) {
        selectedProject.value = projectsState.value[0] || ''
      }
      if (!selectedProject.value) {
        begin(edgesGeneration)
        replaceArray(nodesRows.value, [])
        replaceArray(edgesRows.value, [])
        nodesStateRef.value = emptyState(nodesEvidence, nodesRows.value)
        hasProvenSnapshotValue.value = true
        edgesStateRef.value = emptyState(edgesEvidence, edgesRows.value)
        return
      }

      const project = selectedProject.value
      const path = `/api/graph/nodes?project=${encodeURIComponent(project)}&limit=${GRAPH_LIMIT}`
      const payload = await fetchGraphJson<ApiGraphNodesResponse>(path)
      const nodes = parseNodes(payload, path)
      if (!owns(refreshGeneration, run) || selectedProject.value !== project) return
      replaceArray(nodesRows.value, nodes)
      if (!selectedNodeID.value || !nodesRows.value.some((node) => node.id === selectedNodeID.value)) {
        selectedNodeID.value = nodesRows.value[0]?.id || null
      }
      nodesStateRef.value = nodes.length ? liveState(nodesEvidence, nodesRows.value) : emptyState(nodesEvidence, nodesRows.value)
      hasProvenSnapshotValue.value = true
      await refreshConnectedEdges(selectedNodeID.value)
    } catch (error) {
      if (!owns(refreshGeneration, run)) return
      const mapped = graphSourceError(error, {
        source: 'operator-graph-nodes',
        path: nodesEvidence.endpoint,
        method: 'GET',
      })
      nodesStateRef.value = errorState(nodesEvidence, mapped, {
        source: 'operator-graph-nodes',
        run: async () => {
          await refresh()
          return nodesStateRef.value
        },
      }, nodesRows.value)
    }
  }

  async function createNode(input: CreateGraphNodeInput): Promise<GraphActionResult> {
    const run = begin(mutationGeneration)
    lastMutationError.value = null
    const path = '/api/graph/nodes'
    try {
      const created = await fetchGraphJson<ApiGraphNode>(path, jsonInit('POST', {
        node_type: input.nodeType,
        external_ref: input.externalRef,
        project: input.project,
        privacy_scope: input.privacyScope || 'project',
      }))
      if (created && owns(mutationGeneration, run)) {
        await refresh()
        const createdID = String(created.id)
        if (owns(mutationGeneration, run) && selectedProject.value === input.project && nodesRows.value.some((node) => node.id === createdID)) {
          selectedNodeID.value = createdID
          await refreshConnectedEdges(createdID)
        }
      }
      return { ok: true }
    } catch (error) {
      const mapped = graphErrorFromThrown(error, { endpoint: path, method: 'POST', status: undefined })
      if (owns(mutationGeneration, run)) lastMutationError.value = mapped
      return { ok: false, error: mapped }
    }
  }

  async function createEdge(input: CreateGraphEdgeInput): Promise<GraphActionResult> {
    const run = begin(mutationGeneration)
    lastMutationError.value = null
    const path = '/api/graph/edges'
    try {
      await fetchGraphJson(path, jsonInit('POST', {
        source_type: 'node',
        target_type: 'node',
        node_source_id: Number(input.sourceNodeID),
        node_target_id: Number(input.targetNodeID),
        edge_type: input.edgeType,
        reasoning: input.reasoning || '',
      }))
      if (owns(mutationGeneration, run) && (selectedNodeID.value === input.sourceNodeID || selectedNodeID.value === input.targetNodeID)) {
        await refreshConnectedEdges(selectedNodeID.value)
      }
      return { ok: true }
    } catch (error) {
      const mapped = graphErrorFromThrown(error, { endpoint: path, method: 'POST', status: undefined })
      if (owns(mutationGeneration, run)) lastMutationError.value = mapped
      return { ok: false, error: mapped }
    }
  }

  async function deleteEdge(edgeID: string): Promise<GraphActionResult> {
    const run = begin(mutationGeneration)
    lastMutationError.value = null
    const path = `/api/graph/edges/${encodeURIComponent(edgeID)}`
    try {
      await fetchGraphJson(path, jsonInit('DELETE'))
      if (owns(mutationGeneration, run)) await refreshConnectedEdges(selectedNodeID.value)
      return { ok: true }
    } catch (error) {
      const mapped = graphErrorFromThrown(error, { endpoint: path, method: 'DELETE', status: undefined })
      if (owns(mutationGeneration, run)) lastMutationError.value = mapped
      return { ok: false, error: mapped }
    }
  }

  async function deleteNode(input: DeleteGraphNodeInput): Promise<GraphActionResult> {
    const run = begin(mutationGeneration)
    lastMutationError.value = null
    const path = `/api/graph/nodes/${encodeURIComponent(input.nodeID)}?cascade=${input.cascade ? 'true' : 'false'}`
    try {
      await fetchGraphJson(path, jsonInit('DELETE'))
      if (!owns(mutationGeneration, run)) return { ok: true }
      if (selectedNodeID.value === input.nodeID) {
        selectedNodeID.value = null
      }
      await refresh()
      return { ok: true }
    } catch (error) {
      const mapped = graphErrorFromThrown(error, { endpoint: path, method: 'DELETE', status: undefined })
      if (owns(mutationGeneration, run)) lastMutationError.value = mapped
      return { ok: false, error: mapped }
    }
  }

  async function traverseMemory(memoryID: string, depth = GRAPH_DEFAULT_TRAVERSE_DEPTH) {
    const run = begin(traverseGeneration)
    if (nodesStateRef.value.kind === 'gated' || nodesStateRef.value.kind === 'pending') {
      replaceArray(traverseResults.value, [])
      traverseError.value = null
      return
    }
    traverseBusy.value = true
    traverseError.value = null
    try {
      const path = `/api/graph/traverse?memory_id=${encodeURIComponent(memoryID)}&depth=${encodeURIComponent(String(depth))}`
      const payload = await fetchGraphJson<ApiGraphTraverseResponse>(path)
      if (!owns(traverseGeneration, run)) return
      replaceArray(traverseResults.value, parseTraverse(payload, path))
    } catch (error) {
      if (!owns(traverseGeneration, run)) return
      traverseError.value = graphErrorFromThrown(error, { endpoint: '/api/graph/traverse', method: 'GET', status: undefined })
      replaceArray(traverseResults.value, [])
    } finally {
      if (owns(traverseGeneration, run)) traverseBusy.value = false
    }
  }

  async function findPath(sourceID: string, targetID: string, maxDepth = GRAPH_DEFAULT_PATH_DEPTH) {
    const run = begin(pathGeneration)
    if (nodesStateRef.value.kind === 'gated' || nodesStateRef.value.kind === 'pending') {
      pathResult.value = null
      pathError.value = null
      return
    }
    pathBusy.value = true
    pathError.value = null
    try {
      const path = `/api/graph/find-path?source_id=${encodeURIComponent(sourceID)}&target_id=${encodeURIComponent(targetID)}&max_depth=${encodeURIComponent(String(maxDepth))}`
      const payload = await fetchGraphJson<ApiGraphPathResponse>(path)
      if (!owns(pathGeneration, run)) return
      pathResult.value = parsePath(payload, path)
    } catch (error) {
      if (!owns(pathGeneration, run)) return
      pathError.value = graphErrorFromThrown(error, { endpoint: '/api/graph/find-path', method: 'GET', status: undefined })
      pathResult.value = null
    } finally {
      if (owns(pathGeneration, run)) pathBusy.value = false
    }
  }

  const started = startOnce('operator-graph', refresh)
  onScopeDispose(() => {
    scopeActive = false
    begin(refreshGeneration)
    begin(edgesGeneration)
    begin(traverseGeneration)
    begin(pathGeneration)
    begin(mutationGeneration)
    started.value = false
  })

  return {
    projects: projectsState.value,
    selectedProject,
    nodes: nodesRows.value,
    connectedEdges: edgesRows.value,
    selectedNodeID,
    nodesState,
    hasProvenSnapshot,
    edgesState,
    traverseResults,
    traverseBusy,
    traverseError,
    pathResult,
    pathBusy,
    pathError,
    lastMutationError,
    refresh,
    refreshConnectedEdges,
    createNode,
    createEdge,
    deleteEdge,
    deleteNode,
    invalidateMutations,
    traverseMemory,
    findPath,
  }
}
