import { computed, type ComputedRef } from 'vue'
import type { RuleCreateInput, RuleRow } from './useMockData'
import type {
  OperatorSelection,
  OperatorSelectionTarget,
} from './useOperatorSelection'
import { parseOperatorSelectionSnapshot } from './useOperatorSelection'
import type { OperatorLoadState, OperatorUnsupportedAction } from './useOperatorApi'
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

export type RuleSelectionOperationAction = 'enable' | 'disable' | 'delete' | 'update' | 'reorder'

export interface RuleSelectionFilter {
  scope: string
}

export interface RuleSelectionPage {
  cursor: string
  nextCursor: string
  filterFingerprint: string
  targets: OperatorSelectionTarget[]
  total?: number
}

export interface RuleOperationCurrent {
  id: number
  project: string | null
  content: string
  priority: number
  version: number
  enabled: boolean
}

export type RuleOperationReadback =
  | { kind: 'current'; current: RuleOperationCurrent; version?: number }
  | { kind: 'authorized_absence' }

export interface RuleOperationItem {
  targetId: number
  outcome: MutationItemOutcome
  observedVersion?: number
  readback?: RuleOperationReadback
}

export interface RuleSelectionOperationInput {
  action: RuleSelectionOperationAction
  selection: Exclude<OperatorSelection, { kind: 'none' }>
  content?: string
  priority?: number
  scope?: string
  order?: Array<{ ruleId: number; expectedVersion: number }>
}

type RuleOperationCurrentState = CurrentRule | CurrentRule[]

export interface RuleSelectionOperationResult {
  mutation: MutationResult<RuleSelectionOperationInput, RuleOperationCurrentState>
  items: RuleOperationItem[]
}

interface SelectionEnvelope {
  selection?: unknown
}

function isRFC3339Timestamp(value: unknown): value is string {
  return typeof value === 'string'
    && /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d+)?(?:Z|[+-]\d{2}:\d{2})$/.test(value)
    && !Number.isNaN(Date.parse(value))
}

function isText(value: unknown, maximum = 512): value is string {
  return typeof value === 'string' && value.length > 0 && new TextEncoder().encode(value).length <= maximum
}

function isFingerprint(value: unknown): value is string {
  return typeof value === 'string' && /^sha256:[a-f0-9]{64}$/.test(value)
}

function isPositiveInteger(value: unknown): value is number {
  return typeof value === 'number' && Number.isSafeInteger(value) && value > 0
}

