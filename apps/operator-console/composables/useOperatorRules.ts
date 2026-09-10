import type { ComputedRef } from 'vue'
import type { RuleCreateInput, RuleRow, RuleUpdateInput } from './useMockData'
import type { OperatorLoadState } from './useOperatorApi'
import { executeMutation, type MutationCurrentStateParser, type MutationItemOutcome, type MutationResult } from './useApi'
import {
 emptyState,
 endpointEvidence,
 errorState,
 liveState,
 loadOperatorJson,
 operatorApiUrl,
 operatorFetchJson,
 pendingState,
 unsupportedOperatorAction,
} from './useOperatorApi'

interface ApiRuleRow {
 id: number
 project?: string
 content?: string
 priority?: number
 version?: number
 enabled?: boolean
 edited_by?: string
 created_at?: string
 updated_at?: string
}

interface CurrentRule {
 id: number
 project: string | null
 content: string
 priority: number
 version: number
 enabled: boolean
 created_at: string
 updated_at: string
}

interface CurrentRuleExpectation {
 id?: number
 project?: string | null
 content?: string
 priority?: number
 enabled?: boolean
}


function isRFC3339Timestamp(value: unknown): value is string {
 return typeof value === 'string'
  && /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d+)?(?:Z|[+-]\d{2}:\d{2})$/.test(value)
  && !Number.isNaN(Date.parse(value))
}


function parseCurrentRule(value: unknown, expected: CurrentRuleExpectation): CurrentRule | undefined {
 if (
  typeof value !== 'object' || value === null || Array.isArray(value)
  || !('id' in value) || !('content' in value) || !('priority' in value)
  || !('version' in value) || !('enabled' in value)
  || !('created_at' in value) || !('updated_at' in value)
 ) return undefined

 const projectValue = 'project' in value ? value.project : undefined
 const project = projectValue === undefined || projectValue === null
  ? null
  : typeof projectValue === 'string' && projectValue.trim() ? projectValue : undefined
 const id = value.id
 const content = value.content
 const priority = value.priority
 const version = value.version
 const enabled = value.enabled
 const createdAt = value.created_at
 const updatedAt = value.updated_at
 if (
  typeof id !== 'number' || !Number.isSafeInteger(id) || id <= 0
  || project === undefined
  || typeof content !== 'string' || !content.trim()
  || typeof priority !== 'number' || !Number.isSafeInteger(priority)
  || typeof version !== 'number' || !Number.isSafeInteger(version) || version <= 0
  || typeof enabled !== 'boolean'
  || !isRFC3339Timestamp(createdAt)
  || !isRFC3339Timestamp(updatedAt)
  || (expected.id !== undefined && id !== expected.id)
  || (expected.project !== undefined && project !== expected.project)
  || (expected.content !== undefined && content !== expected.content)
  || (expected.priority !== undefined && priority !== expected.priority)
  || (expected.enabled !== undefined && enabled !== expected.enabled)
 ) return undefined

 return {
  id,
  project,
  content,
  priority,
  version,
  enabled,
  created_at: createdAt,
  updated_at: updatedAt,
 }
}

export function createRuleCurrentStateParser(input: RuleCreateInput): MutationCurrentStateParser<CurrentRule> {
 return (value) => parseCurrentRule(value, {
  project: input.project?.trim() || null,
  content: input.content.trim(),
  priority: input.priority ?? 0,
 })
}

export function updateRuleCurrentStateParser(id: number, input: RuleUpdateInput): MutationCurrentStateParser<CurrentRule> {
 return (value) => parseCurrentRule(value, {
  id,
  ...(input.content === undefined ? {} : { content: input.content.trim() }),
  ...(input.priority === undefined ? {} : { priority: input.priority }),
 })
}

export function toggleRuleCurrentStateParser(id: number, enabled: boolean): MutationCurrentStateParser<CurrentRule> {
 return (value) => parseCurrentRule(value, { id, enabled })
}

function jsonInit(method: 'POST' | 'PATCH' | 'DELETE', body?: unknown): RequestInit {
 const init: RequestInit = { method }
 if (body !== undefined) {
  init.headers = { 'Content-Type': 'application/json' }
  init.body = JSON.stringify(body)
 }
 return init
}

function replaceArray<T>(target: T[], next: readonly T[]) {
 target.splice(0, target.length, ...next)
}