function isNonNegativeInteger(value: unknown): value is number {
  return typeof value === 'number' && Number.isSafeInteger(value) && value >= 0
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
    !isPositiveInteger(id)
    || project === undefined
    || typeof content !== 'string' || !content.trim()
    || typeof priority !== 'number' || !Number.isSafeInteger(priority)
    || !isPositiveInteger(version)
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

function jsonInit(method: 'POST', body: unknown, requestId: string): RequestInit {
  return {
    method,
    headers: {
      'Content-Type': 'application/json',
      'X-Engram-Request-ID': requestId,
    },
    body: JSON.stringify(body),
  }
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

function ruleListPath(scope: string): string {
  const query = new URLSearchParams({ limit: '200' })
  if (scope === 'all') query.set('all', 'true')
  else if (scope !== 'global') query.set('project', scope)
  return `/api/rules?${query}`
}

function selectionPayload(selection: OperatorSelection): Record<string, unknown> {
  switch (selection.kind) {
    case 'none':
      return { kind: 'none' }
    case 'explicit':
      return {
        kind: 'explicit',
        targets: selection.targets.map((target) => ({
          id: target.id,
          ...(target.expectedVersion === undefined ? {} : { expected_version: target.expectedVersion }),
        })),
      }
    case 'page':
      return {
        kind: 'page',
        cursor: selection.cursor,
        targets: selection.targets.map((target) => ({
          id: target.id,
          ...(target.expectedVersion === undefined ? {} : { expected_version: target.expectedVersion }),
        })),
      }
    case 'frozen_filter':
      throw new TypeError('a frozen rule selection must be freshly confirmed by its server-owned filter')
  }
}

function operationSelectionPayload(selection: Exclude<OperatorSelection, { kind: 'none' }>): Record<string, unknown> {
  if (!isPositiveInteger(selection.version)) {
    throw new TypeError('the selected Rules snapshot has no server version')
  }

  switch (selection.kind) {
    case 'explicit':
    case 'page':
      return {
        kind: selection.kind,
        selection_version: selection.version,
      }
    case 'frozen_filter':
      return {
        kind: 'frozen_filter',
        selection_version: selection.version,
        selection_token: selection.selectionToken,
      }
  }
}

function parseRuleSelectionEnvelope(value: unknown): OperatorSelection {
  if (typeof value !== 'object' || value === null || Array.isArray(value)) {
    throw new TypeError('Rules selection response is invalid')
  }
  const selection = Reflect.get(value, 'selection')
  if (
    typeof selection === 'object' && selection !== null && !Array.isArray(selection)
    && Reflect.get(selection, 'kind') === 'frozen_filter'
    && !Object.hasOwn(selection, 'excluded_ids')
  ) {
    return parseOperatorSelectionSnapshot({ ...selection, excluded_ids: [] })
  }
  return parseOperatorSelectionSnapshot(selection)
}

function parseRuleSelectionPage(value: unknown): RuleSelectionPage {
  if (typeof value !== 'object' || value === null || Array.isArray(value)) {
    throw new TypeError('Rules page response is invalid')
  }

  const cursor = Reflect.get(value, 'cursor')
  const nextCursor = Reflect.get(value, 'next_cursor')
  const filterFingerprint = Reflect.get(value, 'filter_fingerprint')
  const rawTargets = Reflect.get(value, 'targets')
  const total = Reflect.get(value, 'total')
  if (!isText(cursor) || (nextCursor !== undefined && nextCursor !== '' && !isText(nextCursor)) || !isFingerprint(filterFingerprint) || !Array.isArray(rawTargets)) {
    throw new TypeError('Rules page response is invalid')
  }

  const targets = rawTargets.length === 0
    ? []
    : (() => {
      const page = parseOperatorSelectionSnapshot({
        domain: 'rules',
        kind: 'page',
        cursor,
        targets: rawTargets,
      })
      return page.kind === 'page' ? page.targets : []
    })()
  if (total !== undefined && !isNonNegativeInteger(total)) {
    throw new TypeError('Rules page total is invalid')
  }

  return {
    cursor,
    nextCursor: typeof nextCursor === 'string' ? nextCursor : '',
    filterFingerprint,
    targets,
    ...(total === undefined ? {} : { total }),
  }
}

function ruleOperationOutcome(value: unknown): MutationItemOutcome | undefined {
  switch (value) {
    case 'committed':
    case 'denied':
    case 'conflict':
    case 'validation_error':
    case 'failed':
    case 'outcome_unknown':
      return value
    default:
      return undefined
  }
}

function parseRuleOperationReadback(value: unknown): RuleOperationReadback | undefined {
  if (typeof value !== 'object' || value === null || Array.isArray(value) || Reflect.get(value, 'authoritative') !== true) {
    return undefined
  }

  const kind = Reflect.get(value, 'kind')
  if (kind === 'authorized_absence') {
    return { kind }
  }
  if (kind !== 'current') {
    return undefined
  }

  const current = parseCurrentRule(Reflect.get(value, 'current_state'), {})
  const version = Reflect.get(value, 'current_version')
  if (current === undefined || (version !== undefined && !isPositiveInteger(version))) {
    return undefined
  }
  return {
    kind,
    current: {
      id: current.id,
      project: current.project,
      content: current.content,
      priority: current.priority,
      version: current.version,
      enabled: current.enabled,
    },
    ...(version === undefined ? {} : { version }),
  }
}

function parseRuleOperationItems(value: unknown): RuleOperationItem[] {
  if (!Array.isArray(value)) return []

  const items: RuleOperationItem[] = []
  for (const item of value) {
    if (typeof item !== 'object' || item === null || Array.isArray(item)) return []

    const targetId = Reflect.get(item, 'target_id')
    const outcome = ruleOperationOutcome(Reflect.get(item, 'outcome'))
    const observedVersion = Reflect.get(item, 'observed_version')
    if (!isPositiveInteger(targetId) || outcome === undefined || (observedVersion !== undefined && !isPositiveInteger(observedVersion))) {
      return []
    }

    const readback = parseRuleOperationReadback(Reflect.get(item, 'readback'))
    items.push({
      targetId,
      outcome,
      ...(observedVersion === undefined ? {} : { observedVersion }),
      ...(readback === undefined ? {} : { readback }),
    })
  }
  return items
}

async function responseItems(response: Response): Promise<RuleOperationItem[]> {
  try {
    const body: unknown = await response.clone().json()
    if (typeof body !== 'object' || body === null || Array.isArray(body)) return []
    return parseRuleOperationItems(Reflect.get(body, 'item_results'))
  } catch {
    return []
  }
}

function parseOperationCurrentState(value: unknown, expected: CurrentRuleExpectation): RuleOperationCurrentState | undefined {
  if (!Array.isArray(value)) return parseCurrentRule(value, expected)
  if (!value.length) return undefined

  const current: CurrentRule[] = []
  for (const item of value) {
    const parsed = parseCurrentRule(item, expected)
    if (parsed === undefined) return undefined
    current.push(parsed)
  }
  return current
}

function operationCurrentStateParser(input: RuleSelectionOperationInput): MutationCurrentStateParser<RuleOperationCurrentState> {
  switch (input.action) {
    case 'enable':
      return (value) => parseOperationCurrentState(value, { enabled: true })
    case 'disable':
      return (value) => parseOperationCurrentState(value, { enabled: false })
    case 'update':
      return (value) => parseOperationCurrentState(value, {
        ...(input.content === undefined ? {} : { content: input.content.trim() }),
        ...(input.priority === undefined ? {} : { priority: input.priority }),
      })
    case 'reorder':
      return (value) => parseOperationCurrentState(value, {})
    case 'delete':
      return () => undefined
  }
}

function operationBody(input: RuleSelectionOperationInput, requestId: string): Record<string, unknown> {
  const selection = operationSelectionPayload(input.selection)
  const body: Record<string, unknown> = {
    request_id: requestId,
    action: input.action,
    selection,
  }
  switch (input.action) {
    case 'enable':
    case 'disable':
      return body
    case 'delete':
      return body
    case 'update':
      return {
        ...body,
        ...(input.content === undefined ? {} : { content: input.content.trim() }),
        ...(input.priority === undefined ? {} : { priority: input.priority }),
        edited_by: 'operator-console',
      }
    case 'reorder':
      if (!input.scope || !input.order?.length) {
        throw new TypeError('Rules reorder requires one complete scope order')
      }
      return {
        ...body,
        scope: input.scope === 'global' ? {} : { project: input.scope },
        order: input.order.map((target) => ({
          rule_id: target.ruleId,
          expected_version: target.expectedVersion,
        })),
      }
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
  refresh: (scope?: string) => Promise<void>
  createRule: (input: RuleCreateInput) => Promise<MutationResult<RuleCreateInput, CurrentRule>>
  saveRuleSelection: (selection: Exclude<OperatorSelection, { kind: 'frozen_filter' }>) => Promise<OperatorSelection>
  freezeRuleSelection: (filter: RuleSelectionFilter, excludedIds: string[]) => Promise<OperatorSelection>
  currentRuleSelection: () => Promise<OperatorSelection>
  loadRulePage: (filter: RuleSelectionFilter, cursor?: string) => Promise<RuleSelectionPage>
  runRuleSelectionOperation: (input: RuleSelectionOperationInput) => Promise<RuleSelectionOperationResult>
  scopeChangeGap: OperatorUnsupportedAction
} {
  const initialEvidence = endpointEvidence(ruleListPath('all'), 'rules-list')
  const rowsState = useState<RuleRow[]>('live:rules-page:rows', () => [])
  const projectOptions = useState<string[]>('live:rules-page:project-options', () => [])
  const state = useState<OperatorLoadState<RuleRow[]>>('live:rules-page:state', () => pendingState(initialEvidence, rowsState.value))

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

  async function refresh(scope = 'all') {
    const endpoint = ruleListPath(scope)
    const evidence = endpointEvidence(endpoint, 'rules-list')
    state.value = pendingState(evidence, rowsState.value)
    await refreshProjects()
    const result = await loadOperatorJson<ApiRuleRow[]>(endpoint, {
      source: 'rules-list',
      empty: (rows) => !rows.length,
    })

    if (result.kind === 'live' || result.kind === 'empty') {
      replaceArray(rowsState.value, result.data.map(mapRuleRow))
      state.value = rowsState.value.length
        ? liveState(evidence, rowsState.value)
        : emptyState(evidence, rowsState.value)
      return
    }

    if (result.kind === 'error') {
      state.value = errorState(evidence, result.error, {
        source: 'rules-list',
        run: async () => {
          await refresh(scope)
          return state.value
        },
      }, rowsState.value)
    } else {
      state.value = {
        ...result,
        ...(result.data === undefined ? {} : { data: rowsState.value }),
      }
    }
  }

  async function createRule(input: RuleCreateInput) {
    const requestId = crypto.randomUUID()
    const request = { requestId, action: 'rule-create', intent: input }
    return executeMutation(
      request,
      fetch(operatorApiUrl('/api/rules'), {
        ...jsonInit('POST', {
          content: input.content,
          priority: input.priority ?? 0,
          edited_by: input.editedBy || 'operator-console',
          ...(input.project ? { project: input.project } : {}),
        }, requestId),
        credentials: 'include',
      }),
      createRuleCurrentStateParser(input),
    )
  }

  async function saveRuleSelection(selection: Exclude<OperatorSelection, { kind: 'frozen_filter' }>): Promise<OperatorSelection> {
    const requestId = crypto.randomUUID()
    const response = await operatorFetchJson<SelectionEnvelope>('/api/collections/selection', jsonInit('POST', {
      domain: 'rules',
      selection: selectionPayload(selection),
    }, requestId), 'rules-selection-snapshot')
    return parseRuleSelectionEnvelope(response)
  }

  async function freezeRuleSelection(filter: RuleSelectionFilter, excludedIds: string[]): Promise<OperatorSelection> {
    const requestId = crypto.randomUUID()
    const response = await operatorFetchJson<SelectionEnvelope>('/api/collections/selection', jsonInit('POST', {
      domain: 'rules',
      selection: {
        kind: 'frozen_filter',
        filter,
        excluded_ids: excludedIds,
      },
    }, requestId), 'rules-selection-freeze')
    return parseRuleSelectionEnvelope(response)
  }

  async function currentRuleSelection(): Promise<OperatorSelection> {
    const requestId = crypto.randomUUID()
    const response = await operatorFetchJson<SelectionEnvelope>('/api/collections/selection/current', jsonInit('POST', {
      domain: 'rules',
    }, requestId), 'rules-selection-current')
    return parseRuleSelectionEnvelope(response)
  }

  async function loadRulePage(filter: RuleSelectionFilter, cursor = ''): Promise<RuleSelectionPage> {
    const requestId = crypto.randomUUID()
    const response = await operatorFetchJson<unknown>('/api/collections/selection/page', jsonInit('POST', {
      domain: 'rules',
      filter,
      ...(cursor ? { cursor } : {}),
      limit: 50,
    }, requestId), 'rules-selection-page')
    return parseRuleSelectionPage(response)
  }

  async function runRuleSelectionOperation(input: RuleSelectionOperationInput): Promise<RuleSelectionOperationResult> {
    const requestId = crypto.randomUUID()
    const request = {
      requestId,
      action: `rule-${input.action}`,
      intent: input,
    }
    const parser = operationCurrentStateParser(input)
    let response: Response
    try {
      response = await fetch(operatorApiUrl('/api/rules'), {
        ...jsonInit('POST', operationBody(input, requestId), requestId),
        credentials: 'include',
      })
    } catch (error) {
      return {
        mutation: await executeMutation(request, Promise.reject(error), parser),
        items: [],
      }
    }

    const items = await responseItems(response)
    return {
      mutation: await executeMutation(request, Promise.resolve(response), parser),
      items,
    }
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
    saveRuleSelection,
    freezeRuleSelection,
    currentRuleSelection,
    loadRulePage,
    runRuleSelectionOperation,
    scopeChangeGap,
  }
}