function compactAge(timestamp?: string): string {
 if (!timestamp) return '-'

 const value = Date.parse(timestamp)
 if (Number.isNaN(value)) return '-'

 const seconds = Math.max(0, Math.floor((Date.now() - value) / 1000))
 if (seconds < 60) return `${seconds}s`
 if (seconds < 3600) return `${Math.floor(seconds / 60)}m`
 if (seconds < 86400) return `${Math.floor(seconds / 3600)}h`
 return `${Math.floor(seconds / 86400)}d`
}

function normalizeProject(value?: string): string {
 return value && value.trim() ? value.trim() : 'global'
}

function mapRuleRow(row: ApiRuleRow): RuleRow {
 return {
  id: row.id,
  content: row.content || '-',
  project: normalizeProject(row.project),
  priority: typeof row.priority === 'number' ? row.priority : 0,
  version: typeof row.version === 'number' ? row.version : 1,
  updated: compactAge(row.updated_at || row.created_at),
  enabled: row.enabled !== false,
 }
}

function sortRules(rows: RuleRow[]): RuleRow[] {
 return [...rows].sort((left, right) => {
  if (left.priority !== right.priority) {
   return right.priority - left.priority
  }
  return left.content.localeCompare(right.content)
 })
}
function submitMutation<TIntent, TCurrent = unknown>(
 action: string,
 intent: TIntent,
 path: string,
 init: RequestInit,
 parseCurrentState: MutationCurrentStateParser<TCurrent> = () => undefined,
): Promise<MutationResult<TIntent, TCurrent>> {
 return executeMutation(
  { requestId: crypto.randomUUID(), action, intent },
  fetch(operatorApiUrl(path), { ...init, credentials: 'include' }),
  parseCurrentState,
 )
}

function itemOutcome(result: MutationResult<unknown>): MutationItemOutcome {
 switch (result.kind) {
  case 'committed_verified':
   return 'committed'
  case 'denied':
   return 'denied'
  case 'conflict':
   return 'conflict'
  case 'validation_error':
   return 'validation_error'
  case 'committed_verification_pending':
  case 'partial':
   return 'outcome_unknown'
  case 'stale':
  case 'unsupported':
  case 'offline':
  case 'timeout':
  case 'network':
  case 'failed':
   return 'failed'
 }
}


function startOnce(key: string, run: () => Promise<void>) {
 const started = useState<boolean>(`live:${key}:started`, () => false)
 if (import.meta.client && !started.value) {
  started.value = true
  void run().catch((error) => {
   if (import.meta.dev) {
    console.warn(`[useOperatorRules] ${key} live load failed`, error)
   }
  })
 }
}

export function useOperatorRules(): {
 rows: RuleRow[]
 scopeOptions: ComputedRef<string[]>
 loadState: ComputedRef<OperatorLoadState<RuleRow[]>>
 pending: ComputedRef<boolean>
 error: ComputedRef<string | null>
 refresh: () => Promise<void>
 createRule: (input: RuleCreateInput) => Promise<MutationResult<RuleCreateInput>>
 updateRule: (id: number, input: RuleUpdateInput) => Promise<MutationResult<{ id: number; input: RuleUpdateInput }>>
 toggleRuleEnabled: (id: number, enabled: boolean) => Promise<MutationResult<{ id: number; enabled: boolean }>>
 reorderRules: (orderedRows: RuleRow[]) => Promise<MutationResult<{ orderedRows: RuleRow[] }>>
 deleteRule: (id: number) => Promise<MutationResult<{ id: number }>>
 scopeChangeGap: ReturnType<typeof unsupportedOperatorAction>
} {
 const evidence = endpointEvidence('/api/rules?all=true&limit=200', 'rules-list')
 const rowsState = useState<RuleRow[]>('live:rules-page:rows', () => [])
 const projectOptions = useState<string[]>('live:rules-page:project-options', () => [])
 const state = useState<OperatorLoadState<RuleRow[]>>('live:rules-page:state', () => pendingState(evidence, rowsState.value))

 const loadState = computed(() => state.value)
 const scopeOptions = computed(() => ['global', ...projectOptions.value])
 const pending = computed(() => state.value.kind === 'pending')
 const error = computed(() => state.value.kind === 'error' ? state.value.error.message : null)

 async function refreshProjects() {
  const result = await loadOperatorJson<string[]>('/api/projects', {
   source: 'rules-projects',
   empty: (rows) => !rows.length,
  })
  if (result.kind === 'live' || result.kind === 'empty') {
   replaceArray(projectOptions.value, [...new Set(result.data.filter((project) => project.trim()))].sort())
  } else if (result.kind === 'error' && import.meta.dev) {
   console.warn('[useOperatorRules] project options unavailable', result.error.message)
  }
 }

 async function refresh() {
  state.value = pendingState(evidence, rowsState.value)
  await refreshProjects()
  const result = await loadOperatorJson<ApiRuleRow[]>('/api/rules?all=true&limit=200', {
   source: 'rules-list',
   empty: (rows) => !rows.length,
  })

  if (result.kind === 'live' || result.kind === 'empty') {
   replaceArray(rowsState.value, sortRules(result.data.map(mapRuleRow)))
   state.value = rowsState.value.length
    ? liveState(evidence, rowsState.value)
    : emptyState(evidence, rowsState.value)
   return
  }

  if (result.kind === 'error') {
   state.value = errorState(evidence, result.error, {
    source: 'rules-list',
    run: async () => {
     await refresh()
     return state.value
    },
   }, rowsState.value)
  } else {
   state.value = result as OperatorLoadState<RuleRow[]>
  }
 }

 async function createRule(input: RuleCreateInput) {
  return submitMutation('rule-create', input, '/api/rules', jsonInit('POST', {
   content: input.content,
   priority: input.priority ?? 0,
   edited_by: input.editedBy || 'operator-console',
   ...(input.project ? { project: input.project } : {}),
  }), createRuleCurrentStateParser(input))
 }

 async function updateRule(id: number, input: RuleUpdateInput) {
  return submitMutation('rule-update', { id, input }, `/api/rules/${id}`, jsonInit('PATCH', {
   ...(input.content !== undefined ? { content: input.content } : {}),
   ...(input.priority !== undefined ? { priority: input.priority } : {}),
   ...(input.editedBy !== undefined ? { edited_by: input.editedBy } : { edited_by: 'operator-console' }),
  }), updateRuleCurrentStateParser(id, input))
 }

 async function toggleRuleEnabled(id: number, enabled: boolean) {
  return submitMutation('rule-enable-toggle', { id, enabled }, `/api/rules/${id}/enabled`, jsonInit('PATCH', {
   enabled,
   edited_by: 'operator-console',
  }), toggleRuleCurrentStateParser(id, enabled))
 }

 async function reorderRules(orderedRows: RuleRow[]): Promise<MutationResult<{ orderedRows: RuleRow[] }>> {
  const nextRows = orderedRows.map((row, index) => ({
   ...row,
   priority: (orderedRows.length - index) * 10,
  }))
  const changed = nextRows.filter((row) => rowsState.value.find((current) => current.id === row.id)?.priority !== row.priority)
  const intent = { orderedRows: nextRows }
  const request = { requestId: crypto.randomUUID(), action: 'rule-reorder', intent }
  const items = await Promise.all(changed.map(async (row) => {
   const result = await executeMutation(
    { requestId: crypto.randomUUID(), action: 'rule-update', intent: { id: row.id, priority: row.priority } },
    fetch(operatorApiUrl(`/api/rules/${row.id}`), {
     ...jsonInit('PATCH', {
      priority: row.priority,
      edited_by: 'operator-console',
     }),
     credentials: 'include',
    }),
    () => undefined,
   )
   return { targetId: row.id, outcome: itemOutcome(result) }
  }))
  return { kind: 'partial', request, httpStatus: 207, items }
 }

 async function deleteRule(id: number) {
  return submitMutation('rule-delete', { id }, `/api/rules/${id}`, jsonInit('DELETE'))
 }

 const scopeChangeGap = unsupportedOperatorAction(
  'rule-scope-change',
  'PATCH /api/rules/{id}/project',
  'Behavioral rule scope changes are intentionally not accepted by the current update endpoint.',
 )

 startOnce('rules-page', refresh)

 return {
  rows: rowsState.value,
  scopeOptions,
  loadState,
  pending,
  error,
  refresh,
  createRule,
  updateRule,
  toggleRuleEnabled,
  reorderRules,
  deleteRule,
  scopeChangeGap,
 }
}
